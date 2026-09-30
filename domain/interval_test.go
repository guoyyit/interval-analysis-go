package domain

import (
	"go/token"
	"math/big"
	"testing"
)

var int64B = IntBounds(64, true)
var uint8B = IntBounds(8, false)

func TestArithmeticNoOverflow(t *testing.T) {
	r, ovf := Add(RangeValue(1, 10), RangeValue(2, 5), int64B)
	if ovf || r.Lo.Int64() != 3 || r.Hi.Int64() != 15 {
		t.Fatalf("Add = %s ovf=%v, want [3,15] false", r, ovf)
	}
}

func TestArithmeticOverflowTop(t *testing.T) {
	// Any possible overflow collapses to full range (sound for wraparound).
	r, ovf := Add(Const(1<<62), Const(1<<62), int64B)
	if !ovf || !IsTop(r, int64B) {
		t.Fatalf("Add overflow = %s ovf=%v, want top+true", r, ovf)
	}
}

func TestDivByZero(t *testing.T) {
	_, _, dbz := Div(RangeValue(0, 10), RangeValue(-2, 2), int64B)
	if !dbz {
		t.Fatal("expected possible division by zero for divisor [-2,2]")
	}
	r, _, dbz := Div(RangeValue(4, 8), RangeValue(2, 4), int64B)
	if dbz || r.Lo.Int64() != 1 || r.Hi.Int64() != 4 {
		t.Fatalf("safe Div = %s dbz=%v want [1,4]", r, dbz)
	}
}

func TestDivMinIntMinusOneOverflow(t *testing.T) {
	minInt := int64B.Min
	_, ovf, dbz := Div(Point(minInt), Const(-1), int64B)
	if !ovf || dbz {
		t.Fatalf("MinInt/-1: ovf=%v dbz=%v, want overflow true, dbz false", ovf, dbz)
	}
}

func TestWidenTerminates(t *testing.T) {
	// Simulate x = x + 1 starting at 0: widening must hit the upper bound.
	state := Const(0)
	for i := 0; i < 100; i++ {
		if state.Hi.Cmp(int64B.Max) == 0 && state.Lo.Int64() == 0 {
			return
		}
		// Transfer of "x = x + 1" computed with the *pre-widening* precise
		// value; widening is applied only at the loop head join.
		next := BigRange(new(big.Int).Set(state.Lo), new(big.Int).Add(state.Hi, big.NewInt(1)))
		if i == 0 {
			state = next
			continue
		}
		state = Widen(state, next, int64B)
		if state.Hi.Cmp(int64B.Max) == 0 && state.Lo.Int64() == 0 {
			return // stabilized at the bound in finitely many steps
		}
	}
	t.Fatalf("widening did not stabilize: %s", state)
}

func TestNarrowDoesNotExpand(t *testing.T) {
	a := BigRange(int64B.Min, Const(10).Hi) // widened
	b := RangeValue(-2, 10)                 // more precise successor
	n := Narrow(a, b, int64B)
	if n.Lo.Cmp(b.Lo) != 0 || n.Hi.Int64() != 10 {
		t.Fatalf("narrow = %s, want lower refined to %d", n, b.Lo)
	}
}

func TestUnsignedBounds(t *testing.T) {
	top := TypeTop(uint8B)
	if top.Lo.Int64() != 0 || top.Hi.Int64() != 255 {
		t.Fatalf("uint8 top = %s", top)
	}
}

func TestRefineComparison(t *testing.T) {
	x := RangeValue(-5, 10)
	y := Const(0)
	rx, _ := RefineCmp(x, y, token.LSS, true) // x < 0
	if rx.Hi.Int64() != -1 || rx.Lo.Int64() != -5 {
		t.Fatalf("x<0 refined = %s, want [-5,-1]", rx)
	}
	rx, _ = RefineCmp(x, y, token.GTR, true) // x > 0
	if rx.Lo.Int64() != 1 || rx.Hi.Int64() != 10 {
		t.Fatalf("x>0 refined = %s, want [1,10]", rx)
	}
}

func TestRefineNonZeroFullRange(t *testing.T) {
	top := TypeTop(int64B)
	r := RefineNonZero(top, true)
	if !r.NonZero {
		t.Fatal("full range after !=0 guard must carry NonZero fact")
	}
	if containsZero(r) {
		t.Fatal("NonZero interval must not be considered to contain zero")
	}
}
