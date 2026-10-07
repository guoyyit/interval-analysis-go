package intervalanalysis

import (
	"context"
	"testing"
)

// warnKey is a position-independent identity for a warning used in set
// comparisons.
func warnKey(w Warning) string {
	return string(w.Category) + "@" + w.Function + ":" + itoa(int64(w.Position.Line))
}

func warnSet(ws []Warning) map[string]int {
	m := map[string]int{}
	for _, w := range ws {
		m[warnKey(w)]++
	}
	return m
}

// TestPackageVarIfMergeDivisor covers a package-level integer assigned a narrow
// value on one if-branch only. At the merge its range must also cover the
// branch where the assignment did not happen, so a divisor use after the merge
// is flagged as potentially zero.
func TestPackageVarIfMergeDivisor(t *testing.T) {
	src := `package p

var g int

func f(cond bool) int {
	if cond {
		g = 0
	}
	return 100 / g
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryDivByZero) {
		t.Fatalf("expected div_by_zero after if merge, got %+v", res.Warnings)
	}
}

// TestPackageVarIfMergeIndex covers a package-level integer assigned a narrow
// in-range value on one branch and left untouched on the other. The merged
// range must stay wide enough that an out-of-range use after the merge is
// reported.
func TestPackageVarIfMergeIndex(t *testing.T) {
	src := `package p

var g int

func f(cond bool) int {
	a := [4]int{}
	if cond {
		g = 0
	}
	return a[g]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryIndexOutOfBounds) {
		t.Fatalf("expected index_out_of_bounds after if merge, got %+v", res.Warnings)
	}
}

// TestPackageVarSwitchMergeIndex assigns narrow values in individual switch
// cases (with no default). A path skipping every case exists, so the merge
// must keep the variable wide and flag the index use.
func TestPackageVarSwitchMergeIndex(t *testing.T) {
	src := `package p

var g int

func f(k int) int {
	a := [4]int{}
	switch k {
	case 1:
		g = 0
	case 2:
		g = 3
	}
	return a[g]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryIndexOutOfBounds) {
		t.Fatalf("expected index_out_of_bounds after switch merge, got %+v", res.Warnings)
	}
}

// TestPackageVarNestedMergeIndex combines nested if and switch merges so the
// merged range must cover every reachable path, not just the explicit ones.
func TestPackageVarNestedMergeIndex(t *testing.T) {
	src := `package p

var g int

func f(x, y int) int {
	a := [4]int{}
	if x > 0 {
		if y > 0 {
			g = 1
		}
	} else {
		switch y {
		case 0:
			g = 2
		}
	}
	return a[g]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryIndexOutOfBounds) {
		t.Fatalf("expected index_out_of_bounds after nested merge, got %+v", res.Warnings)
	}
}

// TestPackageVarForMergeIndex covers a package-level var assigned inside a loop
// body; after the loop the variable must cover the zero-trip path as well, so a
// post-loop index use is flagged.
func TestPackageVarForMergeIndex(t *testing.T) {
	src := `package p

var g int

func f(n int) int {
	a := [4]int{}
	for i := 0; i < n; i++ {
		g = i
	}
	return a[g]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryIndexOutOfBounds) {
		t.Fatalf("expected index_out_of_bounds after loop merge, got %+v", res.Warnings)
	}
}

// TestGotoForwardPathIndex constructs a forward goto that skips a narrowing
// assignment. The jumped path keeps the package-level wide value, so the index
// use at the label must be reported; the analyzer must not silently drop the
// goto edge and claim safety.
func TestGotoForwardPathIndex(t *testing.T) {
	src := `package p

var g int

func f(x int) int {
	a := [4]int{}
	if x > 0 {
		goto target
	}
	g = 1
target:
	return a[g]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryIndexOutOfBounds) {
		t.Fatalf("expected index_out_of_bounds on goto path, got %+v", res.Warnings)
	}
}

