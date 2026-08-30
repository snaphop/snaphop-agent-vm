//go:build integration

package integration

import (
	"context"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

// remoteDestination is the ssh destination the remote-transport tests drive.
// It defaults to this machine, which exercises every command for real against a
// real sshd without needing a second host:
//
//	go test -tags integration ./test/integration/... -run Remote
//	go test -tags integration ./test/integration/... -run Remote -remote-ssh=kvm@hypervisor.lan
//
// Nothing here defines a domain or writes a disk image. It covers the layer
// underneath: the ssh transport in internal/hostexec, and the state directory
// operations internal/state performs through it — which are the parts made of
// remote command lines that no unit test can prove are spelled correctly.
var remoteDestination = flag.String("remote-ssh", "localhost",
	"ssh destination for the remote-hypervisor transport tests; empty skips them")

func remoteRunner(t *testing.T) hostexec.Runner {
	t.Helper()
	if *remoteDestination == "" {
		t.Skip("-remote-ssh is empty; skipping the remote transport tests")
	}
	requireTools(t, "ssh")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	dir := t.TempDir()
	runner := hostexec.NewRemote(hostexec.New(logger), logger, *remoteDestination, dir)

	// A destination that needs a password, or a host key that is not accepted
	// yet, is a setup problem rather than a failure of this code.
	if _, err := runner.Run(context.Background(), hostexec.Command{Name: "true", Effect: hostexec.Read}); err != nil {
		t.Skipf("cannot run a command on %s over ssh (%v); skipping", *remoteDestination, err)
	}
	return runner
}

// remoteStore opens a state directory through the ssh transport. The path is a
// local temporary directory, which is the same path on the far side when the
// destination is this machine; with a real remote it is created there.
func remoteStore(t *testing.T, runner hostexec.Runner) *state.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent-vm-test-state")
	store, err := state.OpenOn(state.Remote(runner), dir, true)
	if err != nil {
		t.Fatalf("opening the remote state directory: %v", err)
	}
	return store
}

// The remote command is re-split by a real shell on the far side, so an
// argument holding spaces, quotes, and characters a shell would otherwise
// expand has to arrive exactly as it left.
func TestRemote_ArgumentsSurviveTheRemoteShell(t *testing.T) {
	runner := remoteRunner(t)

	for _, arg := range []string{
		"plain",
		"kernel_args=root=/dev/vda1 console=ttyS0 rw memhp_default_state=online_movable",
		"$HOME",
		"`id`",
		"a;b",
		"it's",
		"a*b?c[d]",
		"~/not-a-home",
		"tab\there",
	} {
		res, err := runner.Run(context.Background(), hostexec.Command{
			Name: "printf", Args: []string{"%s", arg}, Effect: hostexec.Read,
		})
		if err != nil {
			t.Fatalf("printf %q: %v", arg, err)
		}
		if got := string(res.Stdout); got != arg {
			t.Errorf("argument %q arrived as %q", arg, got)
		}
	}
}

// A tool that is not installed on the far side reaches us as a missing tool,
// not as a generic failure, so doctor can name the package to install.
func TestRemote_MissingToolOverTheTransport(t *testing.T) {
	runner := remoteRunner(t)

	_, err := runner.Run(context.Background(), hostexec.Command{
		Name: "agent-vm-no-such-tool", Effect: hostexec.Read,
	})

	var missing *hostexec.NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("Run = %v, want a *NotFoundError", err)
	}

	if _, err := runner.LookPath("agent-vm-no-such-tool", hostexec.Hypervisor); !errors.As(err, &missing) {
		t.Errorf("LookPath = %v, want a *NotFoundError", err)
	}
	if path, err := runner.LookPath("sh", hostexec.Hypervisor); err != nil || !strings.HasPrefix(path, "/") {
		t.Errorf("LookPath(sh) = %q, %v; want an absolute path", path, err)
	}
}

