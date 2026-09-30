package intervalanalysis

import (
	"go/token"
	"math"
)

// Interval is the numeric abstract element: the closed integer range [Lo, Hi].
// Top represents the full range of the associated type (the greatest element of
// the abstract lattice). Non-top intervals are never empty.
type Interval struct {
	Lo  int64
	Hi  int64
	Top bool
}

// TopInterval is the universal interval.
var TopInterval = Interval{Top: true}

// emptyInterval is a sentinel produced by Refine for an impossible condition.
var emptyInterval = Interval{Lo: 1, Hi: 0}

// IsEmpty reports a sentinel empty refinement.
func (i Interval) IsEmpty() bool { return !i.Top && i.Lo > i.Hi }

// Const returns the singleton interval [v, v].
func Const(v int64) Interval { return Interval{Lo: v, Hi: v} }

// TypeBounds returns the representable [min, max] of an integer type. For
// unsigned 64-bit types max is represented as int64(-1).
func TypeBounds(bits int, signed bool) (min, max int64) {
	if bits <= 0 || bits > 64 {
		bits = 64
	}
	if signed {
		max = int64(uint64(math.MaxInt64) >> (64 - bits))
		min = -max - 1
		return min, max
	}
	min = 0
	if bits == 64 {
		max = -1
	} else {
		max = int64(1<<uint(bits) - 1)
	}
	return min, max
}

// Full returns the representable range of an integer type (always Top).
func Full(bits int, signed bool) Interval { return TopInterval }

// IsConst reports whether the interval is a single value.
func (i Interval) IsConst() bool { return !i.Top && i.Lo == i.Hi }

// Contains reports whether v may be a member of the interval.
func (i Interval) Contains(v int64) bool {
	if i.Top {
		return true
	}
	return v >= i.Lo && v <= i.Hi
}

// Join is the lattice least upper bound.
func Join(a, b Interval) Interval {
	if a.Top || b.Top {
		return TopInterval
	}
	lo := a.Lo
	if b.Lo < lo {
		lo = b.Lo
	}
	hi := a.Hi
	if b.Hi > hi {
		hi = b.Hi
	}
	return Interval{Lo: lo, Hi: hi}
}

func inType(v, min, max int64, signed bool) bool {
	if signed {
		return v >= min && v <= max
	}
	return uint64(v) >= uint64(min) && uint64(v) <= uint64(max)
}

func addOK(a, b int64, signed bool) (int64, bool) {
	if signed {
		s := a + b
		if (a > 0 && b > 0 && s < 0) || (a < 0 && b < 0 && s >= 0) {
			return 0, false
		}
		return s, true
	}
	ua, ub := uint64(a), uint64(b)
	s := ua + ub
	if s < ua {
		return 0, false
	}
	return int64(s), true
}

func subOK(a, b int64, signed bool) (int64, bool) {
	if signed {
		if (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b) {
			return 0, false
		}
		return a - b, true
	}
	if uint64(a) < uint64(b) {
		return 0, false
	}
	return int64(uint64(a) - uint64(b)), true
}

func mulOK(a, b int64, signed bool) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if signed {
		if a == math.MinInt64 && b == -1 {
			return 0, false
		}
		p := a * b
		if p/b != a {
			return 0, false
		}
		return p, true
	}
	ua, ub := uint64(a), uint64(b)
	p := ua * ub
	if p/ua != ub {
		return 0, false
	}
	return int64(p), true
}

type extrema struct {
	set   bool
	min   int64
	max   int64
	usign bool
}

func (x *extrema) add(v int64) {
	if !x.set {
		x.set, x.min, x.max = true, v, v
		return
	}
	less := func(a, b int64) bool {
		if x.usign {
			return uint64(a) < uint64(b)
		}
		return a < b
	}
	if less(v, x.min) {
		x.min = v
	}
	if less(x.max, v) {
		x.max = v
	}
}

