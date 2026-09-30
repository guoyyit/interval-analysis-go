package analyzer

import (
	"go/types"

	"github.com/guoyyit/interval-analysis-go/domain"
)

// State maps program variables to interval abstract values at one CFG node.
//
// A variable absent from the map is deliberately indistinguishable from a
// variable known to be full-range: Get returns domain.TopValue() for it.
// That models the "unconstrained input is full interval" requirement without
// forcing every parameter or package-level object to be seeded explicitly.
// Empty intervals stay present in the map and encode unreachability.
type State struct {
	vars map[types.Object]domain.Interval
}

func NewState() *State {
	return &State{vars: make(map[types.Object]domain.Interval)}
}

func (s *State) Get(obj types.Object) domain.Interval {
	if s == nil || obj == nil {
		return domain.TopValue()
	}
	if iv, ok := s.vars[obj]; ok {
		return iv
	}
	return domain.TopValue()
}

// GetBounded returns the variable's interval bounded to its declared integer
// type's representable range.
func (s *State) GetBounded(obj types.Object, bnd domain.Bounds) domain.Interval {
	iv := s.Get(obj)
	if iv.Empty {
		return iv
	}
	return domain.Intersect(iv, domain.TypeTop(bnd))
}

func (s *State) Set(obj types.Object, iv domain.Interval) {
	if s == nil || obj == nil {
		return
	}
	s.vars[obj] = iv
}

// Join merges another state into s pointwise using interval hull union.
func (s *State) Join(other *State) {
	if s == nil || other == nil {
		return
	}
	for obj, ov := range other.vars {
		if sv, ok := s.vars[obj]; ok {
			s.vars[obj] = domain.Union(sv, ov)
		} else {
			s.vars[obj] = ov
		}
	}
}

func (s *State) Copy() *State {
	if s == nil {
		return nil
	}
	cp := &State{vars: make(map[types.Object]domain.Interval, len(s.vars))}
	for obj, iv := range s.vars {
		cp.vars[obj] = domain.Interval{Lo: iv.Lo, Hi: iv.Hi, Empty: iv.Empty, NonZero: iv.NonZero}
	}
	return cp
}

func (s *State) Equals(other *State) bool {
	if s == nil || other == nil {
		return s == other
	}
	if len(s.vars) != len(other.vars) {
		return false
	}
	for obj, iv := range s.vars {
		ov, ok := other.vars[obj]
		if !ok || !domain.Equals(iv, ov) {
			return false
		}
	}
	return true
}

// LessEqual reports s <= other in the information ordering: every interval in
// s is contained in other's interval for the same variable.
func (s *State) LessEqual(other *State) bool {
	if s == nil || other == nil {
		return false
	}
	for obj, iv := range s.vars {
		if ov, ok := other.vars[obj]; !ok {
			return false
		} else if !domain.Contains(ov, iv) {
			return false
		}
	}
	return true
}

func (s *State) Len() int {
	if s == nil {
		return 0
	}
	return len(s.vars)
}
