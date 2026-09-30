package domain

import (
	"go/token"
	"math/big"
)

// Interval is the abstract value: a closed interval [Lo, Hi] with arbitrary
// precision endpoints, or the empty (unreachable) set. All operations are
// pure functions: they never mutate the endpoints of their arguments.
//
// Endpoints are stored as mathematical integers; type-specific representable
// ranges are described by Bounds. This uniformly covers signed and unsigned
// types of any width (int8..int64, uint8..uint64, plus aliases).
type Interval struct {
	Lo    *big.Int
	Hi    *big.Int
	Empty bool
	// NonZero records the relational fact "value != 0" that an interval
	// cannot express by endpoints alone (e.g. full range after a `x != 0`
	// guard). It never weakens safety: arithmetic clears it unless the result
	// is provably non-zero, and Div/Mod honor it.
	NonZero bool
}

// Bounds describes the representable range of an integer type.
type Bounds struct {
	Min    *big.Int
	Max    *big.Int
	Signed bool
}

var (
	bigZero = big.NewInt(0)
	bigOne  = big.NewInt(1)
	bigNeg1 = big.NewInt(-1)
)

// IntBounds returns the representable range for an integer type of the given
// bit width (typically 8, 16, 32 or 64).
func IntBounds(bits uint, signed bool) Bounds {
	if signed {
		max := new(big.Int).Lsh(bigOne, bits-1)
		min := new(big.Int).Neg(max)
		max.Sub(max, bigOne)
		return Bounds{Min: min, Max: max, Signed: true}
	}
	max := new(big.Int).Lsh(bigOne, bits)
	max.Sub(max, bigOne)
	return Bounds{Min: new(big.Int), Max: max, Signed: false}
}

func iv(lo, hi *big.Int) Interval {
	return Interval{Lo: new(big.Int).Set(lo), Hi: new(big.Int).Set(hi)}
}

func ivnz(lo, hi *big.Int) Interval {
	return Interval{Lo: new(big.Int).Set(lo), Hi: new(big.Int).Set(hi), NonZero: true}
}

func copyIV(i Interval) Interval {
	if i.Empty {
		return EmptyValue()
	}
	return Interval{Lo: new(big.Int).Set(i.Lo), Hi: new(big.Int).Set(i.Hi), NonZero: i.NonZero}
}

func EmptyValue() Interval { return Interval{Empty: true} }

// TopValue is the full signed 64 bit range. Prefer TypeTop with the exact
// type bounds when the type is known.
func TopValue() Interval { return TypeTop(IntBounds(64, true)) }

func Const(v int64) Interval {
	z := big.NewInt(v)
	return Interval{Lo: z, Hi: new(big.Int).Set(z), NonZero: v != 0}
}

func RangeValue(lo, hi int64) Interval {
	return iv(big.NewInt(lo), big.NewInt(hi))
}

// Point is the singleton interval {v} for an arbitrary-precision value.
func Point(v *big.Int) Interval {
	return iv(v, v)
}

// BigRange builds [lo, hi] from arbitrary-precision endpoints.
func BigRange(lo, hi *big.Int) Interval {
	return iv(lo, hi)
}

// TypeTop is the full range of the given type: every representable value is
// possible. External/unconstrained values (parameters, external reads) are
// modeled with it.
func TypeTop(b Bounds) Interval {
	return Interval{Lo: new(big.Int).Set(b.Min), Hi: new(big.Int).Set(b.Max)}
}

func IsEmpty(i Interval) bool { return i.Empty }

func IsTop(i Interval, b Bounds) bool {
	return !i.Empty && i.Lo.Cmp(b.Min) == 0 && i.Hi.Cmp(b.Max) == 0
}

func Equals(a, b Interval) bool {
	if a.Empty || b.Empty {
		return a.Empty == b.Empty
	}
	return a.Lo.Cmp(b.Lo) == 0 && a.Hi.Cmp(b.Hi) == 0 && a.NonZero == b.NonZero
}

// Contains reports whether b is contained in a (b <= a in the information
// ordering). The empty set is contained in every interval.
func Contains(a, b Interval) bool {
	if b.Empty {
		return true
	}
	if a.Empty {
		return false
	}
	return a.Lo.Cmp(b.Lo) <= 0 && a.Hi.Cmp(b.Hi) >= 0
}

