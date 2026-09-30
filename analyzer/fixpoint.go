package analyzer

import (
	"context"
	"go/ast"
	"go/types"

	"github.com/guoyyit/interval-analysis-go/cfg"
	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/report"
)

// runFuncAnalysis performs widening/narrowing abstract interpretation over
// the function's CFG. Parameters are initialized to the full range of their
// declared types (requirement: unconstrained inputs are full intervals).
func (e *Engine) runFuncAnalysis(ctx context.Context, p *pkgView, fn *ast.FuncDecl, store *SummaryStore) *FuncResult {
	graph := cfg.Build(fn)
	name := fnName(fn)
	fa := &funcAnalyzer{
		pkg:      p,
		fn:       fn,
		fnName:   name,
		graph:    graph,
		seen:     make(map[alertKey]bool),
		store:    store,
		arrayEls: make(map[types.Object]map[int64]domain.Interval),
	}
	fa.refineFn = fa.refine

	initial := NewState()
	seedParameters(fn, p.info, initial)
	seedResults(fn, p.info, fa)

	heads := loopHeads(graph)
	in := make([]*State, len(graph.Blocks))
	out := make([]*State, len(graph.Blocks))
	in[graph.Entry.Index] = initial

	incomplete := false
	iteration := 0
	changed := true
	for changed {
		if err := ctx.Err(); err != nil {
			return fa.cancelResult(p, fn, err)
		}
		iteration++
		if iteration > e.cfg.MaxIterations {
			incomplete = true
			break
		}
		changed = false
		for _, b := range graph.Blocks {
			merged := mergePreds(fa, graph, b, in, out)
			if heads[b.Index] {
				if old := in[b.Index]; old != nil && merged != nil {
					bnd := headWidenBounds(old, merged)
					merged = widenStates(old, merged, bnd)
				}
			}
			if in[b.Index] == nil || !in[b.Index].Equals(merged) {
				in[b.Index] = merged
				changed = true
			}
			bo := fa.transferBlock(graph, b, merged)
			out[b.Index] = bo
		}
	}

	// Narrowing: a bounded number of precision-improving re-analyses. Each
	// round only intersects/tightens states, so no possibly-faulting behavior
	// is ever removed; at worst the widened over-approximation is kept.
	for pass := 0; pass < e.cfg.NarrowingPasses && !incomplete; pass++ {
		if err := ctx.Err(); err != nil {
			return fa.cancelResult(p, fn, err)
		}
		progress := false
		narrowIn := make([]*State, len(graph.Blocks))
		narrowOut := make([]*State, len(graph.Blocks))
		for _, b := range graph.Blocks {
			merged := mergePreds(fa, graph, b, narrowIn, narrowOut)
			if merged == nil {
				continue
			}
			if old := in[b.Index]; old != nil {
				tight := narrowStates(old, merged)
				if !tight.Equals(old) {
					progress = true
				}
				merged = tight
			}
			narrowIn[b.Index] = merged
			narrowOut[b.Index] = fa.transferBlock(graph, b, merged)
		}
		in = narrowIn
		out = narrowOut
		if !progress {
			break
		}
	}

	alerts := append([]report.Alert(nil), fa.alerts...)
	report.SortBySeverity(alerts)

	deps := e.collectDeps(p, fn)
	sum := e.buildSummary(p, fn, fa, deps, incomplete)
	pos := p.fset.Position(fn.Pos()).String()
	return &FuncResult{
		Key:        funcKey(p, fn),
		Name:       name,
		Package:    p.path,
		Position:   pos,
		Hash:       p.hash,
		Deps:       deps,
		Alerts:     alerts,
		Incomplete: incomplete,
		Summary:    sum,
	}
}

func (fa *funcAnalyzer) cancelResult(p *pkgView, fn *ast.FuncDecl, err error) *FuncResult {
	return &FuncResult{
		Key:        funcKey(p, fn),
		Name:       fnName(fn),
		Package:    p.path,
		Position:   p.fset.Position(fn.Pos()).String(),
		Hash:       p.hash,
		Alerts:     append([]report.Alert(nil), fa.alerts...),
		Incomplete: true,
		Summary:    nil,
	}
}

// mergePreds joins the out-states of all predecessors. The entry uses its
// seeded in-state. Joining with interval hull is an over-approximation.
func mergePreds(fa *funcAnalyzer, graph *cfg.CFG, b *cfg.Block, in, out []*State) *State {
	if b == graph.Entry {
		if in[b.Index] != nil {
			return in[b.Index]
		}
		return NewState()
	}
	var merged *State
	for _, pred := range b.Preds {
		os := out[pred.Index]
		if os == nil {
			continue
		}
		refined := applyEdgeRefine(fa, graph, pred, b, os)
		if merged == nil {
			merged = refined.Copy()
		} else {
			merged.Join(refined)
		}
	}
	if merged == nil {
		return nil
	}
	return merged
}

// applyEdgeRefine constrains the predecessor's out-state by the edge
// condition. Both true and false edges of a branch are refined, which is what
// makes the analysis path-sensitive at joins in a sound way.
func applyEdgeRefine(fa *funcAnalyzer, graph *cfg.CFG, pred, b *cfg.Block, st *State) *State {
	for _, edge := range pred.Succs {
		if edge.To != b || edge.Cond == nil {
			continue
		}
		if fa.refineFn == nil {
			continue
		}
		st = fa.refineFn(st, edge.Cond, edge.Truth)
	}
	return st
}

// loopHeads marks blocks that are targeted by a back edge (pred index >=
// block index in the traversal ordering produced by the builder). Those are
// exactly the widening points needed for finite convergence.
func loopHeads(graph *cfg.CFG) map[int]bool {
	heads := make(map[int]bool)
	for _, b := range graph.Blocks {
		for _, p := range b.Preds {
			if p.Index >= b.Index {
				heads[b.Index] = true
			}
		}
	}
	return heads
}

func seedParameters(fn *ast.FuncDecl, info *types.Info, st *State) {
	if fn.Type.Params == nil {
		return
	}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			obj := info.ObjectOf(name)
			if obj == nil {
				continue
			}
			if b, ok := boundsOf(obj.Type()); ok {
				st.Set(obj, domain.TypeTop(b))
			}
		}
	}
}

func seedResults(fn *ast.FuncDecl, info *types.Info, fa *funcAnalyzer) {
	if fn.Type.Results == nil {
		return
	}
	for _, field := range fn.Type.Results.List {
		for _, name := range field.Names {
			if obj := info.ObjectOf(name); obj != nil {
				fa.resultObjs = append(fa.resultObjs, obj)
				if b, ok := boundsOf(obj.Type()); ok {
					// Named results start at the zero value but are mutable;
					// the transfer will widen them on writes.
					_ = b
				}
			}
		}
	}
}

func fnName(fn *ast.FuncDecl) string {
	if fn.Name != nil {
		return fn.Name.Name
	}
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return "(method)"
	}
	return "(func)"
}
