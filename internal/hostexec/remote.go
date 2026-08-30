package hostexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Remote runs host tools on another machine over ssh.
//
// It exists because a libvirt URI does more than name a connection: when it
// names another machine, the disks, base images, kernels, and state directory
// this tool manages all live there, and the tools that produce them have to run
// there too (ADR-0010). virt-install and virsh could speak to a remote libvirt
// by themselves, but they would then be handed paths on the wrong machine, so
// they are run on the hypervisor with a local URI instead — the arrangement
// every one of those tools is best tested in.
//
// Only the invocations that belong to the operator stay here: `gh`, which uses
// their GitHub login, and the `ssh` into a guest, which uses their keys and
// their terminal.
type Remote struct {
	// Local runs commands on this machine, and is what a Client-located
	// command and the ssh wrapper itself are handed to.
	Local Runner
	// Destination is the [user@]host ssh connects to.
	Destination string
	// Port is the ssh port, or 0 for the default.
	Port int
	// IdentityFile is an optional private key to offer. It is named to ssh and
	// never read here.
	IdentityFile string
	// NoVerify skips host key checking, mirroring the libvirt URI parameter of
	// the same name. It is off unless the operator asked for it.
	NoVerify bool
	Logger   *slog.Logger

	// controlPath is the ssh connection-sharing socket for this run. One
	// create makes a dozen invocations and an image build many more, and a
	// fresh TCP connection and key exchange for each would dominate the time.
	controlPath string
}

// TransportError is a failure to reach the hypervisor at all, as opposed to a
// tool on it failing. The two need different remedies — one is an ssh problem,
// the other is a host problem — so they are different errors.
type TransportError struct {
	Destination string
	Stderr      string
}

func (e *TransportError) Error() string {
	msg := fmt.Sprintf("cannot reach the hypervisor host %s over ssh", e.Destination)
	if e.Stderr != "" {
		msg += "\n  ssh: " + indent(e.Stderr)
	}
	msg += "\n  agent-vm runs host tools on the hypervisor, so `ssh " + e.Destination +
		" true` must succeed without a prompt: use an SSH agent or a key ssh offers by default,\n" +
		"  or name one with ?keyfile=<path> in the libvirt URI."
	return msg
}

// NewRemote returns a Runner that runs hypervisor-located commands on
// destination over ssh. dir is where the connection-sharing socket is placed;
// an empty dir disables sharing, which only costs time.
func NewRemote(local Runner, logger *slog.Logger, destination, dir string) *Remote {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Remote{Local: local, Destination: destination, Logger: logger}
	if dir != "" {
		// The socket path is kept short: a unix socket path is limited to
		// around 100 bytes and ssh fails outright, rather than falling back,
		// when the ControlPath does not fit.
		r.controlPath = filepath.Join(dir, "ssh")
	}
	return r
}

// remoteExitNotFound is the exit status a shell reports for a command it could
// not find. ssh passes the remote exit status through, so this is how a missing
// tool on the hypervisor reaches us.
const remoteExitNotFound = 127

// sshTransportExit is ssh's own failure status. It overlaps with a tool that
// genuinely exits 255, which is why the stderr is consulted too.
const sshTransportExit = 255

// HypervisorHost is the machine host tools run on.
func (r *Remote) HypervisorHost() string { return r.Destination }

// Render shows the ssh invocation a hypervisor-located command becomes, so
// that what --dry-run prints is what would run.
func (r *Remote) Render(c Command) string {
	if c.Location == Client {
		return r.Local.Render(c)
	}
	return r.wrap(c).String()
}

// Run executes c on the hypervisor, or here when c is Client-located.
func (r *Remote) Run(ctx context.Context, c Command) (*Result, error) {
	if c.Location == Client {
		return r.Local.Run(ctx, c)
	}
	res, err := r.Local.Run(ctx, r.wrap(c))
	return res, r.translate(c, res, err)
}

