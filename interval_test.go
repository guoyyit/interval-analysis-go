package intervalanalysis

import (
	"go/token"
	"testing"
)

func TestArithmeticIntervals(t *testing.T) {
	r, ov := Add(Const(1), Const(2), 32, true)
	if ov || r != Const(3) {
		t.Fatalf("1+2 = %v ov=%v, want 3", r, ov)
	}
	r, ov = Add(Const(maxInt32Const), Const(1), 32, true)
	if !ov || !r.Top {
		t.Fatalf("max32+1 overflow not flagged, got %v ov=%v", r, ov)
	}
	r, dz, ov := Div(Const(10), Const(2), 32, true)
	if dz || ov || r != Const(5) {
		t.Fatalf("10/2 = %v dz=%v ov=%v", r, dz, ov)
	}
	_, dz, _ = Div(Const(10), Interval{Lo: -1, Hi: 2}, 32, true)
	if !dz {
		t.Fatal("expected possible division by zero for divisor [-1,2]")
	}
	_, _, ov = Div(Const(minInt64Const), Const(-1), 64, true)
	if !ov {
		t.Fatal("MinInt64/-1 must be flagged overflow")
	}
	_, dz, _ = Mod(Const(10), Interval{Lo: 0, Hi: 5}, 32, true)
	if !dz {
		t.Fatal("mod by [0,5] must flag divzero")
	}
	r, dz, _ = Mod(TopInterval, Const(10), 32, true)
	if dz {
		t.Fatal("mod by constant 10 cannot be zero divisor")
	}
	if r.Top || r.Lo != -9 || r.Hi != 9 {
		t.Fatalf("x %% 10 bound = %v, want [-9,9]", r)
	}
}

const (
	maxInt32Const = int64(1<<31 - 1)
	minInt64Const = int64(-1 << 63)
)

func TestJoinContains(t *testing.T) {
	j := Join(Const(1), Const(5))
	if j != (Interval{Lo: 1, Hi: 5}) {
		t.Fatalf("join = %v", j)
	}
	if !j.Contains(3) || j.Contains(6) {
		t.Fatal("contains wrong")
	}
	if Join(TopInterval, Const(1)) != TopInterval {
		t.Fatal("join with top must be top")
	}
}

func TestRefine(t *testing.T) {
	full := Interval{Lo: minInt64Const, Hi: int64(1<<63 - 1)}
	r := Refine(full, token.LSS, Const(10), 64, true)
	if r.Top || r.Hi != 9 {
		t.Fatalf("i<10 refine = %v", r)
	}
	r = Refine(full, token.GEQ, Const(0), 64, true)
	if r.Top || r.Lo != 0 {
		t.Fatalf("i>=0 refine = %v", r)
	}
	r = Refine(Const(5), token.EQL, Const(6), 64, true)
	if !r.IsEmpty() {
		t.Fatalf("5==6 should be empty, got %v", r)
	}
	// NEQ against a constant equal to a boundary drops that boundary.
	r = Refine(Const(0), token.NEQ, Const(0), 64, true)
	if !r.IsEmpty() {
		t.Fatalf("0!=0 should be empty, got %v", r)
	}
	r = Refine(Interval{Lo: 0, Hi: 10}, token.NEQ, Const(0), 64, true)
	if r.Lo != 1 || r.Hi != 10 {
		t.Fatalf("[0,10]!=0 = %v", r)
	}
}

func TestUnsignedArithmetic(t *testing.T) {
	// uint8 250 + 10 overflows.
	_, ov := Add(Const(250), Const(10), 8, false)
	if !ov {
		t.Fatal("uint8 250+10 must overflow")
	}
	r, ov := Add(Const(1), Const(2), 8, false)
	if ov || r != Const(3) {
		t.Fatalf("uint8 1+2 = %v ov=%v", r, ov)
	}
}
