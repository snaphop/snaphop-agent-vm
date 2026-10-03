package state

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var _ FS = LocalFS{}

// The state directory lives on the machine the hypervisor runs on, because
// everything in it is something QEMU has to open: base images, kernels, per-VM
// overlays, the cloud-init user-data virt-install reads. When libvirt is on
// another machine that is not this one, so every operation on it goes through
// an FS rather than straight to the os package (ADR-0010).
//
// The containment rule is unchanged by that and is enforced identically on
// both: a path is canonicalized, symlinks included, and rejected unless it
// resolves inside the state directory (SECURITY.md).

// FS is the filesystem the state directory lives on. It is deliberately small:
// every operation here is one this project actually performs, and adding to it
// means adding a remote implementation that behaves identically.
type FS interface {
	// MkdirAll creates a directory and any missing parents.
	MkdirAll(path string, perm fs.FileMode) error
	// WriteFile replaces a file atomically, so an interrupted write cannot
	// leave a half-written record behind.
	WriteFile(path string, data []byte, perm fs.FileMode) error
	// ReadFile reads a file, returning an error satisfying
	// errors.Is(err, fs.ErrNotExist) when it is absent.
	ReadFile(path string) ([]byte, error)
	// RemoveAll deletes a path and everything under it.
	RemoveAll(path string) error
	// Rename moves a path, replacing the destination.
	Rename(from, to string) error
	// Exists reports whether a path exists at all.
	Exists(path string) (bool, error)
	// Size is the size of a regular file in bytes.
	Size(path string) (int64, error)
	// Subdirectories lists the immediate subdirectory names of a directory,
	// and is empty rather than an error when the directory does not exist.
	Subdirectories(path string) ([]string, error)
	// Canonicalize resolves every symlink in a path, dangling ones included,
	// and keeps components that do not exist as written, so a path that has
	// not been created yet can still be checked for containment. It matches
	// `readlink -m`.
	Canonicalize(path string) (string, error)
	// FreeBytes is the space available to an unprivileged user on the
	// filesystem holding path.
	FreeBytes(path string) (uint64, error)
	// Usage is the total size of the files under a directory.
	Usage(path string) (int64, error)
	// TryLock takes an exclusive advisory lock without waiting, returning a
	// *BusyError when another process holds it. Closing the result releases
	// the lock.
	TryLock(path, holder string) (io.Closer, error)
	// Describe names the machine this filesystem is on, for messages. It is
	// empty when that machine is this one.
	Describe() string
}

// LocalFS is the state directory on this machine, which is where it is
// whenever libvirt is local.
type LocalFS struct{}

// Local returns the filesystem of this machine.
func Local() FS { return LocalFS{} }

// Describe is empty: this machine needs no naming.
func (LocalFS) Describe() string { return "" }

func (LocalFS) MkdirAll(path string, perm fs.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return fmt.Errorf("creating %s: %w", path, err)
	}
	return nil
}

func (LocalFS) WriteFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	// A no-op once the rename below succeeds; on every failure path it is what
	// keeps a partial file out of the state directory.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	// Flushed before the rename: otherwise a power loss can leave the rename
	// on disk without the data, and the record replaced by an empty file.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flushing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func (LocalFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (LocalFS) RemoveAll(path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

func (LocalFS) Rename(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("moving %s to %s: %w", from, to, err)
	}
	return nil
}

func (LocalFS) Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("checking %s: %w", path, err)
	}
}

func (LocalFS) Size(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (LocalFS) Subdirectories(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// maxSymlinks bounds how many symlinks Canonicalize follows for one path, as
// the kernel does, so a loop of links is an error rather than a hang.
const maxSymlinks = 40

// Canonicalize resolves path the way `readlink -m` does on a remote
// hypervisor, so containment means the same thing on both. Each component is
// examined with Lstat: a symlink is replaced by its target, followed even when
// that target does not exist, and a component that does not exist is kept as
// written. filepath.EvalSymlinks cannot do this: it fails on a dangling link,
// which would let vms/a/root.qcow2 -> /elsewhere pass as a name not yet
// created, and qemu-img would then follow it out of the state directory.
func (LocalFS) Canonicalize(path string) (string, error) {
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolving %s: %w", path, err)
		}
		path = absolute
	}

	resolved := string(filepath.Separator)
	pending := splitPath(path)
	followed := 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}

		// Every component is examined, even after a missing one: a later ".."
		// can climb back to something that exists and may be a symlink.
		candidate := filepath.Join(resolved, component)
		info, err := os.Lstat(candidate)
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			resolved = candidate
			continue
		case err != nil:
			return "", fmt.Errorf("resolving %s: %w", path, err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			resolved = candidate
			continue
		}

		followed++
		if followed > maxSymlinks {
			return "", fmt.Errorf("resolving %s: too many levels of symbolic links", path)
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", fmt.Errorf("resolving %s: %w", path, err)
		}
		if filepath.IsAbs(target) {
			resolved = string(filepath.Separator)
		}
		pending = append(splitPath(target), pending...)
	}
	return resolved, nil
}

func splitPath(path string) []string {
	return strings.Split(path, string(filepath.Separator))
}

// FreeBytes reports space available to an unprivileged user, which is what
// matters for a base image build or a growing overlay — not the total free
// space, which includes blocks reserved for root.
//
// syscall rather than golang.org/x/sys: this project targets Linux only and
// adding a dependency is a decision that needs approval (AGENTS.md §11).
func (LocalFS) FreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("checking free space in %s: %w", path, err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// Usage sums the sizes of the files under path.
func (LocalFS) Usage(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measuring %s: %w", path, err)
	}
	return total, nil
}

// TryLock takes a flock on path. The lock is held only for the lifetime of the
// process that took it, so a crashed process never leaves one behind for a
// human to clean up — the kernel releases it when the descriptor closes.
func (LocalFS) TryLock(path, holder string) (io.Closer, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockPerm)
	if err != nil {
		return nil, fmt.Errorf("opening lock file %s: %w", path, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		held := readHolder(file)
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &BusyError{Holder: DescribeHolder(held)}
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}

	// The holder is recorded only after the lock is ours, so the file always
	// describes the process that actually holds it.
	if err := writeHolder(file, holder); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	return &localLock{file: file, path: path}, nil
}

type localLock struct {
	file *os.File
	path string
}

func (l *localLock) Close() error {
	// The lock file itself is left in place: removing it would race with
	// another process that has already opened it and is waiting on flock.
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("releasing lock %s: %w", l.path, err)
	}
	return l.file.Close()
}

func writeHolder(file *os.File, holder string) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("preparing lock file: %w", err)
	}
	if _, err := file.WriteAt([]byte(holder), 0); err != nil {
		return fmt.Errorf("recording lock holder: %w", err)
	}
	return nil
}

// readHolder reads the holder description written by the process that owns the
// lock. Its contents are advisory: they come from another process and are only
// ever used in a message.
func readHolder(file *os.File) string {
	buf := make([]byte, holderLimit)
	n, err := file.ReadAt(buf, 0)
	if n == 0 || (err != nil && n == 0) {
		return ""
	}
	return strings.TrimSpace(string(buf[:n]))
}
