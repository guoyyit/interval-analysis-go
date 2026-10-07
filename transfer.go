package intervalanalysis

import (
	"go/ast"
	"go/token"
	"go/types"
)

// transfer applies abstract semantics of one basic block to an entry state and
// returns one exit state per successor. Any operation that cannot be proven
// safe is reported through the warning sink.
type transfer struct {
	cfg      *CFG
	resolver callResolver
	funcName string
	sink     warningSink
	escapes  map[types.Object]bool
}

func newTransfer(cfg *CFG, resolver callResolver, funcName string, sink warningSink,
	escapes map[types.Object]bool) *transfer {
	return &transfer{cfg: cfg, resolver: resolver, funcName: funcName,
		sink: sink, escapes: escapes}
}

func (t *transfer) eval(st *State) *evalContext {
	return &evalContext{
		state:    st,
		info:     t.cfg.Info,
		pkg:      t.cfg.Pkg,
		resolver: t.resolver,
		sink:     t.sink,
		funcName: t.funcName,
	}
}

func (t *transfer) report(pos token.Pos, cat Category, msg, evidence string) {
	if t.sink != nil {
		t.sink.report(pos, cat, msg, evidence)
	}
}

// exec executes block statements and its branch, returning states aligned with
// block.Succs.
func (t *transfer) exec(block *Block, entry *State) []*State {
	if entry.IsBottom() {
		return []*State{BottomState}
	}
	st := entry.Copy()
	for _, stmt := range block.Stmts {
		t.execStmt(st, stmt)
		if st.IsBottom() {
			break
		}
	}
	switch block.Kind {
	case BranchReturn:
		return []*State{st}
	case BranchCond:
		yes := t.assume(st.Copy(), block.Cond, false)
		no := t.assume(st.Copy(), block.Cond, true)
		return []*State{yes, no}
	default:
		return []*State{st}
	}
}

func (t *transfer) execStmt(st *State, stmt ast.Stmt) {
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		t.execAssign(st, s)
	case *ast.IncDecStmt:
		t.execIncDec(st, s)
	case *ast.DeclStmt:
		t.execDecl(st, s)
	case *ast.ExprStmt:
		t.evalForEffects(st, s.X)
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			_, _ = t.eval(st).evalInt(r)
		}
	case *ast.RangeStmt:
		t.execRangeInit(st, s)
	case *ast.SendStmt, *ast.GoStmt, *ast.DeferStmt:
		t.havocEscaping(st)
	case *ast.LabeledStmt:
		t.execStmt(st, s.Stmt)
	}
}

func (t *transfer) execDecl(st *State, s *ast.DeclStmt) {
	gd, ok := s.Decl.(*ast.GenDecl)
	if !ok {
		return
	}
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		if len(vs.Values) == 0 {
			for _, name := range vs.Names {
				if obj := t.cfg.Info.Defs[name]; obj != nil {
					if _, _, ok := intKind(obj.Type()); ok {
						st.Set(obj, Const(0))
					}
				}
			}
			continue
		}
		for i, value := range vs.Values {
			t.evalForEffects(st, value)
			if len(vs.Values) == len(vs.Names) {
				name := vs.Names[i]
				if obj := t.cfg.Info.Defs[name]; obj != nil {
					t.initValue(st, obj, value)
				}
			} else {
				for _, name := range vs.Names {
					if obj := t.cfg.Info.Defs[name]; obj != nil {
						st.Forget(obj)
					}
				}
			}
		}
	}
}

func (t *transfer) initValue(st *State, obj types.Object, value ast.Expr) {
	if _, _, ok := intKind(obj.Type()); ok {
		if iv, ok := t.eval(st).evalInt(value); ok {
			st.Set(obj, iv)
			return
		}
		st.Forget(obj)
		return
	}
	if cl, ok := value.(*ast.CompositeLit); ok {
		if n, ok := compositeLen(t.cfg.Info, cl); ok {
			st.SetLen(obj, Const(int64(n)))
		}
	}
	if iv, ok := t.eval(st).evalBuiltinLen(value); ok {
		st.SetLen(obj, iv)
	}
}

