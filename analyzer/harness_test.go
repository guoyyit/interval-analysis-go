package analyzer

import (
	"context"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/guoyyit/interval-analysis-go/config"
)

// analyzeSrc type-checks and analyzes a single in-memory package without
// touching the module loader. This keeps end-to-end tests hermetic and fast.
func analyzeSrc(t *testing.T, src string, c config.Config) (*Engine, *pkgView, map[string]*FuncResult) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "input.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Implicits:  map[ast.Node]types.Object{},
	}
	conf := types.Config{Importer: importer.Default()}
	tpkg, err := conf.Check("example/p", fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	view := &pkgView{
		path:  "example/p",
		fset:  fset,
		info:  info,
		pkg:   tpkg,
		files: []*ast.File{file},
		hash:  "h1",
	}
	e := New(c)
	return e, view, e.analyzeView(context.Background(), view)
}

func findResult(results map[string]*FuncResult, name string) *FuncResult {
	for k, r := range results {
		if r != nil && r.Name == name {
			_ = k
			return r
		}
	}
	return nil
}

func hasKind(r *FuncResult, kind string) bool {
	for _, a := range r.Alerts {
		if a.KindName == kind {
			return true
		}
	}
	return false
}

func countKind(r *FuncResult, kind string) int {
	n := 0
	for _, a := range r.Alerts {
		if a.KindName == kind {
			n++
		}
	}
	return n
}
