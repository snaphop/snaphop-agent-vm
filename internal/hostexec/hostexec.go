// Package hostexec is the only place in this project where a process is
// spawned. Every helper tool — virt-install, virsh, qemu-img, podman, the
// libguestfs utilities, ip, ssh — is invoked through a Runner with an explicit
// argument vector. No other package may import os/exec.
//
// Two things follow from that placement. Argv construction, timeouts, logging,
// and exit-status handling exist in exactly one place; and because nearly every
// failure in this tool is really another program's failure, the error type here
// carries enough detail (tool, argv, exit status, bounded stderr) for an
// operator to rerun the failing command by hand.
package hostexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// stderrExcerptLimit bounds how much of a failed tool's stderr is carried in an
// error. libguestfs in particular can emit megabytes of appliance output, and
// an error message is not a log file.
const stderrExcerptLimit = 4096

// DefaultTimeout applies to any command that does not set one. Image builds and
// other long operations set their own.
const DefaultTimeout = 2 * time.Minute

// Effect classifies what a command does to host state. It is what makes
// --dry-run useful: read-only commands still run under --dry-run, so the
// printed plan reflects the actual host, while mutating commands are printed
// and skipped.
type Effect int

const (
	// Read inspects host state without changing it.
	Read Effect = iota
	// Mutate changes host state: defines a domain, writes a disk, starts a
	// network. These are printed rather than executed under --dry-run.
	Mutate
)

// Command is a single tool invocation. Name is the tool as it appears on PATH;
// it is also the name used in errors and logs.
type Command struct {
	Name    string
	Args    []string
	Effect  Effect
	Stdin   io.Reader
	Dir     string
	Timeout time.Duration

	// DryRunStdout is returned in place of real output when a Mutate command
	// is skipped under --dry-run, for the rare caller that must keep parsing.
	DryRunStdout string
}

// Argv is the full argument vector, tool included. It is what gets logged and
// what --dry-run prints, so it must always be the exact thing that ran.
func (c Command) Argv() []string {
	return append([]string{c.Name}, c.Args...)
}

// String renders the invocation in a form an operator can paste into a shell.
func (c Command) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	for _, a := range c.Argv() {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// Result is the outcome of a successful invocation.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
	// Skipped reports that --dry-run printed this command instead of running it.
	Skipped bool
}

// Runner executes commands. It is an interface so that tests substitute a fake
// at the process boundary rather than mocking inside our own packages.
type Runner interface {
	Run(ctx context.Context, c Command) (*Result, error)
	// LookPath resolves a tool on PATH, returning *NotFoundError if absent.
	LookPath(name string) (string, error)
}

// Exec is the real Runner.
type Exec struct {
	Logger *slog.Logger
}

// New returns a Runner that spawns processes.
func New(logger *slog.Logger) *Exec {
	if logger == nil {
		logger = slog.Default()
	}
	return &Exec{Logger: logger}
}

// Run executes c, returning a *ToolError for a non-zero exit and a
// *NotFoundError when the tool is not installed.
func (e *Exec) Run(ctx context.Context, c Command) (*Result, error) {
	if c.Name == "" {
		return nil, errors.New("hostexec: command has no tool name")
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Stdin = c.Stdin
	cmd.Dir = c.Dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)

	res := &Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: cmd.ProcessState.ExitCode(),
		Duration: elapsed,
	}

	// The argv of every invocation is logged deliberately: it is the single
	// most useful signal in this tool, because it is a command the operator can
	// rerun. Nothing secret is ever passed as an argument (SECURITY.md).
	e.Logger.Debug("ran host tool",
		"tool", c.Name,
		"argv", c.Argv(),
		"exit", res.ExitCode,
		"duration", elapsed,
	)

	if runErr != nil {
		var exitErr *exec.ExitError
		switch {
		// A killed process also surfaces as an ExitError, so the deadline is
		// checked first: "the tool failed" and "we gave up on it" are different
		// diagnoses for the operator.
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return res, &TimeoutError{Tool: c.Name, Argv: c.Argv(), Timeout: timeout}
		case errors.As(runErr, &exitErr):
			return res, &ToolError{
				Tool:     c.Name,
				Argv:     c.Argv(),
				ExitCode: res.ExitCode,
				Stderr:   excerpt(res.Stderr),
				Duration: elapsed,
			}
		case isNotFound(runErr):
			return res, &NotFoundError{Tool: c.Name}
		default:
			return res, fmt.Errorf("running %s: %w", c.Name, runErr)
		}
	}
	return res, nil
}

// LookPath resolves a tool on PATH.
func (e *Exec) LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", &NotFoundError{Tool: name}
	}
	return path, nil
}

// DryRun wraps a Runner. Read commands are delegated so the printed plan
// reflects real host state; Mutate commands are recorded and printed instead of
// executed.
type DryRun struct {
	Inner Runner
	Out   io.Writer

	planned []Command
}

// NewDryRun returns a Runner that prints mutating commands to out instead of
// running them.
func NewDryRun(inner Runner, out io.Writer) *DryRun {
	return &DryRun{Inner: inner, Out: out}
}

// Run delegates read-only commands and prints mutating ones.
func (d *DryRun) Run(ctx context.Context, c Command) (*Result, error) {
	if c.Effect == Read {
		return d.Inner.Run(ctx, c)
	}
	d.planned = append(d.planned, c)
	if d.Out != nil {
		fmt.Fprintln(d.Out, c.String())
	}
	return &Result{Stdout: []byte(c.DryRunStdout), Skipped: true}, nil
}

// LookPath delegates; resolving a tool changes nothing.
func (d *DryRun) LookPath(name string) (string, error) { return d.Inner.LookPath(name) }

// Planned returns the mutating commands that were printed rather than run.
func (d *DryRun) Planned() []Command { return d.planned }

// excerpt bounds tool stderr so an error stays readable, keeping the tail —
// where the actual diagnostic usually is.
func excerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= stderrExcerptLimit {
		return s
	}
	return "…(truncated)…\n" + s[len(s)-stderrExcerptLimit:]
}

func isNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}

// shellQuote quotes a value for display only. It is never used to build a
// command for execution — this package always uses an explicit argument vector.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' || r == ',' || r == '+' || r == '@' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
