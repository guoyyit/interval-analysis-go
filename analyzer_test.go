package intervalanalysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePkg(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func analyzeSrc(t *testing.T, src string, cfg Config) *Result {
	t.Helper()
	dir := t.TempDir()
	writePkg(t, dir, "p", src)
	a := NewAnalyzer(cfg)
	res, err := a.Analyze(context.Background(), dir, false)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return res
}

func categories(ws []Warning) map[Category]int {
	m := map[Category]int{}
	for _, w := range ws {
		m[w.Category]++
	}
	return m
}

func TestLoopConvergenceAndIndexCheck(t *testing.T) {
	src := `package p

func f(n int) int {
	a := [10]int{}
	s := 0
	for i := 0; i < n; i++ {
		s = s + a[i%10]
	}
	return s
}

func safeLoop(n int) int {
	a := [10]int{}
	s := 0
	for i := 0; i < 10; i++ {
		s = s + a[i]
	}
	return s
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("analysis did not converge: %v", res.Unfinished)
	}
	cats := categories(res.Warnings)
	if cats[CategoryIndexOutOfBounds] == 0 {
		t.Fatalf("expected index warning for a[i%%10] reasoning, got %+v", cats)
	}
	// safeLoop uses a literal-bounded loop variable and must not warn.
	for _, w := range res.Warnings {
		if w.Function == "p.safeLoop" && w.Category == CategoryIndexOutOfBounds {
			t.Fatalf("safeLoop falsely flagged: %+v", w)
		}
	}
}

func TestDivByZeroDataFlow(t *testing.T) {
	src := `package p

func f(x int) int {
	y := x - 5
	if y > 10 {
		return 100 / y
	}
	if x == 5 {
		return 7 / y
	}
	return 0
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	cats := categories(res.Warnings)
	if cats[CategoryDivByZero] == 0 {
		t.Fatal("expected div-by-zero on x==5 path, data-flow not tracked")
	}
}

func TestCrossFunctionSummary(t *testing.T) {
	src := `package p

func gate(n int) int {
	if n >= 0 && n < 4 {
		return n
	}
	return 0
}

func caller(n int) int {
	a := [4]int{}
	return a[gate(n)]
}

func ext(x int) int {
	b := [4]int{}
	return b[x]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	// caller is safe because gate returns [0,3].
	for _, w := range res.Warnings {
		if w.Function == "p.caller" {
			t.Fatalf("caller must be safe via summary, got %+v", w)
		}
	}
	// ext is unsafe because the parameter is full range.
	found := false
	for _, w := range res.Warnings {
		if w.Function == "p.ext" && w.Category == CategoryIndexOutOfBounds {
			found = true
		}
	}
	if !found {
		t.Fatal("ext must warn for full-range parameter")
	}
}

func TestIncrementalReuse(t *testing.T) {
	src1 := `package p

func helper(n int) int {
	if n >= 0 && n < 3 {
		return n
	}
	return 0
}

func use(n int) int {
	a := [3]int{}
	return a[helper(n)]
}
`
	dir := t.TempDir()
	writePkg(t, dir, "p", src1)
	cfg := DefaultConfig()
	a := NewAnalyzer(cfg)
	res1, err := a.Analyze(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res1.Complete || len(res1.Warnings) != 0 {
		t.Fatalf("baseline should be clean, got %+v", res1.Warnings)
	}
	// Package path used by types is the directory name; locate the helper id
	// dynamically instead of assuming it.
	var helperID FuncID
	for _, sum := range a.Store().Snapshot() {
		if sum.ID.Name == "helper" {
			helperID = sum.ID
		}
	}

	// Re-run without changes: results reused.
	res2, err := a.Analyze(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Warnings) != 0 {
		t.Fatalf("unchanged rerun should be clean: %+v", res2.Warnings)
	}
	if sum, ok := a.Store().Get(helperID); !ok || !sum.Complete {
		t.Fatal("helper summary missing after reuse")
	}

	// Change helper to return wider range: use must be re-analyzed and warn.
	src2 := `package p

func helper(n int) int {
	return n
}

func use(n int) int {
	a := [3]int{}
	return a[helper(n)]
}
`
	writePkg(t, dir, "p", src2)
	res3, err := a.Analyze(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range res3.Warnings {
		if w.Function == "p.use" && w.Category == CategoryIndexOutOfBounds {
			found = true
		}
	}
	if !found {
		t.Fatal("dependent not re-analyzed after callee summary changed")
	}
}

func TestSerialParallelEquivalence(t *testing.T) {
	src := `package p

func f0(x int) int { return 10 / x }
func f1(x int) int { a := [2]int{}; return a[x] }
func f2(x int) int { return f0(x) + f1(x) }
func f3(x int) int { return f2(x) + 1 }
func f4(x int) int { z := 0; return f3(x) / (z) }
`
	run := func(workers int) []Warning {
		dir := t.TempDir()
		writePkg(t, dir, "p", src)
		cfg := DefaultConfig()
		cfg.Workers = workers
		a := NewAnalyzer(cfg)
		res, err := a.Analyze(context.Background(), dir, false)
		if err != nil {
			t.Fatal(err)
		}
		return res.Warnings
	}
	serial := run(1)
	par := run(8)
	if len(serial) != len(par) {
		t.Fatalf("warning count differs: serial=%d parallel=%d", len(serial), len(par))
	}
	for i := range serial {
		if serial[i].dedupKey() != par[i].dedupKey() ||
			serial[i].Message != par[i].Message {
			t.Fatalf("mismatch at %d:\n%+v\n%+v", i, serial[i], par[i])
		}
	}
}

func TestTimeoutMarksIncomplete(t *testing.T) {
	src := `package p

func f(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			s += i + j
		}
	}
	return s
}
`
	dir := t.TempDir()
	writePkg(t, dir, "p", src)
	cfg := Config{
		MaxIterations:   5,
		WidenAfter:      1,
		NarrowingPasses: 0,
		Timeout:         time.Nanosecond,
	}
	a := NewAnalyzer(cfg)
	res, err := a.Analyze(context.Background(), dir, false)
	if err != nil {
		t.Fatalf("timeout should be reported via result, got err %v", err)
	}
	if res.Complete {
		t.Fatal("interrupted analysis must not be marked complete")
	}
	if len(res.Unfinished) == 0 {
		t.Fatal("expected unfinished functions on timeout")
	}
}

func TestSeverityFilter(t *testing.T) {
	src := `package p
func f(x int) int { return 1 / x }
`
	res := analyzeSrc(t, src, Config{MinSeverity: SeverityHigh})
	for _, w := range res.Warnings {
		if w.Severity < SeverityHigh {
			t.Fatalf("filter leaked low severity: %+v", w)
		}
	}
}
