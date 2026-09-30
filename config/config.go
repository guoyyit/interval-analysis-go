package config

import (
	"errors"
	"time"
)

// Config controls precision/performance trade-offs. Soundness is independent
// of every field here: changing them can only change the number of warnings
// and the analysis time, never turn a possibly-faulting operation into a
// proven-safe one.
type Config struct {
	// MaxIterations bounds CFG fix-point iterations per function. Widening
	// guarantees stabilization well before this in practice; if the bound is
	// ever reached the function is marked incomplete rather than trusted.
	MaxIterations int
	// NarrowingPasses is the number of precision-improving narrowing rounds
	// applied after the widened post-fixpoint. More passes may remove false
	// positives; zero leaves the widened result (still sound).
	NarrowingPasses int
	// MinSeverity filters alerts: "low", "medium" or "high".
	MinSeverity string
	// Concurrency is the maximum number of functions analyzed in parallel.
	// Values < 1 mean sequential. Output is identical for any value.
	Concurrency int
	// Timeout bounds the whole analysis; zero means no timeout.
	Timeout time.Duration
	// IncludeTests includes _test.go files in the analyzed set.
	IncludeTests bool
	// Recursive descends into subdirectories when a directory is given.
	Recursive bool
	// CachePath, when set, persists function summaries for incremental runs.
	// Empty keeps the cache in memory only.
	CachePath string
}

// Default returns conservative, fast-converging defaults.
func Default() Config {
	return Config{
		MaxIterations:   64,
		NarrowingPasses: 2,
		MinSeverity:     "low",
		Concurrency:     1,
		Timeout:         0,
		IncludeTests:    false,
		Recursive:       false,
	}
}

// Validate normalizes and checks the configuration. Invalid values produce an
// error instead of silently weakening the analysis.
func (c *Config) Validate() error {
	if c.MaxIterations <= 0 {
		c.MaxIterations = 64
	}
	if c.NarrowingPasses < 0 {
		c.NarrowingPasses = 0
	}
	if c.MinSeverity == "" {
		c.MinSeverity = "low"
	}
	switch c.MinSeverity {
	case "low", "medium", "high":
	default:
		return errors.New("config: MinSeverity must be one of low|medium|high")
	}
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	if c.Concurrency < 0 {
		c.Concurrency = 1
	}
	return nil
}
