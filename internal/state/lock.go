package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Locks keep two concurrent agent-vm processes from creating, destroying, or
// rebuilding the same thing at once. They are advisory file locks (flock), held
// only for the lifetime of the process that took them, so a crashed process
// never leaves a lock behind for a human to clean up — the kernel releases it
// when the file descriptor closes.
//
// They are taken on the machine the state directory is on, which is the
// hypervisor. Two operators driving the same remote hypervisor from their own
// laptops contend for the same lock, which is the point: the thing being
// protected is the state directory, not the client.
//
// The lock file records the holder's host, PID, and operation, so a contended
// lock can say who is holding it rather than just timing out.

// lockPollInterval is how often a blocked acquisition retries. Lock waits here
// are short (another create finishing), so polling beats a signal mechanism.
const lockPollInterval = 100 * time.Millisecond

// lockPerm is deliberately group- and world-readable: a contended lock is
// meant to be able to name its holder, and on a shared hypervisor the process
// asking is often not the one that wrote it.
const lockPerm fs.FileMode = 0o644

// holderLimit bounds how much of a lock file is read back. The description is
// one short line; anything longer is not ours and is not worth quoting.
const holderLimit = 256

// Lock is a held file lock. Release must be called, normally with defer.
type Lock struct {
	held io.Closer
	path string
}

// BusyError is a lock held by another process.
type BusyError struct {
	Resource string
	Holder   string
}

func (e *BusyError) Error() string {
	if e.Holder != "" {
		return fmt.Sprintf("%s is busy: another agent-vm process holds its lock (%s)", e.Resource, e.Holder)
	}
	return fmt.Sprintf("%s is busy: another agent-vm process holds its lock", e.Resource)
}

// LockVM takes the lock for one VM. operation is recorded in the lock file so a
// contended lock can name what the other process is doing.
func (s *Store) LockVM(ctx context.Context, name, operation string) (*Lock, error) {
	return s.lock(ctx, filepath.Join(s.root, "locks", "vm-"+name+".lock"), "VM "+name, operation)
}

// LockImage takes the lock for one base image, so two builds of the same
// distro and tag cannot run at once.
func (s *Store) LockImage(ctx context.Context, distro, tag, operation string) (*Lock, error) {
	resource := distro + ":" + tag
	return s.lock(ctx, filepath.Join(s.root, "locks", "image-"+distro+"-"+tag+".lock"), "base image "+resource, operation)
}

// TryLockVM takes the VM lock without waiting, returning *BusyError if another
// process holds it.
func (s *Store) TryLockVM(name, operation string) (*Lock, error) {
	return s.tryLock(filepath.Join(s.root, "locks", "vm-"+name+".lock"), "VM "+name, operation)
}

// lock blocks until the lock is acquired or ctx is done.
func (s *Store) lock(ctx context.Context, path, resource, operation string) (*Lock, error) {
	for {
		l, err := s.tryLock(path, resource, operation)
		if err == nil {
			return l, nil
		}
		var busy *BusyError
		if !errors.As(err, &busy) {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (waited for %s)", busy, resource)
		case <-time.After(lockPollInterval):
		}
	}
}

func (s *Store) tryLock(path, resource, operation string) (*Lock, error) {
	resolved, err := s.Resolve(path)
	if err != nil {
		return nil, err
	}

	held, err := s.fsys.TryLock(resolved, holderDescription(operation))
	if err != nil {
		// The filesystem knows who holds a busy lock but not what the caller
		// was trying to lock, so the resource is named here.
		var busy *BusyError
		if errors.As(err, &busy) {
			busy.Resource = resource
			return nil, busy
		}
		return nil, err
	}
	return &Lock{held: held, path: resolved}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.held == nil {
		return nil
	}
	held := l.held
	l.held = nil
	return held.Close()
}

// holderDescription is the line written into a lock file. The host is included
// because a lock on a shared hypervisor may be held from another machine
// entirely, where a bare PID would be no help at all.
func holderDescription(operation string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("host=%s pid=%d operation=%s\n", host, os.Getpid(), operation)
}

// DescribeHolder renders a lock file's contents for a message. It is parsed
// leniently: the contents come from another process and are only ever shown to
// a person.
func DescribeHolder(line string) string {
	line = strings.TrimSpace(line)
	fields := map[string]string{}
	for _, field := range strings.Fields(line) {
		if key, value, ok := strings.Cut(field, "="); ok {
			fields[key] = value
		}
	}
	pid, err := strconv.Atoi(fields["pid"])
	if err != nil {
		return line
	}

	described := "pid " + strconv.Itoa(pid)
	if host := fields["host"]; host != "" {
		described += " on " + host
	}
	if operation := fields["operation"]; operation != "" {
		described += ", operation=" + operation
	}
	return described
}
