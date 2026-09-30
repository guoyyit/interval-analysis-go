package cfg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func buildCFG(t *testing.T, src string) *CFG {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", "package p\n"+src, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Decls[0].(*ast.FuncDecl)
	return Build(fn)
}

func TestIfBranches(t *testing.T) {
	g := buildCFG(t, `func f(x int) int { if x > 0 { return 1 }; return 0 }`)
	var condEdges int
	for _, b := range g.Blocks {
		for _, e := range b.Succs {
			if e.Cond != nil {
				condEdges++
			}
		}
	}
	if condEdges != 2 {
		t.Fatalf("expected 2 conditional edges (true/false), got %d", condEdges)
	}
}

func TestForLoopHasBackEdge(t *testing.T) {
	g := buildCFG(t, `func f(n int) int { s:=0; for i:=0;i<n;i++ { s+=i }; return s }`)
	heads := 0
	for _, b := range g.Blocks {
		for _, p := range b.Preds {
			if p.Index >= b.Index {
				heads++
			}
		}
	}
	if heads == 0 {
		t.Fatal("expected at least one loop back edge")
	}
}
