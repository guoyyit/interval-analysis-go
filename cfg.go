package intervalanalysis

import (
	"go/ast"
	"go/token"
	"go/types"
)

// BranchKind describes how control leaves a basic block.
type BranchKind int

const (
	BranchNone BranchKind = iota
	BranchCond
	BranchReturn
)

// Block is a basic block: a linear run of statements followed by an optional
// branch. Succs[0] is taken when Cond holds; Succs[1] (if present) otherwise.
type Block struct {
	ID      int
	Stmts   []ast.Stmt
	Cond    ast.Expr
	Succs   []*Block
	Kind    BranchKind
	Comment string
	// BackEdge marks successor edges that are loop back-edges (only widening
	// applied on these edges).
	BackEdge []bool
}

func (b *Block) addSucc(s *Block) {
	if s == nil {
		return
	}
	b.Succs = append(b.Succs, s)
	b.BackEdge = append(b.BackEdge, false)
}

func (b *Block) addBackEdge(s *Block) {
	if s == nil {
		return
	}
	b.Succs = append(b.Succs, s)
	b.BackEdge = append(b.BackEdge, true)
}

// CFG is the control-flow graph of one function.
type CFG struct {
	Entry  *Block
	Exit   *Block
	Blocks []*Block
	Fset   *token.FileSet
	Info   *types.Info
	Pkg    *types.Package
	Func   *ast.FuncDecl
}

// BuildCFG constructs a CFG for fn. Every statement is reachable through the
// graph; unsupported control constructs get conservative edges so no path is
// ever lost.
func BuildCFG(fset *token.FileSet, info *types.Info, pkg *types.Package, fn *ast.FuncDecl) *CFG {
	g := &CFG{Fset: fset, Info: info, Pkg: pkg, Func: fn}
	if fn.Body == nil {
		g.Entry = g.newBlock("entry/extern")
		g.Exit = g.Entry
		return g
	}
	g.Entry = g.newBlock("entry")
	g.Exit = g.newBlock("exit")
	b := g.buildStmtList(fn.Body.List, g.Entry, []*Block{g.Exit})
	b.addSucc(g.Exit)
	g.prune()
	return g
}

func (g *CFG) newBlock(comment string) *Block {
	b := &Block{ID: len(g.Blocks), Comment: comment}
	g.Blocks = append(g.Blocks, b)
	return b
}

// link records pending control-transfer targets.
type link struct {
	breaks     []*Block
	continues  []*Block
	contIsBack []bool
}

// buildStmtList appends stmts to cur and returns the block reached after the
// list falls through. exits are normal function-exit merge targets.
func (g *CFG) buildStmtList(stmts []ast.Stmt, cur *Block, exits []*Block) *Block {
	for _, st := range stmts {
		cur = g.buildStmt(st, cur, exits, link{})
	}
	return cur
}

func (g *CFG) buildStmtListLk(stmts []ast.Stmt, cur *Block, exits []*Block, lk link) *Block {
	for _, st := range stmts {
		cur = g.buildStmt(st, cur, exits, lk)
	}
	return cur
}

