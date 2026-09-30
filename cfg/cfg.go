package cfg

import (
	"go/ast"
	"go/token"
)

// Edge connects two blocks. A nil Cond means the edge is unconditional. A
// non-nil Cond means the edge is taken only when Cond evaluates to Truth;
// the data-flow engine uses that to refine interval states on branch entry.
type Edge struct {
	From  *Block
	To    *Block
	Cond  ast.Expr
	Truth bool
}

// Block is a basic block: a linear list of statements with no internal
// control transfer, plus successor edges.
type Block struct {
	Index int
	Stmts []ast.Stmt
	Succs []*Edge
	Preds []*Block
}

type loopFrame struct {
	breakTo    *Block
	continueTo *Block
}

type builder struct {
	fn     *ast.FuncDecl
	blocks []*Block
	loops  []*loopFrame
}

// CFG is the intra-procedural control-flow graph of one function.
type CFG struct {
	Func   *ast.FuncDecl
	Blocks []*Block
	Entry  *Block
}

func (b *builder) newBlock() *Block {
	blk := &Block{Index: len(b.blocks)}
	b.blocks = append(b.blocks, blk)
	return blk
}

func (b *builder) addEdge(from, to *Block, cond ast.Expr, truth bool) {
	if from == nil || to == nil {
		return
	}
	for _, e := range from.Succs {
		if e.To == to && e.Cond == cond && e.Truth == truth {
			return
		}
	}
	from.Succs = append(from.Succs, &Edge{From: from, To: to, Cond: cond, Truth: truth})
}

// Build constructs a CFG for fn. The graph starts with an empty entry block
// and every reachable statement appears in exactly one block. Control
// constructs the data-flow engine cannot model (select, goto, type switch)
// are wired conservatively: all possible targets are connected, so no
// reachable path is ever dropped.
func Build(fn *ast.FuncDecl) *CFG {
	b := &builder{fn: fn}
	entry := b.newBlock()
	graph := &CFG{Func: fn, Entry: entry}
	if fn.Body == nil {
		graph.Blocks = b.blocks
		return graph
	}
	_ = b.buildSeq(entry, fn.Body.List)
	b.finish()
	graph.Blocks = b.blocks
	return graph
}

func (b *builder) finish() {
	for _, blk := range b.blocks {
		for _, e := range blk.Succs {
			e.To.Preds = append(e.To.Preds, blk)
		}
	}
}

// buildSeq appends stmts starting in cur and returns the live exit blocks.
// A nil entry in the returned list models a path that terminates (return).
func (b *builder) buildSeq(cur *Block, stmts []ast.Stmt) []*Block {
	live := []*Block{cur}
	for _, st := range stmts {
		var next []*Block
		for _, l := range live {
			next = append(next, b.buildStmt(l, st)...)
		}
		live = next
	}
	return live
}

