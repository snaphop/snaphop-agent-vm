// Package state owns the state directory: its layout, the per-VM records and
// base image manifests stored in it, and the locks that serialize concurrent
// operations on the same VM or image.
//
// The layout is public — operators and scripts read it directly — and so are
// the vm.json and manifest.json schemas, both of which carry a schemaVersion
// that this package refuses to guess at.
//
// Every path this package writes to or deletes is resolved, symlinks included,
// and verified to be inside the state directory first. That check is the reason
// this package exists as a boundary rather than as helper functions: it must be
// impossible to delete something outside $STATE_DIR by constructing a path
// somewhere else in the codebase.
package state

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// Directory permissions. The state directory holds no secrets by design, but it
// does hold every VM's disk, so it is not world-writable.
const (
	dirPerm  fs.FileMode = 0o755
	filePerm fs.FileMode = 0o644
)

// Layout computes paths inside a state directory without touching the
// filesystem. It exists separately from Store so that --dry-run can print the
// paths an operation would use without creating anything: a dry run that
// created a directory tree would not be a dry run.
type Layout struct {
	root string
}

// NewLayout returns the layout of the state directory at dir.
func NewLayout(dir string) Layout { return Layout{root: dir} }

// Root is the state directory itself.
func (l Layout) Root() string { return l.root }

// VMDir is where one VM's overlay, user-data, console log, and record live.
func (l Layout) VMDir(name string) string { return filepath.Join(l.root, "vms", name) }

// ImageDir is where one base image's disk, kernel, initrd, and manifest live.
func (l Layout) ImageDir(distro, tag string) string {
	return filepath.Join(l.root, "images", distro, tag)
}

// BaseDiskPath is the immutable base disk a VM's overlay is backed by.
func (l Layout) BaseDiskPath(distro, tag string) string {
	return filepath.Join(l.ImageDir(distro, tag), BaseDiskFile)
}

// KernelPath is the extracted guest kernel handed to virt-install --boot.
func (l Layout) KernelPath(distro, tag string) string {
	return filepath.Join(l.ImageDir(distro, tag), KernelFile)
}

// InitrdPath is the extracted initramfs handed to virt-install --boot.
func (l Layout) InitrdPath(distro, tag string) string {
	return filepath.Join(l.ImageDir(distro, tag), InitrdFile)
}

// Store is a state directory that exists on disk — the hypervisor's disk,
// which is this machine's only when libvirt is local.
type Store struct {
	Layout
	fsys FS
}

// Open returns a Store rooted at dir on this machine, creating the directory
// tree if needed.
func Open(dir string) (*Store, error) { return OpenOn(Local(), dir, true) }

// OpenExisting returns a Store rooted at dir on this machine without creating
// anything. It is what --dry-run uses: creating a directory tree is a change to
// the host, and a dry run makes none.
func OpenExisting(dir string) (*Store, error) { return OpenOn(Local(), dir, false) }

// OpenOn returns a Store rooted at dir on fsys, which is how a state directory
// on a remote hypervisor is opened (ADR-0010).
func OpenOn(fsys FS, dir string, create bool) (*Store, error) {
	if dir == "" {
		return nil, errors.New("state: no state directory configured")
	}
	if !filepath.IsAbs(dir) {
		// A relative path would be resolved against this process's working
		// directory, which says nothing about the hypervisor's filesystem.
		// Configuration makes the state directory absolute before it gets here.
		return nil, fmt.Errorf("state: the state directory %s must be an absolute path", dir)
	}
	store := &Store{Layout: NewLayout(filepath.Clean(dir)), fsys: fsys}
	if !create {
		return store, nil
	}
	for _, path := range []string{
		store.root,
		filepath.Join(store.root, "images"),
		filepath.Join(store.root, "vms"),
		filepath.Join(store.root, "locks"),
		filepath.Join(store.root, "networks"),
	} {
		if err := fsys.MkdirAll(path, dirPerm); err != nil {
			return nil, fmt.Errorf("creating state directory: %w", err)
		}
	}
	return store, nil
}

// FS is the filesystem this state directory lives on. Callers that must move
// or measure a file inside it use it rather than reaching for the os package,
// which would silently act on the wrong machine.
func (s *Store) FS() FS { return s.fsys }

// Host names the machine the state directory is on, or "" when that machine is
// this one. It appears in operator-facing messages, never in a path.
func (s *Store) Host() string { return s.fsys.Describe() }

// Paths within a VM directory, named once so no caller spells them again.
//
// SeedDirectory holds exactly the files cloud-init's NoCloud datasource reads,
// because everything in it is copied onto the seed disk verbatim: user-data
// and meta-data, and nothing else.
const (
	OverlayFile   = "root.qcow2"
	SeedDirectory = "seed"
	UserDataFile  = "user-data"
	MetaDataFile  = "meta-data"
	SeedImageFile = "seed.img"
	DomainXMLFile = "domain.xml"
	ConsoleLog    = "console.log"
	VMRecordFile  = "vm.json"
)

