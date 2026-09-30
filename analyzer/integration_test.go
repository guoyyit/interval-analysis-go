package analyzer

import (
	"context"
	"testing"

	"github.com/guoyyit/interval-analysis-go/report"
)

// End-to-end through the public engine: run a package twice with a shared
// store and confirm reuse produces identical alert sets.
func TestRepeatedRunIdentical(t *testing.T) {
	src := `
package p
func h(x int) int {
	if x != 0 { return 12 / x }
	return 1
}
func g(n int) int {
	a := [3]int{}
	s := 0
	for i := 0; i < n; i++ { s += a[i] }
	return s
}
`
	c := testConfig()
	e, view := mustAnalyze(t, src, c)
	r1 := e.AnalyzePackageView(context.Background(), view)
	r2 := e.AnalyzePackageView(context.Background(), view)
	if alertsSig(r1.Alerts) != alertsSig(r2.Alerts) {
		t.Fatalf("repeated runs differ:\n%s\n%s", alertsSig(r1.Alerts), alertsSig(r2.Alerts))
	}
	if r1.Incomplete != r2.Incomplete {
		t.Fatal("completeness flag must be stable across reuse")
	}
}

// Filtering changes output count but never soundness: low shows more alerts
// than high, and the underlying analysis is unchanged.
func TestSeverityFiltering(t *testing.T) {
	src := `
package p
func f(x int) int { return 10 / x }
func g() int64 { var z int64 = 1<<63 - 1; return z + 1 }
`
	low := testConfig()
	low.MinSeverity = "low"
	_, view := mustAnalyze(t, src, low)
	e := New(low)
	all := e.AnalyzePackageView(context.Background(), view)
	if len(all.Alerts) < 2 {
		t.Fatalf("expected div + overflow at low severity, got %d", len(all.Alerts))
	}
	highC := low
	highC.MinSeverity = "high"
	e2 := New(highC)
	hi := e2.AnalyzePackageView(context.Background(), view)
	if len(hi.Alerts) != 1 || hi.Alerts[0].Kind != report.KindDivByZero {
		t.Fatalf("high filter should keep only divide-by-zero, got %v", hi.Alerts)
	}
}

func alertsSig(a []report.Alert) string {
	out := ""
	for _, x := range a {
		out += x.KindName + "@" + x.Position + ";"
	}
	return out
}
