package intervalanalysis

import "testing"

// goto must create real control-flow edges: paths that jump over an
// assignment are reachable and must be analyzed.
func TestGotoSkipsAssignment(t *testing.T) {
	src := `package p

var Arr [4]int
var I int

func divAfterGoto(x int) int {
	i := 0
	if x > 0 {
		goto skip
	}
	i = 5
skip:
	return 10 / i
}

func indexAfterGoto(x int) int {
	if x > 0 {
		goto done
	}
	I = 1
done:
	return Arr[I]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("analysis incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.divAfterGoto", CategoryDivByZero) {
		t.Errorf("goto path skipping i=5 must warn div_by_zero, got %+v", res.Warnings)
	}
	if !hasWarning(res.Warnings, "p.indexAfterGoto", CategoryIndexOutOfBounds) {
		t.Errorf("goto path skipping I=1 must warn index_out_of_bounds, got %+v", res.Warnings)
	}
}

// A backward goto forms a loop; the analysis must converge (widening) and
// still report the out-of-bounds access on the loop path.
func TestGotoBackwardLoop(t *testing.T) {
	src := `package p

var Arr [4]int

func gotoLoop(n int) int {
	i := 0
again:
	Arr[i] = 1
	i++
	if i < n {
		goto again
	}
	return i
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("goto loop must converge: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.gotoLoop", CategoryIndexOutOfBounds) {
		t.Errorf("backward goto loop must warn index_out_of_bounds, got %+v", res.Warnings)
	}
}

// fallthrough executes the next case body unconditionally; values computed
// along the fallthrough chain must be reflected at the merge.
func TestSwitchFallthrough(t *testing.T) {
	src := `package p

var Arr [4]int

func fall(c int) int {
	i := 0
	switch c {
	case 1:
		i = 5
		fallthrough
	case 2:
		i = i + 1
	}
	return Arr[i]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("analysis incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.fall", CategoryIndexOutOfBounds) {
		t.Errorf("fallthrough path (i=6) must warn index_out_of_bounds, got %+v", res.Warnings)
	}
}

// break inside a switch exits the switch, not an enclosing loop; the loop
// path after the switch must remain reachable and be analyzed.
func TestBreakInsideSwitchInsideLoop(t *testing.T) {
	src := `package p

var Arr [4]int

func breakSwitch(n int) int {
	i := 0
	for k := 0; k < n; k++ {
		switch k {
		case 0:
			break
		}
		i++
	}
	return Arr[i]
}
`
	cfg := DefaultConfig()
	cfg.MaxIterations = 500
	res := analyzeSrc(t, src, cfg)
	if !res.Complete {
		t.Fatalf("analysis incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.breakSwitch", CategoryIndexOutOfBounds) {
		t.Errorf("loop must stay reachable after switch break, got %+v", res.Warnings)
	}
}

// Labeled break/continue target the labeled loop; paths created by them must
// be analyzed.
func TestLabeledBreakContinue(t *testing.T) {
	src := `package p

var Arr [4]int

func labeled(n int) int {
	i := 0
Outer:
	for i < n {
		for j := 0; j < 10; j++ {
			if j > 3 {
				break Outer
			}
			i++
		}
	}
	return Arr[i]
}
`
	cfg := DefaultConfig()
	cfg.MaxIterations = 500
	res := analyzeSrc(t, src, cfg)
	if !res.Complete {
		t.Fatalf("labeled break loop must converge: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.labeled", CategoryIndexOutOfBounds) {
		t.Errorf("labeled break path must warn index_out_of_bounds, got %+v", res.Warnings)
	}
}