func (t *transfer) execAssign(st *State, s *ast.AssignStmt) {
	ev := t.eval(st)
	// Index expressions on the left hand side are concrete indexing operations
	// too: a write out of bounds panics exactly like a read, so every LHS index
	// must be checked against the object length before the assignment.
	for _, lhs := range s.Lhs {
		if ix, ok := lhs.(*ast.IndexExpr); ok {
			ev.checkIndex(ix.X, ix.Index)
		}
	}
	for _, rhs := range s.Rhs {
		t.evalForEffects(st, rhs)
	}
	if len(s.Rhs) == 1 && len(s.Lhs) == 1 {
		t.assignOne(st, s.Lhs[0], s.Rhs[0])
		return
	}
	// Parallel or tuple assignment; tuple results havoc lhs.
	if len(s.Rhs) == 1 {
		t.havocEscaping(st)
	}
	values := make([]Interval, len(s.Lhs))
	for i := range s.Lhs {
		if i < len(s.Rhs) {
			values[i], _ = ev.evalInt(s.Rhs[i])
		} else {
			values[i] = TopInterval
		}
	}
	for i, lhs := range s.Lhs {
		if id, ok := lhs.(*ast.Ident); ok {
			if obj := t.cfg.Info.ObjectOf(id); obj != nil {
				if _, _, isInt := intKind(obj.Type()); isInt {
					st.Set(obj, values[i])
					continue
				}
			}
		}
		t.havocLHS(st, lhs)
	}
}

func (t *transfer) assignOne(st *State, lhs, rhs ast.Expr) {
	if id, ok := lhs.(*ast.Ident); ok {
		obj := t.cfg.Info.ObjectOf(id)
		if obj != nil {
			if _, _, isInt := intKind(obj.Type()); isInt {
				if iv, ok := t.eval(st).evalInt(rhs); ok {
					st.Set(obj, iv)
					return
				}
				st.Forget(obj)
				return
			}
			if cl, ok := rhs.(*ast.CompositeLit); ok {
				if n, ok := compositeLen(t.cfg.Info, cl); ok {
					st.SetLen(obj, Const(int64(n)))
					return
				}
			}
			if iv, ok := t.eval(st).evalBuiltinLen(rhs); ok {
				st.SetLen(obj, iv)
				return
			}
		}
	}
	t.havocLHS(st, lhs)
}

func (t *transfer) havocLHS(st *State, lhs ast.Expr) {
	switch x := lhs.(type) {
	case *ast.Ident:
		if obj := t.cfg.Info.ObjectOf(x); obj != nil {
			if _, _, ok := intKind(obj.Type()); ok {
				st.Forget(obj)
			} else {
				st.ForgetLen(obj)
			}
		}
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if obj := t.cfg.Info.ObjectOf(id); obj != nil {
				st.ForgetLen(obj)
			}
		}
		t.havocEscaping(st)
	case *ast.StarExpr, *ast.SelectorExpr:
		t.havocEscaping(st)
	}
}

func (t *transfer) execIncDec(st *State, s *ast.IncDecStmt) {
	if ix, ok := s.X.(*ast.IndexExpr); ok {
		// a[i]++/-- still evaluates i as an index and can panic out of bounds.
		t.eval(st).checkIndex(ix.X, ix.Index)
		return
	}
	id, ok := s.X.(*ast.Ident)
	if !ok {
		return
	}
	obj := t.cfg.Info.ObjectOf(id)
	if obj == nil {
		return
	}
	bits, signed, isInt := intKind(obj.Type())
	if !isInt {
		return
	}
	cur := st.Get(obj)
	var (
		res      Interval
		overflow bool
	)
	if s.Tok == token.INC {
		res, overflow = Add(cur, Const(1), bits, signed)
	} else {
		res, overflow = Sub(cur, Const(1), bits, signed)
	}
	if overflow {
		t.report(s.Pos(), CategoryIntegerOverflow,
			"increment/decrement may overflow",
			"current range "+cur.String())
	}
	st.Set(obj, res)
}

func (t *transfer) execRangeInit(st *State, s *ast.RangeStmt) {
	havoc := func(e ast.Expr) {
		if id, ok := e.(*ast.Ident); ok && id.Name != "_" {
			if obj := t.cfg.Info.ObjectOf(id); obj != nil {
				st.Forget(obj)
			}
		}
	}
	havoc(s.Key)
	havoc(s.Value)
}

