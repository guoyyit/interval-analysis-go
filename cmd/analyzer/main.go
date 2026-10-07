package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	ia "github.com/guoyyit/interval-analysis-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", ".", "directory to analyze")
	recursive := flag.Bool("recursive", false, "analyze subdirectories recursively")
	format := flag.String("format", "json", "output format: json|text")
	workers := flag.Int("workers", 1, "parallel analysis workers")
	maxIter := flag.Int("max-iterations", 0, "fixpoint iteration cap (0=default)")
	widenAfter := flag.Int("widen-after", 0, "iterations before widening (0=default)")
	narrowing := flag.Int("narrowing-passes", 0, "narrowing/precision passes (0=default)")
	timeout := flag.Duration("timeout", 0, "overall analysis timeout (e.g. 30s)")
	minSev := flag.String("min-severity", "low", "minimum severity: low|medium|high")
	flag.Parse()

	cfg := ia.DefaultConfig()
	cfg.Workers = *workers
	cfg.MaxIterations = *maxIter
	cfg.WidenAfter = *widenAfter
	cfg.NarrowingPasses = *narrowing
	cfg.Timeout = *timeout
	cfg.MinSeverity = ia.ParseSeverity(*minSev)

	ctx := context.Background()
	a := ia.NewAnalyzer(cfg)
	result, err := a.Analyze(ctx, *dir, *recursive)
	if err != nil {
		return err
	}
	if *format == "text" {
		if err := ia.WriteText(os.Stdout, result); err != nil {
			return err
		}
	} else {
		if err := ia.WriteJSON(os.Stdout, result); err != nil {
			return err
		}
	}
	if code := ia.ExitCode(result); code != 0 {
		os.Exit(code)
	}
	return nil
}
