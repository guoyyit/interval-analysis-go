package intervalanalysis

import (
	"context"
	"testing"
)

func TestRecursiveSummaryTerminates(t *testing.T) {
	src := `package p

func fact(n int) int {
	if n <= 1 {
		return 1
	}
	return n * fact(n-1)
}

func mutualEven(n int) int {
	if n == 0 {
		return 0
	}
	return mutualOdd(n - 1)
}

func mutualOdd(n int) int {
	if n == 0 {
		return 1
	}
	return mutualEven(n - 1)
}

func use(n int) int {
	a := [8]int{}
	i := mutualEven(n)
	if i < 0 || i > 7 {
		return 0
	}
	return a[i]
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	if !res.Complete {
		t.Fatalf("recursive SCC must converge: %v", res.Unfinished)
	}
	// Multiplication in factorial of full-range n may overflow: that is a
	// legitimate warning, not a crash or non-termination.
	for _, w := range res.Warnings {
		if w.Category != CategoryIntegerOverflow &&
			w.Category != CategoryDivByZero &&
			w.Category != CategoryIndexOutOfBounds {
			t.Fatalf("unexpected category %s", w.Category)
		}
	}
}

func TestRecursiveDirectoryMultiplePackages(t *testing.T) {
	root := t.TempDir()
	writePkg(t, root+"/a", "a", `package a

func Clamp(n int) int {
	if n < 0 {
		return 0
	}
	if n > 3 {
		return 3
	}
	return n
}

func Identity(n int) int { return n }
`)
	writePkg(t, root+"/b", "b", `package b

import "`+root+`/a"

func Pick(n int) int {
	buf := [4]int{}
	return buf[a.Clamp(n)]
}

func UnsafePick(n int) int {
	buf := [4]int{}
	return buf[a.Identity(n)]
}
`)
	an := NewAnalyzer(DefaultConfig())
	res, err := an.Analyze(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Complete {
		t.Fatalf("expected complete: %v", res.Unfinished)
	}
	safe, unsafe := false, false
	for _, w := range res.Warnings {
		if w.Function == "b.Pick" && w.Category == CategoryIndexOutOfBounds {
			safe = true
		}
		if w.Function == "b.UnsafePick" && w.Category == CategoryIndexOutOfBounds {
			unsafe = true
		}
	}
	if safe {
		t.Fatal("Pick must be safe via cross-package a.Clamp summary")
	}
	if !unsafe {
		t.Fatal("UnsafePick via a.Identity must be flagged (summary not precise?)")
	}
}
