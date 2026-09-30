package analyzer

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"math/big"

	"github.com/guoyyit/interval-analysis-go/cfg"
	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/report"
)

// funcAnalyzer performs the abstract transfer for one function. It evaluates
// integer expressions into intervals, raises alerts for provably-possible
// faults (division by zero, index out of bounds, overflow), and refines
// states along conditional edges.
type funcAnalyzer struct {
	pkg        *pkgView
	fn         *ast.FuncDecl
	fnName     string
	graph      *cfg.CFG
	alerts     []report.Alert
	seen       map[alertKey]bool
	store      *SummaryStore
	params     map[types.Object]bool
	resultObjs []types.Object
	returnVals [][]domain.Interval
	refineFn   func(st *State, cond ast.Expr, truth bool) *State
	// arrayEls records constant-index element intervals for local arrays
	// built from composite literals. Reads at unknown indices soundly join
	// every recorded element; external/unknown arrays are not tracked.
	arrayEls   map[types.Object]map[int64]domain.Interval
	incomplete bool
}

type alertKey struct {
	pos  token.Pos
	kind report.Kind
}

// evalResult carries an interval plus type bounds for the expression.
type evalResult struct {
	iv  domain.Interval
	bnd domain.Bounds
	ok  bool
}

func topResult(bnd domain.Bounds) evalResult {
	return evalResult{iv: domain.TypeTop(bnd), bnd: bnd, ok: true}
}

func (fa *funcAnalyzer) posString(e ast.Node) string {
	return fa.pkg.fset.Position(e.Pos()).String()
}

func (fa *funcAnalyzer) reportAlert(k report.Kind, e ast.Node, condition, basis, msg string) {
	key := alertKey{pos: e.Pos(), kind: k}
	if fa.seen[key] {
		return
	}
	fa.seen[key] = true
	pos := fa.pkg.fset.Position(e.Pos())
	sev := report.DefaultSeverity(k)
	fa.alerts = append(fa.alerts, report.Alert{
		Kind:          k,
		KindName:      k.String(),
		Severity:      sev,
		SeverityName:  sev.String(),
		Position:      pos.String(),
		File:          pos.Filename,
		Line:          pos.Line,
		Column:        pos.Column,
		Function:      fa.fnName,
		Package:       fa.pkg.path,
		Condition:     condition,
		IntervalBasis: basis,
		Message:       msg,
	})
}

// boundsOf computes representable bounds for a basic integer type.
func boundsOf(t types.Type) (domain.Bounds, bool) {
	basic, ok := resolveBasic(t)
	if !ok {
		return domain.Bounds{}, false
	}
	info := basic.Info()
	signed := info&types.IsUnsigned == 0
	if info&types.IsInteger == 0 {
		return domain.Bounds{}, false
	}
	bits := basicBits(basic)
	return domain.IntBounds(bits, signed), true
}

func resolveBasic(t types.Type) (*types.Basic, bool) {
	for {
		switch tt := t.(type) {
		case *types.Basic:
			return tt, true
		case *types.Named:
			t = tt.Underlying()
		default:
			return nil, false
		}
	}
}

func isInteger(t types.Type) bool {
	_, ok := boundsOf(t)
	return ok
}

// resultBounds returns bounds for an expression, defaulting to 64-bit signed
// when the type is unavailable (keeps the analysis conservative).
func (fa *funcAnalyzer) resultBounds(e ast.Expr) domain.Bounds {
	if tv, ok := fa.pkg.info.Types[e]; ok && tv.Type != nil {
		if b, ok := boundsOf(tv.Type); ok {
			return b
		}
	}
	return domain.IntBounds(64, true)
}

func (fa *funcAnalyzer) evalExpr(e ast.Expr, st *State) evalResult {
	if e == nil {
		return evalResult{}
	}
	// Exact compiler constant.
	if tv, ok := fa.pkg.info.Types[e]; ok && tv.Value != nil {
		if b, ok := boundsOf(tv.Type); ok {
			if iv, ok := constInterval(tv.Value, b); ok {
				return evalResult{iv: iv, bnd: b, ok: true}
			}
		}
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return fa.evalExpr(x.X, st)

	case *ast.BasicLit:
		bnd := fa.resultBounds(e)
		v, ok := parseLit(x)
		if !ok {
			return topResult(bnd)
		}
		return evalResult{iv: domain.Point(v), bnd: bnd, ok: true}

	case *ast.Ident:
		bnd := fa.resultBounds(e)
		if x.Name == "_" {
			return topResult(bnd)
		}
		obj := fa.objectOf(x)
		if obj == nil {
			return topResult(bnd)
		}
		// Parameters and any unconstrained object read as full range via
		// State.Get. A global/package var or external read is likewise top.
		iv := st.Get(obj)
		iv = domain.IntersectKeepFact(iv, domain.TypeTop(bnd))
		return evalResult{iv: iv, bnd: bnd, ok: true}

	case *ast.UnaryExpr:
		return fa.evalUnary(x, st)

	case *ast.BinaryExpr:
		return fa.evalBinary(x, st)

	case *ast.CallExpr:
		return fa.evalCall(x, st)

	case *ast.IndexExpr:
		fa.checkIndex(x, st)
		bnd := fa.resultBounds(e)
		if iv, ok := fa.evalArrayElement(x, st, bnd); ok {
			return evalResult{iv: iv, bnd: bnd, ok: true}
		}
		return topResult(bnd)

	case *ast.SelectorExpr:
		bnd := fa.resultBounds(e)
		return topResult(bnd)

	case *ast.TypeAssertExpr:
		bnd := fa.resultBounds(e)
		return topResult(bnd)

	case *ast.StarExpr:
		bnd := fa.resultBounds(e)
		return topResult(bnd)

	case *ast.CompositeLit:
		bnd := fa.resultBounds(e)
		return topResult(bnd)

	case *ast.SliceExpr:
		fa.checkSlice(x, st)
		bnd := fa.resultBounds(e)
		return topResult(bnd)

	case *ast.KeyValueExpr:
		return fa.evalExpr(x.Value, st)

	default:
		bnd := fa.resultBounds(e)
		return topResult(bnd)
	}
}

