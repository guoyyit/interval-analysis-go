package intervalanalysis

import "testing"

func TestSwitchRefinement(t *testing.T) {
	src := `package p

func tagged(x int) int {
	a := [3]int{}
	switch x {
	case 0, 1, 2:
		return a[x]
	default:
		return a[x]
	}
}

func tagless(x int) int {
	a := [5]int{}
	switch {
	case x >= 0 && x < 5:
		return a[x]
	case x == 10:
		return 0
	}
	return 0
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Unfinished)
	}
	taggedWarns, taglessWarns := 0, 0
	for _, w := range res.Warnings {
		if w.Function == "p.tagged" {
			taggedWarns++
		}
		if w.Function == "p.tagless" {
			taglessWarns++
		}
	}
	// tagged: only the default branch can go out of bounds; the case branch is
	// proven within [0,2]. Exactly one index warning expected.
	if taggedWarns != 1 {
		t.Fatalf("tagged expected 1 warning, got %d: %+v", taggedWarns, res.Warnings)
	}
	if taglessWarns != 0 {
		t.Fatalf("tagless must be safe, got %d warnings", taglessWarns)
	}
}
