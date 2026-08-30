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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
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

// Location says which machine a command runs on. The zero value is the
// hypervisor host, because that is where all but a handful of this tool's
// invocations belong: qemu-img writes a disk the hypervisor must open, podman
// and libguestfs build an image the hypervisor must boot, and virt-install
// hands libvirt paths only the hypervisor can resolve. When the libvirt URI is
// local those two machines are the same one and this changes nothing
// (ADR-0010).
type Location int

const (
	// Hypervisor is the machine libvirt and QEMU run on.
	Hypervisor Location = iota
	// Client is the machine the operator typed the command on. It is for the
	// few invocations that belong to the operator rather than to the
	// hypervisor: `gh`, which uses their GitHub login, and the `ssh` into a
	// guest, which uses their keys and their terminal.
	Client
)

// Command is a single tool invocation. Name is the tool as it appears on PATH;
// it is also the name used in errors and logs.
type Command struct {
	Name   string
	Args   []string
	Effect Effect
	// Location selects the machine this runs on. The zero value, Hypervisor,
	// is correct for everything that touches a disk, an image, or a domain.
	Location Location
	Stdin    io.Reader
	Dir      string
	Timeout  time.Duration

	// Output, when set, receives stdout and stderr as they are produced, in
	// addition to the buffers Result carries. It exists for the invocations
	// whose value is in watching them run: a package upgrade inside a guest
	// takes minutes and reports what it is doing the whole time, and holding
	// that until the command exits would leave the operator staring at
	// nothing.
	Output io.Writer

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
	// LookPath resolves a tool on the PATH of the machine loc names, returning
	// *NotFoundError if it is absent there.
	LookPath(name string, loc Location) (string, error)
	// Become replaces this process with the command, so the operator's terminal
	// talks to it directly. It returns only on failure to start.
	Become(c Command) error
	// Start runs a command without waiting for it, handing back its standard
	// input and output. It exists for the one thing Run cannot express: a
	// process that must stay alive while the caller does something else.
	Start(ctx context.Context, c Command) (*Process, error)
	// HypervisorHost names the machine host tools run on, or "" when that is
	// this machine. It is reported to the operator, never used to build a
	// command.
	HypervisorHost() string
	// Render is the command as it would actually be run, transport included.
	// It is what --dry-run prints for a plan assembled ahead of time rather
	// than executed, so that the printed plan and the real run cannot differ.
	Render(c Command) string
}

// Process is a command that has been started but not waited for. It exists for
// the advisory lock on the state directory: a file lock lives only as long as
// some process holds the file open, so taking one on another machine means
// keeping a process alive there and closing it to release.
type Process struct {
	// Stdin is the process's standard input. Closing it is how a remote
	// command that blocks on a read is told to exit.
	Stdin io.WriteCloser
	// Stdout is the process's standard output, buffered so a caller can read
	// one line and leave the rest.
	Stdout *bufio.Reader

	stop func() error
}

