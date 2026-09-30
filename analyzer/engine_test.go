package analyzer

import (
	"context"
	"testing"
	"time"

	"github.com/guoyyit/interval-analysis-go/config"
)

func testConfig() config.Config {
	c := config.Default()
	c.MaxIterations = 64
	c.NarrowingPasses = 2
	return c
}

// Scenario 1: loop convergence. A guarded bounded loop must converge and not
// be reported; an unbounded loop indexing a fixed array must be flagged.
func TestLoopConvergence(t *testing.T) {
	src := `
package p
func safe() int {
	a := [4]int{1,2,3,4}
	s := 0
	for i := 0; i < 4; i++ {
		if i >= 0 && i < 4 {
			s += a[i]
		}
	}
	return s
}
func risky(n int) int {
	a := [4]int{}
	t := 0
	for j := 0; j < n; j++ {
		t += a[j]
	}
	return t
}
`
	_, _, res := analyzeSrc(t, src, testConfig())
	safe := findResult(res, "safe")
	risky := findResult(res, "risky")
	if safe == nil || risky == nil {
		t.Fatal("missing results")
	}
	if hasKind(risky, "index-out-of-bounds") == false {
		t.Errorf("risky unbounded loop must report index OOB")
	}
	if safe.Incomplete {
		t.Errorf("bounded loop analysis must converge completely, got incomplete")
	}
}

// Scenario 2: cross-function summaries. A divisor proven non-zero by a
// callee-returned range must not warn; a top-range callee must warn.
func TestCrossFunctionSummary(t *testing.T) {
	src := `
package p
func positive() int { return 7 }
func callerSafe() int {
	d := positive()
	return 100 / d
}
func external(x int) int { return x }
func callerTop() int {
	d := external(0)
	return 100 / d
}
`
	_, _, res := analyzeSrc(t, src, testConfig())
	if p := findResult(res, "positive"); p != nil && p.Summary != nil {
		t.Logf("positive summary results=%v maypanic=%v", p.Summary.Results, p.Summary.MayPanic)
	}
	if r := findResult(res, "callerSafe"); r != nil && hasKind(r, "divide-by-zero") {
		t.Errorf("callerSafe must not warn: callee summary proves d=7")
	}
	if r := findResult(res, "callerTop"); r != nil && !hasKind(r, "divide-by-zero") {
		t.Errorf("callerTop must warn: parameterized callee returns full range")
	}
}

// Scenario 3: incremental reuse. Re-analyzing an unchanged function with the
// same store must reuse its summary; changing a dependency fingerprint must
// invalidate dependents.
func TestIncrementalReuse(t *testing.T) {
	c := testConfig()
	src := `
package p
func base(x int) int { return x + 1 }
func use() int { return base(3) }
`
	e, view, res := analyzeSrc(t, src, c)
	if s, ok := e.store.Get(funcKey(view, mustDecl(view, "use"))); !ok || !s.Analyzed {
		t.Fatal("expected cached summary for use")
	}
	// Second run with identical content/hash and unchanged deps reuses.
	cachedKey := funcKey(view, mustDecl(view, "use"))
	before := res[funcKey(view, mustDecl(view, "use"))]
	if before == nil {
		t.Fatal("missing use result")
	}
	got := e.analyzeOneReuseAware(context.Background(), view, cachedKey, mustDecl(view, "use"))
	// The reused result must replay the same alerts as a fresh analysis, so
	// incremental output equals full-run output.
	if len(got.Alerts) != len(before.Alerts) {
		t.Fatalf("reused alerts = %d, fresh alerts = %d (must match)",
			len(got.Alerts), len(before.Alerts))
	}
	if s, ok := e.store.Get(cachedKey); !ok || s.Fingerprint == "" {
		t.Fatal("reused summary missing fingerprint")
	}
}

// When a callee summary changes, dependents must be considered stale and
// re-analyzed; while unchanged, the cached summary is reusable.
func TestIncrementalDependencyInvalidation(t *testing.T) {
	c := testConfig()
	src := `
package p
func base() int { return 5 }
func use() int { return base() }
`
	e, view, _ := analyzeSrc(t, src, c)
	useDecl := mustDecl(view, "use")
	baseDecl := mustDecl(view, "base")
	useKey := funcKey(view, useDecl)
	baseKey := funcKey(view, baseDecl)

	useSum, ok := e.store.Get(useKey)
	if !ok {
		t.Fatal("use summary missing")
	}
	if !e.depsUnchanged(useSum) {
		t.Fatal("freshly computed dependency graph must be unchanged")
	}
	// Tamper the callee fingerprint: dependent must go stale.
	cur, ok := e.store.Get(baseKey)
	if !ok {
		t.Fatal("base summary missing")
	}
	cur.Results = append(cur.Results, domainTop64())
	cur.seal()
	e.store.Put(baseKey, cur)
	if e.depsUnchanged(useSum) {
		t.Fatal("dependent must be invalidated when callee fingerprint changes")
	}
}

