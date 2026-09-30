package intervalanalysis

import (
	"encoding/json"
	"fmt"
	"io"
)

// JSONWarning is the structured on-the-wire representation of one warning.
type JSONWarning struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Offset   int    `json:"offset"`
	Function string `json:"function"`
	Message  string `json:"message"`
	Evidence string `json:"evidence"`
}

// JSONReport is the reproducible structured analysis report.
type JSONReport struct {
	Complete   bool          `json:"complete"`
	Unfinished []string      `json:"unfinished,omitempty"`
	Warnings   []JSONWarning `json:"warnings"`
}

// WriteJSON writes a deterministic JSON report.
func WriteJSON(w io.Writer, r *Result) error {
	rep := JSONReport{
		Complete:   r.Complete,
		Unfinished: r.Unfinished,
		Warnings:   make([]JSONWarning, 0, len(r.Warnings)),
	}
	if rep.Unfinished == nil {
		rep.Unfinished = []string{}
	}
	for _, wn := range r.Warnings {
		rep.Warnings = append(rep.Warnings, JSONWarning{
			Category: string(wn.Category),
			Severity: wn.Severity.String(),
			File:     wn.Position.Filename,
			Line:     wn.Position.Line,
			Column:   wn.Position.Column,
			Offset:   wn.Position.Offset,
			Function: wn.Function,
			Message:  wn.Message,
			Evidence: wn.Evidence,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// WriteText writes a human-readable report.
func WriteText(w io.Writer, r *Result) error {
	status := "complete"
	if !r.Complete {
		status = "INCOMPLETE"
	}
	if _, err := fmt.Fprintf(w, "analysis %s: %d warning(s)\n", status, len(r.Warnings)); err != nil {
		return err
	}
	for _, wn := range r.Warnings {
		if _, err := fmt.Fprintf(w, "%s [%s/%s] %s\n    in %s\n    evidence: %s\n",
			wn.Position, wn.Category, wn.Severity, wn.Message,
			wn.Function, wn.Evidence); err != nil {
			return err
		}
	}
	if !r.Complete {
		if _, err := fmt.Fprintf(w, "unfinished: %v\n", r.Unfinished); err != nil {
			return err
		}
	}
	return nil
}
