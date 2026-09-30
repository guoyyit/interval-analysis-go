package analyzer

import (
	"go/ast"
	"go/types"
	"math/big"

	"github.com/guoyyit/interval-analysis-go/domain"
)

func (fa *funcAnalyzer) evalCall(x *ast.CallExpr, st *State) evalResult {
	// Type conversion T(v): alert on narrowing overflow, return bounded top.
	if fa.isConversion(x) {
		fa.checkConversion(x, st)
		bnd, ok := boundsOf(fa.exprType(x))
		if !ok {
			bnd = domain.IntBounds(64, true)
		}
		// After a sound overflow alert the resulting value may have wrapped
		// to anything representable in the destination type.
		return topResult(bnd)
	}

	// Builtins.
	if id, ok := x.Fun.(*ast.Ident); ok {
		switch id.Name {
		case "len", "cap":
			return fa.evalLenCap(x, st, id.Name)
		case "append", "copy", "delete", "close", "print", "println",
			"panic", "recover":
			for _, arg := range x.Args {
				_ = fa.evalExpr(arg, st)
			}
			return evalResult{}
		case "new", "make":
			for _, arg := range x.Args {
				_ = fa.evalExpr(arg, st)
			}
			bnd := fa.resultBounds(x)
			return topResult(bnd)
		case "min":
			return fa.evalMinMax(x, st, true)
		case "max":
			return fa.evalMinMax(x, st, false)
		case "clear":
			for _, arg := range x.Args {
				_ = fa.evalExpr(arg, st)
			}
			return evalResult{}
		}
	}

	// User-defined function: use its summary when available. Arguments are
	// still evaluated so faults inside them are checked at the call site.
	var argResults []evalResult
	for _, arg := range x.Args {
		argResults = append(argResults, fa.evalExpr(arg, st))
	}
	if sum := fa.calleeSummary(x); sum != nil && len(sum.Results) > 0 {
		bnd := fa.resultBounds(x)
		return evalResult{iv: domain.Intersect(sum.Results[0], domain.TypeTop(bnd)), bnd: bnd, ok: true}
	}
	bnd := fa.resultBounds(x)
	_ = argResults
	return topResult(bnd)
}

func (fa *funcAnalyzer) exprType(e ast.Expr) types.Type {
	if tv, ok := fa.pkg.info.Types[e]; ok {
		return tv.Type
	}
	return nil
}

func (fa *funcAnalyzer) isConversion(x *ast.CallExpr) bool {
	// A call whose function is not a function-typed expression is a
	// conversion: T(v) or (T)(v).
	t := fa.exprType(x.Fun)
	if t == nil {
		return false
	}
	if _, ok := t.Underlying().(*types.Interface); ok {
		return false
	}
	// If the Fun expression itself has a type but is a *types.Func it's a
	// real call.
	if obj := fa.calleeObject(x.Fun); obj != nil {
		if _, ok := obj.(*types.Func); ok {
			return false
		}
	}
	if isInteger(t) || isBasicConvertible(t) {
		return len(x.Args) == 1
	}
	return false
}

func isBasicConvertible(t types.Type) bool {
	if b, ok := resolveBasic(t); ok {
		_ = b
		return true
	}
	return false
}

func (fa *funcAnalyzer) calleeObject(fun ast.Expr) types.Object {
	id, ok := fun.(*ast.Ident)
	if !ok {
		return nil
	}
	if fa.pkg.info.Uses == nil {
		return nil
	}
	return fa.pkg.info.Uses[id]
}

// checkConversion alerts when the source value's interval cannot be proven to
// fit in the destination type. This catches both overflowing assignments and
// the wrap that follows an out-of-range integer conversion.
func (fa *funcAnalyzer) checkConversion(x *ast.CallExpr, st *State) {
	dstBnd, dstInt := boundsOf(fa.exprType(x))
	src := fa.evalExpr(x.Args[0], st)
	if !dstInt || !src.ok || src.iv.Empty {
		return
	}
	if src.iv.Lo.Cmp(dstBnd.Min) < 0 || src.iv.Hi.Cmp(dstBnd.Max) > 0 {
		fa.reportOverflowConversion(x, src.iv, dstBnd)
	}
}