// Paths within an image directory.
const (
	BaseDiskFile = "base.qcow2"
	KernelFile   = "vmlinuz"
	InitrdFile   = "initrd"
	ManifestFile = "manifest.json"
)

// ContainmentError is a path that resolves outside the state directory. It is
// always fatal: a path escaping $STATE_DIR is a bug or an attack, never
// something to warn about and continue past (SECURITY.md).
type ContainmentError struct {
	Path     string
	Resolved string
	Root     string
}

func (e *ContainmentError) Error() string {
	return fmt.Sprintf("refusing to touch %s: it resolves to %s, which is outside the state directory %s",
		e.Path, e.Resolved, e.Root)
}

// NotFoundError is a VM or base image that the state directory has no record
// of. The CLI reports it as exit 4.
type NotFoundError struct {
	Kind string
	Name string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no such %s: %s", e.Kind, e.Name)
}

// ExistsError is a VM or base image that already exists. The CLI reports it as
// exit 5.
type ExistsError struct {
	Kind string
	Name string
	Path string
}

func (e *ExistsError) Error() string {
	return fmt.Sprintf("%s %s already exists at %s", e.Kind, e.Name, e.Path)
}

// Resolve verifies that path is inside the state directory and returns the
// resolved form. Symlinks are followed for every component that exists, so a
// symlink planted inside the state directory cannot be used to reach out of it.
// Callers pass the result to any write or delete they perform.
func (s *Store) Resolve(path string) (string, error) {
	if path == "" {
		return "", &ContainmentError{Path: path, Resolved: "", Root: s.root}
	}
	if !filepath.IsAbs(path) {
		// Every path this package is handed is built from the layout, which is
		// rooted at an absolute state directory. A relative one would be
		// resolved against the wrong machine's working directory.
		return "", &ContainmentError{Path: path, Resolved: path, Root: s.root}
	}

	root, err := s.fsys.Canonicalize(s.root)
	if err != nil {
		return "", err
	}

	resolved, err := s.fsys.Canonicalize(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if !within(root, resolved) {
		return "", &ContainmentError{Path: path, Resolved: resolved, Root: root}
	}
	return resolved, nil
}

// within reports whether path is root or sits beneath it. It compares path
// components, so /state-dir-2 is not treated as being inside /state-dir.
func within(root, path string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Remove deletes a path after confirming it is inside the state directory.
// This is the only deletion path in the project; libvirt is never asked to
// remove storage on our behalf (SECURITY.md).
func (s *Store) Remove(path string) error {
	resolved, err := s.Resolve(path)
	if err != nil {
		return err
	}
	return s.fsys.RemoveAll(resolved)
}

// MkdirAll creates a directory inside the state directory.
func (s *Store) MkdirAll(path string) error {
	resolved, err := s.Resolve(path)
	if err != nil {
		return err
	}
	return s.fsys.MkdirAll(resolved, dirPerm)
}

// WriteFile writes a file inside the state directory, replacing it atomically
// so a crash cannot leave a half-written record behind.
func (s *Store) WriteFile(path string, data []byte, perm fs.FileMode) error {
	resolved, err := s.Resolve(path)
	if err != nil {
		return err
	}
	return s.fsys.WriteFile(resolved, data, perm)
}

// ReadFile reads a file from inside the state directory.
func (s *Store) ReadFile(path string) ([]byte, error) {
	resolved, err := s.Resolve(path)
	if err != nil {
		return nil, err
	}
	return s.fsys.ReadFile(resolved)
}

// Rename moves a path inside the state directory onto another, after
// confirming both are contained. Both halves are checked because the
// destination is what survives.
func (s *Store) Rename(from, to string) error {
	resolvedFrom, err := s.Resolve(from)
	if err != nil {
		return err
	}
	resolvedTo, err := s.Resolve(to)
	if err != nil {
		return err
	}
	return s.fsys.Rename(resolvedFrom, resolvedTo)
}

// Exists reports whether a path inside the state directory exists.
func (s *Store) Exists(path string) (bool, error) {
	resolved, err := s.Resolve(path)
	if err != nil {
		return false, err
	}
	return s.fsys.Exists(resolved)
}

// FileSize is the size in bytes of a file inside the state directory.
func (s *Store) FileSize(path string) (int64, error) {
	resolved, err := s.Resolve(path)
	if err != nil {
		return 0, err
	}
	return s.fsys.Size(resolved)
}

// FreeBytes reports free space in the state directory's filesystem, which
// doctor checks and image builds need.
func (s *Store) FreeBytes() (uint64, error) { return s.fsys.FreeBytes(s.root) }

// WriteNetworkXML stores generated libvirt network XML under networks/ and
// returns its path. `virsh net-define` takes a file, and the file is worth
// keeping: it is the record of exactly what this tool asked libvirt to define.
func (s *Store) WriteNetworkXML(name string, xml []byte) (string, error) {
	path := filepath.Join(s.root, "networks", name+".xml")
	if err := s.WriteFile(path, xml, filePerm); err != nil {
		return "", err
	}
	return path, nil
}
