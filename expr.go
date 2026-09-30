package intervalanalysis

import (
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
)

// callResolver maps a call expression to the interval of its integer result
// using function summaries. Unresolvable/external calls yield the full range of
// the result type (a sound approximation).
type callResolver interface {
	ResolveCall(call *ast.CallExpr, args []Interval) (ret Interval, ok bool)
}

// warningSink collects potential runtime errors discovered during evaluation.
type warningSink interface {
	report(pos token.Pos, cat Category, msg, evidence string)
}

// evalContext evaluates integer expressions under an abstract state.
type evalContext struct {
	state    *State
	info     *types.Info
	pkg      *types.Package
	resolver callResolver
	sink     warningSink
	funcName string
}

// evalInt returns the interval of an integer-typed expression. ok is false for
// non-integer expressions.
func (e *evalContext) evalInt(expr ast.Expr) (Interval, bool) {
	if e == nil || expr == nil || e.state.IsBottom() {
		return TopInterval, false
	}
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind == token.INT {
			v, err := strconv.ParseInt(x.Value, 0, 64)
			if err != nil {
				// Oversized literal: type checker would reject except for
				// unsigned 64-bit constants.
				if u, err2 := strconv.ParseUint(x.Value, 0, 64); err2 == nil {
					return Const(int64(u)), true
				}
				return TopInterval, true
			}
			return Const(v), true
		}
	case *ast.Ident:
		if obj := e.info.Uses[x]; obj != nil {
			if _, _, ok := intKind(obj.Type()); ok {
				return e.state.Get(obj), true
			}
		}
	case *ast.ParenExpr:
		return e.evalInt(x.X)
	case *ast.UnaryExpr:
		if x.Op == token.SUB {
			if v, ok := e.evalInt(x.X); ok {
				bits, signed, _ := e.kindOf(x)
				return e.neg(v, bits, signed, x.Pos()), true
			}
		}
		if x.Op == token.XOR {
			if _, ok := e.evalInt(x.X); ok {
				return e.fullOf(x), true
			}
		}
		if x.Op == token.ADD {
			return e.evalInt(x.X)
		}
	case *ast.BinaryExpr:
		return e.evalBinary(x)
	case *ast.CallExpr:
		return e.evalCall(x)
	case *ast.SelectorExpr:
		if obj := e.info.Uses[x.Sel]; obj != nil {
			if _, _, ok := intKind(obj.Type()); ok {
				// Unknown external value (e.g. package-level var): full range.
				return e.state.Get(obj), true
			}
		}
	case *ast.IndexExpr:
		e.checkIndex(x.X, x.Index)
		// Reading an element yields an unknown value of the element type.
		return e.fullOf(x), true
	case *ast.TypeAssertExpr:
		if _, _, ok := e.kindOf(x); ok {
			return e.fullOf(x), true
		}
	}
	// Integer-typed but unsupported expression: sound full range.
	if _, _, ok := e.kindOf(expr); ok {
		return e.fullOf(expr), true
	}
	return TopInterval, false
}

func (e *evalContext) kindOf(expr ast.Expr) (bits int, signed bool, ok bool) {
	tv, present := e.info.Types[expr]
	if !present || tv.Type == nil {
		return 0, false, false
	}
	return intKind(tv.Type)
}

func (e *evalContext) fullOf(expr ast.Expr) Interval {
	if bits, signed, ok := e.kindOf(expr); ok {
		return Full(bits, signed)
	}
	return TopInterval
}

func (e *evalContext) neg(v Interval, bits int, signed bool, pos token.Pos) Interval {
	if v.Top {
		return Full(bits, signed)
	}
	if !signed {
		// Two's-complement wrap: conservatively full range.
		return Full(bits, signed)
	}
	min, _ := TypeBounds(bits, signed)
	if signed && v.Lo == min {
		e.report(pos, CategoryIntegerOverflow,
			"unary negation may overflow (e.g. negate of minimum value)",
			"operand range "+v.String())
		return Full(bits, signed)
	}
	return Interval{Lo: -v.Hi, Hi: -v.Lo}
}

