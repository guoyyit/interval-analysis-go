package analyzer

import (
	"go/ast"
	"sort"
	"strings"
	"testing"

	"github.com/guoyyit/interval-analysis-go/config"
	"github.com/guoyyit/interval-analysis-go/domain"
)

func mustDecl(view *pkgView, name string) *ast.FuncDecl {
	for _, f := range view.files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
				return fn
			}
		}
	}
	return nil
}

func mustAnalyze(t *testing.T, src string, c config.Config) (*Engine, *pkgView) {
	t.Helper()
	e, view, _ := analyzeSrc(t, src, c)
	return e, view
}

// signature renders an analysis result set as a deterministic multiset of
// (function, alert kind, position) so serial and parallel runs can be
// compared exactly.
func signature(results map[string]*FuncResult) string {
	var lines []string
	for key, r := range results {
		if r == nil {
			lines = append(lines, key+":<nil>")
			continue
		}
		if len(r.Alerts) == 0 {
			lines = append(lines, key+":ok")
			continue
		}
		for _, a := range r.Alerts {
			lines = append(lines, key+":"+a.KindName+"@"+a.Position)
		}
		if r.Incomplete {
			lines = append(lines, key+":incomplete")
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func domainTop64() domain.Interval { return domain.TopValue() }