func (x *extrema) interval() Interval {
	if !x.set {
		return TopInterval
	}
	return Interval{Lo: x.min, Hi: x.max}
}

func binRange(a, b Interval, bits int, signed bool,
	op func(x, y int64) (int64, bool)) (Interval, bool) {
	min, max := TypeBounds(bits, signed)
	if a.Top || b.Top {
		return TopInterval, true
	}
	var ex = extrema{usign: !signed}
	overflows := false
	for _, x := range []int64{a.Lo, a.Hi} {
		for _, y := range []int64{b.Lo, b.Hi} {
			r, ok := op(x, y)
			if !ok || !inType(r, min, max, signed) {
				overflows = true
				continue
			}
			ex.add(r)
		}
	}
	if overflows {
		return TopInterval, true
	}
	return ex.interval(), false
}

// Add abstracts signed/unsigned integer addition; overflow reports whether the
// abstract result may exceed the type's representable range.
func Add(a, b Interval, bits int, signed bool) (r Interval, overflow bool) {
	return binRange(a, b, bits, signed, func(x, y int64) (int64, bool) {
		return addOK(x, y, signed)
	})
}

// Sub abstracts integer subtraction.
func Sub(a, b Interval, bits int, signed bool) (r Interval, overflow bool) {
	return binRange(a, b, bits, signed, func(x, y int64) (int64, bool) {
		return subOK(x, y, signed)
	})
}

// Mul abstracts integer multiplication.
func Mul(a, b Interval, bits int, signed bool) (r Interval, overflow bool) {
	return binRange(a, b, bits, signed, func(x, y int64) (int64, bool) {
		return mulOK(x, y, signed)
	})
}

// divisorCandidates returns the denominator values bounding division/remainder
// over an interval excluding zero.
func divisorCandidates(b Interval) []int64 {
	cands := []int64{}
	if b.Lo < 0 {
		cands = append(cands, b.Lo)
		if b.Hi >= -1 {
			cands = append(cands, -1)
		}
	}
	if b.Hi > 0 {
		cands = append(cands, b.Hi)
		if b.Lo <= 1 {
			cands = append(cands, 1)
		}
	}
	return cands
}

func canReachMin(a Interval, bits int, signed bool) bool {
	if !signed {
		return false
	}
	if a.Top {
		return true
	}
	min, _ := TypeBounds(bits, signed)
	return a.Lo <= min
}

// Div abstracts integer division. divZero reports whether the divisor may be 0.
func Div(a, b Interval, bits int, signed bool) (r Interval, divZero, overflow bool) {
	if b.Top {
		// Divisor can be any value: zero is possible. MinInt/-1 overflow is
		// possible only when the numerator can actually reach MinInt and the
		// type width is 64 (for narrower types the -1 quotient wraps within the
		// abstract full-range but is a real overflow only at the minimum).
		return TopInterval, true, canReachMin(a, bits, signed)
	}
	divZero = b.Contains(0)
	if a.Top {
		if signed && canReachMin(a, bits, signed) && b.Contains(-1) {
			return TopInterval, divZero, true
		}
		return TopInterval, divZero, false
	}
	min, max := TypeBounds(bits, signed)
	var ex = extrema{usign: !signed}
	for _, y := range divisorCandidates(b) {
		for _, x := range []int64{a.Lo, a.Hi} {
			if signed && x == min && y == -1 {
				overflow = true
				continue
			}
			q := x / y
			if !inType(q, min, max, signed) {
				overflow = true
				continue
			}
			ex.add(q)
		}
	}
	if overflow {
		return TopInterval, divZero, true
	}
	return ex.interval(), divZero, false
}

