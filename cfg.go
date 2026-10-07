package intervalanalysis

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
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
	// labels maps a statement label to the block starting the labeled
	// statement; gotos records goto edges in source order. Both are filled
	// during construction and resolved by wireGotos.
	labels map[string]*Block
	gotos  []gotoEdge
}

// gotoEdge is an unresolved goto: control leaves block from for label name.
type gotoEdge struct {
	from *Block
	name string
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
	g.labels = map[string]*Block{}
	b := g.buildStmtList(fn.Body.List, g.Entry, []*Block{g.Exit})
	b.addSucc(g.Exit)
	g.wireGotos()
	g.prune()
	return g
}

// wireGotos resolves every recorded goto to its label block. A goto whose
// target was created earlier is a backward jump, so the edge is marked as a
// back-edge to trigger widening and keep the fixpoint convergent. Unresolved
// labels (only possible in ill-typed input) stay sound by jumping to every
// known label, or to the exit when the function has no labels.
func (g *CFG) wireGotos() {
	for _, ge := range g.gotos {
		target, ok := g.labels[ge.name]
		if ok {
			if target.ID <= ge.from.ID {
				ge.from.addBackEdge(target)
			} else {
				ge.from.addSucc(target)
			}
			continue
		}
		if len(g.labels) == 0 {
			ge.from.addSucc(g.Exit)
			continue
		}
		names := make([]string, 0, len(g.labels))
		for name := range g.labels {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			ge.from.addSucc(g.labels[name])
		}
	}
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
	// stmtLabels are labels of directly enclosing LabeledStmts, consumed by
	// the next loop/switch/select statement (their labeled break/continue
	// targets).
	stmtLabels []string
	// lbreaks/lcontinues resolve labeled break/continue statements to the
	// targets of the loop/switch/select carrying that label.
	lbreaks    map[string][]*Block
	lcontinues map[string][]*Block
	lcontBack  map[string][]bool
}

// bindLabels returns a copy of lk with the pending statement labels bound to
// the break/continue targets of the immediately enclosing loop/switch/select.
func (lk link) bindLabels(breaks, continues []*Block, contBack []bool) link {
	out := lk
	if len(lk.stmtLabels) == 0 {
		return out
	}
	out.lbreaks = cloneBlockTargets(lk.lbreaks)
	out.lcontinues = cloneBlockTargets(lk.lcontinues)
	out.lcontBack = cloneBackTargets(lk.lcontBack)
	for _, name := range lk.stmtLabels {
		out.lbreaks[name] = breaks
		out.lcontinues[name] = continues
		out.lcontBack[name] = contBack
	}
	out.stmtLabels = nil
	return out
}

