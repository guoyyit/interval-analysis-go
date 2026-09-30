package intervalanalysis

import (
	"go/ast"
	"go/types"
	"sort"
)

// CallGraph is the static call relation between analyzed functions.
type CallGraph struct {
	edges   map[FuncID][]FuncID
	reverse map[FuncID][]FuncID
	funcs   map[FuncID]*FunctionInfo
}

// BuildCallGraph derives call edges from all target functions.
func BuildCallGraph(targets []*AnalysisTarget) *CallGraph {
	g := &CallGraph{
		edges:   map[FuncID][]FuncID{},
		reverse: map[FuncID][]FuncID{},
		funcs:   map[FuncID]*FunctionInfo{},
	}
	for _, t := range targets {
		for _, fn := range t.Functions {
			g.funcs[fn.ID] = fn
		}
	}
	for _, t := range targets {
		for _, fn := range t.Functions {
			callees := g.calleesOf(fn)
			g.edges[fn.ID] = callees
			for _, c := range callees {
				g.reverse[c] = append(g.reverse[c], fn.ID)
			}
		}
	}
	for id := range g.edges {
		sortIDs(g.edges[id])
	}
	for id := range g.reverse {
		g.reverse[id] = uniqueIDs(g.reverse[id])
		sortIDs(g.reverse[id])
	}
	return g
}

func sortIDs(ids []FuncID) {
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].PkgPath != ids[j].PkgPath {
			return ids[i].PkgPath < ids[j].PkgPath
		}
		return ids[i].Name < ids[j].Name
	})
}

func uniqueIDs(ids []FuncID) []FuncID {
	seen := map[FuncID]bool{}
	out := ids[:0]
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// Callees returns the functions directly called by id.
func (g *CallGraph) Callees(id FuncID) []FuncID { return g.edges[id] }

// Callers returns functions that directly call id.
func (g *CallGraph) Callers(id FuncID) []FuncID { return g.reverse[id] }

// Lookup returns the analyzed function with the given id.
func (g *CallGraph) Lookup(id FuncID) (*FunctionInfo, bool) {
	fn, ok := g.funcs[id]
	return fn, ok
}

func (g *CallGraph) calleesOf(fn *FunctionInfo) []FuncID {
	seen := map[FuncID]bool{}
	var out []FuncID
	ast.Inspect(fn.Decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := g.resolveCall(call, fn.Pkg); ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
		return true
	})
	sortIDs(out)
	return out
}

// resolveCall maps a call expression to a FuncID of an analyzed function.
func (g *CallGraph) resolveCall(call *ast.CallExpr, lp *LoadedPackage) (FuncID, bool) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj := lp.Info.Uses[fun]
		return g.objectToID(obj)
	case *ast.SelectorExpr:
		obj := lp.Info.Uses[fun.Sel]
		return g.objectToID(obj)
	}
	return FuncID{}, false
}

func (g *CallGraph) objectToID(obj types.Object) (FuncID, bool) {
	fn, ok := obj.(*types.Func)
	if !ok {
		return FuncID{}, false
	}
	id := objectFuncID(fn)
	if _, known := g.funcs[id]; known {
		return id, true
	}
	return FuncID{}, false
}

// objectFuncID computes the FuncID of a types.Func, including receivers.
func objectFuncID(fn *types.Func) FuncID {
	pkgPath := ""
	if fn.Pkg() != nil {
		pkgPath = fn.Pkg().Path()
	}
	name := fn.Name()
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		name = recvTypeName(sig.Recv().Type()) + "." + name
	}
	return FuncID{PkgPath: pkgPath, Name: name}
}

func recvTypeName(t types.Type) string {
	if pt, ok := t.(*types.Pointer); ok {
		t = pt.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return "recv"
}