// Start runs c on the hypervisor without waiting for it.
func (r *Remote) Start(ctx context.Context, c Command) (*Process, error) {
	if c.Location == Client {
		return r.Local.Start(ctx, c)
	}
	return r.Local.Start(ctx, r.wrap(c))
}

// Become replaces this process with c. A hypervisor-located command becomes an
// ssh session running it there, with a terminal allocated so that `virsh
// console` behaves the way it does locally.
//
// Connection sharing is deliberately off for it. Becoming a command is the last
// thing this process does, so there are no further invocations to amortize a
// shared connection over — and the caller removes the socket's directory before
// this, because a process that is about to be replaced never gets to clean up
// after itself.
func (r *Remote) Become(c Command) error {
	if c.Location == Client {
		return r.Local.Become(c)
	}
	wrapped := r.sshCommand(c, remoteCommand(c, true), false)
	// -t forces a terminal even though ssh's own stdin is one, because the
	// remote command is given as an argument and ssh does not allocate one for
	// those by default.
	wrapped.Args = append([]string{"-t"}, wrapped.Args...)
	return r.Local.Become(wrapped)
}

// LookPath resolves a tool on the PATH of the machine loc names. For the
// hypervisor that is asked with `command -v`, the shell builtin every POSIX
// shell has: ssh runs the remote argument through a shell regardless, so
// nothing extra needs to be installed there to answer this.
func (r *Remote) LookPath(name string, loc Location) (string, error) {
	if loc == Client {
		return r.Local.LookPath(name, loc)
	}
	probe := Command{Name: "command", Args: []string{"-v", name}, Effect: Read}
	// `command` is a shell builtin with no binary behind it, so this is the
	// one invocation that must not be exec'd — exec would look for a program
	// named "command" and report that it does not exist, for every tool.
	res, err := r.Local.Run(context.Background(), r.sshCommand(probe, remoteCommand(probe, false), true))
	if err != nil {
		var transport *TransportError
		if errors.As(r.translate(Command{Name: name}, res, err), &transport) {
			return "", transport
		}
		return "", &NotFoundError{Tool: name}
	}
	path := strings.TrimSpace(string(res.Stdout))
	if path == "" {
		return "", &NotFoundError{Tool: name}
	}
	return path, nil
}

// wrap turns a tool invocation into the ssh invocation that runs it on the
// hypervisor. The tool's own argument vector is quoted for the remote shell —
// ssh has no way to pass an argument vector through untouched, so this is the
// one place in the project where a command becomes a string, and it is why the
// quoting below is exhaustive rather than best-effort.
func (r *Remote) wrap(c Command) Command {
	return r.sshCommand(c, remoteCommand(c, true), true)
}

// sshCommand builds the ssh invocation carrying an already-rendered remote
// command line. share asks for the run's shared connection, which every
// invocation wants except the one this process is replaced by.
func (r *Remote) sshCommand(c Command, remote string, share bool) Command {
	args := []string{
		// A prompt would be invisible: this tool captures ssh's streams. An
		// immediate, explained refusal beats a command that appears to hang.
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
	}
	if share && r.controlPath != "" {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+r.controlPath,
			// Long enough to cover a whole image build's worth of
			// invocations, short enough that the connection is gone soon
			// after the command an operator ran.
			"-o", "ControlPersist=120",
		)
	}
	if r.NoVerify {
		args = append(args, "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null")
	}
	if r.Port != 0 {
		args = append(args, "-p", strconv.Itoa(r.Port))
	}
	if r.IdentityFile != "" {
		args = append(args, "-i", r.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, "--", r.Destination, remote)

	wrapped := c
	wrapped.Name = SSH.Name
	wrapped.Args = args
	// Dir is expressed in the remote command rather than here: it names a
	// directory on the hypervisor, which this machine may not even have.
	wrapped.Dir = ""
	return wrapped
}

