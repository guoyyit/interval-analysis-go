package intervalanalysis

import (
	"context"
	"go/ast"
	"go/token"
	"go/types"
)

const (
	defaultMaxIterations   = 100
	defaultWidenAfter      = 2
	defaultNarrowingPasses = 2
)

// FuncResult is the per-function outcome of abstract interpretation.
type FuncResult struct {
	ID       FuncID
	Warnings []Warning
	Complete bool
}

// engineResolver answers calls via the call graph and summary store. Cycles
// (including self-recursion) that lack a complete summary get a sound full-range
// provisional result.
type engineResolver struct {
	graph    *CallGraph
	store    *SummaryStore
	caller   FuncID
	inflight map[FuncID]bool
	lp       *LoadedPackage
}

func (r *engineResolver) ResolveCall(call *ast.CallExpr, args []Interval) (Interval, bool) {
	id, ok := r.graph.resolveCall(call, r.lp)
	if !ok {
		return r.fullResult(call), true
	}
	r.store.AddDependency(r.caller, id)
	if r.inflight[id] {
		return r.fullResult(call), true
	}
	if sum, ok := r.store.Get(id); ok && sum.Complete {
		return sum.Return, true
	}
	return r.fullResult(call), true
}

func (r *engineResolver) fullResult(call *ast.CallExpr) Interval {
	if bits, signed, ok := intKind(r.lp.Info.TypeOf(call)); ok {
		return Full(bits, signed)
	}
	return TopInterval
}

// Engine runs CFG fixpoint analysis for individual functions.
type Engine struct {
	config Config
	store  *SummaryStore
	graph  *CallGraph
}

// NewEngine builds an Engine.
func NewEngine(cfg Config, store *SummaryStore) *Engine {
	return &Engine{config: normalizeConfig(cfg), store: store}
}

func (e *Engine) withGraph(g *CallGraph) *Engine {
	e.graph = g
	return e
}

func normalizeConfig(cfg Config) Config {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = defaultMaxIterations
	}
	if cfg.WidenAfter <= 0 {
		cfg.WidenAfter = defaultWidenAfter
	}
	if cfg.NarrowingPasses <= 0 {
		cfg.NarrowingPasses = defaultNarrowingPasses
	}
	return cfg
}

type engineSink struct {
	fset     *token.FileSet
	fn       string
	warnings []Warning
}

func (s *engineSink) report(pos token.Pos, cat Category, msg, evidence string) {
	s.warnings = append(s.warnings, Warning{
		Category: cat,
		Severity: categorySeverity(cat),
		Position: s.fset.Position(pos),
		Function: s.fn,
		Message:  msg,
		Evidence: evidence,
	})
}

// AnalyzeFunction performs interval abstract interpretation of one function.
// It always terminates: bounded iterations with widening, followed by bounded
// narrowing passes.
func (e *Engine) AnalyzeFunction(ctx context.Context, fn *FunctionInfo) *Summary {
	sum := &Summary{
		ID:         fn.ID,
		Return:     TopInterval,
		SourceHash: fn.SourceHash,
		Pos:        fn.Pos,
	}
	cfg := BuildCFG(fn.Pkg.Fset, fn.Pkg.Info, fn.Pkg.Pkg, fn.Decl)
	sink := &engineSink{fset: fn.Pkg.Fset, fn: fn.ID.String()}
	resolver := &engineResolver{
		graph:    e.graph,
		store:    e.store,
		caller:   fn.ID,
		inflight: map[FuncID]bool{fn.ID: true},
		lp:       fn.Pkg,
	}
	tr := newTransfer(cfg, resolver, fn.ID.String(), sink, collectEscapes(fn))

	entry := NewState()
	initParams(entry, fn)

	_, out, converged := e.fixpoint(ctx, cfg, tr, entry)
	sum.Warnings = dedupWarnings(sink.warnings)
	sum.Return = returnInterval(fn, cfg, tr, out)
	sum.Complete = converged
	return sum
}

