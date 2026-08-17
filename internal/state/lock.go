package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Locks keep two concurrent agent-vm processes from creating, destroying, or
// rebuilding the same thing at once. They are advisory file locks (flock), held
// only for the lifetime of the process that took them, so a crashed process
// never leaves a lock behind for a human to clean up — the kernel releases it
// when the file descriptor closes.
//
// The lock file records the holder's PID and the operation, so a contended lock
// can say who is holding it rather than just timing out.

// lockPollInterval is how often a blocked acquisition retries. Lock waits here
// are short (another create finishing), so polling beats a signal mechanism.
const lockPollInterval = 100 * time.Millisecond

// Lock is a held file lock. Release must be called, normally with defer.
type Lock struct {
	file *os.File
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
	if err := os.MkdirAll(filepath.Dir(resolved), dirPerm); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}

	file, err := os.OpenFile(resolved, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", resolved, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readHolder(file)
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &BusyError{Resource: resource, Holder: holder}
		}
		return nil, fmt.Errorf("locking %s: %w", resolved, err)
	}

	// Record the holder only after the lock is ours, so the file always
	// describes the process that actually holds it.
	if err := writeHolder(file, operation); err != nil {
		syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
		return nil, err
	}
	return &Lock{file: file, path: resolved}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil

	// The lock file itself is left in place: removing it would race with
	// another process that has already opened it and is waiting on flock.
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		file.Close()
		return fmt.Errorf("releasing lock %s: %w", l.path, err)
	}
	return file.Close()
}

func writeHolder(file *os.File, operation string) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("preparing lock file: %w", err)
	}
	if _, err := file.WriteAt([]byte(fmt.Sprintf("pid=%d operation=%s\n", os.Getpid(), operation)), 0); err != nil {
		return fmt.Errorf("recording lock holder: %w", err)
	}
	return nil
}

// readHolder reads the holder description written by the process that owns the
// lock. Its contents are advisory: they come from another process and are only
// ever used in a message.
func readHolder(file *os.File) string {
	buf := make([]byte, 256)
	n, err := file.ReadAt(buf, 0)
	if n == 0 || (err != nil && n == 0) {
		return ""
	}
	line := strings.TrimSpace(string(buf[:n]))
	if pid, rest, ok := strings.Cut(strings.TrimPrefix(line, "pid="), " "); ok {
		if _, err := strconv.Atoi(pid); err == nil {
			return "pid " + pid + ", " + rest
		}
	}
	return line
}
