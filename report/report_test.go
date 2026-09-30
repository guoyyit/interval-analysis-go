package report

import "testing"

func TestDedupSortFilter(t *testing.T) {
	a := Alert{Kind: KindDivByZero, Severity: SeverityHigh, Position: "x.go:2", File: "x.go", Line: 2}
	b := Alert{Kind: KindIndexOutOfBounds, Severity: SeverityHigh, Position: "x.go:2", File: "x.go", Line: 2}
	c := Alert{Kind: KindIntegerOverflow, Severity: SeverityMedium, Position: "x.go:1", File: "x.go", Line: 1}
	dup := a
	alerts := Deduplicate([]Alert{a, b, c, dup})
	if len(alerts) != 3 {
		t.Fatalf("dedup = %d, want 3", len(alerts))
	}
	SortBySeverity(alerts)
	if alerts[0].Line != 2 {
		t.Fatalf("high severity must sort first, got line %d", alerts[0].Line)
	}
	med := FilterSeverity(alerts, SeverityHigh)
	for _, x := range med {
		if x.Severity < SeverityHigh {
			t.Fatal("filter leaked medium alert")
		}
	}
}

func TestParseSeverity(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Severity
	}{
		{"low", SeverityLow}, {"medium", SeverityMedium}, {"high", SeverityHigh},
	} {
		got, ok := ParseSeverity(tc.in)
		if !ok || got != tc.want {
			t.Fatalf("ParseSeverity(%q)=%v,%v", tc.in, got, ok)
		}
	}
	if _, ok := ParseSeverity("nope"); ok {
		t.Fatal("invalid severity must fail")
	}
}