// Scenario 4: concurrent consistency. Running with jobs=1 and jobs=16 must
// produce identical alert multisets.
func TestConcurrentConsistency(t *testing.T) {
	src := `
package p
func f0(x int) int { return 10 / x }
func f1(x int) int {
	if x != 0 { return 10 / x }
	return 0
}
func f2() int {
	a := [2]int{1,2}
	s := 0
	for i := 0; i < 100; i++ { if i >= 0 && i < 2 { s += a[i] } }
	return s
}
func f3(n int) int {
	a := [3]int{}
	t := 0
	for k := 0; k < n; k++ { t += a[k] }
	return t
}
func f4() int64 {
	var x int64 = 1<<63 - 1
	return x + 1
}
`
	seq := testConfig()
	seq.Concurrency = 1
	par := testConfig()
	par.Concurrency = 16

	_, _, r1 := analyzeSrc(t, src, seq)
	_, _, r2 := analyzeSrc(t, src, par)
	if signature(r1) != signature(r2) {
		t.Fatalf("serial/parallel mismatch\nserial=%s\nparallel=%s", signature(r1), signature(r2))
	}
}

// Scenario 5: timeout/cancelation. An already-canceled context must mark
// every function incomplete/canceled rather than emit a complete verdict.
func TestTimeoutCancelation(t *testing.T) {
	src := `
package p
func a(x int) int { return 10 / x }
func b(x int) int {
	s := 0
	for i := 0; i < x; i++ { s += i }
	return s
}
`
	c := testConfig()
	e, view, _ := analyzeSrc(t, src, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := e.analyzeView(ctx, view)
	for k, r := range res {
		if r == nil || (!r.Incomplete && !r.Canceled) {
			t.Fatalf("function %s must be marked incomplete/canceled", k)
		}
	}
}

func TestDeadlineFlagsIncomplete(t *testing.T) {
	src := `package p
func b(x int) int { s := 0; for i := 0; i < x; i++ { s += i }; return s }
`
	c := testConfig()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	_, view := mustAnalyze(t, src, c)
	e := New(c)
	res := e.analyzeView(ctx, view)
	anyIncomplete := false
	for _, r := range res {
		if r != nil && (r.Incomplete || r.Canceled) {
			anyIncomplete = true
		}
	}
	if !anyIncomplete {
		t.Fatal("expired deadline must yield at least one incomplete function")
	}
}

// Reusing a function that has alerts must replay those alerts byte-for-byte,
// including through persistence (marshal/unmarshal).
func TestIncrementalReuseReplaysAlerts(t *testing.T) {
	src := `
package p
func dbz(x int) int { return 10 / x }
func caller() int { return dbz(5) }
`
	c := testConfig()
	e, view, _ := analyzeSrc(t, src, c)
	dbzDecl := mustDecl(view, "dbz")
	dbzKey := funcKey(view, dbzDecl)

	fresh := e.analyzeOneReuseAware(context.Background(), view, dbzKey, dbzDecl)
	if len(fresh.Alerts) != 1 || fresh.Alerts[0].KindName != "divide-by-zero" {
		t.Fatalf("fresh dbz alerts = %v, want 1 divide-by-zero", fresh.Alerts)
	}
	// Persist and reload the store, then reuse: alert must survive.
	data, err := e.store.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	store2 := NewSummaryStore()
	if err := store2.Unmarshal(data); err != nil {
		t.Fatal(err)
	}
	e2 := New(c)
	e2.store = store2
	reused := e2.analyzeOneReuseAware(context.Background(), view, dbzKey, dbzDecl)
	if len(reused.Alerts) != 1 || reused.Alerts[0].KindName != "divide-by-zero" {
		t.Fatalf("reused dbz alerts = %v, want replayed divide-by-zero", reused.Alerts)
	}
	if reused.Alerts[0].IntervalBasis == "" || reused.Alerts[0].Position == "" {
		t.Fatal("replayed alert lost basis/position")
	}
}