// Union is the interval hull join: the smallest interval containing both.
func Union(a, b Interval) Interval {
	if a.Empty {
		return copyIV(b)
	}
	if b.Empty {
		return copyIV(a)
	}
	lo := a.Lo
	if b.Lo.Cmp(lo) < 0 {
		lo = b.Lo
	}
	hi := a.Hi
	if b.Hi.Cmp(hi) > 0 {
		hi = b.Hi
	}
	// The non-zero fact only survives a join when BOTH operands carry it.
	return Interval{Lo: lo, Hi: hi, NonZero: a.NonZero && b.NonZero}
}

func Intersect(a, b Interval) Interval {
	if a.Empty || b.Empty {
		return EmptyValue()
	}
	lo := a.Lo
	if b.Lo.Cmp(lo) > 0 {
		lo = b.Lo
	}
	hi := a.Hi
	if b.Hi.Cmp(hi) < 0 {
		hi = b.Hi
	}
	if lo.Cmp(hi) > 0 {
		return EmptyValue()
	}
	return Interval{Lo: lo, Hi: hi, NonZero: a.NonZero && b.NonZero}
}

// IntersectKeepFact intersects endpoints while preserving the relational
// non-zero fact carried by either operand. It is used when bounding a value
// by its type range, which must not erase guard-derived facts.
func IntersectKeepFact(a, b Interval) Interval {
	r := Intersect(a, b)
	if !r.Empty {
		r.NonZero = a.NonZero || b.NonZero
	}
	return r
}

// Widen is the classic interval widening: any endpoint of b that moves past
// the corresponding endpoint of a is pushed straight to the type bound.
// Applied at loop heads it guarantees stabilization in finitely many steps
// without losing soundness: the result always contains both operands.
func Widen(a, b Interval, bnd Bounds) Interval {
	if a.Empty {
		return copyIV(b)
	}
	if b.Empty {
		return copyIV(a)
	}
	lo := new(big.Int).Set(a.Lo)
	if b.Lo.Cmp(a.Lo) < 0 {
		lo.Set(bnd.Min)
	}
	hi := new(big.Int).Set(a.Hi)
	if b.Hi.Cmp(a.Hi) > 0 {
		hi.Set(bnd.Max)
	}
	return Interval{Lo: lo, Hi: hi, NonZero: a.NonZero && b.NonZero}
}

// Narrow improves a widened post-fixpoint a with its more precise successor
// b = F(a): endpoints previously pushed to a type bound are refined to the
// precise successor endpoint. The result remains a post-fixpoint, so
// soundness is preserved; narrowing never expands the interval.
func Narrow(a, b Interval, bnd Bounds) Interval {
	if a.Empty || b.Empty {
		return copyIV(a)
	}
	lo := new(big.Int).Set(a.Lo)
	if a.Lo.Cmp(bnd.Min) == 0 && b.Lo.Cmp(a.Lo) > 0 {
		lo.Set(b.Lo)
	}
	hi := new(big.Int).Set(a.Hi)
	if a.Hi.Cmp(bnd.Max) == 0 && b.Hi.Cmp(a.Hi) < 0 {
		hi.Set(b.Hi)
	}
	if lo.Cmp(hi) > 0 {
		return copyIV(a)
	}
	return Interval{Lo: lo, Hi: hi, NonZero: a.NonZero}
}

// clampExact keeps the exact interval when it is fully representable;
// otherwise it reports possible overflow. On possible overflow the result is
// the full type range, which soundly covers the wrapped values produced by
// Go's modular integer semantics.
func clampExact(exact Interval, bnd Bounds) (Interval, bool) {
	if exact.Empty {
		return EmptyValue(), false
	}
	if exact.Lo.Cmp(bnd.Min) >= 0 && exact.Hi.Cmp(bnd.Max) <= 0 {
		// Result is provably non-zero iff the whole exact interval excludes
		// zero; any operand relational flags alone are insufficient.
		exact.NonZero = exact.Lo.Sign() > 0 || exact.Hi.Sign() < 0
		return exact, false
	}
	return TypeTop(bnd), true
}

func Add(a, b Interval, bnd Bounds) (Interval, bool) {
	if a.Empty || b.Empty {
		return EmptyValue(), false
	}
	return clampExact(iv(new(big.Int).Add(a.Lo, b.Lo), new(big.Int).Add(a.Hi, b.Hi)), bnd)
}

