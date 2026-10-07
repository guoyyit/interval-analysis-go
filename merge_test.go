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