func (b *builder) buildStmt(cur *Block, st ast.Stmt) []*Block {
	switch s := st.(type) {
	case *ast.BlockStmt:
		return b.buildSeq(cur, s.List)

	case *ast.IfStmt:
		if s.Init != nil {
			cur.Stmts = append(cur.Stmts, s.Init)
		}
		thenEntry := b.newBlock()
		b.addEdge(cur, thenEntry, s.Cond, true)
		thenExits := b.buildSeq(thenEntry, s.Body.List)
		elseEntry := b.newBlock()
		b.addEdge(cur, elseEntry, s.Cond, false)
		var elseExits []*Block
		if s.Else != nil {
			elseExits = b.buildStmt(elseEntry, s.Else)
		} else {
			elseExits = []*Block{elseEntry}
		}
		return append(thenExits, elseExits...)

	case *ast.ForStmt:
		if s.Init != nil {
			cur.Stmts = append(cur.Stmts, s.Init)
		}
		head := b.newBlock()
		b.addEdge(cur, head, nil, false)
		exit := b.newBlock()
		b.loops = append(b.loops, &loopFrame{breakTo: exit, continueTo: head})
		bodyEntry := b.newBlock()
		if s.Cond != nil {
			b.addEdge(head, bodyEntry, s.Cond, true)
			b.addEdge(head, exit, s.Cond, false)
		} else {
			b.addEdge(head, bodyEntry, nil, false)
		}
		bodyExits := b.buildSeq(bodyEntry, s.Body.List)
		if s.Post != nil {
			postBlk := b.newBlock()
			for _, x := range bodyExits {
				b.addEdge(x, postBlk, nil, false)
			}
			postBlk.Stmts = append(postBlk.Stmts, s.Post)
			b.addEdge(postBlk, head, nil, false)
		} else {
			for _, x := range bodyExits {
				b.addEdge(x, head, nil, false)
			}
		}
		b.loops = b.loops[:len(b.loops)-1]
		return []*Block{exit}

	case *ast.RangeStmt:
		head := b.newBlock()
		b.addEdge(cur, head, nil, false)
		exit := b.newBlock()
		body := b.newBlock()
		b.loops = append(b.loops, &loopFrame{breakTo: exit, continueTo: head})
		b.addEdge(head, body, nil, false)
		b.addEdge(head, exit, nil, false)
		bodyExits := b.buildSeq(body, s.Body.List)
		for _, x := range bodyExits {
			b.addEdge(x, head, nil, false)
		}
		b.loops = b.loops[:len(b.loops)-1]
		return []*Block{exit}

	case *ast.SwitchStmt:
		if s.Init != nil {
			cur.Stmts = append(cur.Stmts, s.Init)
		}
		if s.Tag != nil {
			cur.Stmts = append(cur.Stmts, &ast.ExprStmt{X: s.Tag})
		}
		exit := b.newBlock()
		b.loops = append(b.loops, &loopFrame{breakTo: exit, continueTo: cur})
		hasDefault := false
		var caseExits []*Block
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CaseClause)
			caseBlk := b.newBlock()
			if clause.List == nil {
				hasDefault = true
				b.addEdge(cur, caseBlk, nil, false)
			} else {
				for _, cond := range clause.List {
					if s.Tag == nil {
						b.addEdge(cur, caseBlk, cond, true)
					} else {
						match := &ast.BinaryExpr{X: s.Tag, Y: cond, Op: token.EQL}
						b.addEdge(cur, caseBlk, match, true)
					}
				}
			}
			caseExits = append(caseExits, b.buildSeq(caseBlk, clause.Body)...)
		}
		if !hasDefault {
			b.addEdge(cur, exit, nil, false)
		}
		for _, x := range caseExits {
			b.addEdge(x, exit, nil, false)
		}
		b.loops = b.loops[:len(b.loops)-1]
		return []*Block{exit}

	case *ast.TypeSwitchStmt:
		if s.Init != nil {
			cur.Stmts = append(cur.Stmts, s.Init)
		}
		if s.Assign != nil {
			cur.Stmts = append(cur.Stmts, s.Assign)
		}
		exit := b.newBlock()
		b.loops = append(b.loops, &loopFrame{breakTo: exit, continueTo: cur})
		var caseExits []*Block
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CaseClause)
			caseBlk := b.newBlock()
			b.addEdge(cur, caseBlk, nil, false)
			caseExits = append(caseExits, b.buildSeq(caseBlk, clause.Body)...)
		}
		b.addEdge(cur, exit, nil, false)
		for _, x := range caseExits {
			b.addEdge(x, exit, nil, false)
		}
		b.loops = b.loops[:len(b.loops)-1]
		return []*Block{exit}

	case *ast.BranchStmt:
		switch s.Tok {
		case token.BREAK:
			if len(b.loops) > 0 {
				b.addEdge(cur, b.loops[len(b.loops)-1].breakTo, nil, false)
			}
		case token.CONTINUE:
			if len(b.loops) > 0 {
				b.addEdge(cur, b.loops[len(b.loops)-1].continueTo, nil, false)
			}
		case token.GOTO:
			// Without a label map, fall through to a fresh block; the
			// skipped code stays reachable from elsewhere and the engine
			// treats untracked successors as may-reach (conservative).
			target := b.newBlock()
			b.addEdge(cur, target, nil, false)
			return []*Block{target}
		}
		return nil

	case *ast.ReturnStmt:
		cur.Stmts = append(cur.Stmts, s)
		return nil

	case *ast.LabeledStmt:
		return b.buildStmt(cur, s.Stmt)

	case *ast.SelectStmt:
		exit := b.newBlock()
		var bodyExits []*Block
		for _, cc := range s.Body.List {
			clause := cc.(*ast.CommClause)
			caseBlk := b.newBlock()
			b.addEdge(cur, caseBlk, nil, false)
			if clause.Comm != nil {
				caseBlk.Stmts = append(caseBlk.Stmts, clause.Comm)
			}
			bodyExits = append(bodyExits, b.buildSeq(caseBlk, clause.Body)...)
		}
		for _, x := range bodyExits {
			b.addEdge(x, exit, nil, false)
		}
		b.addEdge(cur, exit, nil, false)
		return []*Block{exit}

	default:
		cur.Stmts = append(cur.Stmts, st)
		return []*Block{cur}
	}
}
