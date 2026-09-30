package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/guoyyit/interval-analysis-go/analyzer"
	"github.com/guoyyit/interval-analysis-go/config"
	"github.com/guoyyit/interval-analysis-go/report"
)

func main() {
	cfg := config.Default()

	flag.IntVar(&cfg.MaxIterations, "max-iterations", cfg.MaxIterations,
		"upper bound on fix-point iterations per function")
	flag.IntVar(&cfg.NarrowingPasses, "narrowing", cfg.NarrowingPasses,
		"precision-improving narrowing passes after widening")
	flag.IntVar(&cfg.Concurrency, "jobs", cfg.Concurrency,
		"number of functions analyzed in parallel (1 = sequential)")
	flag.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout,
		"overall analysis timeout, e.g. 30s (0 = no timeout)")
	flag.StringVar(&cfg.MinSeverity, "min-severity", cfg.MinSeverity,
		"only show alerts at/above: low|medium|high")
	flag.BoolVar(&cfg.IncludeTests, "tests", cfg.IncludeTests,
		"include _test.go files")
	flag.BoolVar(&cfg.Recursive, "recursive", cfg.Recursive,
		"recurse into subdirectories when a directory is given")
	flag.StringVar(&cfg.CachePath, "cache", "",
		"path to a persistent summary cache file for incremental runs")
	jsonOut := flag.Bool("json", false, "emit structured JSON instead of text")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] <package-or-directory>...\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	engine := analyzer.New(cfg)
	if cfg.CachePath != "" {
		if data, err := os.ReadFile(cfg.CachePath); err == nil {
			_ = engine.Store().Unmarshal(data)
		}
	}

	ctx, stop := signalContext(cfg.Timeout)
	defer stop()

	rep, err := engine.AnalyzePaths(ctx, flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "analysis error:", err)
		os.Exit(1)
	}

	// Persist summaries only for complete analyses; a partial cache would be
	// unsound to reuse as if it were a full result.
	if cfg.CachePath != "" && !rep.Incomplete && ctx.Err() == nil {
		if data, err := engine.Store().Marshal(); err == nil {
			_ = os.WriteFile(cfg.CachePath, data, 0o644)
		}
	}

	if *jsonOut {
		data, err := report.FormatJSON(rep)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Stdout.Write(data)
	} else {
		fmt.Print(report.FormatText(rep))
	}

	// Never exit 0 when results are partial: incomplete output must not be
	// mistaken for a complete "no defects" verdict.
	if rep.Incomplete || ctx.Err() != nil {
		os.Exit(3)
	}
}

func signalContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(sigCh)
		cancel()
	}
}

var _ = strings.TrimSpace
