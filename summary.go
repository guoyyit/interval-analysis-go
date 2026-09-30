package intervalanalysis

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/token"
	"sort"
	"sync"
)

// FuncID identifies a function by package import path plus qualified name.
type FuncID struct {
	PkgPath string
	Name    string
}

func (id FuncID) String() string {
	if id.PkgPath == "" {
		return id.Name
	}
	return id.PkgPath + "." + id.Name
}

// Summary is the reusable, sound analysis result of one function.
//
// Return is the possible range of the integer return value computed with all
// parameters at full range, hence it covers every concrete invocation. Warnings
// are those found in the function body. Complete distinguishes a finished
// analysis from an interrupted one.
type Summary struct {
	ID         FuncID
	Return     Interval
	Warnings   []Warning
	SourceHash string
	Pos        token.Position
	Complete   bool
}

// HashSource computes a stable hash of a function declaration's source bytes.
func HashSource(fset *token.FileSet, decl *ast.FuncDecl, src []byte) string {
	if decl == nil {
		return ""
	}
	f := fset.File(decl.Pos())
	if f == nil {
		return ""
	}
	start := f.Offset(decl.Pos())
	end := f.Offset(decl.End())
	if start < 0 || end < 0 || start > end || end > len(src) {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(f.Name()))
	h.Write(src[start:end])
	return hex.EncodeToString(h.Sum(nil))
}

// SummaryStore is a concurrency-safe cache of summaries plus a caller->callee
// dependency index, used for incremental invalidation.
type SummaryStore struct {
	mu        sync.RWMutex
	summaries map[FuncID]*Summary
	// edges[caller] = set of callees
	edges map[FuncID]map[FuncID]bool
	// reverse[callee] = set of callers
	reverse map[FuncID]map[FuncID]bool
}

// NewSummaryStore builds an empty store.
func NewSummaryStore() *SummaryStore {
	return &SummaryStore{
		summaries: map[FuncID]*Summary{},
		edges:     map[FuncID]map[FuncID]bool{},
		reverse:   map[FuncID]map[FuncID]bool{},
	}
}

// Get returns a cached summary.
func (s *SummaryStore) Get(id FuncID) (*Summary, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sum, ok := s.summaries[id]
	return sum, ok
}

// Put stores a summary.
func (s *SummaryStore) Put(sum *Summary) {
	if sum == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summaries[sum.ID] = sum
}

// AddDependency records that caller depends on callee.
func (s *SummaryStore) AddDependency(caller, callee FuncID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.edges[caller] == nil {
		s.edges[caller] = map[FuncID]bool{}
	}
	s.edges[caller][callee] = true
	if s.reverse[callee] == nil {
		s.reverse[callee] = map[FuncID]bool{}
	}
	s.reverse[callee][caller] = true
}

// Invalidate removes id and all its transitive dependents, returning the full
// set of removed FuncIDs (id included).
func (s *SummaryStore) Invalidate(id FuncID) []FuncID {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := map[FuncID]bool{}
	stack := []FuncID{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if removed[cur] {
			continue
		}
		removed[cur] = true
		for caller := range s.reverse[cur] {
			if !removed[caller] {
				stack = append(stack, caller)
			}
		}
	}
	for fn := range removed {
		delete(s.summaries, fn)
	}
	out := make([]FuncID, 0, len(removed))
	for fn := range removed {
		out = append(out, fn)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PkgPath != out[j].PkgPath {
			return out[i].PkgPath < out[j].PkgPath
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Dependents returns the transitive callers of id (excluding id itself).
func (s *SummaryStore) Dependents(id FuncID) []FuncID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[FuncID]bool{}
	stack := []FuncID{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for caller := range s.reverse[cur] {
			if !seen[caller] {
				seen[caller] = true
				stack = append(stack, caller)
			}
		}
	}
	out := make([]FuncID, 0, len(seen))
	for fn := range seen {
		out = append(out, fn)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PkgPath != out[j].PkgPath {
			return out[i].PkgPath < out[j].PkgPath
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Snapshot returns a deterministic copy of all complete summaries.
func (s *SummaryStore) Snapshot() []*Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Summary, 0, len(s.summaries))
	for _, sum := range s.summaries {
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID.PkgPath != out[j].ID.PkgPath {
			return out[i].ID.PkgPath < out[j].ID.PkgPath
		}
		return out[i].ID.Name < out[j].ID.Name
	})
	return out
}
