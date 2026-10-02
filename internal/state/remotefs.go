package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// RemoteFS is the state directory on the machine libvirt runs on, reached over
// the same ssh transport every other host tool goes through.
//
// Every operation here is one standard utility doing what it already does
// (ADR-0009): coreutils writes, reads, moves and removes files, findutils
// lists directories, and util-linux's flock provides the same advisory lock
// the local implementation takes with flock(2). Nothing is reimplemented in
// Go and nothing is done with a shell construct — each operation is one
// argument vector, quoted by internal/hostexec for the remote shell.
type RemoteFS struct {
	runner hostexec.Runner
}

var _ FS = (*RemoteFS)(nil)

// Remote returns the filesystem of the hypervisor host, driven through runner.
func Remote(runner hostexec.Runner) FS { return &RemoteFS{runner: runner} }

// Describe names the machine this filesystem is on.
func (r *RemoteFS) Describe() string { return r.runner.HypervisorHost() }

// run executes one utility on the hypervisor. Every caller here is operating
// inside the state directory on paths Store.Resolve has already contained.
func (r *RemoteFS) run(effect hostexec.Effect, stdin io.Reader, name string, args ...string) (*hostexec.Result, error) {
	return r.runner.Run(context.Background(), hostexec.Command{
		Name:   name,
		Args:   args,
		Effect: effect,
		Stdin:  stdin,
	})
}

// exitStatus reports a tool's exit code, and whether the failure was the tool
// exiting at all — as opposed to ssh never reaching it, which must never be
// read as "the file is not there".
func exitStatus(err error) (int, bool) {
	var toolErr *hostexec.ToolError
	if errors.As(err, &toolErr) {
		return toolErr.ExitCode, true
	}
	return 0, false
}

func (r *RemoteFS) MkdirAll(path string, _ fs.FileMode) error {
	// The mode is left to the remote umask rather than forced: these
	// directories hold no secrets, and a host whose umask is deliberately
	// tighter than ours should keep it.
	if _, err := r.run(hostexec.Mutate, nil, "mkdir", "-p", "--", path); err != nil {
		return fmt.Errorf("creating %s on %s: %w", path, r.Describe(), err)
	}
	return nil
}

func (r *RemoteFS) WriteFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := r.MkdirAll(dir, dirPerm); err != nil {
		return err
	}

	// Written beside the destination and moved onto it, so a connection that
	// drops mid-write leaves a temporary file rather than a truncated record.
	// The name is fixed per destination rather than random: this process holds
	// the lock for whatever it is writing, and a predictable leftover is one an
	// operator can recognize.
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp")

	if _, err := r.run(hostexec.Mutate, bytes.NewReader(data), "dd", "status=none", "of="+tmp); err != nil {
		return fmt.Errorf("writing %s on %s: %w", path, r.Describe(), err)
	}
	if _, err := r.run(hostexec.Mutate, nil, "chmod", strconv.FormatUint(uint64(perm.Perm()), 8), "--", tmp); err != nil {
		return fmt.Errorf("setting permissions on %s on %s: %w", path, r.Describe(), err)
	}
	// -T keeps mv from moving the file *into* the destination when that
	// destination happens to be a directory.
	if _, err := r.run(hostexec.Mutate, nil, "mv", "-fT", "--", tmp, path); err != nil {
		return fmt.Errorf("replacing %s on %s: %w", path, r.Describe(), err)
	}
	return nil
}

func (r *RemoteFS) ReadFile(path string) ([]byte, error) {
	res, err := r.run(hostexec.Read, nil, "cat", "--", path)
	if err == nil {
		return res.Stdout, nil
	}
	// cat cannot say *why* it failed in a way worth parsing, and its message
	// is localized. The question is asked again, of a tool whose exit status
	// answers it, so that "not there yet" is never confused with "unreadable".
	if _, exited := exitStatus(err); exited {
		if found, existsErr := r.Exists(path); existsErr == nil && !found {
			return nil, fmt.Errorf("reading %s on %s: %w", path, r.Describe(), fs.ErrNotExist)
		}
	}
	return nil, fmt.Errorf("reading %s on %s: %w", path, r.Describe(), err)
}

func (r *RemoteFS) RemoveAll(path string) error {
	if _, err := r.run(hostexec.Mutate, nil, "rm", "-rf", "--", path); err != nil {
		return fmt.Errorf("removing %s on %s: %w", path, r.Describe(), err)
	}
	return nil
}

