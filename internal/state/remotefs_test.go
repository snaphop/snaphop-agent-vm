package state

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

func remoteStore(t *testing.T, fake *hostexec.Fake) *Store {
	t.Helper()
	fake.Hypervisor = "kvm@hv.example.com"
	// Containment resolves the state directory and the path through the
	// hypervisor's readlink, so a fake has to answer for both.
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		switch c.Name {
		case "readlink":
			return hostexec.FakeResponse{Stdout: c.Args[len(c.Args)-1] + "\n"}, true
		case "mktemp":
			// -p <dir> <template>: mktemp fills in the X's.
			return hostexec.FakeResponse{Stdout: filepath.Join(c.Args[1], strings.Replace(c.Args[2], "XXXXXX", "q3ZtLk", 1)) + "\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}
	store, err := OpenOn(Remote(fake), "/srv/agent-vm", false)
	if err != nil {
		t.Fatalf("OpenOn: %v", err)
	}
	return store
}

// A remote state directory is written the same way a local one is: beside the
// destination, then moved onto it, so a dropped connection leaves a temporary
// file rather than a truncated record.
func TestRemoteFS_WriteFileIsAtomic(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)

	if err := store.WriteFile("/srv/agent-vm/vms/web/vm.json", []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// mktemp creates the temporary file 0600 under a unique name before
	// anything is written to it; only the finished file gets its final mode.
	want := []string{
		"mkdir -p -- /srv/agent-vm/vms/web",
		"mktemp -p /srv/agent-vm/vms/web .vm.json.XXXXXX",
		"dd status=none of=/srv/agent-vm/vms/web/.vm.json.q3ZtLk",
		"chmod 644 -- /srv/agent-vm/vms/web/.vm.json.q3ZtLk",
		"mv -fT -- /srv/agent-vm/vms/web/.vm.json.q3ZtLk /srv/agent-vm/vms/web/vm.json",
	}
	var writes []string
	for _, argv := range fake.Argvs() {
		for _, w := range want {
			if argv == w {
				writes = append(writes, argv)
			}
		}
	}
	if strings.Join(writes, "\n") != strings.Join(want, "\n") {
		t.Errorf("writes = \n%s\nwant, in order:\n%s", strings.Join(writes, "\n"), strings.Join(want, "\n"))
	}
}

func TestRemoteFS_WriteFileRemovesItsTemporaryFileOnFailure(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	fake.RespondPrefix("dd status=none", hostexec.FakeResponse{ExitCode: 1, Stderr: "dd: error writing: No space left on device"})

	if err := store.WriteFile("/srv/agent-vm/vms/web/vm.json", []byte("{}\n"), 0o644); err == nil {
		t.Fatal("WriteFile reported success after dd failed")
	}
	if !fake.Ran("rm -f -- /srv/agent-vm/vms/web/.vm.json.q3ZtLk") {
		t.Errorf("the temporary file was left behind:\n%s", fake)
	}
	if fake.Ran("mv -fT -- /srv/agent-vm/vms/web/.vm.json.q3ZtLk /srv/agent-vm/vms/web/vm.json") {
		t.Error("a partly written file was moved onto the record")
	}
}

func TestRemoteFS_WriteFileRefusesATemporaryFileOutsideItsDirectory(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	base := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == "mktemp" {
			return hostexec.FakeResponse{Stdout: "/tmp/.vm.json.q3ZtLk\n"}, true
		}
		return base(c)
	}

	if err := store.WriteFile("/srv/agent-vm/vms/web/vm.json", []byte("{}\n"), 0o644); err == nil {
		t.Fatal("WriteFile wrote through a temporary file outside the destination's directory")
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "dd ") {
			t.Errorf("wrote to an unexpected temporary file: %s", argv)
		}
	}
}

func TestRemoteFS_ReadFileReportsAMissingFileAsNotExist(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	base := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if r, ok := base(c); ok {
			return r, true
		}
		switch c.Name {
		case "cat":
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "cat: no such file"}, true
		case "test":
			// `test -e` exits 1 for a path that is not there.
			return hostexec.FakeResponse{ExitCode: 1}, true
		}
		return hostexec.FakeResponse{}, false
	}

	_, err := store.ReadFile("/srv/agent-vm/vms/web/vm.json")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile = %v, want a not-exist error", err)
	}
}

