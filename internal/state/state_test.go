package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestResolve_RefusesADanglingSymlinkPointingOutOfTheStateDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	dir := store.VMDir("agent-01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// The target does not exist yet. qemu-img writing the overlay would follow
	// the link and create it, outside the state directory.
	target := filepath.Join(t.TempDir(), "outside", "root.qcow2")
	overlay := filepath.Join(dir, OverlayFile)
	if err := os.Symlink(target, overlay); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := store.Resolve(overlay)

	var cerr *ContainmentError
	if !errors.As(err, &cerr) {
		t.Fatalf("Resolve through a dangling escaping symlink = %v, want *ContainmentError", err)
	}
	if cerr.Resolved != target {
		t.Errorf("Resolved = %s, want the link's target %s", cerr.Resolved, target)
	}
}

func TestResolve_FollowsARelativeDanglingSymlinkBackOutThroughDotDot(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	// vms/a -> ../../escaped: relative, dangling, and climbing out of the
	// state directory, which only resolving the link itself reveals.
	link := filepath.Join(store.Root(), "vms", "agent-01")
	if err := os.Symlink(filepath.Join("..", "..", "escaped"), link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err := store.Resolve(filepath.Join(link, OverlayFile))

	var cerr *ContainmentError
	if !errors.As(err, &cerr) {
		t.Fatalf("Resolve through a relative escaping symlink = %v, want *ContainmentError", err)
	}
}

func TestResolve_ChecksSymlinksReachedAfterAMissingComponent(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	escape := filepath.Join(store.Root(), "vms", "escape")
	if err := os.Symlink(t.TempDir(), escape); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	// "missing" does not exist, and ".." climbs back to the existing vms/,
	// where the next component is the escaping link.
	_, err := store.Resolve(filepath.Join(store.Root(), "vms", "missing") + "/../escape/root.qcow2")

	var cerr *ContainmentError
	if !errors.As(err, &cerr) {
		t.Fatalf("Resolve = %v, want *ContainmentError", err)
	}
}

func TestResolve_KeepsNamesThatDoNotExistYet(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	path := filepath.Join(store.VMDir("agent-01"), SeedDirectory, UserDataFile)

	got, err := store.Resolve(path)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v, want nil", path, err)
	}
	root, err := filepath.EvalSymlinks(store.Root())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	want := filepath.Join(root, "vms", "agent-01", SeedDirectory, UserDataFile)
	if got != want {
		t.Errorf("Resolve(%s) = %s, want %s", path, got, want)
	}
}

func TestResolve_RefusesASymlinkLoop(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	first := filepath.Join(store.Root(), "vms", "first")
	second := filepath.Join(store.Root(), "vms", "second")
	if err := os.Symlink(second, first); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if err := os.Symlink(first, second); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if _, err := store.Resolve(filepath.Join(first, OverlayFile)); err == nil {
		t.Fatal("Resolve through a symlink loop = nil, want an error")
	}
}

func TestRemove_RefusesToDeleteTheStateDirectoryItself(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	keep := filepath.Join(store.Root(), "images", "keep.txt")
	if err := os.WriteFile(keep, []byte("base image"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	for _, path := range []string{store.Root(), filepath.Join(store.Root(), "vms", "..")} {
		err := store.Remove(path)

		var cerr *ContainmentError
		if !errors.As(err, &cerr) {
			t.Errorf("Remove(%s) = %v, want *ContainmentError", path, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the state directory's contents were removed: %v", err)
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

// cacheImage writes a complete cached image into dir — manifest, disk, kernel,
// and initrd — whose manifest names distro:tag, as a build would leave it.
func cacheImage(t *testing.T, store *Store, dir, distro, tag string) {
	t.Helper()
	data, err := MarshalManifest(&Manifest{SchemaVersion: ManifestSchemaVersion, Distro: distro, Tag: tag})
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}
	if err := store.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	files := map[string][]byte{ManifestFile: data, BaseDiskFile: nil, KernelFile: nil, InitrdFile: nil}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
}

func TestListImages_ListsOnlyImagesInTheirOwnDirectory(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	ubuntu := filepath.Dir(store.ImageDir("ubuntu", "24.04"))

	cacheImage(t, store, store.ImageDir("ubuntu", "24.04"), "ubuntu", "24.04")
	// A real image whose tag happens to end in ".previous" is still an image.
	cacheImage(t, store, store.ImageDir("ubuntu", "24.04.previous"), "ubuntu", "24.04.previous")
	// The copy of ubuntu:22.04 a rebuild moves aside, under the current name
	// and under the name older releases used: neither is an image of its own,
	// and listing either would show an image `image rm` cannot reach.
	cacheImage(t, store, filepath.Join(ubuntu, ".22.04.previous"), "ubuntu", "22.04")
	cacheImage(t, store, filepath.Join(ubuntu, "22.04.previous"), "ubuntu", "22.04")
	// A build workspace that got as far as writing its manifest.
	cacheImage(t, store, filepath.Join(ubuntu, ".build-22.04-4242"), "ubuntu", "22.04")

	images, err := store.ListImages()
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	var refs []string
	for _, m := range images {
		refs = append(refs, m.Ref())
	}
	want := []string{"ubuntu:24.04", "ubuntu:24.04.previous"}
	if strings.Join(refs, " ") != strings.Join(want, " ") {
		t.Errorf("ListImages = %v, want %v", refs, want)
	}
}