// Every state directory operation is a real command on the far side. This
// exercises the ones a create and a list actually perform.
func TestRemote_StateDirectoryOperations(t *testing.T) {
	runner := remoteRunner(t)
	store := remoteStore(t, runner)

	record := filepath.Join(store.VMDir("agent-vm-test-01"), state.VMRecordFile)
	contents := []byte("{\n  \"schemaVersion\": 1\n}\n")
	if err := store.WriteFile(record, contents, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := store.ReadFile(record)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(contents) {
		t.Errorf("ReadFile = %q, want %q", got, contents)
	}

	if size, err := store.FileSize(record); err != nil || size != int64(len(contents)) {
		t.Errorf("FileSize = %d, %v; want %d", size, err, len(contents))
	}

	// The temporary file the atomic write goes through must not be left behind.
	names, err := store.FS().Subdirectories(filepath.Join(store.Root(), "vms"))
	if err != nil {
		t.Fatalf("Subdirectories: %v", err)
	}
	if len(names) != 1 || names[0] != "agent-vm-test-01" {
		t.Errorf("Subdirectories = %v, want [agent-vm-test-01]", names)
	}

	free, err := store.FreeBytes()
	if err != nil || free == 0 {
		t.Errorf("FreeBytes = %d, %v; want a nonzero size", free, err)
	}
	if usage, err := store.FS().Usage(store.Root()); err != nil || usage < int64(len(contents)) {
		t.Errorf("Usage = %d, %v; want at least %d", usage, err, len(contents))
	}

	if err := store.Remove(store.VMDir("agent-vm-test-01")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := store.ReadFile(record); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile after Remove = %v, want a not-exist error", err)
	}
}

// The lock is flock on the far side, held by a process whose standard input
// this tool keeps open. A second attempt has to be refused while the first is
// held, and succeed once it is released — that is the whole point of taking it
// there rather than here.
func TestRemote_LockIsHeldOnTheHypervisor(t *testing.T) {
	runner := remoteRunner(t)
	requireTools(t, "flock")
	store := remoteStore(t, runner)

	held, err := store.TryLockVM("agent-vm-test-01", "create")
	if err != nil {
		t.Fatalf("TryLockVM: %v", err)
	}

	_, err = store.TryLockVM("agent-vm-test-01", "destroy")
	var busy *state.BusyError
	if !errors.As(err, &busy) {
		_ = held.Release()
		t.Fatalf("second TryLockVM = %v, want a *BusyError", err)
	}
	// The holder is recorded so a contended lock can say who has it.
	if !strings.Contains(busy.Error(), "operation=create") {
		t.Errorf("BusyError does not name the holder's operation: %s", busy)
	}

	// A different VM's lock is independent.
	other, err := store.TryLockVM("agent-vm-test-02", "create")
	if err != nil {
		_ = held.Release()
		t.Fatalf("TryLockVM for another VM: %v", err)
	}
	if err := other.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}

	if err := held.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	regained, err := store.TryLockVM("agent-vm-test-01", "create")
	if err != nil {
		t.Fatalf("TryLockVM after release: %v", err)
	}
	if err := regained.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
}

// Containment is enforced against the far side's filesystem: the symlink is
// resolved there, by that host's readlink, and a path leaving the state
// directory is refused before any command touches it (SECURITY.md).
func TestRemote_ContainmentUsesTheHypervisorsFilesystem(t *testing.T) {
	runner := remoteRunner(t)
	store := remoteStore(t, runner)

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep-me"), []byte("important\n"), 0o600); err != nil {
		t.Fatalf("writing the file to protect: %v", err)
	}
	escape := filepath.Join(store.Root(), "vms", "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatalf("planting the symlink: %v", err)
	}

	err := store.Remove(escape)

	var containment *state.ContainmentError
	if !errors.As(err, &containment) {
		t.Fatalf("Remove = %v, want a *ContainmentError", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep-me")); err != nil {
		t.Errorf("the file outside the state directory was touched: %v", err)
	}
}
