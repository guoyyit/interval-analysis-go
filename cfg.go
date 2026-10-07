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
	// labels maps a label name to the block that control enters when jumping to
	// it. Every reachable path created by a goto must be analyzed, so goto edges
	// point here instead of letting control silently fall through.
	labels map[string]*Block
	// labelPos records the source position of each label, used to tell backward
	// jumps (potential loops) from forward jumps.
	labelPos map[string]token.Pos
	// labelOrder records label names in source order for deterministic output.
	labelOrder []*ast.LabeledStmt
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
	g.collectLabels(fn.Body)
	b := g.buildStmtList(fn.Body.List, g.Entry, []*Block{g.Exit})
	b.addSucc(g.Exit)
	g.prune()
	return g
}

// collectLabels pre-scans the function body and creates the jump-target block
// for every labeled statement. The target block is the entry of the labeled
// statement; normal fall-through and goto edges both converge on it, so no
// path is lost regardless of how the label is reached.
func (g *CFG) collectLabels(body *ast.BlockStmt) {
	g.labels = map[string]*Block{}
	g.labelPos = map[string]token.Pos{}
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		ls, ok := n.(*ast.LabeledStmt)
		if !ok {
			return true
		}
		if _, exists := g.labels[ls.Label.Name]; !exists {
			g.labels[ls.Label.Name] = g.newBlock("label:" + ls.Label.Name)
			g.labelPos[ls.Label.Name] = ls.Pos()
			g.labelOrder = append(g.labelOrder, ls)
		}
		return true
	})
}

// labelTarget returns the jump-target block of a labeled statement.
func (g *CFG) labelTarget(ls *ast.LabeledStmt) *Block {
	if t := g.labels[ls.Label.Name]; t != nil {
		return t
	}
	t := g.newBlock("label:" + ls.Label.Name)
	g.labels[ls.Label.Name] = t
	g.labelPos[ls.Label.Name] = ls.Pos()
	return t
}

// isBackward reports whether a goto jumps to a label earlier in source order;
// such an edge closes a cycle and is treated as a loop back edge.
func (g *CFG) isBackward(gt *ast.BranchStmt, target *Block) bool {
	lp := g.labelPos[gt.Label.Name]
	return lp.IsValid() && lp < gt.Pos()
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
	// namedBreaks/namedContinues resolve labeled break/continue to enclosing
	// for/switch/range blocks. Maps are inherited (shared) across nesting.
	namedBreaks       map[string]*Block
	namedContinues    map[string]*Block
	namedContIsBack   map[string]bool
	enclosingLabel    string
	fallthroughTarget *Block
}

func (lk link) withEnclosingLabel(name string) link {
	out := lk
	out.enclosingLabel = name
	return out
}

// bindNamed registers name as a break/continue target of the innermost
// directly-labeled control construct (if any).
func (lk link) bindNamed(name string, brk, cont *Block, contBack bool) link {
	out := lk
	if name == "" {
		return out
	}
	out.namedBreaks = copyBlockMap(lk.namedBreaks)
	out.namedBreaks[name] = brk
	if cont != nil {
		out.namedContinues = copyBlockMap(lk.namedContinues)
		out.namedContinues[name] = cont
		out.namedContIsBack = copyBoolMap(lk.namedContIsBack)
		out.namedContIsBack[name] = contBack
	}
	return out
}

func copyBlockMap(m map[string]*Block) map[string]*Block {
	c := make(map[string]*Block, len(m)+1)
	for k, v := range m {
		c[k] = v
	}
	return c
}

func copyBoolMap(m map[string]bool) map[string]bool {
	c := make(map[string]bool, len(m)+1)
	for k, v := range m {
		c[k] = v
	}
	return c
}