func (fa *funcAnalyzer) objectOf(id *ast.Ident) types.Object {
	if fa.pkg.info.Uses != nil {
		if obj, ok := fa.pkg.info.Uses[id]; ok {
			return obj
		}
	}
	return nil
}

func (fa *funcAnalyzer) evalUnary(x *ast.UnaryExpr, st *State) evalResult {
	bnd := fa.resultBounds(x)
	switch x.Op {
	case token.SUB:
		op := fa.evalExpr(x.X, st)
		if !op.ok {
			return topResult(bnd)
		}
		res, ovf := domain.Neg(op.iv, bnd)
		if ovf {
			fa.reportAlert(report.KindIntegerOverflow, x,
				"unary minus may exceed the representable range",
				fmt.Sprintf("operand in %s, type range %s..%s", op.iv, bnd.Min, bnd.Max),
				"negation can overflow (e.g. MinInt)")
		}
		return evalResult{iv: res, bnd: bnd, ok: true}
	case token.XOR:
		// Bitwise complement: evaluate operand for checks, result full range.
		_ = fa.evalExpr(x.X, st)
		return topResult(bnd)
	case token.NOT:
		return evalResult{}
	case token.AND, token.ARROW:
		_ = fa.evalExpr(x.X, st)
		return topResult(bnd)
	}
	return topResult(bnd)
}

func (fa *funcAnalyzer) evalBinary(x *ast.BinaryExpr, st *State) evalResult {
	// Short-circuit logical ops: evaluate both sides for their checks but
	// refine for the taken side to avoid false alarms when possible.
	if x.Op == token.LAND || x.Op == token.LOR {
		fa.evalBooleanSideEffects(x.X, st)
		fa.evalBooleanSideEffects(x.Y, st)
		return evalResult{}
	}
	if isComparison(x.Op) {
		fa.evalCmpOperands(x, st)
		return evalResult{}
	}
	bnd := fa.resultBounds(x)
	l := fa.evalExpr(x.X, st)
	r := fa.evalExpr(x.Y, st)
	if !l.ok || !r.ok {
		return topResult(bnd)
	}
	var res domain.Interval
	var ovf bool
	switch x.Op {
	case token.ADD:
		res, ovf = domain.Add(l.iv, r.iv, bnd)
	case token.SUB:
		res, ovf = domain.Sub(l.iv, r.iv, bnd)
	case token.MUL:
		res, ovf = domain.Mul(l.iv, r.iv, bnd)
	case token.QUO:
		var dbz bool
		res, ovf, dbz = domain.Div(l.iv, r.iv, bnd)
		if dbz {
			fa.reportAlert(report.KindDivByZero, x,
				fmt.Sprintf("divisor range %s contains zero", r.iv),
				fmt.Sprintf("dividend in %s, divisor in %s", l.iv, r.iv),
				"integer division by zero is possible")
		}
	case token.REM:
		var dbz bool
		res, ovf, dbz = domain.Mod(l.iv, r.iv, bnd)
		if dbz {
			fa.reportAlert(report.KindDivByZero, x,
				fmt.Sprintf("modulus range %s contains zero", r.iv),
				fmt.Sprintf("operand in %s, modulus in %s", l.iv, r.iv),
				"integer modulo by zero is possible")
		}
	case token.AND, token.OR, token.XOR:
		res = domain.TypeTop(bnd)
	case token.SHL, token.SHR:
		res = domain.TypeTop(bnd)
	case token.AND_NOT:
		res = domain.TypeTop(bnd)
	default:
		res = domain.TypeTop(bnd)
	}
	if ovf {
		fa.reportAlert(report.KindIntegerOverflow, x,
			fmt.Sprintf("result of %s over operands %s and %s may leave type range",
				x.Op, l.iv, r.iv),
			fmt.Sprintf("left in %s, right in %s, type range %s..%s",
				l.iv, r.iv, bnd.Min, bnd.Max),
			"integer arithmetic may overflow")
	}
	return evalResult{iv: res, bnd: bnd, ok: true}
}

func (fa *funcAnalyzer) evalBooleanSideEffects(e ast.Expr, st *State) {
	if b, ok := e.(*ast.BinaryExpr); ok && (b.Op == token.LAND || b.Op == token.LOR) {
		fa.evalBooleanSideEffects(b.X, st)
		fa.evalBooleanSideEffects(b.Y, st)
		return
	}
	_ = fa.evalExpr(e, st)
}

func (fa *funcAnalyzer) evalCmpOperands(x *ast.BinaryExpr, st *State) {
	_ = fa.evalExpr(x.X, st)
	_ = fa.evalExpr(x.Y, st)
}

func isComparison(op token.Token) bool {
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	}
	return false
}

var _ = big.NewInt
