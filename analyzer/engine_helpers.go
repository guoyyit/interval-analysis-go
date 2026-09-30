package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/report"
)

func funcKey(p *pkgView, fn *ast.FuncDecl) string {
	if obj := declaredFuncObject(p, fn); obj != nil {
		return p.path + "#" + obj.Id()
	}
	return p.path + "#" + fn.Name.Name
}

func collectFuncs(p *pkgView) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, file := range p.files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				out = append(out, fn)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		pi := p.fset.Position(out[i].Pos()).Offset
		pj := p.fset.Position(out[j].Pos()).Offset
		return pi < pj
	})
	return out
}

// declaredFuncObject resolves the types.Func introduced by a FuncDecl. For
// methods that is keyed by Id() (receiver type + method), giving one stable
// key shared by dependency collection and cache lookup.
func declaredFuncObject(p *pkgView, fn *ast.FuncDecl) types.Object {
	if p != nil && p.info != nil && p.info.Defs != nil {
		if obj, ok := p.info.Defs[fn.Name]; ok && obj != nil {
			return obj
		}
	}
	if fn.Name == nil {
		return nil
	}
	if obj, ok := fn.Name.Obj.Data.(*types.Func); ok {
		return obj
	}
	return nil
}

// collectDeps returns the keys of same-package functions called by fn.
// External callees are not dependencies (their summaries are top by design).
func (e *Engine) collectDeps(p *pkgView, fn *ast.FuncDecl) []string {
	set := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		obj := p.info.Uses[id]
		f, ok := obj.(*types.Func)
		if !ok || f.Pkg() == nil {
			return true
		}
		if f.Pkg().Path() != p.pkg.Path() {
			return true
		}
		set[p.path+"#"+f.Id()] = true
		return true
	})
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) buildSummary(p *pkgView, fn *ast.FuncDecl, fa *funcAnalyzer, deps []string, incomplete bool) *Summary {
	sum := &Summary{
		Key:           funcKey(p, fn),
		Name:          fa.fnName,
		HasResults:    fn.Type.Results != nil,
		Deps:          deps,
		DepsSummaries: map[string]string{},
		Hash:          p.hash,
		Analyzed:      true,
		Incomplete:    incomplete,
	}
	if len(fa.alerts) > 0 {
		sum.MayPanic = true
	}
	nResults := 0
	if fn.Type.Results != nil {
		for _, f := range fn.Type.Results.List {
			count := len(f.Names)
			if count == 0 {
				count = 1
			}
			nResults += count
		}
	}
	results := make([]domain.Interval, nResults)
	for i := range results {
		results[i] = domain.EmptyValue()
	}
	// Join intervals observed at every return statement, a sound hull of all
	// possible outputs for top-input parameters.
	for _, ret := range fa.returnVals {
		for i := 0; i < len(ret) && i < nResults; i++ {
			if results[i].Empty {
				results[i] = ret[i]
			} else {
				results[i] = domain.Union(results[i], ret[i])
			}
		}
	}
	// No reachable explicit return recorded (e.g. only naked returns, or a
	// function we could not model fully): stay full range, never pretend a
	// tighter output.
	for i := range results {
		if results[i].Empty {
			results[i] = domain.TopValue()
		}
	}
	// Naked returns: join named result variables from the final state is
	// handled by callers only if explicit returns exist; otherwise variables
	// stay top (conservative).
	sum.Results = results
	sum.Alerts = append([]report.Alert(nil), fa.alerts...)
	for _, dep := range deps {
		if s, ok := e.store.Get(dep); ok {
			sum.DepsSummaries[dep] = s.Fingerprint
			if s.MayPanic {
				sum.MayPanic = true
			}
		} else {
			// Unanalyzed/recursive callee: mark dependency with empty
			// fingerprint so the result stays conservative and triggers
			// re-analysis later.
			sum.DepsSummaries[dep] = ""
		}
	}
	return sum
}

// widenStates widens each variable present in the new incoming state against
// its previous value, using the variable's declared type bounds.
func widenStates(old, incoming *State, fallback domain.Bounds) *State {
	out := NewState()
	for obj := range unionObjects(old, incoming) {
		a := old.Get(obj)
		b := incoming.Get(obj)
		bnd := boundsForObject(obj, fallback)
		out.Set(obj, domain.Widen(a, b, bnd))
	}
	return out
}

func narrowStates(old, incoming *State) *State {
	out := NewState()
	for obj := range unionObjects(old, incoming) {
		a := old.Get(obj)
		b := incoming.Get(obj)
		bnd := boundsForObject(obj, domain.IntBounds(64, true))
		out.Set(obj, domain.Narrow(a, b, bnd))
	}
	return out
}

func headWidenBounds(old, incoming *State) domain.Bounds {
	for obj := range old.vars {
		if b, ok := boundsOf(obj.Type()); ok {
			return b
		}
	}
	return domain.IntBounds(64, true)
}

func boundsForObject(obj types.Object, fallback domain.Bounds) domain.Bounds {
	if b, ok := boundsOf(obj.Type()); ok {
		return b
	}
	return fallback
}

func unionObjects(a, b *State) map[types.Object]bool {
	set := map[types.Object]bool{}
	for o := range a.vars {
		set[o] = true
	}
	for o := range b.vars {
		set[o] = true
	}
	return set
}

var _ = token.NoPos
