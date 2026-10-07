package intervalanalysis

import "testing"

// hasWarning reports whether ws contains a warning of cat in function fn.
func hasWarning(ws []Warning, fn string, cat Category) bool {
	for _, w := range ws {
		if w.Function == fn && w.Category == cat {
			return true
		}
	}
	return false
}

// Package-level variables assigned on only some branches must be top at the
// merge point: the paths that skip the assignment leave them at their
// (unknown) previous value.
func TestMergePointPackageLevelVar(t *testing.T) {
	src := `package p

var D int
var I int
var Arr [4]int

func divAfterIf(x int) int {
	if x > 0 {
		D = 3
	}
	return 100 / D
}

func indexAfterIf(x int) int {
	if x > 0 {
		I = 1
	}
	return Arr[I]
}

func divAfterSwitch(c int) int {
	switch c {
	case 1:
		D = 1
	case 2:
		D = 2
	}
	return 100 / D
}

func indexAfterSwitch(c int) int {
	switch c {
	case 1:
		I = 1
	case 2:
		I = 2
	}
	return Arr[I]
}

func divAfterFor(n int) int {
	for i := 0; i < n; i++ {
		D = 7
	}
	return 100 / D
}

func indexAfterFor(n int) int {
	for i := 0; i < n; i++ {
		I = 2
	}
	return Arr[I]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("analysis incomplete: %v", res.Unfinished)
	}
	checks := []struct {
		fn  string
		cat Category
	}{
		{"p.divAfterIf", CategoryDivByZero},
		{"p.indexAfterIf", CategoryIndexOutOfBounds},
		{"p.divAfterSwitch", CategoryDivByZero},
		{"p.indexAfterSwitch", CategoryIndexOutOfBounds},
		{"p.divAfterFor", CategoryDivByZero},
		{"p.indexAfterFor", CategoryIndexOutOfBounds},
	}
	for _, c := range checks {
		if !hasWarning(res.Warnings, c.fn, c.cat) {
			t.Errorf("expected %s warning in %s, got %+v", c.cat, c.fn, res.Warnings)
		}
	}
}

// Nested control structures must also merge conservatively.
func TestMergePointNested(t *testing.T) {
	src := `package p

var D int
var Arr [4]int
var I int

func nestedIfSwitch(x int) int {
	if x > 0 {
		switch x {
		case 1:
			D = 5
		}
	}
	return 100 / D
}

func nestedIndex(x int) int {
	if x > 100 {
		if x < 200 {
			I = 1
		}
	}
	return Arr[I]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("analysis incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.nestedIfSwitch", CategoryDivByZero) {
		t.Errorf("expected div_by_zero in nestedIfSwitch, got %+v", res.Warnings)
	}
	if !hasWarning(res.Warnings, "p.nestedIndex", CategoryIndexOutOfBounds) {
		t.Errorf("expected index_out_of_bounds in nestedIndex, got %+v", res.Warnings)
	}
}

// A variable assigned on EVERY path stays precise at the merge point: no
// warning may be produced when every reachable value is safe.
func TestMergePointAllPathsAssigned(t *testing.T) {
	src := `package p

var Arr [4]int

func allPaths(x int) int {
	i := 0
	if x > 0 {
		i = 1
	} else {
		i = 2
	}
	return Arr[i]
}

func allCases(c int) int {
	d := 1
	switch c {
	case 1:
		d = 2
	case 2:
		d = 3
	default:
		d = 4
	}
	return 100 / d
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("analysis incomplete: %v", res.Unfinished)
	}
	for _, w := range res.Warnings {
		if w.Function == "p.allPaths" || w.Function == "p.allCases" {
			t.Errorf("fully assigned merge must stay precise, unexpected warning: %+v", w)
		}
	}
}

// A merged value that may exceed its declared type's range must trigger an
// overflow report when used in arithmetic.
func TestMergePointOverflow(t *testing.T) {
	src := `package p

var small int8
var u uint8

func addAfterIf(x int) int8 {
	if x > 0 {
		small = 100
	}
	return small + small
}

func incAfterIf(x int) uint8 {
	if x > 0 {
		u = 200
	}
	return u + u
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("analysis incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.addAfterIf", CategoryIntegerOverflow) {
		t.Errorf("merged int8 top + itself must warn overflow, got %+v", res.Warnings)
	}
	if !hasWarning(res.Warnings, "p.incAfterIf", CategoryIntegerOverflow) {
		t.Errorf("merged uint8 top + itself must warn overflow, got %+v", res.Warnings)
	}
}

// Statements after a range loop must be analyzed (the loop-exit edge is
// reachable), and a loop variable reused across iterations must not keep a
// stale pre-loop value.
func TestRangeLoopExitAndReuse(t *testing.T) {
	src := `package p

var Big [8]int
var Small [4]int
var D int

func rangeReuse() int {
	i := 0
	for i = range Big {
	}
	return Small[i]
}

func divAfterRange(x int) int {
	for _, v := range Big {
		if v > 0 {
			D = 9
		}
	}
	return 10 / D
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("analysis incomplete: %v", res.Unfinished)
	}
	if !hasWarning(res.Warnings, "p.rangeReuse", CategoryIndexOutOfBounds) {
		t.Errorf("reused range variable must be top after loop, got %+v", res.Warnings)
	}
	if !hasWarning(res.Warnings, "p.divAfterRange", CategoryDivByZero) {
		t.Errorf("package var assigned only inside range loop must warn, got %+v", res.Warnings)
	}
}
