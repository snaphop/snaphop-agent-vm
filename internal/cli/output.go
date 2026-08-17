package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// OutputFormat selects how command results are rendered. Both forms are a
// public contract; json in particular is consumed by agent supervisors.
type OutputFormat string

const (
	OutputText OutputFormat = "text"
	OutputJSON OutputFormat = "json"
)

// output writes command results to stdout and progress to stderr, so
// `--output json` can be piped safely (docs/cli.md).
type output struct {
	stdout io.Writer
	stderr io.Writer
	format OutputFormat
	quiet  bool
}

// JSON writes a result as indented JSON to stdout.
func (o *output) JSON(value any) error {
	encoder := json.NewEncoder(o.stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// Printf writes a command result to stdout.
func (o *output) Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(o.stdout, format, args...)
}

// Progress writes human-readable progress to stderr, unless --quiet.
func (o *output) Progress(format string, args ...any) {
	if o.quiet {
		return
	}
	_, _ = fmt.Fprintf(o.stderr, format, args...)
}

// Table writes aligned columns to stdout. Header cells are given as the first
// row.
func (o *output) Table(rows [][]string) {
	if len(rows) == 0 {
		return
	}
	w := tabwriter.NewWriter(o.stdout, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
}