func (t *transfer) evalForEffects(st *State, expr ast.Expr) {
	ev := t.eval(st)
	ast.Inspect(expr, func(n ast.Node) bool {
		if ix, ok := n.(*ast.IndexExpr); ok {
			ev.checkIndex(ix.X, ix.Index)
		}
		return true
	})
}

func (t *transfer) havocEscaping(st *State) {
	for obj := range t.escapes {
		if _, _, ok := intKind(obj.Type()); ok {
			st.Forget(obj)
		} else {
			st.ForgetLen(obj)
		}
	}
}

// assume filters a state by cond being true (negate=false) or false.
// An impossible condition yields bottom.
func (t *transfer) assume(st *State, cond ast.Expr, negate bool) *State {
	if st.IsBottom() {
		return st
	}
	switch c := cond.(type) {
	case *ast.ParenExpr:
		return t.assume(st, c.X, negate)
	case *ast.Ident:
		switch c.Name {
		case "true":
			if negate {
				return BottomState
			}
		case "false":
			if !negate {
				return BottomState
			}
		}
		return st
	case *ast.UnaryExpr:
		if c.Op == token.NOT {
			return t.assume(st, c.X, !negate)
		}
	case *ast.BinaryExpr:
		switch c.Op {
		case token.LAND:
			if negate {
				s1 := t.assume(st.Copy(), c.X, true)
				s2 := t.assume(st.Copy(), c.Y, true)
				return StateJoin(s1, s2)
			}
			return t.assume(t.assume(st, c.X, false), c.Y, false)
		case token.LOR:
			if negate {
				return t.assume(t.assume(st, c.X, true), c.Y, true)
			}
			s1 := t.assume(st.Copy(), c.X, false)
			s2 := t.assume(st.Copy(), c.Y, false)
			return StateJoin(s1, s2)
		default:
			return t.assumeComparison(st, c, negate)
		}
	}
	return st
}

func (t *transfer) assumeComparison(st *State, c *ast.BinaryExpr, negate bool) *State {
	op := c.Op
	if negate {
		op = negateCmpOp(op)
	}
	if !isRelational(op) {
		return st
	}
	t.refineSide(st, c.X, c.Y, op)
	t.refineSide(st, c.Y, c.X, swapCmpOp(op))
	return st
}

func isRelational(op token.Token) bool {
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	}
	return false
}

func negateCmpOp(op token.Token) token.Token {
	switch op {
	case token.EQL:
		return token.NEQ
	case token.NEQ:
		return token.EQL
	case token.LSS:
		return token.GEQ
	case token.LEQ:
		return token.GTR
	case token.GTR:
		return token.LEQ
	case token.GEQ:
		return token.LSS
	}
	return op
}

func swapCmpOp(op token.Token) token.Token {
	switch op {
	case token.LSS:
		return token.GTR
	case token.LEQ:
		return token.GEQ
	case token.GTR:
		return token.LSS
	case token.GEQ:
		return token.LEQ
	}
	return op
}

func (t *transfer) refineSide(st *State, left, right ast.Expr, op token.Token) {
	id, ok := left.(*ast.Ident)
	if !ok {
		return
	}
	obj := t.cfg.Info.ObjectOf(id)
	if obj == nil {
		return
	}
	bits, signed, isInt := intKind(obj.Type())
	if !isInt {
		return
	}
	rhs, ok := t.eval(st).evalInt(right)
	if !ok {
		return
	}
	cur := st.Get(obj)
	refined := Refine(cur, op, rhs, bits, signed)
	if refined.IsEmpty() {
		*st = *BottomState
		return
	}
	st.Set(obj, refined)
}

// compositeLen reports the static element count of an array literal.
func compositeLen(info *types.Info, cl *ast.CompositeLit) (int, bool) {
	switch at := info.TypeOf(cl).(type) {
	case *types.Array:
		return int(at.Len()), true
	case *types.Slice:
		// [...]T style is illegal for slices; use the literal element count,
		// which is a lower bound sound for subsequent reads up to that index.
		_ = at
		return len(cl.Elts), true
	}
	return 0, false
}