// isControlConstruct reports whether s is a for/range/switch/select statement
// that can be labeled and targeted by a labeled break/continue.
func isControlConstruct(s ast.Stmt) bool {
	switch s.(type) {
	case *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
		*ast.TypeSwitchStmt, *ast.SelectStmt:
		return true
	}
	return false
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
		// Fall-through converges onto the same target block goto jumps use.
		target := g.labelTarget(s)
		cur.addSucc(target)
		inner := lk
		if isControlConstruct(s.Stmt) {
			// The directly labeled control construct registers this name as a
			// break/continue target while its body is built.
			inner = inner.withEnclosingLabel(s.Label.Name)
		}
		return g.buildStmt(s.Stmt, target, exits, inner)

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
			breaks:          []*Block{after},
			continues:       []*Block{continueTarget},
			contIsBack:      []bool{s.Post == nil},
			namedBreaks:     lk.namedBreaks,
			namedContinues:  lk.namedContinues,
			namedContIsBack: lk.namedContIsBack,
		}
		nlk = nlk.bindNamed(lk.enclosingLabel, after, continueTarget, s.Post == nil)
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
		nlk := link{breaks: []*Block{after}, continues: []*Block{head}, contIsBack: []bool{true},
			namedBreaks: lk.namedBreaks, namedContinues: lk.namedContinues, namedContIsBack: lk.namedContIsBack}
		nlk = nlk.bindNamed(lk.enclosingLabel, after, head, true)
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
		nlk := link{breaks: []*Block{after}, continues: lk.continues, contIsBack: lk.contIsBack,
			namedBreaks: lk.namedBreaks, namedContinues: lk.namedContinues, namedContIsBack: lk.namedContIsBack}
		nlk = nlk.bindNamed(lk.enclosingLabel, after, nil, false)
		for _, st := range s.Body.List {
			cc := st.(*ast.CaseClause)
			cb := g.newBlock("typeswitch-case")
			entry.addSucc(cb)
			end := g.buildStmtListLk(cc.Body, cb, exits, nlk)
			end.addSucc(after)
			prev = cb
		}
		_ = prev
		entry.addSucc(after)
		return after

	case *ast.SelectStmt:
		after := g.newBlock("after-select")
		nlk := link{breaks: []*Block{after}, continues: lk.continues, contIsBack: lk.contIsBack,
			namedBreaks: lk.namedBreaks, namedContinues: lk.namedContinues, namedContIsBack: lk.namedContIsBack}
		nlk = nlk.bindNamed(lk.enclosingLabel, after, nil, false)
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
				if t, ok := lk.namedBreaks[s.Label.Name]; ok {
					targets = []*Block{t}
				}
			}
			for _, t := range targets {
				cur.addSucc(t)
			}
			return g.newBlock("after-break")
		case token.CONTINUE:
			targets := lk.continues
			backFlags := lk.contIsBack
			if s.Label != nil {
				if t, ok := lk.namedContinues[s.Label.Name]; ok {
					targets = []*Block{t}
					backFlags = []bool{lk.namedContIsBack[s.Label.Name]}
				}
			}
			for i, t := range targets {
				if i < len(backFlags) && backFlags[i] {
					cur.addBackEdge(t)
				} else {
					cur.addSucc(t)
				}
			}
			return g.newBlock("after-continue")
		case token.GOTO:
			// Every path formed by a goto must be analyzed: connect to the
			// label's target block (marking backward jumps as loop-like back
			// edges so widening guarantees convergence). The returned block is
			// unreachable; statements following the goto in source order are
			// merely retained in it and pruned unless another edge reaches them.
			if s.Label != nil {
				if t := g.labels[s.Label.Name]; t != nil {
					if g.isBackward(s, t) {
						cur.addBackEdge(t)
					} else {
						cur.addSucc(t)
					}
				}
			}
			return g.newBlock("after-goto")
		case token.FALLTHROUGH:
			if t := lk.fallthroughTarget; t != nil {
				cur.addSucc(t)
			}
			return g.newBlock("after-fallthrough")
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
	tagExpr := tag
	tagless := tag == nil
	if tagExpr == nil {
		tagExpr = &ast.Ident{Name: "true"}
	}

	// Pre-create the entry block of every clause so fallthrough edges can point
	// to the following clause body.
	type clause struct {
		cc        *ast.CaseClause
		bodyB     *Block
		isDefault bool
	}
	var clauses []*clause
	defaultIdx := -1
	for _, st := range body.List {
		cc := st.(*ast.CaseClause)
		isDef := len(cc.List) == 0
		cl := &clause{cc: cc, bodyB: g.newBlock("switch-case-body"), isDefault: isDef}
		if isDef {
			defaultIdx = len(clauses)
		}
		clauses = append(clauses, cl)
	}

	// Dispatch chain: each value case gets a conditional block; the residual
	// chain enters the default clause or leaves the switch.
	dispatch := cur
	for _, cl := range clauses {
		if cl.isDefault {
			continue
		}
		conds := make([]ast.Expr, 0, len(cl.cc.List))
		for _, val := range cl.cc.List {
			if tagless {
				conds = append(conds, val)
			} else {
				conds = append(conds, &ast.BinaryExpr{X: tagExpr, Op: token.EQL, Y: val})
			}
		}
		nextB := g.newBlock("switch-next")
		dispatch.Kind = BranchCond
		dispatch.Cond = joinConds(conds, token.LOR)
		dispatch.addSucc(cl.bodyB)
		dispatch.addSucc(nextB)
		dispatch = nextB
	}
	if defaultIdx >= 0 {
		dispatch.addSucc(clauses[defaultIdx].bodyB)
	} else {
		dispatch.addSucc(after)
	}

	// Build clause bodies. break targets the after block; fallthrough reaches
	// the next clause body unconditionally. All paths are kept: a case with no
	// terminator merges at after, fallthrough continues into the next body.
	caseLk := link{
		breaks:          []*Block{after},
		continues:       lk.continues,
		contIsBack:      lk.contIsBack,
		namedBreaks:     lk.namedBreaks,
		namedContinues:  lk.namedContinues,
		namedContIsBack: lk.namedContIsBack,
	}
	caseLk = caseLk.bindNamed(lk.enclosingLabel, after, nil, false)
	for i, cl := range clauses {
		clk := caseLk
		if i+1 < len(clauses) {
			clk.fallthroughTarget = clauses[i+1].bodyB
		}
		end := g.buildStmtListLk(cl.cc.Body, cl.bodyB, exits, clk)
		end.addSucc(after)
	}
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