// An unreadable file and a missing one are different problems, and the caller
// treats a not-exist error as "no such VM".
func TestRemoteFS_UnreadableFileIsNotReportedAsMissing(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	base := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if r, ok := base(c); ok {
			return r, true
		}
		if c.Name == "cat" {
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "cat: permission denied"}, true
		}
		// `test -e` succeeds: the file is there, we just cannot read it.
		return hostexec.FakeResponse{}, false
	}

	_, err := store.ReadFile("/srv/agent-vm/vms/web/vm.json")
	if err == nil {
		t.Fatal("ReadFile = nil error, want a failure")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile = %v, want it not reported as a missing file", err)
	}
}

// Containment is enforced on the hypervisor's filesystem exactly as it is here:
// a symlink planted inside the state directory cannot be used to reach out of
// it (SECURITY.md).
func TestRemoteFS_RefusesToTouchAnythingOutsideTheStateDirectory(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	fake.Hypervisor = "hv"
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name != "readlink" {
			return hostexec.FakeResponse{}, false
		}
		path := c.Args[len(c.Args)-1]
		if strings.HasPrefix(path, "/srv/agent-vm/vms/escape") {
			// The hypervisor resolves this one outside the state directory.
			return hostexec.FakeResponse{Stdout: "/etc\n"}, true
		}
		return hostexec.FakeResponse{Stdout: path + "\n"}, true
	}
	store, err := OpenOn(Remote(fake), "/srv/agent-vm", false)
	if err != nil {
		t.Fatalf("OpenOn: %v", err)
	}

	err = store.Remove("/srv/agent-vm/vms/escape")

	var containment *ContainmentError
	if !errors.As(err, &containment) {
		t.Fatalf("Remove = %v, want a *ContainmentError", err)
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "rm ") {
			t.Errorf("a removal ran anyway: %s", argv)
		}
	}
}

func TestRemoteFS_SubdirectoriesListsVMs(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	base := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if r, ok := base(c); ok {
			return r, true
		}
		if c.Name == "find" {
			return hostexec.FakeResponse{Stdout: "/srv/agent-vm/vms/web\n/srv/agent-vm/vms/api\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	names, err := store.FS().Subdirectories("/srv/agent-vm/vms")
	if err != nil {
		t.Fatalf("Subdirectories: %v", err)
	}
	// Sorted, because these are listed for people to read and find promises no
	// order of its own.
	if len(names) != 2 || names[0] != "api" || names[1] != "web" {
		t.Errorf("Subdirectories = %v, want [api web]", names)
	}
}

func TestRemoteFS_SubdirectoriesOfAMissingDirectoryIsEmpty(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	base := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if r, ok := base(c); ok {
			return r, true
		}
		switch c.Name {
		case "find":
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "find: no such directory"}, true
		case "test":
			return hostexec.FakeResponse{ExitCode: 1}, true
		}
		return hostexec.FakeResponse{}, false
	}

	names, err := store.FS().Subdirectories("/srv/agent-vm/vms")
	if err != nil {
		t.Fatalf("Subdirectories = %v, want no error for a directory that is not there yet", err)
	}
	if len(names) != 0 {
		t.Errorf("Subdirectories = %v, want empty", names)
	}
}

