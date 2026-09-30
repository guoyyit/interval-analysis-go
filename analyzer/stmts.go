package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/guoyyit/interval-analysis-go/cfg"
	"github.com/guoyyit/interval-analysis-go/domain"
)

// transferBlock executes all statements of a basic block on a copy of the
// incoming state and returns the outgoing state. Alerts raised here are
// recorded on the funcAnalyzer (and de-duplicated per location).
func (fa *funcAnalyzer) transferBlock(graph *cfg.CFG, b *cfg.Block, in *State) *State {
	if in == nil {
		return nil
	}
	st := in.Copy()
	if st == nil {
		return nil
	}
	for _, stmt := range b.Stmts {
		st = fa.transferStmt(stmt, st)
		if st == nil {
			return nil
		}
	}
	return st
}

func (fa *funcAnalyzer) transferStmt(stmt ast.Stmt, st *State) *State {
	switch s := stmt.(type) {
	case *ast.DeclStmt:
		return fa.transferDecl(s, st)
	case *ast.AssignStmt:
		return fa.transferAssign(s, st)
	case *ast.IncDecStmt:
		return fa.transferIncDec(s, st)
	case *ast.ExprStmt:
		_ = fa.evalExpr(s.X, st)
		return st
	case *ast.ReturnStmt:
		var vals []domain.Interval
		for _, r := range s.Results {
			res := fa.evalExpr(r, st)
			if res.ok {
				vals = append(vals, res.iv)
			} else {
				vals = append(vals, domain.TopValue())
			}
		}
		if s.Results != nil {
			fa.returnVals = append(fa.returnVals, vals)
		}
		return st
	case *ast.SendStmt:
		_ = fa.evalExpr(s.Value, st)
		_ = fa.evalExpr(s.Chan, st)
		return st
	case *ast.GoStmt:
		_ = fa.evalExpr(s.Call, st)
		return st
	case *ast.DeferStmt:
		_ = fa.evalExpr(s.Call, st)
		return st
	case *ast.EmptyStmt:
		return st
	case *ast.BlockStmt:
		return fa.transferSeq(s.List, st)
	default:
		// Labeled / branch / select: conservatively return state unchanged.
		return st
	}
}

func (fa *funcAnalyzer) transferSeq(list []ast.Stmt, st *State) *State {
	for _, s := range list {
		st = fa.transferStmt(s, st)
		if st == nil {
			return nil
		}
	}
	return st
}

func (fa *funcAnalyzer) transferDecl(s *ast.DeclStmt, st *State) *State {
	gd, ok := s.Decl.(*ast.GenDecl)
	if !ok {
		return st
	}
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range vs.Names {
			obj := fa.defObject(name)
			if obj == nil {
				continue
			}
			if vs.Type != nil {
				// var x T: uninitialized -> full range of T (external rule).
				if bnd, ok := boundsOf(fa.typeExpr(vs.Type)); ok {
					st.Set(obj, domain.TypeTop(bnd))
					continue
				}
			}
			if i < len(vs.Values) {
				r := fa.evalExpr(vs.Values[i], st)
				if r.ok {
					st.Set(obj, r.iv)
					if lit, ok := vs.Values[i].(*ast.CompositeLit); ok {
						fa.registerArrayLiteral(obj, lit, st)
					}
				} else if bnd, ok := boundsOf(obj.Type()); ok {
					st.Set(obj, domain.TypeTop(bnd))
				}
			} else if bnd, ok := boundsOf(obj.Type()); ok {
				st.Set(obj, domain.TypeTop(bnd))
			}
		}
	}
	return st
}

func (fa *funcAnalyzer) defObject(id *ast.Ident) types.Object {
	if fa.pkg.info.Defs != nil {
		if obj, ok := fa.pkg.info.Defs[id]; ok {
			return obj
		}
	}
	return nil
}

func (fa *funcAnalyzer) typeExpr(e ast.Expr) types.Type {
	if tv, ok := fa.pkg.info.Types[e]; ok {
		return tv.Type
	}
	return nil
}

func (fa *funcAnalyzer) transferAssign(s *ast.AssignStmt, st *State) *State {
	// Compound assignment l op= r behaves as l = l op r (and for l[i] op= r
	// the index is evaluated on the read side too).
	if s.Tok != token.ASSIGN && s.Tok != token.DEFINE {
		return fa.transferCompound(s, st)
	}
	// Evaluate every RHS first (left-to-right), then bind LHS, mirroring Go's
	// assignment semantics and keeping multi-return calls conservative.
	var results []evalResult
	for _, rhs := range s.Rhs {
		results = append(results, fa.evalExpr(rhs, st))
	}
	if len(s.Lhs) == 1 && len(s.Rhs) == 1 {
		fa.bindLHS(s.Lhs[0], results[0], st)
		if id, ok := s.Lhs[0].(*ast.Ident); ok {
			if obj := fa.assignObject(id); obj != nil {
				if lit, ok := s.Rhs[0].(*ast.CompositeLit); ok {
					fa.registerArrayLiteral(obj, lit, st)
				}
			}
		}
		return st
	}
	// Multi-value assignment: bind each LHS from the matching RHS when known.
	for i, lhs := range s.Lhs {
		if i < len(results) && results[i].ok {
			fa.bindLHS(lhs, results[i], st)
		} else {
			fa.killLHS(lhs, st)
		}
	}
	return st
}