// Close stops the process and waits for it, so that whatever it held — a lock,
// an ssh connection — is released before this returns.
func (p *Process) Close() error {
	if p == nil || p.stop == nil {
		return nil
	}
	stop := p.stop
	p.stop = nil
	return stop()
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
	if c.Output != nil {
		// os/exec fills these from two goroutines, so the shared writer is
		// serialized rather than handed to both.
		live := &syncWriter{w: c.Output}
		cmd.Stdout = io.MultiWriter(&stdout, live)
		cmd.Stderr = io.MultiWriter(&stderr, live)
	}

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

// Become replaces this process with c via execve.
//
// An interactive session — `ssh` into a guest, `virsh console` on one — is not
// something to supervise: the operator's terminal, signals, window size, and
// exit status all belong to the tool being run. Replacing this process hands
// all of that over at once, and is why `agent-vm ssh` behaves exactly like the
// `ssh` it prints under --dry-run.
func (e *Exec) Become(c Command) error {
	path, err := e.LookPath(c.Name, c.Location)
	if err != nil {
		return err
	}
	e.Logger.Debug("replacing this process", "tool", c.Name, "argv", c.Argv())

	// On success this never returns: the process image is gone.
	if err := syscall.Exec(path, c.Argv(), os.Environ()); err != nil {
		return fmt.Errorf("running %s: %w", c.Name, err)
	}
	return nil
}

// Start runs a command in the background, wired to pipes rather than to this
// process's own streams.
func (e *Exec) Start(ctx context.Context, c Command) (*Process, error) {
	if c.Name == "" {
		return nil, errors.New("hostexec: command has no tool name")
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", c.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", c.Name, err)
	}
	// stderr is collected rather than discarded so that a process which dies on
	// startup can say why when Close reports the failure.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	e.Logger.Debug("started host tool", "tool", c.Name, "argv", c.Argv())
	if err := cmd.Start(); err != nil {
		if isNotFound(err) {
			return nil, &NotFoundError{Tool: c.Name}
		}
		return nil, fmt.Errorf("running %s: %w", c.Name, err)
	}

	return &Process{
		Stdin:  stdin,
		Stdout: bufio.NewReader(stdout),
		stop: func() error {
			// Closing stdin is the graceful stop: a command blocked on a read
			// sees end-of-file and exits, releasing what it held. The kill is
			// the fallback for one that does not.
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil
		},
	}, nil
}

// HypervisorHost is empty: this runner runs everything on this machine.
func (e *Exec) HypervisorHost() string { return "" }

// Render is the command itself: nothing wraps it here.
func (e *Exec) Render(c Command) string { return c.String() }

// LookPath resolves a tool on PATH.
func (e *Exec) LookPath(name string, _ Location) (string, error) {
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
		_, _ = fmt.Fprintln(d.Out, c.String())
	}
	return &Result{Stdout: []byte(c.DryRunStdout), Skipped: true}, nil
}

// LookPath delegates; resolving a tool changes nothing.
func (d *DryRun) LookPath(name string, loc Location) (string, error) {
	return d.Inner.LookPath(name, loc)
}

// Become prints the command instead of becoming it. This is the documented way
// to get the exact `ssh` or `virsh console` invocation for a VM and run it
// yourself (docs/cli.md).
func (d *DryRun) Become(c Command) error {
	d.planned = append(d.planned, c)
	if d.Out != nil {
		_, _ = fmt.Fprintln(d.Out, c.String())
	}
	return nil
}

// Start delegates a read-only command and, for a mutating one, hands back a
// process that does nothing. The mutating case is the state directory lock: a
// dry run must not take one, and must not fail for want of it either.
func (d *DryRun) Start(ctx context.Context, c Command) (*Process, error) {
	if c.Effect == Read {
		return d.Inner.Start(ctx, c)
	}
	d.planned = append(d.planned, c)
	if d.Out != nil {
		_, _ = fmt.Fprintln(d.Out, c.String())
	}
	return &Process{
		Stdin:  nopWriteCloser{io.Discard},
		Stdout: bufio.NewReader(strings.NewReader(c.DryRunStdout)),
	}, nil
}

// HypervisorHost reports what the wrapped runner would use.
func (d *DryRun) HypervisorHost() string { return d.Inner.HypervisorHost() }

// Render defers to the wrapped runner, which is what would have run it.
func (d *DryRun) Render(c Command) string { return d.Inner.Render(c) }

// Planned returns the mutating commands that were printed rather than run.
func (d *DryRun) Planned() []Command { return d.planned }

// nopWriteCloser adds a no-op Close, for a process that has no real stdin.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// syncWriter serializes writes from the stdout and stderr copiers onto one
// destination.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

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
	if strings.IndexFunc(s, func(r rune) bool { return !isUnquotedRune(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isUnquotedRune reports whether a rune can appear in displayed argv without
// quoting — the characters a shell would pass through untouched.
func isUnquotedRune(r rune) bool {
	return r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' || r == ',' || r == '+' || r == '@' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
