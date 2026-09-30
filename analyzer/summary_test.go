package analyzer

import (
	"testing"

	"github.com/guoyyit/interval-analysis-go/domain"
)

func TestSummaryStoreRoundTrip(t *testing.T) {
	s := NewSummaryStore()
	s.Put("p#f", &Summary{
		Name:    "f",
		Results: []domain.Interval{domain.RangeValue(1, 9)},
		Hash:    "h", Analyzed: true,
	})
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s2 := NewSummaryStore()
	if err := s2.Unmarshal(data); err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get("p#f")
	if !ok {
		t.Fatal("summary lost across round trip")
	}
	if got.Fingerprint == "" {
		t.Fatal("fingerprint must be recomputed")
	}
	if got.Results[0].Lo.Int64() != 1 || got.Results[0].Hi.Int64() != 9 {
		t.Fatalf("results round trip = %v", got.Results)
	}
}

func TestSummaryStoreConcurrent(t *testing.T) {
	s := NewSummaryStore()
	done := make(chan struct{})
	for w := 0; w < 16; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 200; i++ {
				key := "p#f"
				s.Put(key, &Summary{Name: "f", Hash: "h", Results: []domain.Interval{domain.Const(int64(i))}, Analyzed: true})
				_, _ = s.Get(key)
				_ = s.Len()
			}
		}(w)
	}
	for w := 0; w < 16; w++ {
		<-done
	}
}
