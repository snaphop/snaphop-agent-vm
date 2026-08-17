package hostexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRun_ReturnsToolErrorCarryingArgvExitStatusAndStderr(t *testing.T) {
	runner := New(quietLogger())

	_, err := runner.Run(context.Background(), Command{
		Name:   "false",
		Effect: Read,
	})

	var terr *ToolError
	if !errors.As(err, &terr) {
		t.Fatalf("got %v, want *ToolError", err)
	}
	if terr.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", terr.ExitCode)
	}
	if !strings.Contains(terr.Error(), "false exited 1") {
		t.Errorf("error message %q does not name the tool and its exit status", terr.Error())
	}
}

func TestRun_MissingToolIsANotFoundError(t *testing.T) {
	runner := New(quietLogger())

	_, err := runner.Run(context.Background(), Command{Name: "agent-vm-no-such-tool", Effect: Read})

	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("got %v, want *NotFoundError", err)
	}
}

func TestRun_CapturesStdoutAndStderrSeparately(t *testing.T) {
	runner := New(quietLogger())

	// `env` needs no shell and writes only to stdout, which is the point: this
	// package never builds a command string for a shell.
	res, err := runner.Run(context.Background(), Command{
		Name:   "printf",
		Args:   []string{"hello"},
		Effect: Read,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "hello" {
		t.Errorf("Stdout = %q, want %q", res.Stdout, "hello")
	}
	if len(res.Stderr) != 0 {
		t.Errorf("Stderr = %q, want empty", res.Stderr)
	}
}

func TestRun_TimeoutIsReportedAsATimeoutError(t *testing.T) {
	runner := New(quietLogger())

	_, err := runner.Run(context.Background(), Command{
		Name:    "sleep",
		Args:    []string{"5"},
		Effect:  Read,
		Timeout: 50 * time.Millisecond,
	})

	var terr *TimeoutError
	if !errors.As(err, &terr) {
		t.Fatalf("got %v, want *TimeoutError", err)
	}
}

func TestDryRun_PrintsMutatingCommandsAndRunsReadOnlyOnes(t *testing.T) {
	inner := NewFake().Respond("virsh net-list --name", FakeResponse{Stdout: "agent-vm-nat\n"})
	var out bytes.Buffer
	dry := NewDryRun(inner, &out)
	ctx := context.Background()

	read, err := dry.Run(ctx, Command{Name: "virsh", Args: []string{"net-list", "--name"}, Effect: Read})
	if err != nil {
		t.Fatalf("read command: %v", err)
	}
	if strings.TrimSpace(string(read.Stdout)) != "agent-vm-nat" {
		t.Errorf("read command did not reach the host: stdout = %q", read.Stdout)
	}

	if _, err := dry.Run(ctx, Command{
		Name:   "virt-install",
		Args:   []string{"--import", "--name", "agent-01"},
		Effect: Mutate,
	}); err != nil {
		t.Fatalf("mutating command: %v", err)
	}

	if inner.Ran("virt-install --import --name agent-01") {
		t.Errorf("--dry-run executed a mutating command\n%s", inner)
	}
	if got := out.String(); !strings.Contains(got, "virt-install --import --name agent-01") {
		t.Errorf("--dry-run output %q does not contain the planned invocation", got)
	}
	if len(dry.Planned()) != 1 {
		t.Errorf("Planned() has %d commands, want 1", len(dry.Planned()))
	}
}

func TestCommandString_QuotesArgumentsForDisplay(t *testing.T) {
	c := Command{
		Name: "virt-install",
		Args: []string{"--boot", "kernel=/var/lib/vmlinuz,kernel_args=root=/dev/vda1 console=ttyS0"},
	}
	want := `virt-install --boot 'kernel=/var/lib/vmlinuz,kernel_args=root=/dev/vda1 console=ttyS0'`
	if got := c.String(); got != want {
		t.Errorf("String() = %s, want %s", got, want)
	}
}

func TestToolError_StderrExcerptIsBounded(t *testing.T) {
	// libguestfs can emit megabytes of appliance output; an error is not a log.
	huge := strings.Repeat("appliance noise\n", 2000)
	got := excerpt([]byte(huge + "libguestfs: error: could not create appliance"))

	if len(got) > stderrExcerptLimit+len("…(truncated)…\n") {
		t.Errorf("excerpt is %d bytes, want at most %d", len(got), stderrExcerptLimit)
	}
	if !strings.Contains(got, "could not create appliance") {
		t.Error("excerpt dropped the tail, which is where the diagnostic is")
	}
}