func initParams(st *State, fn *FunctionInfo) {
	if fn.Decl.Type.Params == nil {
		return
	}
	for _, field := range fn.Decl.Type.Params.List {
		for _, name := range field.Names {
			obj := fn.Pkg.Info.Defs[name]
			if obj == nil {
				continue
			}
			if _, _, ok := intKind(obj.Type()); ok {
				// External input: leave unmapped -> full type range.
				st.Forget(obj)
			}
		}
	}
}

func (e *Engine) fixpoint(ctx context.Context, cfg *CFG, tr *transfer,
	entry *State) ([]*State, []*State, bool) {
	n := len(cfg.Blocks)
	in := make([]*State, n)
	out := make([]*State, n)
	counts := make([]int, n)
	lastWiden := make([]*State, n)
	for i := range in {
		in[i] = BottomState
		out[i] = BottomState
	}
	in[cfg.Entry.ID] = entry
	worklist := []*Block{cfg.Entry}
	queued := map[int]bool{cfg.Entry.ID: true}
	iter := 0
	converged := true
	thresholds := wideningThresholds()

	for len(worklist) > 0 {
		if iter >= e.config.MaxIterations {
			converged = false
			break
		}
		if err := ctx.Err(); err != nil {
			converged = false
			break
		}
		b := worklist[0]
		worklist = worklist[1:]
		queued[b.ID] = false
		iter++

		succStates := tr.exec(b, in[b.ID])
		newOut := joinOuts(b, succStates)
		outChanged := !StateEqual(newOut, out[b.ID])
		out[b.ID] = newOut
		e.propagate(b, succStates, in, &worklist, queued, counts, lastWiden,
			thresholds, outChanged)
	}

	if converged {
		converged = e.narrow(ctx, cfg, tr, in, out)
	}
	return in, out, converged
}

func joinOuts(b *Block, outs []*State) *State {
	if b.Kind == BranchCond && len(outs) == 2 {
		return StateJoin(outs[0], outs[1])
	}
	if len(outs) >= 1 {
		return outs[0]
	}
	return BottomState
}

func (e *Engine) propagate(b *Block, outs []*State, in []*State,
	worklist *[]*Block, queued map[int]bool, counts []int,
	lastWiden []*State, thresholds []int64, force bool) {
	for i, succ := range b.Succs {
		isBack := i < len(b.BackEdge) && b.BackEdge[i]
		candidate := BottomState
		if b.Kind == BranchCond && len(outs) == 2 {
			candidate = outs[i]
		} else if len(outs) == 1 {
			candidate = outs[0]
		}
		joined := StateJoin(in[succ.ID], candidate)
		if isBack {
			counts[succ.ID]++
		}
		if isBack && counts[succ.ID] > e.config.WidenAfter {
			base := lastWiden[succ.ID]
			if base == nil {
				base = in[succ.ID]
			}
			joined = Widen(base, joined, thresholds)
		}
		if isBack {
			lastWiden[succ.ID] = joined
		}
		if force || !StateEqual(joined, in[succ.ID]) {
			in[succ.ID] = joined
			if !queued[succ.ID] {
				queued[succ.ID] = true
				*worklist = append(*worklist, succ)
			}
		}
	}
}