func (fa *funcAnalyzer) reportOverflowConversion(x *ast.CallExpr, src domain.Interval, dstBnd domain.Bounds) {
	alertFromConversion(fa, x, src, dstBnd)
}

func (fa *funcAnalyzer) evalLenCap(x *ast.CallExpr, st *State, builtin string) evalResult {
	bnd := domain.IntBounds(64, true)
	if len(x.Args) != 1 {
		return topResult(bnd)
	}
	arg := x.Args[0]
	_ = fa.evalExpr(arg, st)
	tv, ok := fa.pkg.info.Types[arg]
	if !ok || tv.Type == nil {
		return topResult(bnd)
	}
	switch t := tv.Type.Underlying().(type) {
	case *types.Array:
		return evalResult{iv: domain.Const(t.Len()), bnd: bnd, ok: true}
	case *types.Pointer:
		if a, ok := t.Elem().Underlying().(*types.Array); ok {
			return evalResult{iv: domain.Const(a.Len()), bnd: bnd, ok: true}
		}
	case *types.Basic:
		if t.Kind() == types.String {
			// Length unknown but non-negative: [0, MaxInt].
			return evalResult{iv: domain.BigRange(big.NewInt(0), bnd.Max), bnd: bnd, ok: true}
		}
	case *types.Slice, *types.Map, *types.Chan:
		return evalResult{iv: domain.BigRange(big.NewInt(0), bnd.Max), bnd: bnd, ok: true}
	}
	return topResult(bnd)
}

func (fa *funcAnalyzer) evalMinMax(x *ast.CallExpr, st *State, isMin bool) evalResult {
	bnd := domain.IntBounds(64, true)
	if len(x.Args) == 0 {
		return topResult(bnd)
	}
	var acc domain.Interval
	first := true
	for _, a := range x.Args {
		r := fa.evalExpr(a, st)
		if !r.ok {
			return topResult(bnd)
		}
		if b, ok := boundsOf(fa.exprType(a)); ok {
			r.bnd = b
			r.iv = domain.Intersect(r.iv, domain.TypeTop(b))
		}
		if first {
			acc = r.iv
			first = false
			continue
		}
		if isMin {
			// min(a,b) range: [min(a.lo,b.lo), min(a.hi,b.hi)]
			lo := minBig(acc.Lo, r.iv.Lo)
			hi := minBig(acc.Hi, r.iv.Hi)
			acc = domain.BigRange(lo, hi)
		} else {
			lo := maxBig(acc.Lo, r.iv.Lo)
			hi := maxBig(acc.Hi, r.iv.Hi)
			acc = domain.BigRange(lo, hi)
		}
	}
	return evalResult{iv: acc, bnd: bnd, ok: true}
}

func minBig(a, b *big.Int) *big.Int {
	if a.Cmp(b) < 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

func maxBig(a, b *big.Int) *big.Int {
	if a.Cmp(b) > 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

// calleeSummary resolves a same-package function summary, or nil when the
// callee is external/unknown (the caller then treats the result as full).
func (fa *funcAnalyzer) calleeSummary(x *ast.CallExpr) *Summary {
	obj := fa.calleeObject(x.Fun)
	if obj == nil || obj.Pkg() == nil {
		return nil
	}
	if obj.Pkg().Path() != fa.pkg.pkg.Path() {
		return nil
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return nil
	}
	key := fa.pkg.path + "#" + fn.Id()
	if fa.store == nil {
		return nil
	}
	sum, ok := fa.store.Get(key)
	if !ok || !sum.Analyzed {
		return nil
	}
	return sum
}
