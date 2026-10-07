package intervalanalysis

import (
	"context"
	"path/filepath"
	"testing"
)

// analyzeSrcWith is analyzeSrc but allows a pre-configured analyzer.
func analyzeSrcWith(t *testing.T, a *Analyzer, src string) *Result {
	t.Helper()
	dir := t.TempDir()
	writePkg(t, dir, "p", src)
	res, err := a.Analyze(context.Background(), dir, false)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return res
}

// analyzeDir analyzes the package in dir with a fresh analyzer per config, so
// all configs see identical file paths and results are directly comparable.
func analyzeDir(t *testing.T, dir string, cfg Config) *Result {
	t.Helper()
	res, err := NewAnalyzer(cfg).Analyze(context.Background(), dir, false)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return res
}

// controlFlowSrc exercises merge points, switch cases, goto and fallthrough
// with package-level variables.
const controlFlowSrc = `package p

var D int
var I int
var Arr [4]int

func mergeDiv(x int) int {
	if x > 0 {
		D = 3
	}
	return 100 / D
}

func mergeIndex(x int) int {
	if x > 0 {
		I = 1
	}
	return Arr[I]
}

func switchDiv(c int) int {
	switch c {
	case 1:
		D = 1
	case 2:
		D = 2
	}
	return 50 / D
}

func gotoIndex(x int) int {
	if x > 0 {
		goto done
	}
	I = 2
done:
	return Arr[I]
}

func fallDiv(c int) int {
	d := 1
	switch c {
	case 1:
		d = 0
		fallthrough
	case 2:
		d = d - 1
	}
	if d != 0 {
		return 0
	}
	return 9 / d
}
`

// The same input must produce exactly the same warnings for any worker count.
func TestSerialParallelIdenticalOnControlFlow(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, "p", controlFlowSrc)
	run := func(workers int) []Warning {
		cfg := DefaultConfig()
		cfg.Workers = workers
		return analyzeDir(t, dir, cfg).Warnings
	}
	base := run(1)
	for _, workers := range []int{2, 4, 8} {
		got := run(workers)
		if len(got) != len(base) {
			t.Fatalf("workers=%d: %d warnings, serial had %d", workers, len(got), len(base))
		}
		for i := range base {
			if got[i] != base[i] {
				t.Fatalf("workers=%d: warning %d differs:\nserial: %+v\nparallel: %+v",
					workers, i, base[i], got[i])
			}
		}
	}
	if len(base) == 0 {
		t.Fatal("expected warnings in control-flow fixture")
	}
}

// Precision/performance knobs may change warning counts and runtime, but a
// real reachable error must never vanish from the warning set. On this
// loop-free fixture every config converges to the same exact warning set.
func TestConfigConservationExact(t *testing.T) {
	configs := []Config{
		{},
		{MaxIterations: 50},
		{MaxIterations: 200, WidenAfter: 1},
		{MaxIterations: 300, WidenAfter: 5, NarrowingPasses: 1},
		{MaxIterations: 100, WidenAfter: 3, NarrowingPasses: 4},
		{Workers: 4, MaxIterations: 150},
	}
	dir := t.TempDir()
	writePkg(t, dir, "p", controlFlowSrc)
	var base []Warning
	for i, cfg := range configs {
		res := analyzeDir(t, dir, cfg)
		if !res.Complete {
			t.Fatalf("config %d did not converge: %v", i, res.Unfinished)
		}
		if i == 0 {
			base = res.Warnings
			continue
		}
		if len(res.Warnings) != len(base) {
			t.Fatalf("config %d: %d warnings, baseline had %d", i, len(res.Warnings), len(base))
		}
		for j := range base {
			if res.Warnings[j] != base[j] {
				t.Fatalf("config %d warning %d differs:\n%+v\n%+v", i, j, base[j], res.Warnings[j])
			}
		}
	}
}