// narrow performs bounded descending iteration. At a post-fixpoint a >= F(a),
// so intersecting the current bound with F(a) cannot remove real values. Every
// block is narrowed against the JOIN of all its incoming edge states: meeting
// per edge would be unsound, because values arriving via a different edge
// could be cut below the real reachable set.
func (e *Engine) narrow(ctx context.Context, cfg *CFG, tr *transfer,
	in, out []*State) bool {
	for pass := 0; pass < e.config.NarrowingPasses; pass++ {
		if err := ctx.Err(); err != nil {
			return false
		}
		changed := false
		// Joined incoming edge state per block.
		incoming := make([]*State, len(cfg.Blocks))
		for i := range incoming {
			incoming[i] = BottomState
		}
		for _, b := range cfg.Blocks {
			outs := tr.exec(b, in[b.ID])
			newOut := joinOuts(b, outs)
			if !StateEqual(newOut, out[b.ID]) {
				out[b.ID] = newOut
				changed = true
			}
			for i, succ := range b.Succs {
				candidate := BottomState
				if b.Kind == BranchCond && len(outs) == 2 {
					candidate = outs[i]
				} else if len(outs) == 1 {
					candidate = outs[0]
				}
				incoming[succ.ID] = StateJoin(incoming[succ.ID], candidate)
			}
		}
		for _, b := range cfg.Blocks {
			if incoming[b.ID].IsBottom() {
				// No incoming edges (entry or unreachable): keep as is.
				continue
			}
			narrowed, okMeet := meetStates(in[b.ID], incoming[b.ID])
			if okMeet && !StateEqual(narrowed, in[b.ID]) {
				in[b.ID] = narrowed
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return true
}

func meetStates(a, b *State) (*State, bool) {
	if a.IsBottom() || b.IsBottom() {
		return BottomState, true
	}
	c := a.Copy()
	changed := meetMap(c.vars, b.vars)
	if meetMap(c.length, b.length) {
		changed = true
	}
	return c, changed
}

func meetMap(dst, src map[types.Object]Interval) bool {
	changed := false
	for k, v := range src {
		old, ok := dst[k]
		if !ok {
			// dst side is implicitly top; meet(top, v) = v.
			dst[k] = v
			changed = true
			continue
		}
		if m, ok := meetInterval(old, v); ok && m != old {
			dst[k] = m
			changed = true
		}
	}
	return changed
}

func meetInterval(a, b Interval) (Interval, bool) {
	if a.Top {
		return b, true
	}
	if b.Top {
		return a, true
	}
	lo := a.Lo
	if b.Lo > lo {
		lo = b.Lo
	}
	hi := a.Hi
	if b.Hi < hi {
		hi = b.Hi
	}
	if lo > hi {
		return Interval{}, false
	}
	return Interval{Lo: lo, Hi: hi}, true
}

func wideningThresholds() []int64 {
	return []int64{
		-1 << 31, -1 << 15, -1 << 7, -1, 0, 1,
		1<<7 - 1, 1<<15 - 1, 1<<31 - 1,
		128, 256, 1024, 4096, 65535,
	}
}

func returnInterval(fn *FunctionInfo, cfg *CFG, tr *transfer,
	out []*State) Interval {
	result := TopInterval
	found := false
	ast.Inspect(fn.Decl.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		st := stateAtNode(cfg, ret, out)
		ev := &evalContext{
			state:    st,
			info:     fn.Pkg.Info,
			pkg:      fn.Pkg.Pkg,
			resolver: tr.resolver,
			funcName: fn.ID.String(),
		}
		if iv, ok := ev.evalInt(ret.Results[0]); ok {
			if !found {
				result, found = iv, true
			} else {
				result = Join(result, iv)
			}
		}
		return true
	})
	return result
}

func stateAtNode(cfg *CFG, node ast.Node, out []*State) *State {
	for _, b := range cfg.Blocks {
		for _, st := range b.Stmts {
			if st == node {
				return out[b.ID]
			}
		}
	}
	return NewState()
}

// collectEscapes finds locals that may be aliased across a call (address taken
// or passed as an argument) plus package-level globals.
func collectEscapes(fn *FunctionInfo) map[types.Object]bool {
	escapes := map[types.Object]bool{}
	add := func(obj types.Object) {
		if obj != nil {
			escapes[obj] = true
		}
	}
	ast.Inspect(fn.Decl.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.UnaryExpr:
			if x.Op == token.AND {
				if id, ok := x.X.(*ast.Ident); ok {
					add(fn.Pkg.Info.Uses[id])
				}
			}
		case *ast.CallExpr:
			for _, arg := range x.Args {
				ast.Inspect(arg, func(m ast.Node) bool {
					if id, ok := m.(*ast.Ident); ok {
						add(fn.Pkg.Info.Uses[id])
					}
					return true
				})
			}
		case *ast.Ident:
			if obj := fn.Pkg.Info.Uses[x]; obj != nil {
				if obj.Pkg() != nil && obj.Pkg() == fn.Pkg.Pkg {
					if _, isVar := obj.(*types.Var); isVar && isPackageLevel(obj) {
						add(obj)
					}
				}
			}
		}
		return true
	})
	return escapes
}

func isPackageLevel(obj types.Object) bool {
	_, ok := obj.(*types.Var)
	if !ok {
		return false
	}
	return obj.Parent() == obj.Pkg().Scope()
}