// remoteCommand renders a command as a string the hypervisor's shell will
// re-split into exactly the argument vector we started with.
//
// withExec asks for the shell to be replaced by the tool, so that the tool is
// what ssh reports the exit status and the signal disposition of. It is right
// for every real program and wrong for the one shell builtin this package
// invokes, which has no binary to exec.
func remoteCommand(c Command, withExec bool) string {
	var b strings.Builder
	if c.Dir != "" {
		b.WriteString("cd ")
		b.WriteString(remoteQuote(c.Dir))
		b.WriteString(" && ")
	}
	if withExec {
		b.WriteString("exec ")
	}
	for i, arg := range c.Argv() {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(remoteQuote(arg))
	}
	return b.String()
}

// remoteQuote makes one argument survive the hypervisor's shell unchanged.
//
// Unlike shellQuote, which formats an argv for a human to read, the result of
// this is executed, so it quotes conservatively: anything that is not plainly
// safe is wrapped in single quotes, inside which a shell interprets nothing at
// all, and an embedded single quote is spliced with the '\” idiom.
func remoteQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool { return !isRemoteSafeRune(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isRemoteSafeRune reports whether a rune needs no quoting in any POSIX shell.
// The set is deliberately narrower than shellQuote's: "~" and ":" are listed
// there for readability, and "~" is expanded by a shell.
func isRemoteSafeRune(r rune) bool {
	switch r {
	case '-', '_', '.', '/', ',', '+', '=', ':', '@', '%':
		return true
	}
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// translate turns a failure of the ssh invocation into one that names the tool
// the operator actually asked for. Without it every remote failure would be
// reported as "ssh exited 1", which says nothing about what went wrong.
func (r *Remote) translate(c Command, res *Result, err error) error {
	if err == nil {
		return nil
	}

	var toolErr *ToolError
	if errors.As(err, &toolErr) {
		stderr := toolErr.Stderr
		switch {
		case toolErr.ExitCode == sshTransportExit && looksLikeTransportFailure(stderr):
			return &TransportError{Destination: r.Destination, Stderr: stderr}
		case toolErr.ExitCode == remoteExitNotFound:
			return &NotFoundError{Tool: c.Name}
		}
		return &ToolError{
			Tool:     c.Name,
			Argv:     c.Argv(),
			Host:     r.Destination,
			ExitCode: toolErr.ExitCode,
			Stderr:   stderr,
			Duration: toolErr.Duration,
		}
	}

	var timeout *TimeoutError
	if errors.As(err, &timeout) {
		return &TimeoutError{Tool: c.Name, Argv: c.Argv(), Host: r.Destination, Timeout: timeout.Timeout}
	}

	var missing *NotFoundError
	if errors.As(err, &missing) && missing.Tool == SSH.Name {
		// ssh itself is not installed here. That is a client-side problem and
		// must not be reported as a missing tool on the hypervisor.
		return missing
	}
	return err
}

// looksLikeTransportFailure distinguishes ssh failing to connect from a remote
// tool that happens to exit 255. ssh names itself in these messages; a remote
// tool's stderr does not.
func looksLikeTransportFailure(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, marker := range []string{
		"permission denied",
		"connection refused",
		"connection closed",
		"connection timed out",
		"could not resolve hostname",
		"no route to host",
		"host key verification failed",
		"operation timed out",
		"network is unreachable",
		"remote host identification has changed",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	// A connection that never opened produces no remote output at all, which
	// is the shape of a silent BatchMode refusal.
	return strings.TrimSpace(stderr) == ""
}

// ControlDir creates the directory the ssh connection-sharing socket lives in.
// It is a caller's job to remove it when the run ends; the socket inside it is
// ssh's to manage.
func ControlDir() (string, error) {
	dir, err := os.MkdirTemp("", "agent-vm-ssh-")
	if err != nil {
		return "", fmt.Errorf("creating a directory for the ssh control socket: %w", err)
	}
	return dir, nil
}
