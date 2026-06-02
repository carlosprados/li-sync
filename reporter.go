package main

import (
	"fmt"
	"io"
)

// Reporter receives progress steps from long-running operations (publish,
// republish): the preflight result, thumbnail upload, schedule warnings. It is
// the seam that lets the same core logic drive both the CLI (steps go to
// stderr, verbatim with the previous behaviour) and a future TUI (steps become
// tea.Msg events) without the logic knowing which front-end consumes them.
//
// Implementations must treat each Stepf call as one line; the format string
// carries no trailing newline.
type Reporter interface {
	Stepf(format string, args ...any)
}

// writerReporter writes each step as a single line to w. The CLI uses one bound
// to os.Stderr, preserving the exact progress output li-sync printed before the
// logic was decoupled.
type writerReporter struct{ w io.Writer }

func newWriterReporter(w io.Writer) writerReporter { return writerReporter{w: w} }

func (r writerReporter) Stepf(format string, args ...any) {
	fmt.Fprintf(r.w, format+"\n", args...)
}

// nopReporter discards progress. Useful in tests and for callers that only want
// the returned result.
type nopReporter struct{}

func (nopReporter) Stepf(string, ...any) {}