func Sub(a, b Interval, bnd Bounds) (Interval, bool) {
	if a.Empty || b.Empty {
		return EmptyValue(), false
	}
	return clampExact(iv(new(big.Int).Sub(a.Lo, b.Hi), new(big.Int).Sub(a.Hi, b.Lo)), bnd)
}

func Mul(a, b Interval, bnd Bounds) (Interval, bool) {
	if a.Empty || b.Empty {
		return EmptyValue(), false
	}
	corners := [][2]*big.Int{
		{a.Lo, b.Lo}, {a.Lo, b.Hi}, {a.Hi, b.Lo}, {a.Hi, b.Hi},
	}
	lo := new(big.Int).Mul(a.Lo, b.Lo)
	hi := new(big.Int).Set(lo)
	for _, c := range corners[1:] {
		p := new(big.Int).Mul(c[0], c[1])
		if p.Cmp(lo) < 0 {
			lo.Set(p)
		}
		if p.Cmp(hi) > 0 {
			hi.Set(p)
		}
	}
	return clampExact(iv(lo, hi), bnd)
}

func Neg(a Interval, bnd Bounds) (Interval, bool) {
	if a.Empty {
		return EmptyValue(), false
	}
	return clampExact(iv(new(big.Int).Neg(a.Hi), new(big.Int).Neg(a.Lo)), bnd)
}

func containsZero(i Interval) bool {
	if i.NonZero {
		return false
	}
	return !i.Empty && i.Lo.Sign() <= 0 && i.Hi.Sign() >= 0
}

func containsConst(i Interval, v *big.Int) bool {
	return !i.Empty && i.Lo.Cmp(v) <= 0 && i.Hi.Cmp(v) >= 0
}

// Div returns (result, mayOverflow, mayDivideByZero). The result is the exact
// interval hull: quotient extrema are attained at dividend endpoints and at
// divisor endpoints or at divisor values +/-1 (smallest absolute divisor).
// MinInt/-1 is the only arithmetic-overflow case for integer division.
func Div(a, b Interval, bnd Bounds) (Interval, bool, bool) {
	if a.Empty || b.Empty {
		return EmptyValue(), false, false
	}
	if containsZero(b) {
		return TypeTop(bnd), false, true
	}
	divisors := []*big.Int{b.Lo, b.Hi}
	if containsConst(b, bigOne) {
		divisors = append(divisors, bigOne)
	}
	if containsConst(b, bigNeg1) {
		divisors = append(divisors, bigNeg1)
	}
	var lo, hi *big.Int
	ovf := false
	for _, x := range []*big.Int{a.Lo, a.Hi} {
		for _, y := range divisors {
			if x.Cmp(bnd.Min) == 0 && y.Cmp(bigNeg1) == 0 {
				ovf = true
				continue
			}
			q := new(big.Int).Quo(x, y)
			if lo == nil || q.Cmp(lo) < 0 {
				lo = new(big.Int).Set(q)
			}
			if hi == nil || q.Cmp(hi) > 0 {
				hi = new(big.Int).Set(q)
			}
		}
	}
	if ovf && lo == nil {
		return TypeTop(bnd), true, false
	}
	res, exOvf := clampExact(iv(lo, hi), bnd)
	return res, ovf || exOvf, false
}

// Mod returns (result, mayOverflow, mayDivideByZero). Go's % follows
// truncated division, so the result sign follows the dividend and its
// absolute value is bounded by the divisor's maximum absolute value minus 1.
// Mod never overflows (MinInt % -1 == 0).
func Mod(a, b Interval, bnd Bounds) (Interval, bool, bool) {
	if a.Empty || b.Empty {
		return EmptyValue(), false, false
	}
	if containsZero(b) {
		return TypeTop(bnd), false, true
	}
	absLo := new(big.Int).Abs(b.Lo)
	absHi := new(big.Int).Abs(b.Hi)
	maxAbs := absLo
	if absHi.Cmp(maxAbs) > 0 {
		maxAbs = absHi
	}
	if maxAbs.Sign() <= 1 {
		// Divisor restricted to +/-1: remainder is always 0.
		return Const(0), false, false
	}
	bound := new(big.Int).Sub(maxAbs, bigOne)
	loR := new(big.Int).Neg(bound)
	hiR := new(big.Int).Set(bound)
	if a.Lo.Sign() >= 0 {
		loR.Set(bigZero)
	}
	if a.Hi.Sign() <= 0 {
		hiR.Set(bigZero)
	}
	res, ovf := clampExact(iv(loR, hiR), bnd)
	return res, ovf, false
}

