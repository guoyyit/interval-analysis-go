package intervalanalysis

import (
	"context"
	"testing"
)

func TestContextCancellationIncomplete(t *testing.T) {
	src := `package p
func f(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += i
	}
	return s
}
`
	dir := t.TempDir()
	writePkg(t, dir, "p", src)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := NewAnalyzer(Config{MaxIterations: 10})
	res, err := a.Analyze(ctx, dir, false)
	if err != nil {
		t.Fatalf("cancellation should not surface as error: %v", err)
	}
	if res.Complete || len(res.Unfinished) == 0 {
		t.Fatalf("cancelled run must be incomplete: %+v", res)
	}
}

func TestExternalInputIsFullRange(t *testing.T) {
	// An unconstrained parameter used as an index must be reported; a bounded
	// copy of it must not. This also guards against accidental assumptions on
	// parameter signs.
	src := `package p

func rawIndex(i int) int {
	a := [4]int{}
	return a[i]
}

func guardedIndex(i int) int {
	a := [4]int{}
	if i >= 0 && i <= 3 {
		return a[i]
	}
	return -1
}

func unsignedIndex(u uint) int {
	a := [4]int{}
	if u < 4 {
		return a[u]
	}
	return -1
}
`
	res := analyzeSrc(t, src, DefaultConfig())
	hasRaw, guarded, unsigned := false, 0, 0
	for _, w := range res.Warnings {
		switch w.Function {
		case "p.rawIndex":
			hasRaw = true
		case "p.guardedIndex":
			guarded++
		case "p.unsignedIndex":
			unsigned++
		}
	}
	if !hasRaw {
		t.Fatal("full-range parameter index must be reported")
	}
	if guarded != 0 {
		t.Fatalf("guarded signed index must be safe, got %d", guarded)
	}
	if unsigned != 0 {
		t.Fatalf("guarded unsigned index must be safe, got %d", unsigned)
	}
}
