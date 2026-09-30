package intervalanalysis

import "go/types"

// State is the abstract data environment at a CFG block entry/exit.
//
// It maps local integer variables to intervals. Unmapped integer variables are
// treated as top (the full range of their type), which is the sound default for
// external inputs such as function parameters and uncached reads. It also
// tracks known array/slice lengths used by index checks.
type State struct {
	bottom bool
	vars   map[types.Object]Interval
	length map[types.Object]Interval
}

// BottomState is the unreachable state (bottom of the lattice).
var BottomState = &State{bottom: true}

// NewState returns an empty state (all variables start top).
func NewState() *State {
	return &State{
		vars:   map[types.Object]Interval{},
		length: map[types.Object]Interval{},
	}
}

// IsBottom reports an unreachable state.
func (s *State) IsBottom() bool { return s == nil || s.bottom }

// Copy returns a deep copy.
func (s *State) Copy() *State {
	if s.IsBottom() {
		return BottomState
	}
	c := &State{
		vars:   make(map[types.Object]Interval, len(s.vars)),
		length: make(map[types.Object]Interval, len(s.length)),
	}
	for k, v := range s.vars {
		c.vars[k] = v
	}
	for k, v := range s.length {
		c.length[k] = v
	}
	return c
}

// Get returns the interval of obj. Unmapped integers default to the full range
// of their declared type (sound treatment of unknown/external values).
func (s *State) Get(obj types.Object) Interval {
	if s.IsBottom() {
		return TopInterval
	}
	if i, ok := s.vars[obj]; ok {
		return i
	}
	if obj != nil {
		if bits, signed, ok := intKind(obj.Type()); ok {
			return Full(bits, signed)
		}
	}
	return TopInterval
}

// Set binds obj to i.
func (s *State) Set(obj types.Object, i Interval) {
	if s.IsBottom() {
		return
	}
	s.vars[obj] = i
}

// Forget removes any binding for obj, making it top again.
func (s *State) Forget(obj types.Object) {
	if s.IsBottom() {
		return
	}
	delete(s.vars, obj)
}

// GetLen returns the known length interval of an indexed object, or top.
func (s *State) GetLen(obj types.Object) Interval {
	if s.IsBottom() {
		return TopInterval
	}
	if i, ok := s.length[obj]; ok {
		return i
	}
	return TopInterval
}

// SetLen records a known length interval for an indexed object.
func (s *State) SetLen(obj types.Object, i Interval) {
	if s.IsBottom() {
		return
	}
	s.length[obj] = i
}

// ForgetLen removes a known length binding.
func (s *State) ForgetLen(obj types.Object) {
	if s.IsBottom() {
		return
	}
	delete(s.length, obj)
}

func joinMaps(dst, src map[types.Object]Interval) {
	for k, v := range src {
		if old, ok := dst[k]; ok {
			dst[k] = Join(old, v)
		} else {
			dst[k] = v
		}
	}
}

// StateJoin is the pointwise lattice least upper bound.
func StateJoin(a, b *State) *State {
	if a.IsBottom() {
		return b
	}
	if b.IsBottom() {
		return a
	}
	c := a.Copy()
	joinMaps(c.vars, b.vars)
	joinMaps(c.length, b.length)
	return c
}

// StateEqual reports pointwise equality of bindings.
func StateEqual(a, b *State) bool {
	if a.IsBottom() || b.IsBottom() {
		return a.IsBottom() == b.IsBottom()
	}
	if len(a.vars) != len(b.vars) || len(a.length) != len(b.length) {
		return false
	}
	for k, v := range a.vars {
		if bv, ok := b.vars[k]; !ok || v != bv {
			return false
		}
	}
	for k, v := range a.length {
		if bv, ok := b.length[k]; !ok || v != bv {
			return false
		}
	}
	return true
}

// Widen performs standard interval widening: a bound that grew jumps to the
// nearest threshold beyond it, otherwise to infinity. It is an upper bound
// operator (result >= Join(old, cur)), so soundness is never sacrificed; it
// only guarantees finite convergence on loops.
func Widen(old, cur *State, thresholds []int64) *State {
	if old.IsBottom() {
		return cur
	}
	if cur.IsBottom() {
		return old
	}
	return &State{
		vars:   widenMap(old.vars, cur.vars, thresholds),
		length: widenMap(old.length, cur.length, thresholds),
	}
}

func widenMap(old, cur map[types.Object]Interval, thresholds []int64) map[types.Object]Interval {
	out := make(map[types.Object]Interval, len(cur))
	for k, ci := range cur {
		oi, ok := old[k]
		if !ok {
			out[k] = ci
			continue
		}
		if ci.Top {
			out[k] = TopInterval
			continue
		}
		if oi.Top {
			out[k] = TopInterval
			continue
		}
		bits, signed, typeOK := intKind(k.Type())
		lo, hi := oi.Lo, oi.Hi
		if ci.Lo < lo {
			lo = jumpDown(ci.Lo, thresholds, signed)
		}
		if ci.Hi > hi {
			hi = jumpUp(ci.Hi, thresholds, signed)
		}
		if typeOK {
			min, max := TypeBounds(bits, signed)
			if signed {
				if lo <= min && hi >= max {
					out[k] = TopInterval
					continue
				}
			} else {
				if uint64(lo) <= uint64(min) && uint64(hi) >= uint64(max) {
					out[k] = TopInterval
					continue
				}
			}
		}
		if lo == minInt64Threshold && hi == maxInt64Threshold {
			out[k] = TopInterval
		} else {
			out[k] = Interval{Lo: lo, Hi: hi}
		}
	}
	return out
}

const (
	minInt64Threshold = -1 << 63
	maxInt64Threshold = 1<<63 - 1
)

func jumpUp(v int64, thresholds []int64, signed bool) int64 {
	if !signed {
		// Unsigned values jump to uint max (int64 -1) when no threshold fits.
		best := int64(-1)
		for _, t := range thresholds {
			if t >= 0 && uint64(t) >= uint64(v) && uint64(t) < uint64(best) {
				best = t
			}
		}
		return best
	}
	best := int64(maxInt64Threshold)
	for _, t := range thresholds {
		if t >= v && t < best {
			best = t
		}
	}
	return best
}

func jumpDown(v int64, thresholds []int64, signed bool) int64 {
	if !signed {
		if v <= 0 {
			return 0
		}
		best := int64(0)
		for _, t := range thresholds {
			if t >= 0 && uint64(t) <= uint64(v) && uint64(t) > uint64(best) {
				best = t
			}
		}
		return best
	}
	best := int64(minInt64Threshold)
	for _, t := range thresholds {
		if t <= v && t > best {
			best = t
		}
	}
	return best
}