func cloneBlockTargets(m map[string][]*Block) map[string][]*Block {
	out := make(map[string][]*Block, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneBackTargets(m map[string][]bool) map[string][]bool {
	out := make(map[string][]bool, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
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
		*ast.SendStmt, *ast.GoStmt, *ast.DeferStmt, *ast.EmptyStmt:
		cur.Stmts = append(cur.Stmts, st)
		return cur

	case *ast.LabeledStmt:
		// A label starts a new block so gotos can target it; control also
		// falls through from the previous statement.
		lb := g.newBlock("label:" + s.Label.Name)
		cur.addSucc(lb)
		g.labels[s.Label.Name] = lb
		nlk := lk
		nlk.stmtLabels = append(append([]string{}, lk.stmtLabels...), s.Label.Name)
		return g.buildStmt(s.Stmt, lb, exits, nlk)

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
		brks := []*Block{after}
		conts := []*Block{continueTarget}
		backs := []bool{s.Post == nil}
		nlk := lk.bindLabels(brks, conts, backs)
		nlk.breaks = brks
		nlk.continues = conts
		nlk.contIsBack = backs
		bEnd := g.buildStmtListLk(bodyList(s.Body), bodyB, exits, nlk)
		if s.Post != nil {
			bEnd.addSucc(continueTarget)
		} else {
			bEnd.addBackEdge(head)
		}
		return after

	case *ast.RangeStmt:
		head := g.newBlock("range-head")
		// The range statement itself executes at the head: the ranged
		// expression is evaluated and the key/value variables take unknown
		// values on every iteration (handled by the transfer function).
		head.Stmts = append(head.Stmts, s)
		cur.addSucc(head)
		bodyB := g.newBlock("range-body")
		after := g.newBlock("after-range")
		head.Kind = BranchCond
		head.Cond = rangeGuard(s)
		head.addSucc(bodyB)
		head.addSucc(after)
		brks := []*Block{after}
		conts := []*Block{head}
		backs := []bool{true}
		nlk := lk.bindLabels(brks, conts, backs)
		nlk.breaks = brks
		nlk.continues = conts
		nlk.contIsBack = backs
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
		brks := []*Block{after}
		nlk := lk.bindLabels(brks, nil, nil)
		nlk.breaks = brks
		for _, st := range s.Body.List {
			cc := st.(*ast.CaseClause)
			cb := g.newBlock("typeswitch-case")
			entry.addSucc(cb)
			end := g.buildStmtListLk(cc.Body, cb, exits, nlk)
			end.addSucc(after)
		}
		entry.addSucc(after)
		return after

	case *ast.SelectStmt:
		after := g.newBlock("after-select")
		brks := []*Block{after}
		nlk := lk.bindLabels(brks, nil, nil)
		nlk.breaks = brks
		for _, st := range s.Body.List {
			comm := st.(*ast.CommClause)
			cb := g.newBlock("select-case")
			cur.addSucc(cb)
			stmts := []ast.Stmt{}
			if comm.Comm != nil {
				stmts = append(stmts, comm.Comm)
			}
			stmts = append(stmts, comm.Body...)
			end := g.buildStmtListLk(stmts, cb, exits, nlk)
			end.addSucc(after)
		}
		cur.addSucc(after)
		return after

	case *ast.BranchStmt:
		switch s.Tok {
		case token.BREAK:
			targets := lk.breaks
			if s.Label != nil {
				if lt, ok := lk.lbreaks[s.Label.Name]; ok {
					targets = lt
				}
			}
			for _, t := range targets {
				cur.addSucc(t)
			}
			if len(cur.Succs) == 0 {
				// Ill-typed break with no target: stay sound by exiting.
				for _, ex := range exits {
					cur.addSucc(ex)
				}
			}
			return g.newBlock("after-break")
		case token.CONTINUE:
			targets := lk.continues
			backs := lk.contIsBack
			if s.Label != nil {
				if lt, ok := lk.lcontinues[s.Label.Name]; ok {
					targets = lt
					backs = lk.lcontBack[s.Label.Name]
				}
			}
			for i, t := range targets {
				if i < len(backs) && backs[i] {
					cur.addBackEdge(t)
				} else {
					cur.addSucc(t)
				}
			}
			if len(cur.Succs) == 0 {
				for _, ex := range exits {
					cur.addSucc(ex)
				}
			}
			return g.newBlock("after-continue")
		case token.GOTO:
			// Resolved after the whole body is built (labels may be forward).
			g.gotos = append(g.gotos, gotoEdge{from: cur, name: s.Label.Name})
			return g.newBlock("after-goto")
		}
		// FALLTHROUGH is wired by buildSwitch; anything else falls through.
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
	// Unknown iteration condition: both the body and the after-loop edges
	// stay reachable, with no refinement on either. A constant true here
	// would make the loop-exit edge unreachable and silently skip every
	// statement after a range loop.
	return nil
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
	// break inside a case exits the switch, not an enclosing loop.
	brks := []*Block{after}
	nlk := lk.bindLabels(brks, nil, nil)
	nlk.breaks = brks
	var defaultBody []ast.Stmt
	tagExpr := tag
	tagless := tag == nil
	if tagExpr == nil {
		tagExpr = &ast.Ident{Name: "true"}
	}
	// First pass: build the condition of every case and pre-create its body
	// block, so a fallthrough at the end of a case can be wired to the body
	// of the next one.
	type caseInfo struct {
		cond  ast.Expr
		body  []ast.Stmt
		bodyB *Block
		falls bool
	}
	var cases []caseInfo
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
		caseBody, falls := stripFallthrough(cc.Body)
		cases = append(cases, caseInfo{
			cond:  joinConds(conds, token.LOR),
			body:  caseBody,
			bodyB: g.newBlock("switch-case-body"),
			falls: falls,
		})
	}
	for i, ci := range cases {
		// cur is the conditional dispatch block; bodyB the case body.
		nextB := g.newBlock("switch-next")
		cur.Kind = BranchCond
		cur.Cond = ci.cond
		cur.addSucc(ci.bodyB)
		cur.addSucc(nextB)
		end := g.buildStmtListLk(ci.body, ci.bodyB, exits, nlk)
		if ci.falls && i+1 < len(cases) {
			// fallthrough: execute the next case body unconditionally.
			end.addSucc(cases[i+1].bodyB)
		} else {
			end.addSucc(after)
		}
		cur = nextB
	}
	if defaultBody != nil {
		end := g.buildStmtListLk(defaultBody, cur, exits, nlk)
		end.addSucc(after)
	} else {
		// No default: remaining values fall through.
		cur.addSucc(after)
	}
	_ = tagExpr
	return after
}

// stripFallthrough drops a trailing fallthrough statement from a case body and
// reports its presence.
func stripFallthrough(body []ast.Stmt) ([]ast.Stmt, bool) {
	if len(body) == 0 {
		return body, false
	}
	if bs, ok := body[len(body)-1].(*ast.BranchStmt); ok && bs.Tok == token.FALLTHROUGH {
		return body[:len(body)-1], true
	}
	return body, false
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
