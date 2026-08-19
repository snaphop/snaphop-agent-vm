// Package progress renders the progress of a long, multi-step operation to a
// terminal. It is presentation only: the packages that do the work report which
// step they have reached, and this package decides what an operator sees.
//
// Two renderings exist because two audiences do. On a terminal the steps
// collapse into one line that is redrawn in place, with a bar and the elapsed
// time, so a build that takes minutes still looks alive. Everywhere else — a
// pipe, a log file, a CI job, `--verbose` where slog is already writing to the
// same stream — each step is one plain line, because a line redrawn with
// carriage returns is unreadable once it is captured rather than displayed.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// barWidth is the drawn width of the bar itself, in cells. It is fixed rather
// than derived from the terminal size: querying the width needs an ioctl and a
// dependency this project does not have, and the whole line stays inside 80
// columns for every step name we render.
const barWidth = 20

// redrawInterval is how often an unchanged bar is redrawn so its elapsed time
// keeps moving. A `podman build` step can run for minutes without producing a
// single event of its own.
const redrawInterval = time.Second

// Bar reports the progress of a sequence of named steps.
//
// The zero value is not usable; construct one with NewBar. A nil *Bar is safe
// and does nothing, which is how a caller expresses --quiet.
type Bar struct {
	out     io.Writer
	animate bool

	mu      sync.Mutex
	started time.Time
	number  int
	total   int
	name    string
	drawn   bool

	stop chan struct{}
	done chan struct{}
}

// NewBar returns a Bar writing to out. It redraws a single line in place only
// when out is a terminal and the caller permits animation; otherwise it writes
// one line per step. Callers pass animate=false when something else is already
// writing to the same stream — `--verbose` logging, most of all.
func NewBar(out io.Writer, animate bool) *Bar {
	return &Bar{out: out, animate: animate && isTerminal(out)}
}

// Step reports that a 1-based numbered step out of total has begun.
func (b *Bar) Step(number, total int, name string) {
	if b == nil {
		return
	}

	b.mu.Lock()
	if b.started.IsZero() {
		b.started = time.Now()
	}
	b.number, b.total, b.name = number, total, name
	b.mu.Unlock()

	if !b.animate {
		_, _ = fmt.Fprintf(b.out, "[%d/%d] %s\n", number, total, name)
		return
	}
	b.startTicker()
	b.draw()
}

// Finish ends the report. On a terminal it erases the bar, leaving the stream
// to whatever the command prints as its result; err is accepted so that a
// failed operation does not erase the step it failed on.
func (b *Bar) Finish(err error) {
	if b == nil {
		return
	}
	b.stopTicker()

	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.animate || !b.drawn {
		return
	}
	if err != nil {
		// The step that was on screen is the step that failed, and the error
		// about to be printed does not name it. Keep the line and end it.
		_, _ = fmt.Fprintln(b.out)
	} else {
		_, _ = fmt.Fprint(b.out, "\r\033[K")
	}
	b.drawn = false
}

// startTicker begins redrawing the bar so its elapsed time advances between
// steps. It is idempotent: every step calls it, only the first one starts it.
func (b *Bar) startTicker() {
	b.mu.Lock()
	if b.stop != nil {
		b.mu.Unlock()
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	b.stop, b.done = stop, done
	b.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(redrawInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				b.draw()
			}
		}
	}()
}

func (b *Bar) stopTicker() {
	b.mu.Lock()
	stop, done := b.stop, b.done
	b.stop, b.done = nil, nil
	b.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
}

// draw renders the current step in place. It is called from both the caller's
// goroutine and the redraw ticker, so the whole render happens under the lock:
// two interleaved writes would leave the terminal holding half of each.
func (b *Bar) draw() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.total == 0 {
		return
	}
	_, _ = fmt.Fprintf(b.out, "\r%s\033[K", b.line())
	b.drawn = true
}

// line is the rendered bar, without the terminal control sequences that place
// it. It is separate so a test can assert on what an operator reads.
func (b *Bar) line() string {
	filled := b.number * barWidth / b.total
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	return fmt.Sprintf("[%s] %d/%d %s %s", bar, b.number, b.total, b.name, FormatDuration(time.Since(b.started)))
}

// FormatDuration renders an elapsed time the way a progress line wants it:
// short, fixed in shape, and never more precise than the operator cares about.
func FormatDuration(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

// isTerminal reports whether w is a character device we may redraw in place. A
// writer that is not an *os.File — a test buffer, a pipe wrapper — never is.
func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	// A dumb terminal is one that cannot be assumed to handle the cursor
	// movement a redrawn line needs.
	if term := os.Getenv("TERM"); term == "" || term == "dumb" {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
