package intervalanalysis

import (
	"context"
	"sync"
)

// PackageResult is the outcome for one analyzed package.
type PackageResult struct {
	Dir       string
	PkgPath   string
	Warnings  []Warning
	Complete  bool
	Functions int
}

// Result is the complete batch-analysis outcome.
type Result struct {
	Packages []PackageResult
	Warnings []Warning
	// Complete is false when timeout/cancellation prevented full analysis; in
	// that case no claim of safety is made for the unfinished functions.
	Complete bool
	// Unfinished lists functions or packages not fully analyzed.
	Unfinished []string
}

// Analyzer orchestrates loading, dependency-ordered (optionally parallel)
// analysis, summary caching and incremental reuse.
type Analyzer struct {
	config Config
	store  *SummaryStore
	engine *Engine
	graph  *CallGraph
}

// NewAnalyzer constructs an Analyzer with a fresh summary store.
func NewAnalyzer(cfg Config) *Analyzer {
	return NewAnalyzerWithStore(cfg, NewSummaryStore())
}

// NewAnalyzerWithStore reuses an existing store (enables incremental runs).
func NewAnalyzerWithStore(cfg Config, store *SummaryStore) *Analyzer {
	return &Analyzer{config: normalizeConfig(cfg), store: store,
		engine: NewEngine(cfg, store)}
}

// Store exposes the summary store for reuse in later runs.
func (a *Analyzer) Store() *SummaryStore { return a.store }

// Analyze runs batch analysis over dir (recursively if recursive).
func (a *Analyzer) Analyze(ctx context.Context, dir string, recursive bool) (*Result, error) {
	if a.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.config.Timeout)
		defer cancel()
	}
	pkgs, err := LoadPackages(ctx, dir, recursive)
	if err != nil {
		if ctx.Err() != nil {
			return &Result{Complete: false, Warnings: []Warning{},
				Unfinished: []string{dir}}, nil
		}
		return nil, err
	}
	targets := CollectTargets(pkgs)
	graph := BuildCallGraph(targets)
	a.graph = graph
	a.engine.withGraph(graph)

	a.invalidateChanged(graph)

	plan := analysisOrder(graph)
	failed := a.runOrder(ctx, graph, plan)

	result := a.assemble(targets, failed)
	if ctxErr := ctx.Err(); ctxErr != nil {
		result.Complete = false
		return result, nil
	}
	return result, nil
}

// runOrder evaluates SCCs level by level. SCCs at the same level do not depend
// on each other and are processed in parallel (bounded by Config.Workers). All
// shared state goes through the concurrency-safe SummaryStore, and results are
// assembled in deterministic order, so output equals serial execution.
func (a *Analyzer) runOrder(ctx context.Context, g *CallGraph,
	plan analysisPlan) map[FuncID]bool {
	failed := map[FuncID]bool{}
	levels := sccLevels(g, plan)
	for _, level := range levels {
		if err := ctx.Err(); err != nil {
			for _, s := range level {
				for _, id := range sccMembers(plan, s) {
					failed[id] = true
				}
			}
			continue
		}
		a.runLevel(ctx, g, plan, level, failed)
	}
	return failed
}

func sccMembers(plan analysisPlan, s int) []FuncID {
	var out []FuncID
	for _, id := range plan.order {
		if plan.scc[id] == s {
			out = append(out, id)
		}
	}
	return out
}

// sccLevels groups SCCs into dependency levels using edges between SCCs.
func sccLevels(g *CallGraph, plan analysisPlan) [][]int {
	deps := map[int]map[int]bool{}
	for id, s := range plan.scc {
		if deps[s] == nil {
			deps[s] = map[int]bool{}
		}
		for _, c := range g.Callees(id) {
			if cs, ok := plan.scc[c]; ok && cs != s {
				deps[s][cs] = true
			}
		}
	}
	done := map[int]bool{}
	var levels [][]int
	for len(done) < len(deps) {
		var level []int
		for s := range deps {
			if done[s] {
				continue
			}
			ready := true
			for d := range deps[s] {
				if !done[d] {
					ready = false
					break
				}
			}
			if ready {
				level = append(level, s)
			}
		}
		if len(level) == 0 {
			// Should be impossible after SCC condensation; guard anyway.
			for s := range deps {
				if !done[s] {
					level = append(level, s)
				}
			}
		}
		sortInts(level)
		levels = append(levels, level)
		for _, s := range level {
			done[s] = true
		}
	}
	return levels
}

func sortInts(x []int) {
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && x[j-1] > x[j]; j-- {
			x[j-1], x[j] = x[j], x[j-1]
		}
	}
}