func (r *RemoteFS) Rename(from, to string) error {
	if _, err := r.run(hostexec.Mutate, nil, "mv", "-fT", "--", from, to); err != nil {
		return fmt.Errorf("moving %s to %s on %s: %w", from, to, r.Describe(), err)
	}
	return nil
}

func (r *RemoteFS) Exists(path string) (bool, error) {
	_, err := r.run(hostexec.Read, nil, "test", "-e", path)
	if err == nil {
		return true, nil
	}
	// test reports "no" by exiting 1 and nothing else; any other status, or a
	// failure to reach the host at all, is a real error.
	if code, exited := exitStatus(err); exited && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("checking %s on %s: %w", path, r.Describe(), err)
}

func (r *RemoteFS) Size(path string) (int64, error) {
	res, err := r.run(hostexec.Read, nil, "stat", "-c", "%s", "--", path)
	if err != nil {
		if found, existsErr := r.Exists(path); existsErr == nil && !found {
			return 0, fmt.Errorf("reading the size of %s on %s: %w", path, r.Describe(), fs.ErrNotExist)
		}
		return 0, fmt.Errorf("reading the size of %s on %s: %w", path, r.Describe(), err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(res.Stdout)), 10, 64)
	if err != nil {
		return 0, &hostexec.ParseError{Tool: "stat", What: "a file size", Output: string(res.Stdout)}
	}
	return size, nil
}

func (r *RemoteFS) Subdirectories(path string) ([]string, error) {
	// Full paths rather than find's -printf: the latter is a GNU extension
	// this does not need, and filepath.Base gives the same answer.
	res, err := r.run(hostexec.Read, nil, "find", path, "-mindepth", "1", "-maxdepth", "1", "-type", "d")
	if err != nil {
		if found, existsErr := r.Exists(path); existsErr == nil && !found {
			return nil, nil
		}
		return nil, fmt.Errorf("listing %s on %s: %w", path, r.Describe(), err)
	}

	names := []string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, filepath.Base(line))
		}
	}
	// find does not promise an order, and callers of this list VMs and images
	// for people to read.
	sort.Strings(names)
	return names, nil
}

func (r *RemoteFS) Canonicalize(path string) (string, error) {
	// -m canonicalizes without requiring any component to exist, which is
	// exactly what the local implementation approximates by resolving the
	// longest existing prefix.
	res, err := r.run(hostexec.Read, nil, "readlink", "-m", "--", path)
	if err != nil {
		return "", fmt.Errorf("resolving %s on %s: %w", path, r.Describe(), err)
	}
	resolved := strings.TrimSpace(string(res.Stdout))
	if resolved == "" {
		return "", &hostexec.ParseError{Tool: "readlink", What: "a resolved path", Output: string(res.Stdout)}
	}
	return resolved, nil
}

func (r *RemoteFS) FreeBytes(path string) (uint64, error) {
	// -P is the POSIX output format: one row per filesystem, six
	// whitespace-separated columns, with the available blocks fourth. -B1
	// makes those blocks bytes.
	res, err := r.run(hostexec.Read, nil, "df", "-P", "-B1", "--", path)
	if err != nil {
		return 0, fmt.Errorf("checking free space in %s on %s: %w", path, r.Describe(), err)
	}
	return parseDFAvailable(string(res.Stdout))
}

// parseDFAvailable reads the available bytes out of `df -P -B1` output. A
// device name long enough to wrap is the one shape POSIX allows that would
// break a naive read, so the columns are counted from the end of the row.
func parseDFAvailable(out string) (uint64, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, &hostexec.ParseError{Tool: "df", What: "free space", Output: out}
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0, &hostexec.ParseError{Tool: "df", What: "free space", Output: out}
	}
	// ... size used available capacity mounted-on
	available, err := strconv.ParseUint(fields[len(fields)-3], 10, 64)
	if err != nil {
		return 0, &hostexec.ParseError{Tool: "df", What: "free space", Output: out}
	}
	return available, nil
}

// Usage sums the sizes of the files under path. --apparent-size makes du
// report the sizes of the files themselves rather than the blocks they
// occupy, which is what the local implementation measures and what an
// operator comparing a base image against its manifest expects.
func (r *RemoteFS) Usage(path string) (int64, error) {
	res, err := r.run(hostexec.Read, nil, "du", "--summarize", "--apparent-size", "--block-size=1", "--", path)
	if err != nil {
		return 0, fmt.Errorf("measuring %s on %s: %w", path, r.Describe(), err)
	}
	// du prints "<bytes>\t<path>".
	fields := strings.Fields(string(res.Stdout))
	if len(fields) == 0 {
		return 0, &hostexec.ParseError{Tool: "du", What: "a total size", Output: string(res.Stdout)}
	}
	total, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, &hostexec.ParseError{Tool: "du", What: "a total size", Output: string(res.Stdout)}
	}
	return total, nil
}

