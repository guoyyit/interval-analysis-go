package intervalanalysis

import "time"

// Config controls precision/performance trade-offs of the analyzer.
// Changing these values only affects warning count and runtime; it never
// relaxes the conservative soundness guarantee.
type Config struct {
	// MaxIterations is the upper bound for CFG fixpoint iteration per function.
	MaxIterations int
	// WidenAfter is the number of plain join iterations before widening kicks in.
	WidenAfter int
	// NarrowingPasses is the number of precision-improvement (narrowing) passes
	// performed after the widening fixpoint has been reached.
	NarrowingPasses int
	// Workers is the maximum number of functions analyzed in parallel.
	// A value <= 1 means serial execution.
	Workers int
	// Timeout bounds the whole analysis; zero means no timeout. Cancellation via
	// context is honored as well.
	Timeout time.Duration
	// MinSeverity filters warnings below the given severity level.
	MinSeverity Severity
}

// DefaultConfig returns conservative defaults that terminate for all inputs.
func DefaultConfig() Config {
	return Config{}
}