// With loops, precision knobs may legitimately change the total warning set,
// but warnings about real reachable errors must be conserved.
func TestConfigConservationTrueErrors(t *testing.T) {
	src := `package p

var Arr [4]int
var D int

func loopIndex(n int) int {
	i := 0
	for k := 0; k < n; k++ {
		i = k
	}
	return Arr[i]
}

func loopDiv(n int) int {
	for k := 0; k < n; k++ {
		D = 8
	}
	return 10 / D
}
`
	trueErrors := []struct {
		fn  string
		cat Category
	}{
		{"p.loopIndex", CategoryIndexOutOfBounds},
		{"p.loopDiv", CategoryDivByZero},
	}
	configs := []Config{
		{MaxIterations: 500},
		{MaxIterations: 500, WidenAfter: 1},
		{MaxIterations: 500, WidenAfter: 4, NarrowingPasses: 1},
		{MaxIterations: 500, WidenAfter: 2, NarrowingPasses: 5},
		{MaxIterations: 500, Workers: 4},
	}
	for i, cfg := range configs {
		res := analyzeSrcWith(t, NewAnalyzer(cfg), src)
		if !res.Complete {
			t.Fatalf("config %d did not converge: %v", i, res.Unfinished)
		}
		for _, te := range trueErrors {
			if !hasWarning(res.Warnings, te.fn, te.cat) {
				t.Errorf("config %d lost true error %s in %s (warnings: %+v)",
					i, te.cat, te.fn, res.Warnings)
			}
		}
	}
}

// Narrowing (precision improvement) must never drop a true warning, including
// through summary return intervals consumed by callers.
func TestNarrowingConservesTrueWarnings(t *testing.T) {
	src := `package p

var Arr [5]int

func get(c bool) int {
	x := 6
	if c {
		x = 0
	}
	return x
}

func caller(c bool) int {
	return Arr[get(c)]
}
`
	for _, passes := range []int{1, 2, 3, 5} {
		cfg := DefaultConfig()
		cfg.NarrowingPasses = passes
		res := analyzeSrcWith(t, NewAnalyzer(cfg), src)
		if !res.Complete {
			t.Fatalf("passes=%d incomplete: %v", passes, res.Unfinished)
		}
		if !hasWarning(res.Warnings, "p.caller", CategoryIndexOutOfBounds) {
			t.Errorf("passes=%d lost caller index warning: %+v", passes, res.Warnings)
		}
	}
}

// Exhausting the iteration budget must mark the function unfinished, make the
// result incomplete and yield a non-zero exit code instead of a clean bill.
func TestIterationBudgetExhaustion(t *testing.T) {
	src := `package p

func f(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += i
	}
	return s
}
`
	cfg := Config{MaxIterations: 1}
	res := analyzeSrcWith(t, NewAnalyzer(cfg), src)
	if res.Complete {
		t.Fatal("budget exhaustion must not report a complete analysis")
	}
	if len(res.Unfinished) == 0 {
		t.Fatal("budget exhaustion must list unfinished functions")
	}
	if code := ExitCode(res); code == 0 {
		t.Fatalf("incomplete result must exit non-zero, got %d", code)
	}
}

// ExitCode encodes the exit-status convention: 0 only for a complete result.
func TestExitCodeConvention(t *testing.T) {
	if ExitCode(nil) == 0 {
		t.Fatal("nil result must not exit 0")
	}
	if ExitCode(&Result{Complete: false}) == 0 {
		t.Fatal("incomplete result must not exit 0")
	}
	if ExitCode(&Result{Complete: true}) != 0 {
		t.Fatal("complete result must exit 0")
	}
	// Warnings do not affect the exit status; completeness does.
	clean := analyzeSrc(t, `package p
func f(x int) int { return 1 / x }
`, DefaultConfig())
	if !clean.Complete || ExitCode(clean) != 0 {
		t.Fatalf("complete analysis with warnings must exit 0: %+v", clean)
	}
}

// A nonexistent target directory must surface as an error rather than a
// "complete, zero warnings" conclusion about code that was never read.
func TestNonexistentDirIsError(t *testing.T) {
	a := NewAnalyzer(DefaultConfig())
	_, err := a.Analyze(context.Background(), filepath.Join(t.TempDir(), "missing"), false)
	if err == nil {
		t.Fatal("analyzing a nonexistent directory must return an error")
	}
}
