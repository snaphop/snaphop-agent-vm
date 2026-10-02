package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func TestOpen_CreatesTheDocumentedLayout(t *testing.T) {
	t.Parallel()
	store := newStore(t)

	for _, dir := range []string{"images", "vms", "locks"} {
		info, err := os.Stat(filepath.Join(store.Root(), dir))
		if err != nil {
			t.Errorf("state directory is missing %s: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}

func TestResolve_AcceptsPathsInsideTheStateDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)

	paths := []string{
		store.Root(),
		store.VMDir("agent-01"),
		filepath.Join(store.VMDir("agent-01"), OverlayFile),
		store.ImageDir("ubuntu", "24.04"),
	}
	for _, path := range paths {
		if _, err := store.Resolve(path); err != nil {
			t.Errorf("Resolve(%s) = %v, want nil", path, err)
		}
	}
}

func TestResolve_RefusesPathsOutsideTheStateDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)

	outside := []struct {
		name, path string
	}{
		{"absolute path elsewhere", "/etc/passwd"},
		{"parent of the state directory", filepath.Dir(store.Root())},
		{"traversal out and back", filepath.Join(store.Root(), "vms", "..", "..", "etc")},
		{"sibling with a shared prefix", store.Root() + "-2"},
		{"empty path", ""},
	}
	for _, tt := range outside {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.Resolve(tt.path)
			var cerr *ContainmentError
			if !errors.As(err, &cerr) {
				t.Fatalf("Resolve(%s) = %v, want *ContainmentError", tt.path, err)
			}
		})
	}
}

func TestResolve_RefusesASymlinkPointingOutOfTheStateDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	escape := filepath.Join(store.Root(), "vms", "escape")
	target := t.TempDir()
	if err := os.Symlink(target, escape); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	// The symlink itself lives inside the state directory; what matters is
	// where it points, which is why containment is checked after resolution.
	_, err := store.Resolve(filepath.Join(escape, "root.qcow2"))

	var cerr *ContainmentError
	if !errors.As(err, &cerr) {
		t.Fatalf("Resolve through an escaping symlink = %v, want *ContainmentError", err)
	}
}

func TestRemove_RefusesToDeleteOutsideTheStateDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	victim := filepath.Join(t.TempDir(), "important.txt")
	if err := os.WriteFile(victim, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("writing victim file: %v", err)
	}

	err := store.Remove(victim)

	var cerr *ContainmentError
	if !errors.As(err, &cerr) {
		t.Fatalf("Remove(%s) = %v, want *ContainmentError", victim, err)
	}
	if _, statErr := os.Stat(victim); statErr != nil {
		t.Errorf("file outside the state directory was removed: %v", statErr)
	}
}

func TestRemove_DeletesInsideTheStateDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	dir := store.VMDir("agent-01")
	if err := store.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := store.Remove(dir); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("VM directory still present: %v", err)
	}
}

func TestWriteFile_ReplacesAtomicallyAndSetsPermissions(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	path := filepath.Join(store.VMDir("agent-01"), UserDataFile)

	if err := store.WriteFile(path, []byte("#cloud-config\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := store.WriteFile(path, []byte("#cloud-config\nhostname: agent-01\n"), 0o600); err != nil {
		t.Fatalf("WriteFile (replace): %v", err)
	}

	got, err := store.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "#cloud-config\nhostname: agent-01\n" {
		t.Errorf("contents = %q", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %v, want 0600", info.Mode().Perm())
	}

	// The temporary file used for the atomic replace must not be left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the written file", len(entries))
	}
}

func TestWriteFile_RefusesToWriteOutsideTheStateDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)

	err := store.WriteFile(filepath.Join(t.TempDir(), "escape.txt"), []byte("nope"), 0o600)

	var cerr *ContainmentError
	if !errors.As(err, &cerr) {
		t.Fatalf("WriteFile outside the state directory = %v, want *ContainmentError", err)
	}
}

func TestFreeBytes_ReportsSpaceInTheStateDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)

	free, err := store.FreeBytes()
	if err != nil {
		t.Fatalf("FreeBytes: %v", err)
	}
	if free == 0 {
		t.Error("FreeBytes = 0, want the free space of the temp filesystem")
	}
}