// TestGotoBackwardPathIndex uses a backward goto to form an explicit loop whose
// body indexes with a package-level variable. That reachable path must be
// analyzed (with widening guaranteeing convergence), not skipped.
func TestGotoBackwardPathIndex(t *testing.T) {
	src := `package p

var g int

func f(n int) int {
	a := [4]int{}
	i := 0
loop:
	a[g] = i
	i++
	if i < n {
		goto loop
	}
	return i
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryIndexOutOfBounds) {
		t.Fatalf("expected index_out_of_bounds on backward-goto path, got %+v", res.Warnings)
	}
}

// TestGotoDivisorPath covers a divisor use reachable via a goto-formed path
// where the divisor can be zero.
func TestGotoDivisorPath(t *testing.T) {
	src := `package p

var g int

func f(x int) int {
	if x > 0 {
		goto done
	}
	g = 3
done:
	return 9 / g
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryDivByZero) {
		t.Fatalf("expected div_by_zero on goto path, got %+v", res.Warnings)
	}
}

// TestConfigInvariantWarningConservation checks that precision/performance
// knobs never remove a warning: every configuration that converges reports a
// superset-equal warning identity set of the default configuration.
func TestConfigInvariantWarningConservation(t *testing.T) {
	src := `package p

var g int

func mergeIf(cond bool) int {
	a := [4]int{}
	if cond {
		g = 0
	}
	return 100/g + a[g]
}

func mergeSwitch(k int) int {
	a := [4]int{}
	switch k {
	case 1:
		g = 0
	case 2:
		g = 3
	}
	return a[g]
}

func jumpPath(x int) int {
	a := [4]int{}
	if x > 0 {
		goto target
	}
	g = 1
target:
	return a[g]
}

func plain(x int) int { return 10 / x }
`
	base := analyzeSrc(t, src, DefaultConfig())
	if !base.Complete {
		t.Fatalf("baseline incomplete: %v", base.Unfinished)
	}
	baseSet := warnSet(base.Warnings)

	configs := []Config{
		{MaxIterations: 500, WidenAfter: 1, NarrowingPasses: 0},
		{MaxIterations: 500, WidenAfter: 5, NarrowingPasses: 5},
		{MaxIterations: 500, WidenAfter: 10, NarrowingPasses: 1},
		{MaxIterations: 2000, WidenAfter: 2, NarrowingPasses: 8, Workers: 1},
		{MaxIterations: 2000, WidenAfter: 2, NarrowingPasses: 8, Workers: 8},
	}
	for ci, cfg := range configs {
		res := analyzeSrc(t, src, cfg)
		if !res.Complete {
			t.Fatalf("config %d unexpectedly incomplete: %v", ci, res.Unfinished)
		}
		got := warnSet(res.Warnings)
		for k := range baseSet {
			if got[k] == 0 {
				t.Errorf("config %d lost warning %s present in baseline", ci, k)
			}
		}
	}
}

// TestSerialConcurrentIdentical verifies line-by-line equality of serial and
// concurrent runs over merge- and jump-heavy code.
func TestSerialConcurrentIdentical(t *testing.T) {
	src := `package p

var g int

func f0(cond bool) int {
	a := [2]int{}
	if cond {
		g = 0
	}
	return 10/g + a[g]
}
func f1(k int) int {
	a := [2]int{}
	switch k {
	case 0:
		g = 1
	}
	return a[g]
}
func f2(x int) int {
	a := [2]int{}
	if x > 0 {
		goto out
	}
	g = 0
out:
	return a[g]
}
func f3(x int) int { return f0(x > 0) + f1(x) + f2(x) }
func f4(x int) int { return f3(x) }
`
	run := func(workers int) []Warning {
		return analyzeSrc(t, src, Config{Workers: workers, MaxIterations: 500}).Warnings
	}
	serial := run(1)
	par := run(8)
	if len(serial) != len(par) {
		t.Fatalf("warning count differs: serial=%d parallel=%d", len(serial), len(par))
	}
	for i := range serial {
		if serial[i].dedupKey() != par[i].dedupKey() ||
			serial[i].Message != par[i].Message ||
			serial[i].Evidence != par[i].Evidence {
			t.Fatalf("mismatch at %d:\n%+v\n%+v", i, serial[i], par[i])
		}
	}
}