func (a *Analyzer) runLevel(ctx context.Context, g *CallGraph, plan analysisPlan,
	level []int, failed map[FuncID]bool) {
	workers := a.config.Workers
	if workers <= 1 || len(level) == 1 {
		for _, s := range level {
			if !a.runSCC(ctx, g, plan, s) {
				for _, id := range sccMembers(plan, s) {
					failed[id] = true
				}
			}
		}
		return
	}
	if workers > len(level) {
		workers = len(level)
	}
	jobs := make(chan int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				ok := a.runSCC(ctx, g, plan, s)
				if !ok {
					mu.Lock()
					for _, id := range sccMembers(plan, s) {
						failed[id] = true
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, s := range level {
		jobs <- s
	}
	close(jobs)
	wg.Wait()
}

func (a *Analyzer) runSCC(ctx context.Context, g *CallGraph, plan analysisPlan,
	s int) bool {
	members := sccMembers(plan, s)
	if len(members) == 1 {
		return a.ensureOne(ctx, g, members[0])
	}
	for _, m := range members {
		if !a.ensureOne(ctx, g, m) {
			return false
		}
	}
	return a.fixpointSCC(ctx, g, members)
}

// ensureOne analyzes fn if it has no cached complete summary and its non-cycle
// dependencies are ready.
func (a *Analyzer) ensureOne(ctx context.Context, g *CallGraph, id FuncID) bool {
	if sum, ok := a.store.Get(id); ok && sum.Complete {
		return true
	}
	fn, ok := g.Lookup(id)
	if !ok {
		return true
	}
	if err := ctx.Err(); err != nil {
		return false
	}
	sum := a.engine.AnalyzeFunction(ctx, fn)
	a.store.Put(sum)
	return sum.Complete
}

// fixpointSCC repeats analysis of an SCC until summaries stabilize or the
// iteration budget is exhausted. Exhaustion marks members incomplete (sound:
// no false "safe" claim is emitted).
func (a *Analyzer) fixpointSCC(ctx context.Context, g *CallGraph, members []FuncID) bool {
	for round := 0; round < a.config.MaxIterations; round++ {
		if err := ctx.Err(); err != nil {
			return false
		}
		changed := false
		for _, id := range members {
			fn, ok := g.Lookup(id)
			if !ok {
				continue
			}
			old, _ := a.store.Get(id)
			sum := a.engine.AnalyzeFunction(ctx, fn)
			a.store.Put(sum)
			if !sum.Complete {
				return false
			}
			if old == nil || !summaryStable(old, sum) {
				changed = true
			}
		}
		if !changed {
			return true
		}
	}
	return false
}

func summaryStable(a, b *Summary) bool {
	return a.Return == b.Return && a.Complete == b.Complete
}

// assemble builds the structured result from cached summaries.
func (a *Analyzer) assemble(targets []*AnalysisTarget, failed map[FuncID]bool) *Result {
	res := &Result{Complete: true, Warnings: []Warning{}}
	if len(failed) > 0 {
		res.Complete = false
		for id := range failed {
			res.Unfinished = append(res.Unfinished, id.String())
		}
		sortStrings(res.Unfinished)
	}
	var all []Warning
	for _, t := range targets {
		pr := &PackageResult{
			Dir:       t.Dir,
			PkgPath:   t.Pkg.Path,
			Warnings:  []Warning{},
			Complete:  true,
			Functions: len(t.Functions),
		}
		for _, fn := range t.Functions {
			sum, ok := a.store.Get(fn.ID)
			if !ok {
				pr.Complete = false
				res.Complete = false
				if !containsString(res.Unfinished, fn.ID.String()) {
					res.Unfinished = append(res.Unfinished, fn.ID.String())
				}
				continue
			}
			if !sum.Complete || failed[fn.ID] {
				pr.Complete = false
				res.Complete = false
			}
			pr.Warnings = append(pr.Warnings, sum.Warnings...)
			all = append(all, sum.Warnings...)
		}
		if !pr.Complete {
			res.Complete = false
		}
		pr.Warnings = FilterWarnings(dedupWarnings(pr.Warnings), a.config.MinSeverity)
		SortWarnings(pr.Warnings)
		res.Packages = append(res.Packages, *pr)
	}
	all = FilterWarnings(dedupWarnings(all), a.config.MinSeverity)
	SortWarnings(all)
	res.Warnings = all
	sortStrings(res.Unfinished)
	return res
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// invalidateChanged removes cached summaries whose source hash changed together
// with all their transitive dependents.
func (a *Analyzer) invalidateChanged(graph *CallGraph) {
	var stale []FuncID
	for id, fn := range graph.funcs {
		if sum, ok := a.store.Get(id); ok && sum.SourceHash != fn.SourceHash {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		a.store.Invalidate(id)
	}
}

// analysisOrder returns functions in dependency order: callees before callers,
// with cycles (SCCs) contiguous. Ordering is deterministic.
// analysisPlan is a callee-first ordering plus SCC membership (for bounded
// fixpoint iteration over recursive groups).
type analysisPlan struct {
	order []FuncID
	scc   map[FuncID]int
}

func analysisOrder(g *CallGraph) analysisPlan {
	ids := make([]FuncID, 0, len(g.funcs))
	for id := range g.funcs {
		ids = append(ids, id)
	}
	sortIDs(ids)

	index := map[FuncID]int{}
	low := map[FuncID]int{}
	onStack := map[FuncID]bool{}
	var stack []FuncID
	var sccs [][]FuncID
	sccMap := map[FuncID]int{}
	next := 0

	var visit func(id FuncID)
	visit = func(id FuncID) {
		index[id] = next
		low[id] = next
		next++
		stack = append(stack, id)
		onStack[id] = true
		for _, c := range g.Callees(id) {
			if _, known := g.funcs[c]; !known {
				continue
			}
			if _, seen := index[c]; !seen {
				visit(c)
				if low[c] < low[id] {
					low[id] = low[c]
				}
			} else if onStack[c] && index[c] < low[id] {
				low[id] = index[c]
			}
		}
		if low[id] == index[id] {
			var scc []FuncID
			for {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[top] = false
				scc = append(scc, top)
				if top == id {
					break
				}
			}
			num := len(sccs)
			for _, member := range scc {
				sccMap[member] = num
			}
			sccs = append(sccs, scc)
		}
	}
	for _, id := range ids {
		if _, seen := index[id]; !seen {
			visit(id)
		}
	}
	// Tarjan emits sink SCCs first; sort members within each SCC for
	// determinism. Flatten SCCs (callee-first order is preserved).
	var order []FuncID
	for _, scc := range sccs {
		sortIDs(scc)
		order = append(order, scc...)
	}
	return analysisPlan{order: order, scc: sccMap}
}
