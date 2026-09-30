package analyzer

import (
	"context"
	"go/ast"
	"go/types"
	"sort"
	"sync"
	"time"

	"github.com/guoyyit/interval-analysis-go/config"
	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/loader"
	"github.com/guoyyit/interval-analysis-go/report"
)

// FuncResult is the outcome of analyzing one function.
type FuncResult struct {
	Key        string
	Name       string
	Package    string
	Position   string
	Hash       string
	Deps       []string
	Alerts     []report.Alert
	Incomplete bool
	Canceled   bool
	Summary    *Summary
}

// Engine runs batch interval analysis. It is safe for concurrent use across
// packages because all shared state goes through SummaryStore's locking.
type Engine struct {
	cfg   config.Config
	store *SummaryStore
}

func New(cfg config.Config) *Engine {
	_ = cfg.Validate()
	return &Engine{cfg: cfg, store: NewSummaryStore()}
}

func (e *Engine) Store() *SummaryStore { return e.store }

// AnalyzePaths loads every requested path and analyzes all its packages.
// Cancelation/timeout is honored: interrupted functions/packages are flagged
// incomplete and the top-level report is marked incomplete too.
func (e *Engine) AnalyzePaths(ctx context.Context, paths []string) (*report.Report, error) {
	start := time.Now()
	root := ""
	if len(paths) > 0 {
		root = paths[0]
	}
	rep := &report.Report{Root: root, StartedAt: start}
	if err := ctx.Err(); err != nil {
		rep.Incomplete = true
		for _, path := range paths {
			rep.Packages = append(rep.Packages, report.PackageReport{
				Package: path, Dir: path, Canceled: true, Incomplete: true,
			})
		}
		sort.Slice(rep.Packages, func(i, j int) bool { return rep.Packages[i].Package < rep.Packages[j].Package })
		return rep, nil
	}
	ld := loader.New(e.cfg.IncludeTests)
	seenDir := map[string]bool{}
	for _, path := range paths {
		pkgs, err := ld.LoadPath(path, e.cfg.Recursive)
		if err != nil {
			rep.Packages = append(rep.Packages, report.PackageReport{
				Package: path, Dir: path, Error: err.Error(), Incomplete: true,
			})
			rep.Incomplete = true
			continue
		}
		for _, p := range pkgs {
			if seenDir[p.Path] {
				continue
			}
			seenDir[p.Path] = true
			pr := e.AnalyzePackage(ctx, p)
			if pr.Incomplete || pr.Canceled {
				rep.Incomplete = true
			}
			rep.Packages = append(rep.Packages, pr)
		}
	}
	sort.Slice(rep.Packages, func(i, j int) bool { return rep.Packages[i].Package < rep.Packages[j].Package })
	return rep, nil
}

// AnalyzePackage analyzes one loaded package. Functions with no body
// (externals/assembly stubs) are skipped, not treated as safe.
func (e *Engine) AnalyzePackage(ctx context.Context, p *loader.Package) report.PackageReport {
	start := time.Now()
	view := viewOf(p)
	pr := e.analyzeReport(ctx, view, start)
	pr.Dir = p.Dir
	return pr
}

// AnalyzePackageView runs the full report pipeline on an already-loaded
// in-memory package view. It is used by tests and embedders that build
// packages without the on-disk loader.
func (e *Engine) AnalyzePackageView(ctx context.Context, view *pkgView) report.PackageReport {
	return e.analyzeReport(ctx, view, time.Now())
}

func (e *Engine) analyzeView(ctx context.Context, view *pkgView) map[string]*FuncResult {
	funcs := collectFuncs(view)
	deps := make(map[string][]string, len(funcs))
	keys := make([]string, 0, len(funcs))
	byKey := make(map[string]*ast.FuncDecl, len(funcs))
	for _, fn := range funcs {
		k := funcKey(view, fn)
		keys = append(keys, k)
		byKey[k] = fn
		deps[k] = e.collectDeps(view, fn)
	}
	return e.schedule(ctx, view, keys, deps, byKey)
}