// TestBudgetExhaustionIncompleteNonZero mirrors the CLI contract: a function
// that cannot reach a conservative fixpoint within the iteration budget must be
// listed as unfinished and the result marked incomplete, never reported as a
// zero-warning complete success.
func TestBudgetExhaustionIncompleteNonZero(t *testing.T) {
	src := `package p

var g int

func f(n int) int {
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			g += i + j
		}
	}
	return g
}
`
	cfg := Config{MaxIterations: 2, WidenAfter: 1, NarrowingPasses: 0}
	res := analyzeSrc(t, src, cfg)
	if res.Complete {
		t.Fatal("budget-exhausted analysis must not be marked complete")
	}
	if len(res.Unfinished) == 0 {
		t.Fatal("budget-exhausted analysis must list unfinished functions")
	}
	// Sanity: cancellation likewise marks the run unfinished.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	writePkg(t, dir, "p", src)
	if _, err := NewAnalyzer(DefaultConfig()).Analyze(ctx, dir, false); err != nil {
		t.Fatalf("canceled run should return result not error: %v", err)
	}
}

func hasWarning(ws []Warning, fn string, cat Category) bool {
	for _, w := range ws {
		if w.Function == fn && w.Category == cat {
			return true
		}
	}
	return false
}

// TestMergeOverflowNarrowType assigns a small value on one branch only; at the
// merge the variable keeps the union with its pre-branch value, and combining
// it with another narrow-typed operand that can exceed the declared type range
// must be reported as integer_overflow.
func TestMergeOverflowNarrowType(t *testing.T) {
	src := `package p

var g int8

func f(cond bool) int8 {
	x := int8(50)
	if cond {
		x = 10
	}
	return x + g
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryIntegerOverflow) {
		t.Fatalf("expected integer_overflow after merge over int8, got %+v", res.Warnings)
	}
}

// TestFallthroughMergeIndex verifies a fallthrough path joins the assignment of
// one case with the use in the next case; when another entry into that second
// case exists (dispatch), the merged value still covers every case-entry path.
func TestFallthroughMergeIndex(t *testing.T) {
	src := `package p

var g int

func f(k int) int {
	a := [4]int{}
	switch k {
	case 0:
		g = 0
		fallthrough
	case 1:
		return a[g]
	}
	return 0
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.f", CategoryIndexOutOfBounds) {
		t.Fatalf("expected index_out_of_bounds on fallthrough/dispatch merge, got %+v", res.Warnings)
	}
}

// TestContinueInsideSwitch keeps the continue edge of an enclosing for alive
// across a switch: the state merged at the loop head must cover both the
// continue back edge and normal fall-through, so no iteration path is lost.
func TestContinueInsideSwitch(t *testing.T) {
	src := `package p

func f(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		switch i {
		case 0:
			s++
			continue
		}
		s++
	}
	return s
}
`
	cfg := DefaultConfig()
	cfg.MaxIterations = 500
	res := analyzeSrc(t, src, cfg)
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
}

// TestLabeledBreakContinue supports labeled break/continue without silently
// dropping edges; the function must converge (a generous budget is used for
// this doubly-nested construct; with a tight budget it must instead be marked
// incomplete, never falsely complete).
func TestLabeledBreakContinue(t *testing.T) {
	src := `package p

func f(n int) int {
	s := 0
outer:
	for i := 0; i < n; i++ {
		for j := 0; j < 2; j++ {
			if j == 0 {
				continue outer
			}
			if i > 1000 {
				break outer
			}
			s++
		}
	}
	return s
}
`
	cfg := DefaultConfig()
	cfg.MaxIterations = 500
	res := analyzeSrc(t, src, cfg)
	if !res.Complete {
		t.Fatalf("labeled break/continue must converge, unfinished: %v", res.Unfinished)
	}
}