func (g *CFG) buildStmt(st ast.Stmt, cur *Block, exits []*Block, lk link) *Block {
	switch s := st.(type) {
	case *ast.DeclStmt, *ast.AssignStmt, *ast.IncDecStmt,
		*ast.SendStmt, *ast.GoStmt, *ast.DeferStmt, *ast.EmptyStmt,
		*ast.LabeledStmt:
		if ls, ok := st.(*ast.LabeledStmt); ok {
			return g.buildStmt(ls.Stmt, cur, exits, lk)
		}
		cur.Stmts = append(cur.Stmts, st)
		return cur

	case *ast.ExprStmt:
		cur.Stmts = append(cur.Stmts, st)
		return cur

	case *ast.ReturnStmt:
		cur.Stmts = append(cur.Stmts, st)
		cur.Kind = BranchReturn
		for _, ex := range exits {
			cur.addSucc(ex)
		}
		return g.newBlock("after-return")

	case *ast.BlockStmt:
		return g.buildStmtListLk(s.List, cur, exits, lk)

	case *ast.IfStmt:
		if s.Init != nil {
			cur.Stmts = append(cur.Stmts, s.Init)
		}
		thenB := g.newBlock("then")
		elseB := g.newBlock("else")
		after := g.newBlock("after-if")
		cur.Kind = BranchCond
		cur.Cond = s.Cond
		cur.addSucc(thenB)
		cur.addSucc(elseB)
		tEnd := g.buildStmtListLk(bodyList(s.Body), thenB, exits, lk)
		tEnd.addSucc(after)
		eEnd := elseB
		if s.Else != nil {
			eEnd = g.buildStmt(s.Else, elseB, exits, lk)
		}
		eEnd.addSucc(after)
		return after

	case *ast.ForStmt:
		if s.Init != nil {
			cur.Stmts = append(cur.Stmts, s.Init)
		}
		head := g.newBlock("for-head")
		cur.addSucc(head)
		bodyB := g.newBlock("for-body")
		after := g.newBlock("after-for")
		if s.Cond != nil {
			head.Kind = BranchCond
			head.Cond = s.Cond
			head.addSucc(bodyB)
			head.addSucc(after)
		} else {
			head.addSucc(bodyB)
		}
		continueTarget := head
		if s.Post != nil {
			post := g.newBlock("for-post")
			post.Stmts = append(post.Stmts, s.Post)
			post.addBackEdge(head)
			continueTarget = post
		}
		nlk := link{
			breaks:     []*Block{after},
			continues:  []*Block{continueTarget},
			contIsBack: []bool{s.Post == nil},
		}
		bEnd := g.buildStmtListLk(bodyList(s.Body), bodyB, exits, nlk)
		if s.Post != nil {
			bEnd.addSucc(continueTarget)
		} else {
			bEnd.addBackEdge(head)
		}
		return after

	case *ast.RangeStmt:
		head := g.newBlock("range-head")
		cur.addSucc(head)
		bodyB := g.newBlock("range-body")
		after := g.newBlock("after-range")
		head.Kind = BranchCond
		head.Cond = rangeGuard(s)
		head.addSucc(bodyB)
		head.addSucc(after)
		nlk := link{breaks: []*Block{after}, continues: []*Block{head}, contIsBack: []bool{true}}
		bEnd := g.buildStmtListLk(bodyList(s.Body), bodyB, exits, nlk)
		bEnd.addBackEdge(head)
		return after

	case *ast.SwitchStmt:
		return g.buildSwitch(s.Init, s.Tag, s.Body, cur, exits, lk)

	case *ast.TypeSwitchStmt:
		// No interval information for type switches; conservatively flow into
		// every case and through after.
		after := g.newBlock("after-typeswitch")
		entry := cur
		if s.Init != nil {
			entry.Stmts = append(entry.Stmts, s.Init)
		}
		if s.Assign != nil {
			entry.Stmts = append(entry.Stmts, s.Assign)
		}
		var prev *Block
		for _, st := range s.Body.List {
			cc := st.(*ast.CaseClause)
			cb := g.newBlock("typeswitch-case")
			entry.addSucc(cb)
			end := g.buildStmtListLk(cc.Body, cb, exits, lk)
			end.addSucc(after)
			prev = cb
		}
		_ = prev
		entry.addSucc(after)
		return after

	case *ast.SelectStmt:
		after := g.newBlock("after-select")
		for _, st := range s.Body.List {
			comm := st.(*ast.CommClause)
			cb := g.newBlock("select-case")
			cur.addSucc(cb)
			stmts := []ast.Stmt{}
			if comm.Comm != nil {
				stmts = append(stmts, comm.Comm)
			}
			stmts = append(stmts, comm.Body...)
			end := g.buildStmtListLk(stmts, cb, exits, lk)
			end.addSucc(after)
		}
		cur.addSucc(after)
		return after

	case *ast.BranchStmt:
		switch s.Tok {
		case token.BREAK:
			for _, t := range lk.breaks {
				cur.addSucc(t)
			}
			return g.newBlock("after-break")
		case token.CONTINUE:
			for i, t := range lk.continues {
				if i < len(lk.contIsBack) && lk.contIsBack[i] {
					cur.addBackEdge(t)
				} else {
					cur.addSucc(t)
				}
			}
			return g.newBlock("after-continue")
		}
		return cur

	default:
		// Unrecognized statement: keep it and fall through (sound).
		cur.Stmts = append(cur.Stmts, st)
		return cur
	}
}

