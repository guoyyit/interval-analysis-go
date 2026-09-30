package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Severity int

const (
	SeverityLow Severity = iota
	SeverityMedium
	SeverityHigh
)

// String renders a severity; ParseSeverity is its inverse.
func (s Severity) String() string {
	switch s {
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	default:
		return "low"
	}
}

func ParseSeverity(s string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "low":
		return SeverityLow, true
	case "medium", "med":
		return SeverityMedium, true
	case "high":
		return SeverityHigh, true
	}
	return SeverityLow, false
}

type Kind int

const (
	KindDivByZero Kind = iota
	KindIndexOutOfBounds
	KindIntegerOverflow
)

func (k Kind) String() string {
	switch k {
	case KindDivByZero:
		return "divide-by-zero"
	case KindIndexOutOfBounds:
		return "index-out-of-bounds"
	case KindIntegerOverflow:
		return "integer-overflow"
	}
	return "unknown"
}

// DefaultSeverity assigns a fixed severity per defect class. Division by zero
// and index out of bounds always panic at runtime; overflow produces a
// silently wrapped value (and on 32/64 bit platforms rarely crashes), so it
// is reported one level lower.
func DefaultSeverity(k Kind) Severity {
	switch k {
	case KindDivByZero:
		return SeverityHigh
	case KindIndexOutOfBounds:
		return SeverityHigh
	case KindIntegerOverflow:
		return SeverityMedium
	}
	return SeverityLow
}

// Alert is a single potential runtime defect. The analysis never claims a
// fault is certain unless the offending interval proves it; Condition is a
// simplified human-readable trigger and IntervalBasis states which abstract
// values led to the conclusion. One source location/defect is reported at
// most once per function.
type Alert struct {
	Kind          Kind     `json:"kind"`
	KindName      string   `json:"kind_name"`
	Severity      Severity `json:"severity"`
	SeverityName  string   `json:"severity_name"`
	Position      string   `json:"position"`
	File          string   `json:"file"`
	Line          int      `json:"line"`
	Column        int      `json:"column"`
	Function      string   `json:"function"`
	Package       string   `json:"package"`
	Condition     string   `json:"condition"`
	IntervalBasis string   `json:"interval_basis"`
	Message       string   `json:"message"`
}

type PackageReport struct {
	Package    string  `json:"package"`
	Dir        string  `json:"dir"`
	Alerts     []Alert `json:"alerts"`
	Incomplete bool    `json:"incomplete"`
	Canceled   bool    `json:"canceled"`
	Functions  int     `json:"functions_analyzed"`
	Skipped    int     `json:"functions_skipped"`
	DurationMS int64   `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
}

type Report struct {
	Root       string          `json:"root"`
	StartedAt  time.Time       `json:"started_at"`
	Packages   []PackageReport `json:"packages"`
	Incomplete bool            `json:"incomplete"`
}

// FilterSeverity returns alerts at or above min.
func FilterSeverity(alerts []Alert, min Severity) []Alert {
	out := make([]Alert, 0, len(alerts))
	for _, a := range alerts {
		if a.Severity >= min {
			out = append(out, a)
		}
	}
	return out
}

// SortBySeverity orders alerts high -> low, then by position for deterministic
// output. Sorting is stable so equal alerts keep their original order, which
// is part of the serial/parallel equivalence guarantee.
func SortBySeverity(alerts []Alert) {
	sort.SliceStable(alerts, func(i, j int) bool {
		if alerts[i].Severity != alerts[j].Severity {
			return alerts[i].Severity > alerts[j].Severity
		}
		if alerts[i].File != alerts[j].File {
			return alerts[i].File < alerts[j].File
		}
		if alerts[i].Line != alerts[j].Line {
			return alerts[i].Line < alerts[j].Line
		}
		if alerts[i].Column != alerts[j].Column {
			return alerts[i].Column < alerts[j].Column
		}
		return alerts[i].Kind < alerts[j].Kind
	})
}

// Deduplicate keeps the first alert per (position, kind).
func Deduplicate(alerts []Alert) []Alert {
	seen := make(map[[2]interface{}]bool, len(alerts))
	out := make([]Alert, 0, len(alerts))
	for _, a := range alerts {
		key := [2]interface{}{a.Position, a.Kind}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
	}
	return out
}

func FormatJSON(r *Report) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func FormatText(r *Report) string {
	var b strings.Builder
	if r.Incomplete {
		b.WriteString("WARNING: analysis incomplete; results below are partial.\n\n")
	}
	for _, p := range r.Packages {
		header := fmt.Sprintf("package %s (%s)", p.Package, p.Dir)
		b.WriteString(header)
		b.WriteByte('\n')
		b.WriteString(strings.Repeat("-", len(header)))
		b.WriteByte('\n')
		if p.Error != "" {
			fmt.Fprintf(&b, "  load error: %s\n", p.Error)
		}
		if p.Canceled {
			b.WriteString("  status: CANCELED (partial results only)\n")
		} else if p.Incomplete {
			b.WriteString("  status: INCOMPLETE (partial results only)\n")
		}
		if len(p.Alerts) == 0 {
			b.WriteString("  no alerts\n")
		}
		for _, a := range p.Alerts {
			fmt.Fprintf(&b, "  [%s] %s: %s\n", a.SeverityName, a.Position, a.KindName)
			fmt.Fprintf(&b, "      %s\n", a.Message)
			fmt.Fprintf(&b, "      condition: %s\n", a.Condition)
			fmt.Fprintf(&b, "      intervals: %s\n", a.IntervalBasis)
		}
		fmt.Fprintf(&b, "  (%d functions analyzed, %d skipped, %d alert(s), %dms)\n\n",
			p.Functions, p.Skipped, len(p.Alerts), p.DurationMS)
	}
	return b.String()
}

// Finalize applies deterministic dedup/sort/filter to a package report and
// returns it. It must be called before rendering so serial and parallel runs
// emit identical alert lists.
func Finalize(p PackageReport, min Severity) PackageReport {
	p.Alerts = Deduplicate(p.Alerts)
	p.Alerts = FilterSeverity(p.Alerts, min)
	SortBySeverity(p.Alerts)
	return p
}