func (e *Engine) analyzeReport(ctx context.Context, view *pkgView, start time.Time) report.PackageReport {
	sev, _ := report.ParseSeverity(e.cfg.MinSeverity)
	pr := report.PackageReport{Package: view.path, Dir: view.dir0()}

	results := e.analyzeView(ctx, view)
	keys := make([]string, 0, len(results))
	for k := range results {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	alertSeen := map[[2]interface{}]bool{}
	for _, key := range keys {
		res, ok := results[key]
		if !ok || res == nil {
			pr.Skipped++
			pr.Incomplete = true
			continue
		}
		if res.Canceled {
			pr.Canceled = true
			pr.Incomplete = true
		}
		if res.Incomplete {
			pr.Incomplete = true
		}
		pr.Functions++
		for _, a := range res.Alerts {
			k := [2]interface{}{a.Position, a.Kind}
			if alertSeen[k] {
				continue
			}
			alertSeen[k] = true
			pr.Alerts = append(pr.Alerts, a)
		}
	}
	pr.DurationMS = time.Since(start).Milliseconds()
	return report.Finalize(pr, sev)
}

// schedule analyzes functions level-by-level over the dependency DAG. Within
// a level functions are independent, so they run in parallel; the next level
// only starts once the previous summaries are committed. Recursive groups are
// detected and analyzed iteratively to a stable over-approximation.
func (e *Engine) schedule(
	ctx context.Context,
	view *pkgView,
	keys []string,
	deps map[string][]string,
	byKey map[string]*ast.FuncDecl,
) map[string]*FuncResult {
	results := make(map[string]*FuncResult, len(keys))
	done := make(map[string]bool, len(keys))
	workers := e.cfg.Concurrency
	if workers < 1 {
		workers = 1
	}

	for len(done) < len(keys) {
		if err := ctx.Err(); err != nil {
			for _, k := range keys {
				if !done[k] {
					results[k] = canceledResult(view.path, byKey[k], k)
					done[k] = true
				}
			}
			break
		}
		level := readyLevel(keys, deps, done)
		if len(level) == 0 {
			// Remaining nodes participate in a recursion cycle: analyze them
			// jointly, repeatedly, until their summaries stop growing.
			level = remaining(keys, done)
			e.analyzeCycle(ctx, view, level, byKey, results, done)
			continue
		}
		e.runLevel(ctx, view, level, byKey, results, done, workers)
	}
	return results
}

func (e *Engine) runLevel(
	ctx context.Context,
	view *pkgView,
	level []string,
	byKey map[string]*ast.FuncDecl,
	results map[string]*FuncResult,
	done map[string]bool,
	workers int,
) {
	if workers == 1 || len(level) == 1 {
		for _, k := range level {
			res := e.analyzeOneReuseAware(ctx, view, k, byKey[k])
			results[k] = res
			done[k] = true
			if res.Summary != nil {
				e.store.Put(k, res.Summary)
			}
		}
		return
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, k := range level {
		if err := ctx.Err(); err != nil {
			results[k] = canceledResult(view.path, byKey[k], k)
			done[k] = true
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(key string) {
			defer wg.Done()
			defer func() { <-sem }()
			res := e.analyzeOneReuseAware(ctx, view, key, byKey[key])
			mu.Lock()
			results[key] = res
			done[key] = true
			mu.Unlock()
			if res.Summary != nil {
				e.store.Put(key, res.Summary)
			}
		}(k)
	}
	wg.Wait()
}

// analyzeCycle reaches a stable over-approximation for mutually recursive
// functions by iterating analysis with updated summaries. The interval
// lattice has finite height once widened, so this terminates; if the iteration
// budget is exhausted the group is marked incomplete.
func (e *Engine) analyzeCycle(
	ctx context.Context,
	view *pkgView,
	level []string,
	byKey map[string]*ast.FuncDecl,
	results map[string]*FuncResult,
	done map[string]bool,
) {
	stable := false
	for round := 0; round < e.cfg.MaxIterations && !stable; round++ {
		if err := ctx.Err(); err != nil {
			break
		}
		stable = true
		prevFp := make(map[string]string, len(level))
		for _, k := range level {
			if s, ok := e.store.Get(k); ok {
				prevFp[k] = s.Fingerprint
			}
		}
		for _, k := range level {
			res := e.analyzeFresh(ctx, view, byKey[k])
			results[k] = res
			if res.Summary != nil {
				e.store.Put(k, res.Summary)
				if res.Summary.Fingerprint != prevFp[k] {
					stable = false
				}
			}
		}
	}
	for _, k := range level {
		if results[k] == nil {
			results[k] = canceledResult(view.path, byKey[k], k)
		}
		if err := ctx.Err(); err != nil {
			results[k].Canceled = true
			results[k].Incomplete = true
		}
		done[k] = true
	}
}

func readyLevel(keys []string, deps map[string][]string, done map[string]bool) []string {
	var level []string
	for _, k := range keys {
		if done[k] {
			continue
		}
		ready := true
		for _, d := range deps[k] {
			if _, isLocal := deps[d]; isLocal && !done[d] {
				ready = false
				break
			}
		}
		if ready {
			level = append(level, k)
		}
	}
	sort.Strings(level)
	return level
}

func remaining(keys []string, done map[string]bool) []string {
	var out []string
	for _, k := range keys {
		if !done[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func canceledResult(pkgPath string, fn *ast.FuncDecl, key string) *FuncResult {
	return &FuncResult{
		Key:        key,
		Name:       safeName(fn),
		Package:    pkgPath,
		Incomplete: true,
		Canceled:   true,
	}
}

func safeName(fn *ast.FuncDecl) string {
	if fn != nil && fn.Name != nil {
		return fn.Name.Name
	}
	return "(unknown)"
}

// analyzeOneReuseAware returns a cached summary's previously stored result
// only when the function and every callee summary are unchanged; otherwise it
// re-analyzes. Alerts for reused functions are not silently dropped: the
// cached alerts are replayed so reports stay identical to a full run.
func (e *Engine) analyzeOneReuseAware(ctx context.Context, view *pkgView, key string, fn *ast.FuncDecl) *FuncResult {
	if cached, ok := e.store.Get(key); ok && cached.Analyzed && cached.Hash == view.hash {
		if e.depsUnchanged(cached) {
			return &FuncResult{
				Key:        key,
				Name:       cached.Name,
				Package:    view.path,
				Hash:       view.hash,
				Deps:       append([]string(nil), cached.Deps...),
				Alerts:     append([]report.Alert(nil), cached.Alerts...),
				Incomplete: cached.Incomplete,
				Summary:    cached,
			}
		}
	}
	return e.analyzeFresh(ctx, view, fn)
}

func (e *Engine) depsUnchanged(s *Summary) bool {
	for dep, fp := range s.DepsSummaries {
		cur, ok := e.store.Get(dep)
		if !ok {
			return false
		}
		if cur.Fingerprint != fp {
			return false
		}
	}
	return true
}

func (e *Engine) analyzeFresh(ctx context.Context, view *pkgView, fn *ast.FuncDecl) *FuncResult {
	return e.runFuncAnalysis(ctx, view, fn, e.store)
}

var _ = types.Universe
var _ = domain.TopValue

func (p *pkgView) dir0() string { return p.path }