func bodyList(b *ast.BlockStmt) []ast.Stmt {
	if b == nil {
		return nil
	}
	return b.List
}

func rangeGuard(s *ast.RangeStmt) ast.Expr {
	// Placeholder condition: true while iterating. Precise range widening of the
	// key/value is handled by havocing them in the transfer function.
	return &ast.Ident{Name: "true"}
}

func (g *CFG) buildSwitch(init ast.Stmt, tag ast.Expr, body *ast.BlockStmt,
	cur *Block, exits []*Block, lk link) *Block {
	if init != nil {
		cur.Stmts = append(cur.Stmts, init)
	}
	after := g.newBlock("after-switch")
	if body == nil {
		cur.addSucc(after)
		return after
	}
	var defaultBody []ast.Stmt
	tagExpr := tag
	tagless := tag == nil
	if tagExpr == nil {
		tagExpr = &ast.Ident{Name: "true"}
	}
	var caseConds []ast.Expr
	for _, st := range body.List {
		cc := st.(*ast.CaseClause)
		if len(cc.List) == 0 {
			defaultBody = cc.Body
			continue
		}
		conds := make([]ast.Expr, 0, len(cc.List))
		for _, val := range cc.List {
			if tagless {
				conds = append(conds, val)
			} else {
				conds = append(conds, &ast.BinaryExpr{X: tagExpr, Op: token.EQL, Y: val})
			}
		}
		cond := joinConds(conds, token.LOR)
		caseConds = append(caseConds, cond)
		// cur is the conditional dispatch block; bodyB the case body.
		bodyB := g.newBlock("switch-case-body")
		nextB := g.newBlock("switch-next")
		cur.Kind = BranchCond
		cur.Cond = cond
		cur.addSucc(bodyB)
		cur.addSucc(nextB)
		end := g.buildStmtListLk(cc.Body, bodyB, exits, lk)
		end.addSucc(after)
		cur = nextB
	}
	if defaultBody != nil {
		end := g.buildStmtListLk(defaultBody, cur, exits, lk)
		end.addSucc(after)
	} else {
		// No default: remaining values fall through.
		cur.addSucc(after)
	}
	_ = tagExpr
	return after
}

func joinConds(cs []ast.Expr, op token.Token) ast.Expr {
	if len(cs) == 0 {
		return &ast.Ident{Name: "true"}
	}
	r := cs[0]
	for _, c := range cs[1:] {
		r = &ast.BinaryExpr{X: r, Op: op, Y: c}
	}
	return r
}

// prune removes blocks unreachable from entry (e.g. synthetic blocks created
// after terminators) while preserving the exit block.
func (g *CFG) prune() {
	reachable := map[*Block]bool{}
	var walk func(b *Block)
	walk = func(b *Block) {
		if b == nil || reachable[b] {
			return
		}
		reachable[b] = true
		for _, s := range b.Succs {
			walk(s)
		}
	}
	walk(g.Entry)
	kept := g.Blocks[:0]
	for _, b := range g.Blocks {
		if reachable[b] {
			kept = append(kept, b)
		}
	}
	g.Blocks = kept
	for i, b := range g.Blocks {
		b.ID = i
	}
}
