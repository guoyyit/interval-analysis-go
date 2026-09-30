package intervalanalysis

import (
	"fmt"
	"go/token"
	"sort"
	"strings"
)

// Severity ranks warnings for filtering and sorting.
type Severity int

const (
	SeverityLow Severity = iota
	SeverityMedium
	SeverityHigh
)

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

// ParseSeverity parses low|medium|high (case-insensitive).
func ParseSeverity(s string) Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high":
		return SeverityHigh
	case "medium", "med":
		return SeverityMedium
	default:
		return SeverityLow
	}
}

// Category identifies the kind of potential runtime error.
type Category string

const (
	CategoryDivByZero        Category = "div_by_zero"
	CategoryIndexOutOfBounds Category = "index_out_of_bounds"
	CategoryIntegerOverflow  Category = "integer_overflow"
)

// Warning is a single, deduplicated potential-defect report.
type Warning struct {
	Category Category
	Severity Severity
	Position token.Position
	Function string
	// Message is a short, simplified description of the trigger condition.
	Message string
	// Evidence records the interval reasoning supporting the report.
	Evidence string
}

func (w Warning) String() string {
	return fmt.Sprintf("%s: [%s/%s] %s", w.Position, w.Category, w.Severity, w.Message)
}

// dedupKey identifies a defect site: the same operation position and category
// is reported at most once, regardless of how many fixpoint iterations touch it.
func (w Warning) dedupKey() string {
	return fmt.Sprintf("%d:%s:%s", w.Position.Offset, w.Category, w.Function)
}

// dedupWarnings removes duplicate reports, preserving first occurrence.
func dedupWarnings(ws []Warning) []Warning {
	seen := make(map[string]struct{}, len(ws))
	out := ws[:0]
	for _, w := range ws {
		key := w.dedupKey()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, w)
	}
	return out
}

// FilterWarnings returns warnings at or above min severity.
func FilterWarnings(ws []Warning, min Severity) []Warning {
	out := make([]Warning, 0, len(ws))
	for _, w := range ws {
		if w.Severity >= min {
			out = append(out, w)
		}
	}
	return out
}

// SortWarnings orders warnings deterministically: file, position, category,
// function. This makes parallel and serial outputs identical line by line.
func SortWarnings(ws []Warning) {
	sort.SliceStable(ws, func(i, j int) bool {
		a, b := ws[i], ws[j]
		if a.Position.Filename != b.Position.Filename {
			return a.Position.Filename < b.Position.Filename
		}
		if a.Position.Offset != b.Position.Offset {
			return a.Position.Offset < b.Position.Offset
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.Function < b.Function
	})
}

// categorySeverity assigns default severity per defect category.
func categorySeverity(c Category) Severity {
	switch c {
	case CategoryDivByZero:
		return SeverityHigh
	case CategoryIndexOutOfBounds:
		return SeverityHigh
	case CategoryIntegerOverflow:
		return SeverityMedium
	}
	return SeverityLow
}