func (fa *funcAnalyzer) bindLHS(lhs ast.Expr, r evalResult, st *State) {
	switch l := lhs.(type) {
	case *ast.Ident:
		if l.Name == "_" {
			return
		}
		obj := fa.assignObject(l)
		if obj == nil {
			return
		}
		iv := r.iv
		if bnd, ok := boundsOf(obj.Type()); ok && r.ok {
			iv = domain.Intersect(r.iv, domain.TypeTop(bnd))
		}
		st.Set(obj, iv)
	default:
		// x.f = ..., a[i] = ...: no interval model for fields/elements;
		// evaluate any index expression for OOB checks then kill nothing.
		if ix, ok := l.(*ast.IndexExpr); ok {
			_ = fa.evalExpr(ix.Index, st)
		}
	}
}

func (fa *funcAnalyzer) killLHS(lhs ast.Expr, st *State) {
	if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
		if obj := fa.assignObject(id); obj != nil {
			if bnd, ok := boundsOf(obj.Type()); ok {
				st.Set(obj, domain.TypeTop(bnd))
			}
		}
	}
}

// transferCompound implements l op= r. Only identifier left-hand sides carry
// an interval; index left-hand sides are checked for OOB but cannot be
// updated element-wise, so their container is left unchanged (conservative).
func (fa *funcAnalyzer) transferCompound(s *ast.AssignStmt, st *State) *State {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		for _, r := range s.Rhs {
			_ = fa.evalExpr(r, st)
		}
		return st
	}
	lhs, rhs := s.Lhs[0], s.Rhs[0]
	r := fa.evalExpr(rhs, st)
	if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
		obj := fa.assignObject(id)
		if obj != nil {
			bnd, isInt := boundsOf(obj.Type())
			if isInt && r.ok {
				cur := st.GetBounded(obj, bnd)
				res, _ := fa.applyBinaryOp(s.Tok, cur, r.iv, bnd, s)
				st.Set(obj, res)
				return st
			}
		}
	}
	if ix, ok := lhs.(*ast.IndexExpr); ok {
		_ = fa.evalExpr(ix.Index, st)
	}
	return st
}

// applyBinaryOp maps an assignment token to the matching integer domain op.
func (fa *funcAnalyzer) applyBinaryOp(tok token.Token, a, b domain.Interval, bnd domain.Bounds, at ast.Node) (domain.Interval, bool) {
	switch tok {
	case token.ADD_ASSIGN:
		res, ovf := domain.Add(a, b, bnd)
		fa.maybeArithmeticOverflow(at, ovf, a, b, bnd, "+=")
		return res, ovf
	case token.SUB_ASSIGN:
		res, ovf := domain.Sub(a, b, bnd)
		fa.maybeArithmeticOverflow(at, ovf, a, b, bnd, "-=")
		return res, ovf
	case token.MUL_ASSIGN:
		res, ovf := domain.Mul(a, b, bnd)
		fa.maybeArithmeticOverflow(at, ovf, a, b, bnd, "*=")
		return res, ovf
	case token.QUO_ASSIGN:
		res, ovf, dbz := domain.Div(a, b, bnd)
		if dbz {
			fa.reportCompoundDivZero(at, b, a, "/=")
		}
		fa.maybeArithmeticOverflow(at, ovf, a, b, bnd, "/=")
		return res, ovf || dbz
	case token.REM_ASSIGN:
		res, ovf, dbz := domain.Mod(a, b, bnd)
		if dbz {
			fa.reportCompoundDivZero(at, b, a, "%=")
		}
		fa.maybeArithmeticOverflow(at, ovf, a, b, bnd, "%=")
		return res, ovf || dbz
	case token.AND_ASSIGN, token.OR_ASSIGN, token.XOR_ASSIGN,
		token.SHL_ASSIGN, token.SHR_ASSIGN, token.AND_NOT_ASSIGN:
		return domain.TypeTop(bnd), false
	}
	return domain.TypeTop(bnd), false
}

func (fa *funcAnalyzer) assignObject(id *ast.Ident) types.Object {
	if obj, ok := fa.pkg.info.Defs[id]; ok && obj != nil {
		return obj
	}
	if obj, ok := fa.pkg.info.Uses[id]; ok {
		return obj
	}
	return nil
}