func (e *evalContext) evalBinary(x *ast.BinaryExpr) (Interval, bool) {
	if _, _, ok := e.kindOf(x); !ok {
		return TopInterval, false
	}
	bits, signed, _ := e.kindOf(x)
	switch x.Op {
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM:
		return e.evalArith(x, bits, signed), true
	case token.AND, token.OR, token.XOR, token.AND_NOT:
		// Bitwise results can cover the full type range in general.
		return Full(bits, signed), true
	case token.SHL, token.SHR:
		// Shifts can overflow for unsigned count; result conservatively top.
		return Full(bits, signed), true
	default:
		return Full(bits, signed), true
	}
}

func (e *evalContext) evalArith(x *ast.BinaryExpr, bits int, signed bool) Interval {
	la, _ := e.evalInt(x.X)
	rb, _ := e.evalInt(x.Y)
	var (
		res      Interval
		overflow bool
		divZero  bool
	)
	switch x.Op {
	case token.ADD:
		res, overflow = Add(la, rb, bits, signed)
	case token.SUB:
		res, overflow = Sub(la, rb, bits, signed)
	case token.MUL:
		res, overflow = Mul(la, rb, bits, signed)
	case token.QUO:
		res, divZero, overflow = Div(la, rb, bits, signed)
	case token.REM:
		res, divZero, overflow = Mod(la, rb, bits, signed)
	}
	if divZero {
		e.report(x.Y.Pos(), CategoryDivByZero,
			"divisor may be zero",
			"divisor range "+rb.String()+" may contain 0")
	}
	if overflow {
		e.report(x.OpPos, CategoryIntegerOverflow,
			"integer arithmetic may overflow",
			"left "+la.String()+" op right "+rb.String())
	}
	return res
}

func (e *evalContext) report(pos token.Pos, cat Category, msg, evidence string) {
	if e.sink != nil {
		e.sink.report(pos, cat, msg, evidence)
	}
}

func (e *evalContext) evalCall(x *ast.CallExpr) (Interval, bool) {
	bits, signed, typeOK := e.kindOf(x)
	if !typeOK {
		return TopInterval, false
	}
	// Builtins.
	if id, ok := x.Fun.(*ast.Ident); ok {
		switch id.Name {
		case "len", "cap":
			if len(x.Args) == 1 {
				if iv, ok := e.evalLenCap(x.Args[0], id.Name == "cap"); ok {
					return iv, true
				}
			}
			return Full(bits, signed), true
		case "complex", "imag", "real":
			return Full(bits, signed), true
		}
	}
	// Conversion T(v): evaluate then conservatively keep full range when the
	// source interval may exceed T; exact when both ranges agree.
	if e.isConversion(x) {
		if len(x.Args) == 1 {
			if v, ok := e.evalInt(x.Args[0]); ok {
				srcBits, srcSigned, _ := func() (int, bool, bool) { return e.kindOf(x.Args[0]) }()
				if !v.Top && fitsConversion(v, srcBits, srcSigned, bits, signed) {
					return v, true
				}
				if !v.Top {
					e.report(x.Pos(), CategoryIntegerOverflow,
						"integer conversion may overflow",
						"source range "+v.String())
				}
				return Full(bits, signed), true
			}
		}
		return Full(bits, signed), true
	}
	// Summaries.
	args := make([]Interval, len(x.Args))
	for i, arg := range x.Args {
		args[i], _ = e.evalInt(arg)
	}
	if e.resolver != nil {
		if ret, ok := e.resolver.ResolveCall(x, args); ok {
			return ret, true
		}
	}
	return Full(bits, signed), true
}

func (e *evalContext) isConversion(x *ast.CallExpr) bool {
	switch f := x.Fun.(type) {
	case *ast.Ident:
		_, ok := e.info.Uses[f].(*types.TypeName)
		return ok
	case *ast.SelectorExpr:
		_, ok := e.info.Uses[f.Sel].(*types.TypeName)
		return ok
	default:
		// Parenthesized or constructed types are rare; TypeOf with a non-nil
		// signature means a function call rather than a conversion.
		if e.info.TypeOf(x.Fun) != nil {
			_, isSig := e.info.TypeOf(x.Fun).Underlying().(*types.Signature)
			return !isSig
		}
	}
	return false
}