// lockReadyToken is echoed back through the held process to prove the lock was
// taken. flock(1) reports failure by exiting, which a started-and-not-waited-for
// process does not surface, so the handshake is what distinguishes "we hold it"
// from "someone else does".
const lockReadyToken = "agent-vm-lock-ready\n"

// TryLock takes an exclusive advisory lock on the hypervisor.
//
// The lock is flock(2) there just as it is here, taken by flock(1) on a process
// that then blocks reading its standard input. That is what preserves the
// property the local implementation has: the lock lives exactly as long as a
// process holds the file open, so an agent-vm that is killed — or an ssh
// connection that drops — releases it without anything to clean up by hand.
func (r *RemoteFS) TryLock(path, holder string) (io.Closer, error) {
	if err := r.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, err
	}

	// cat is the process that holds the descriptor: it exits when its standard
	// input closes, which is how Close releases the lock.
	process, err := r.runner.Start(context.Background(), hostexec.Command{
		Name:         "flock",
		Args:         []string{"--exclusive", "--nonblock", path, "cat"},
		Effect:       hostexec.Mutate,
		DryRunStdout: lockReadyToken,
	})
	if err != nil {
		// flock reports a lock someone else holds by exiting, which usually
		// surfaces below as a handshake that gets nothing back — but a runner
		// that notices the exit here must reach the same verdict.
		return nil, r.classifyLockFailure(path, err)
	}

	if err := handshake(process); err != nil {
		_ = process.Close()
		return nil, r.classifyLockFailure(path, err)
	}
	if _, err := r.run(hostexec.Mutate, strings.NewReader(holder), "dd", "status=none", "of="+path); err != nil {
		// The lock is ours but unlabelled. That only costs a contended lock its
		// "who holds it" line, so it is reported rather than treated as a
		// failure to lock.
		_ = process.Close()
		return nil, fmt.Errorf("recording the holder of %s on %s: %w", path, r.Describe(), err)
	}
	return &remoteLock{process: process, path: path, host: r.Describe()}, nil
}

// handshake writes a token into the held process and reads it back, which
// proves flock got the lock and execed cat rather than exiting.
func handshake(process *hostexec.Process) error {
	if _, err := io.WriteString(process.Stdin, lockReadyToken); err != nil {
		return err
	}
	line, err := process.Stdout.ReadString('\n')
	if err != nil {
		return err
	}
	if line != lockReadyToken {
		return fmt.Errorf("unexpected reply %q", strings.TrimSpace(line))
	}
	return nil
}

// classifyLockFailure asks why the handshake failed. flock exits 1 when
// another process holds the lock, which is the ordinary case and the one that
// deserves the holder's name rather than a transport error.
func (r *RemoteFS) classifyLockFailure(path string, cause error) error {
	if _, err := r.run(hostexec.Read, nil, "flock", "--exclusive", "--nonblock", path, "true"); err != nil {
		if code, exited := exitStatus(err); exited && code == 1 {
			return &BusyError{Holder: DescribeHolder(r.readHolder(path))}
		}
		return fmt.Errorf("locking %s on %s: %w", path, r.Describe(), err)
	}
	// The lock is free now, so the handshake failed for another reason and the
	// original error is the one worth reporting.
	return fmt.Errorf("locking %s on %s: %w", path, r.Describe(), cause)
}

// readHolder reads the description the holding process wrote. It is advisory —
// it comes from another process and only ever appears in a message — so a
// failure to read it yields an unnamed holder rather than an error.
func (r *RemoteFS) readHolder(path string) string {
	res, err := r.run(hostexec.Read, nil, "head", "-c", strconv.Itoa(holderLimit), "--", path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
}

type remoteLock struct {
	process *hostexec.Process
	path    string
	host    string
}

// Close ends the holding process, which closes the descriptor and releases the
// lock. The lock file itself is left in place: removing it would race with
// another process that has already opened it and is waiting on flock.
func (l *remoteLock) Close() error {
	if err := l.process.Close(); err != nil {
		return fmt.Errorf("releasing lock %s on %s: %w", l.path, l.host, err)
	}
	return nil
}