func (fa *funcAnalyzer) transferIncDec(s *ast.IncDecStmt, st *State) *State {
	id, ok := s.X.(*ast.Ident)
	if !ok {
		_ = fa.evalExpr(s.X, st)
		return st
	}
	obj := fa.assignObject(id)
	if obj == nil {
		return st
	}
	bnd, ok := boundsOf(obj.Type())
	if !ok {
		return st
	}
	cur := st.GetBounded(obj, bnd)
	one := domain.Const(1)
	var res domain.Interval
	var ovf bool
	if s.Tok == token.INC {
		res, ovf = domain.Add(cur, one, bnd)
	} else {
		res, ovf = domain.Sub(cur, one, bnd)
	}
	if ovf {
		fa.reportIncDecOverflow(s, cur, bnd)
	}
	st.Set(obj, res)
	return st
}

// refine returns a copy of st constrained by assuming cond evaluates to
// truth. It is the data-flow counterpart of a conditional CFG edge and is the
// mechanism that ties control flow to the interval analysis (so guarded
// divisions, indexed loops and bounded arrays are tracked, not guessed from a
// single expression).
func (fa *funcAnalyzer) refine(st *State, cond ast.Expr, truth bool) *State {
	if st == nil || cond == nil {
		return st
	}
	out := st.Copy()
	fa.refineInto(out, cond, truth)
	return out
}

func (fa *funcAnalyzer) refineInto(st *State, cond ast.Expr, truth bool) {
	switch c := cond.(type) {
	case *ast.ParenExpr:
		fa.refineInto(st, c.X, truth)
	case *ast.UnaryExpr:
		if c.Op == token.NOT {
			fa.refineInto(st, c.X, !truth)
		}
	case *ast.BinaryExpr:
		switch c.Op {
		case token.LAND:
			if truth {
				fa.refineInto(st, c.X, true)
				fa.refineInto(st, c.Y, true)
			} else {
				// x && y false: cannot assign sides uniquely; be conservative
				// but still refine each side under the other being true path
				// is not representable with one state, so skip.
			}
		case token.LOR:
			if !truth {
				fa.refineInto(st, c.X, false)
				fa.refineInto(st, c.Y, false)
			}
		default:
			if isComparison(c.Op) {
				fa.refineComparison(st, c, truth)
			}
		}
	}
}

func (fa *funcAnalyzer) refineComparison(st *State, c *ast.BinaryExpr, truth bool) {
	// Support var op constant, constant op var, and var op var with interval
	// refinement. Non-constant sides we cannot map to a variable are ignored.
	lr := fa.evalExpr(c.X, st)
	rr := fa.evalExpr(c.Y, st)
	if !lr.ok || !rr.ok {
		return
	}
	nl, nr := domain.RefineCmp(lr.iv, rr.iv, c.Op, truth)
	fa.applyRefined(st, c.X, nl, lr.bnd)
	fa.applyRefined(st, c.Y, nr, rr.bnd)
	// Special-case divisor-guard: `d != 0` true must remove zero where
	// representable; RefineNonZero handles singleton-zero elimination.
	if c.Op == token.NEQ {
		if isZeroLiteral(c.Y) {
			if id, ok := c.X.(*ast.Ident); ok {
				if obj := fa.objectOf(id); obj != nil {
					b := lr.bnd
					cur := st.GetBounded(obj, b)
					st.Set(obj, domain.RefineNonZero(cur, truth))
				}
			}
		} else if isZeroLiteral(c.X) {
			if id, ok := c.Y.(*ast.Ident); ok {
				if obj := fa.objectOf(id); ok {
					b := rr.bnd
					cur := st.GetBounded(obj, b)
					st.Set(obj, domain.RefineNonZero(cur, truth))
				}
			}
		}
	}
}

func (fa *funcAnalyzer) applyRefined(st *State, e ast.Expr, iv domain.Interval, bnd domain.Bounds) {
	if iv.Empty {
		// Mark unreachability by forcing an empty interval on a tracked
		// variable; transfer/join propagate emptiness conservatively.
		if id, ok := e.(*ast.Ident); ok {
			if obj := fa.objectOf(id); obj != nil {
				st.Set(obj, domain.EmptyValue())
			}
		}
		return
	}
	id, ok := e.(*ast.Ident)
	if !ok {
		return
	}
	obj := fa.objectOf(id)
	if obj == nil {
		return
	}
	cur := st.Get(obj)
	refined := domain.Intersect(cur, iv)
	refined = domain.Intersect(refined, domain.TypeTop(bnd))
	// Preserve relational facts (e.g. != 0) produced by the guard even when
	// the endpoint intersection cannot express them.
	refined.NonZero = refined.NonZero || iv.NonZero
	st.Set(obj, refined)
}

func isZeroLiteral(e ast.Expr) bool {
	bl, ok := e.(*ast.BasicLit)
	return ok && bl.Kind == token.INT && bl.Value == "0"
}