func capHi(i Interval, v *big.Int) Interval {
	if i.Empty || i.Hi.Cmp(v) <= 0 {
		return copyIV(i)
	}
	if v.Cmp(i.Lo) < 0 {
		return EmptyValue()
	}
	return iv(i.Lo, v)
}

func capLo(i Interval, v *big.Int) Interval {
	if i.Empty || i.Lo.Cmp(v) >= 0 {
		return copyIV(i)
	}
	if v.Cmp(i.Hi) > 0 {
		return EmptyValue()
	}
	return iv(v, i.Hi)
}

// RefineCmp constrains operand intervals assuming the relational expression
// x op y has truth value `truth`. Returns refined (x, y). Only sound endpoint
// constraints are applied; disequality is not expressible without holes, so
// it is ignored except for the zero case (RefineNonZero).
func RefineCmp(x, y Interval, op token.Token, truth bool) (Interval, Interval) {
	if x.Empty || y.Empty {
		return x, y
	}
	if !truth {
		switch op {
		case token.EQL:
			op = token.NEQ
		case token.NEQ:
			op = token.EQL
		case token.LSS:
			op = token.GEQ
		case token.LEQ:
			op = token.GTR
		case token.GTR:
			op = token.LEQ
		case token.GEQ:
			op = token.LSS
		}
	}
	switch op {
	case token.EQL:
		common := Intersect(x, y)
		if common.Empty {
			return EmptyValue(), EmptyValue()
		}
		return copyIV(common), copyIV(common)
	case token.NEQ:
		return x, y
	case token.LSS: // x < y
		origLoX, origHiY := new(big.Int).Set(x.Lo), new(big.Int).Set(y.Hi)
		x2 := capHi(x, new(big.Int).Sub(origHiY, bigOne))
		y2 := capLo(y, new(big.Int).Add(origLoX, bigOne))
		return x2, y2
	case token.LEQ: // x <= y
		origLoX, origHiY := new(big.Int).Set(x.Lo), new(big.Int).Set(y.Hi)
		x2 := capHi(x, origHiY)
		y2 := capLo(y, origLoX)
		return x2, y2
	case token.GTR: // x > y
		origHiX, origLoY := new(big.Int).Set(x.Hi), new(big.Int).Set(y.Lo)
		x2 := capLo(x, new(big.Int).Add(origLoY, bigOne))
		y2 := capHi(y, new(big.Int).Sub(origHiX, bigOne))
		return x2, y2
	case token.GEQ: // x >= y
		origHiX, origLoY := new(big.Int).Set(x.Hi), new(big.Int).Set(y.Lo)
		x2 := capLo(x, origLoY)
		y2 := capHi(y, origHiX)
		return x2, y2
	}
	return x, y
}

// RefineNonZero constrains i to values != 0 when truth is true, or to {0}
// when truth is false. When the interval is [c, 0] or [0, c] the zero end
// can be cut exactly; a wider interval straddling zero cannot be represented
// with a single interval and is returned unchanged (a sound precision loss).
func RefineNonZero(i Interval, truth bool) Interval {
	if i.Empty {
		return i
	}
	if !truth {
		return Intersect(i, Const(0))
	}
	if i.Lo.Sign() == 0 && i.Hi.Sign() == 0 {
		return EmptyValue()
	}
	// i == 0 on one endpoint only: cut it.
	if i.Lo.Sign() == 0 && i.Hi.Sign() > 0 {
		return ivnz(bigOne, new(big.Int).Set(i.Hi))
	}
	if i.Hi.Sign() == 0 && i.Lo.Sign() < 0 {
		return ivnz(new(big.Int).Set(i.Lo), bigNeg1)
	}
	// Full range crossing zero with an established !=0 guard: keep endpoints
	// but carry the relational fact.
	if i.Lo.Sign() < 0 && i.Hi.Sign() > 0 {
		return Interval{Lo: new(big.Int).Set(i.Lo), Hi: new(big.Int).Set(i.Hi), NonZero: true}
	}
	return i
}

// String renders an interval, primarily for diagnostics and tests.
func (i Interval) String() string {
	if i.Empty {
		return "empty"
	}
	return "[" + i.Lo.String() + ", " + i.Hi.String() + "]"
}