// The lock is flock on the hypervisor, held by a process that blocks on its
// standard input, so it is released when this process dies just as a local one
// would be.
func TestRemoteFS_TryLockHoldsAProcessOnTheHypervisor(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	fake.Default = hostexec.FakeResponse{Stdout: lockReadyToken}
	store := remoteStore(t, fake)

	lock, err := store.TryLockVM("web", "create")
	if err != nil {
		t.Fatalf("TryLockVM: %v", err)
	}

	started := fake.Started()
	if len(started) != 1 {
		t.Fatalf("started %d processes, want 1: %s", len(started), fake)
	}
	if got := strings.Join(started[0].Argv(), " "); got != "flock --exclusive --nonblock /srv/agent-vm/locks/vm-web.lock cat" {
		t.Errorf("held process = %q", got)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// A lock another process holds is an ordinary outcome that has to name the
// holder, not a transport failure.
func TestRemoteFS_TryLockReportsTheHolderWhenBusy(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	base := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if r, ok := base(c); ok {
			return r, true
		}
		switch c.Name {
		case "flock":
			// The handshake gets nothing back, and the probe exits 1.
			return hostexec.FakeResponse{ExitCode: 1}, true
		case "head":
			return hostexec.FakeResponse{Stdout: "host=laptop pid=4242 operation=create\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	_, err := store.TryLockVM("web", "create")

	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("TryLockVM = %v, want a *BusyError", err)
	}
	if !strings.Contains(busy.Error(), "pid 4242 on laptop") {
		t.Errorf("BusyError does not name the holder: %s", busy)
	}
	if !strings.Contains(busy.Error(), "VM web") {
		t.Errorf("BusyError does not name the resource: %s", busy)
	}
}

func TestRemoteFS_TryLockRetriesALockReleasedWhileItAskedWhy(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	base := fake.MatchFunc
	var attempts atomic.Int32
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if r, ok := base(c); ok {
			return r, true
		}
		if c.Name != "flock" {
			return hostexec.FakeResponse{}, false
		}
		if c.Args[len(c.Args)-1] == "true" {
			// The probe finds the lock free: its holder has just let go.
			return hostexec.FakeResponse{}, true
		}
		if attempts.Add(1) == 1 {
			// The first attempt met the holder, so flock exited at once.
			return hostexec.FakeResponse{}, true
		}
		return hostexec.FakeResponse{Stdout: lockReadyToken}, true
	}

	lock, err := store.TryLockVM("web", "create")
	if err != nil {
		t.Fatalf("TryLockVM = %v, want the lock its holder released", err)
	}
	_ = lock.Release()
	if attempts.Load() != 2 {
		t.Errorf("flock was attempted %d times, want 2", attempts.Load())
	}
}

func TestRemoteFS_TryLockReportsALockThatKeepsFailingWhileFree(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	store := remoteStore(t, fake)
	base := fake.MatchFunc
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if r, ok := base(c); ok {
			return r, true
		}
		if c.Name == "flock" {
			// Every attempt fails, and the lock is free each time: this is
			// not contention, and must not be retried as if it were.
			return hostexec.FakeResponse{}, true
		}
		return hostexec.FakeResponse{}, false
	}

	_, err := store.TryLockVM("web", "create")
	var busy *BusyError
	if err == nil || errors.As(err, &busy) {
		t.Fatalf("TryLockVM = %v, want the failure reported", err)
	}
	if n := len(fake.Started()); n != 2 {
		t.Errorf("started %d lock processes, want 2", n)
	}
}

func TestParseDFAvailable(t *testing.T) {
	t.Parallel()
	out := "Filesystem     1B-blocks         Used    Available Capacity Mounted on\n" +
		"/dev/mapper/vg-root 494384795648 120795955200 348362203136      26% /\n"

	got, err := parseDFAvailable(out)
	if err != nil {
		t.Fatalf("parseDFAvailable: %v", err)
	}
	if got != 348362203136 {
		t.Errorf("parseDFAvailable = %d, want 348362203136", got)
	}
}

// A device name long enough to wrap is the one shape POSIX df output allows
// that a naive column read would get wrong.
func TestParseDFAvailable_WrappedDeviceName(t *testing.T) {
	t.Parallel()
	out := "Filesystem 1B-blocks Used Available Capacity Mounted on\n" +
		"/dev/disk/by-uuid/6f1c9d3a-0b2e-4c77-9a71-2f0b6d5c8e14\n" +
		"                 494384795648 120795955200 348362203136 26% /srv\n"

	got, err := parseDFAvailable(out)
	if err != nil {
		t.Fatalf("parseDFAvailable: %v", err)
	}
	if got != 348362203136 {
		t.Errorf("parseDFAvailable = %d, want 348362203136", got)
	}
}

func TestParseDFAvailable_RejectsUnreadableOutput(t *testing.T) {
	t.Parallel()
	for _, out := range []string{"", "Filesystem 1B-blocks Used Available Capacity Mounted on\n", "nonsense\nalso nonsense\n"} {
		if _, err := parseDFAvailable(out); err == nil {
			t.Errorf("parseDFAvailable(%q) = nil error, want a parse failure", out)
		}
	}
}

func TestDescribeHolder(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"host=laptop pid=4242 operation=create": "pid 4242 on laptop, operation=create",
		"pid=7 operation=image build":           "pid 7, operation=image",
		"garbage":                               "garbage",
	} {
		if got := DescribeHolder(in); got != want {
			t.Errorf("DescribeHolder(%q) = %q, want %q", in, got, want)
		}
	}
}
