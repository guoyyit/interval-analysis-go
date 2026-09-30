package analyzer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"sort"
	"sync"

	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/report"
)

// wireInterval is the JSON form of an interval.
type wireInterval struct {
	Lo    string `json:"lo"`
	Hi    string `json:"hi"`
	Empty bool   `json:"empty"`
}

func encodeInterval(i domain.Interval) wireInterval {
	if i.Empty {
		return wireInterval{Empty: true}
	}
	return wireInterval{Lo: i.Lo.String(), Hi: i.Hi.String()}
}

func decodeInterval(w wireInterval) domain.Interval {
	if w.Empty {
		return domain.EmptyValue()
	}
	lo, _ := new(big.Int).SetString(w.Lo, 10)
	hi, _ := new(big.Int).SetString(w.Hi, 10)
	if lo == nil || hi == nil {
		return domain.TopValue()
	}
	return domain.BigRange(lo, hi)
}

// Summary is a reusable, sound over-approximation of one function:
//   - Results: possible range of each integer result (others omitted);
//   - MayPanic: the body can panic for some reachable input;
//   - Deps: keys of same-package callees;
//   - DepsSummaries: the summary fingerprint of each callee at analysis time.
//
// Summaries are computed with every parameter set to full range, so they hold
// for all inputs. Unknown behavior (external calls, channel ops, reflection)
// collapses affected results to full range; a summary can therefore produce
// extra warnings at call sites, but never hide a real fault.
type Summary struct {
	Key           string            `json:"key"`
	Name          string            `json:"name"`
	Results       []domain.Interval `json:"-"`
	ResultsWire   []wireInterval    `json:"results"`
	HasResults    bool              `json:"has_results"`
	MayPanic      bool              `json:"may_panic"`
	Deps          []string          `json:"deps"`
	DepsSummaries map[string]string `json:"deps_summaries"`
	Hash          string            `json:"hash"`
	Analyzed      bool              `json:"analyzed"`
	Incomplete    bool              `json:"incomplete"`
	Fingerprint   string            `json:"fingerprint"`
	Alerts        []report.Alert    `json:"alerts,omitempty"`
}

func (s *Summary) seal() {
	s.ResultsWire = make([]wireInterval, len(s.Results))
	for i, r := range s.Results {
		s.ResultsWire[i] = encodeInterval(r)
	}
	s.Fingerprint = s.computeFingerprint()
}

func (s *Summary) unseal() {
	s.Results = make([]domain.Interval, len(s.ResultsWire))
	for i, w := range s.ResultsWire {
		s.Results[i] = decodeInterval(w)
	}
}

func (s *Summary) computeFingerprint() string {
	h := sha256.New()
	write := func(p string) { h.Write([]byte(p)); h.Write([]byte{0}) }
	write(s.Key)
	write(s.Hash)
	write(s.Name)
	if s.MayPanic {
		write("P")
	}
	if s.Incomplete {
		write("I")
	}
	keys := make([]string, 0, len(s.DepsSummaries))
	for k := range s.DepsSummaries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(k)
		write(s.DepsSummaries[k])
	}
	for _, w := range s.ResultsWire {
		write(w.Lo)
		write(w.Hi)
		if w.Empty {
			write("E")
		}
	}
	for _, a := range s.Alerts {
		write(a.KindName)
		write(a.Position)
		write(a.Condition)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SummaryStore is the concurrency-safe cache of function summaries. All reads
// and writes take the lock, so parallel analyses of independent functions
// observe a consistent map. Get returns copies; callers may never mutate a
// stored summary in place.
type SummaryStore struct {
	mu        sync.RWMutex
	summaries map[string]*Summary
}

func NewSummaryStore() *SummaryStore {
	return &SummaryStore{summaries: make(map[string]*Summary)}
}

func cloneSummary(s *Summary) *Summary {
	if s == nil {
		return nil
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(s)
	var cp Summary
	_ = json.NewDecoder(&buf).Decode(&cp)
	cp.unseal()
	return &cp
}

func (s *SummaryStore) Get(key string) (*Summary, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.summaries[key]
	if !ok {
		return nil, false
	}
	return cloneSummary(v), true
}

func (s *SummaryStore) Put(key string, sum *Summary) {
	sum.Key = key
	sum.seal()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summaries[key] = cloneSummary(sum)
}

// PutIfAbsent stores only when nothing is cached; reports whether it stored.
func (s *SummaryStore) PutIfAbsent(key string, sum *Summary) bool {
	sum.Key = key
	sum.seal()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.summaries[key]; ok {
		return false
	}
	s.summaries[key] = cloneSummary(sum)
	return true
}

func (s *SummaryStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.summaries, key)
}

func (s *SummaryStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.summaries)
}

// Snapshot returns a deterministic slice of key/summary pairs.
func (s *SummaryStore) Snapshot() []*Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.summaries))
	for k := range s.summaries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*Summary, 0, len(keys))
	for _, k := range keys {
		out = append(out, cloneSummary(s.summaries[k]))
	}
	return out
}

// Marshal persists all summaries for incremental reuse across processes.
func (s *SummaryStore) Marshal() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := make([]*Summary, 0, len(s.summaries))
	for _, v := range s.summaries {
		cp := cloneSummary(v)
		cp.Key = v.Key
		all = append(all, cp)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
	return json.MarshalIndent(all, "", "  ")
}

func (s *SummaryStore) Unmarshal(data []byte) error {
	var all []*Summary
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range all {
		v.unseal()
		v.Fingerprint = v.computeFingerprint()
		s.summaries[v.Key] = v
	}
	return nil
}