func (e *evalContext) evalLenCap(arg ast.Expr, cap_ bool) (Interval, bool) {
	// Arrays keep static length regardless of expression form.
	if at, ok := e.info.TypeOf(arg).(*types.Array); ok {
		return Const(at.Len()), true
	}
	_ = cap_
	// Try the length lens keyed on the referenced object.
	if id, ok := arg.(*ast.Ident); ok {
		if obj := e.info.Uses[id]; obj != nil {
			return e.state.GetLen(obj), true
		}
	}
	return TopInterval, false
}

// evalBuiltinLen evaluates len(arg) soundly (used when tracking slice lengths).
func (e *evalContext) evalBuiltinLen(arg ast.Expr) (Interval, bool) {
	return e.evalLenCap(arg, false)
}

// checkIndex flags x[index] when the index interval may leave [0, len-1].
func (e *evalContext) checkIndex(x, index ast.Expr) {
	iv, ok := e.evalInt(index)
	if !ok {
		return
	}
	length := e.lengthOf(x)
	safe := false
	if !iv.Top {
		if length.IsConst() && iv.Lo >= 0 {
			safe = uint64(iv.Hi) < uint64(length.Lo)
		}
	}
	if !safe {
		e.report(index.Pos(), CategoryIndexOutOfBounds,
			"array/slice index may be out of bounds",
			"index range "+iv.String()+", length "+length.String())
	}
}

func (e *evalContext) lengthOf(x ast.Expr) Interval {
	if at, ok := e.info.TypeOf(x).(*types.Array); ok {
		return Const(at.Len())
	}
	if id, ok := x.(*ast.Ident); ok {
		if obj := e.info.Uses[id]; obj != nil {
			return e.state.GetLen(obj)
		}
	}
	return TopInterval
}

func fitsConversion(v Interval, srcBits int, srcSigned bool, dstBits int, dstSigned bool) bool {
	smin, smax := TypeBounds(srcBits, srcSigned)
	dmin, dmax := TypeBounds(dstBits, dstSigned)
	if v.Top {
		// Whole source range must fit destination.
		return rangeFits(smin, smax, dmin, dmax, srcSigned, dstSigned)
	}
	for _, p := range []int64{v.Lo, v.Hi} {
		if !rangeFits(p, p, dmin, dmax, srcSigned, dstSigned) {
			return false
		}
	}
	return true
}

func rangeFits(lo, hi, dmin, dmax int64, srcSigned, dstSigned bool) bool {
	in := func(v int64) bool {
		if dstSigned {
			return v >= dmin && v <= dmax
		}
		if !srcSigned {
			return uint64(v) <= uint64(dmax)
		}
		return v >= 0 && uint64(v) <= uint64(dmax)
	}
	return in(lo) && in(hi)
}

// hasIntType reports whether expr has an integer basic type.
func hasIntType(info *types.Info, expr ast.Expr) bool {
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	_, _, ok = intKind(tv.Type)
	return ok
}

// intKind returns bit width and signedness of an integer type.
func intKind(t types.Type) (bits int, signed bool, ok bool) {
	if t == nil {
		return 0, false, false
	}
	b, ok := t.Underlying().(*types.Basic)
	if !ok {
		return 0, false, false
	}
	info := b.Info()
	if info&types.IsInteger == 0 {
		return 0, false, false
	}
	signed = info&types.IsUnsigned == 0
	switch b.Kind() {
	case types.Int8, types.Uint8:
		bits = 8
	case types.Int16, types.Uint16:
		bits = 16
	case types.Int32, types.Uint32:
		bits = 32
	case types.Int64, types.Uint64:
		bits = 64
	case types.Uintptr:
		bits = 64
	default: // int, uint, byte, rune aliases resolve to basic kinds above
		bits = 64
	}
	return bits, signed, true
}
