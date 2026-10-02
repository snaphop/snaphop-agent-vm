package progress

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBar_WritesOneLinePerStepWhenNotOnATerminal(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	bar := NewBar(&out, true)

	bar.Step(1, 3, "pulling the source image")
	bar.Step(2, 3, "building the image layers")
	bar.Finish(nil)

	want := "[1/3] pulling the source image\n[2/3] building the image layers\n"
	if out.String() != want {
		t.Errorf("output =\n%q\nwant\n%q", out.String(), want)
	}
}

// A captured stream must never contain the control sequences that redraw a
// line in place: piping progress into a log is exactly where they are unusable.
func TestBar_WritesNoTerminalControlSequencesWhenNotOnATerminal(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	bar := NewBar(&out, true)

	bar.Step(1, 2, "creating the base disk")
	bar.Finish(errors.New("virt-make-fs failed"))

	if strings.ContainsAny(out.String(), "\r\033") {
		t.Errorf("output contains terminal control sequences: %q", out.String())
	}
}

func TestBar_RendersTheFilledFractionOfTheCurrentStep(t *testing.T) {
	t.Parallel()
	bar := NewBar(&bytes.Buffer{}, true)
	bar.started = time.Now().Add(-90 * time.Second)
	bar.number, bar.total, bar.name = 5, 10, "creating the base disk"

	line := bar.line()
	filled := strings.Count(line, "█")
	if filled != barWidth/2 {
		t.Errorf("filled cells = %d, want %d, in %q", filled, barWidth/2, line)
	}
	for _, want := range []string{"5/10", "creating the base disk", "1m30s"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q does not contain %q", line, want)
		}
	}
}

func TestBar_NilReportsNothing(t *testing.T) {
	t.Parallel()
	var bar *Bar
	bar.Step(1, 2, "pulling the source image")
	bar.Finish(nil)
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{1500 * time.Millisecond, "2s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m00s"},
		{7*time.Minute + 5*time.Second, "7m05s"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.in); got != c.want {
			t.Errorf("FormatDuration(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}