// Mod abstracts integer remainder.
func Mod(_ Interval, b Interval, bits int, signed bool) (r Interval, divZero, overflow bool) {
	if b.Top {
		return TopInterval, true, false
	}
	divZero = b.Contains(0)
	nonZero := b
	if divZero {
		// Compute bounds from the parts of b that exclude 0.
		// Result bound is |y|-1 for the largest |y| present.
		maxAbs := maxAbsDivisor(b)
		if signed {
			return Interval{Lo: -(maxAbs - 1), Hi: maxAbs - 1}, true, false
		}
		return Interval{Lo: 0, Hi: maxAbs - 1}, true, false
	}
	if signed {
		maxAbs := maxAbsDivisor(nonZero)
		return Interval{Lo: -(maxAbs - 1), Hi: maxAbs - 1}, false, false
	}
	// Unsigned: result in [0, max(y)-1].
	return Interval{Lo: 0, Hi: uintMaxLessOne(nonZero)}, false, false
}

func maxAbsDivisor(b Interval) int64 {
	// max(|b.Lo|, |b.Hi|) avoiding abs(MinInt64).
	abs := func(v int64) int64 {
		if v < 0 {
			if v == -1<<63 {
				return 1<<63 - 1
			}
			return -v
		}
		return v
	}
	x, y := abs(b.Lo), abs(b.Hi)
	if x > y {
		return x
	}
	return y
}

func uintMaxLessOne(b Interval) int64 {
	return b.Hi - 1
}

func ltVal(a, b int64, signed bool) bool {
	if signed {
		return a < b
	}
	return uint64(a) < uint64(b)
}

func minVal(a, b int64, signed bool) int64 {
	if ltVal(b, a, signed) {
		return b
	}
	return a
}

func maxVal(a, b int64, signed bool) int64 {
	if ltVal(a, b, signed) {
		return b
	}
	return a
}

func add1(v int64, signed bool) int64 {
	if signed {
		if v == math.MaxInt64 {
			return v
		}
		return v + 1
	}
	if uint64(v) == math.MaxUint64 {
		return v
	}
	return int64(uint64(v) + 1)
}

func sub1(v int64, signed bool) int64 {
	if signed {
		if v == math.MinInt64 {
			return v
		}
		return v - 1
	}
	if uint64(v) == 0 {
		return v
	}
	return int64(uint64(v) - 1)
}

// Refine narrows i under the condition i op rhs (used for branch conditions).
// The result never removes a value that could satisfy the condition; an
// impossible condition yields an empty interval (see IsEmpty).
func Refine(i Interval, op token.Token, rhs Interval, bits int, signed bool) Interval {
	if rhs.Top {
		return i
	}
	min, max := TypeBounds(bits, signed)
	if i.Top {
		i = Interval{Lo: min, Hi: max}
	}
	lo, hi := i.Lo, i.Hi
	rlo, rhi := rhs.Lo, rhs.Hi
	switch op {
	case token.EQL:
		lo, hi = maxVal(lo, rlo, signed), minVal(hi, rhi, signed)
	case token.NEQ:
		if rlo == rhi {
			switch rlo {
			case lo:
				lo = add1(lo, signed)
			case hi:
				hi = sub1(hi, signed)
			}
		}
	case token.LSS:
		hi = minVal(hi, sub1(rhi, signed), signed)
	case token.LEQ:
		hi = minVal(hi, rhi, signed)
	case token.GTR:
		lo = maxVal(lo, add1(rlo, signed), signed)
	case token.GEQ:
		lo = maxVal(lo, rlo, signed)
	default:
		return i
	}
	r := Interval{Lo: lo, Hi: hi}
	if r.IsEmpty() {
		return emptyInterval
	}
	if lo == min && hi == max {
		return TopInterval
	}
	return r
}

func (i Interval) String() string {
	if i.Top {
		return "[-inf,+inf]"
	}
	if i.IsEmpty() {
		return "empty"
	}
	if i.IsConst() {
		return itoa(i.Lo)
	}
	return "[" + itoa(i.Lo) + "," + itoa(i.Hi) + "]"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	u := uint64(v)
	if neg {
		u = -u
	}
	buf := [20]byte{}
	pos := len(buf)
	for u > 0 {
		pos--
		buf[pos] = byte('0' + u%10)
		u /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
