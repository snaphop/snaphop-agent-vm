package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// otherUser is an identity that owns nothing the tests create, so a directory
// grants it access only through the "other" permission bits or an ACL. 65534 is
// nobody on every distribution this project supports.
func otherUser() *hypervisorIdentity {
	return &hypervisorIdentity{Name: "libvirt-qemu", UID: 65534, GIDs: map[uint32]bool{65534: true}}
}

// selfUser is an identity matching the user running the tests, used to check
// that owner permissions are honoured.
func selfUser(t *testing.T) *hypervisorIdentity {
	t.Helper()
	return &hypervisorIdentity{
		Name: "self",
		UID:  uint32(os.Getuid()),
		GIDs: map[uint32]bool{uint32(os.Getgid()): true},
	}
}

// tree builds root/a/b/c with the given mode on each level and returns the leaf.
func tree(t *testing.T, modes ...os.FileMode) (root, leaf string) {
	t.Helper()
	root = t.TempDir()
	// t.TempDir() creates <tmp>/<TestName><random>/001, both levels 0700, which
	// would block every identity before the test reaches the level it means to
	// exercise. Both are opened up so the modes under test are the only ones
	// that decide the outcome.
	for _, dir := range []string{filepath.Dir(root), root} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
	}

	leaf = root
	for i, mode := range modes {
		leaf = filepath.Join(leaf, string(rune('a'+i)))
		if err := os.Mkdir(leaf, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", leaf, err)
		}
		if err := os.Chmod(leaf, mode); err != nil {
			t.Fatalf("chmod %s: %v", leaf, err)
		}
	}
	return root, leaf
}

func TestFirstUntraversable_AllowsAPathOfWorldSearchableDirectories(t *testing.T) {
	_, leaf := tree(t, 0o755, 0o755, 0o711)

	blocker, err := firstUntraversable(leaf, otherUser())
	if err != nil {
		t.Fatalf("firstUntraversable: %v", err)
	}
	if blocker != "" {
		t.Errorf("firstUntraversable = %q, want no blocker: every directory is world-searchable", blocker)
	}
}

// A home directory is commonly 0700, and the default state directory sits
// underneath it. This is the exact shape of the failure this check exists for.
func TestFirstUntraversable_ReportsAPrivateHomeDirectory(t *testing.T) {
	root, leaf := tree(t, 0o700, 0o755, 0o755)
	want := filepath.Join(root, "a")

	blocker, err := firstUntraversable(leaf, otherUser())
	if err != nil {
		t.Fatalf("firstUntraversable: %v", err)
	}
	if blocker != want {
		t.Errorf("firstUntraversable = %q, want %q", blocker, want)
	}
}

// The remedy names one directory at a time, so it must be the shallowest one:
// fixing a deeper directory first changes nothing.
func TestFirstUntraversable_ReportsTheShallowestBlockingDirectory(t *testing.T) {
	root, leaf := tree(t, 0o700, 0o755, 0o700)
	want := filepath.Join(root, "a")

	blocker, err := firstUntraversable(leaf, otherUser())
	if err != nil {
		t.Fatalf("firstUntraversable: %v", err)
	}
	if blocker != want {
		t.Errorf("firstUntraversable = %q, want the shallowest blocker %q", blocker, want)
	}
}

// create makes the per-VM directory itself, so a state directory that does not
// exist yet is not a problem as long as its parents are reachable.
func TestFirstUntraversable_IgnoresDirectoriesThatDoNotExistYet(t *testing.T) {
	_, leaf := tree(t, 0o755)

	blocker, err := firstUntraversable(filepath.Join(leaf, "not", "created", "yet"), otherUser())
	if err != nil {
		t.Fatalf("firstUntraversable: %v", err)
	}
	if blocker != "" {
		t.Errorf("firstUntraversable = %q, want no blocker for an absent directory", blocker)
	}
}

func TestFirstUntraversable_HonoursOwnerPermissions(t *testing.T) {
	_, leaf := tree(t, 0o700, 0o700)

	blocker, err := firstUntraversable(leaf, selfUser(t))
	if err != nil {
		t.Fatalf("firstUntraversable: %v", err)
	}
	if blocker != "" {
		t.Errorf("firstUntraversable = %q, want no blocker: the identity owns every directory", blocker)
	}
}

// The remedy this check prints is `setfacl -m u:<user>:x`. If the check could
// not see the resulting ACL it would keep failing after the operator applied
// the fix, which is worse than not checking at all.
func TestFirstUntraversable_HonoursAnACLGrantingSearch(t *testing.T) {
	if _, err := exec.LookPath("setfacl"); err != nil {
		t.Skip("setfacl is not installed; the ACL decoding itself is covered by TestACL_* below")
	}
	root, leaf := tree(t, 0o700, 0o755)
	blocked := filepath.Join(root, "a")

	identity := selfUser(t)
	// Granting to the running user's own uid keeps the test independent of
	// which accounts exist on the host, so the named-user entry is what must be
	// honoured rather than ownership.
	identity.UID = 65534
	identity.GIDs = map[uint32]bool{65534: true}

	cmd := exec.Command("setfacl", "-m", "u:65534:x", blocked)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("setfacl failed on %s (%v): %s", t.TempDir(), err, out)
	}

	blocker, err := firstUntraversable(leaf, identity)
	if err != nil {
		t.Fatalf("firstUntraversable: %v", err)
	}
	if blocker != "" {
		t.Errorf("firstUntraversable = %q, want no blocker: an ACL grants search to uid 65534", blocker)
	}
}

// aclFixture is the system.posix_acl_access attribute captured from a real
// directory after `setfacl -m u:libvirt-qemu:x`. See test/toolout/README.md.
func aclFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../test/toolout/posix-acl-access-search-grant.bin")
	if err != nil {
		t.Fatalf("reading the captured ACL fixture: %v", err)
	}
	return data
}

func TestACL_DecodesAnAttributeCapturedFromARealDirectory(t *testing.T) {
	entries, err := parseACL(aclFixture(t))
	if err != nil {
		t.Fatalf("parseACL: %v", err)
	}

	// getfacl reported: user::rwx, user:libvirt-qemu:--x, group::---,
	// mask::--x, other::---
	want := []aclEntry{
		{Tag: tagUserObj, Perm: 0o7, ID: ^uint32(0)},
		{Tag: tagUser, Perm: permExecute, ID: 957},
		{Tag: tagGroupObj, Perm: 0, ID: ^uint32(0)},
		{Tag: tagMask, Perm: permExecute, ID: ^uint32(0)},
		{Tag: tagOther, Perm: 0, ID: ^uint32(0)},
	}
	if len(entries) != len(want) {
		t.Fatalf("parseACL returned %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], want[i])
		}
	}
}

func TestACL_GrantsSearchToTheNamedUserAndNobodyElse(t *testing.T) {
	entries, err := parseACL(aclFixture(t))
	if err != nil {
		t.Fatalf("parseACL: %v", err)
	}
	// The captured directory is owned by uid 1000, gid 1000.
	stat := &syscall.Stat_t{Uid: 1000, Gid: 1000}

	granted := &hypervisorIdentity{Name: "libvirt-qemu", UID: 957, GIDs: map[uint32]bool{957: true}}
	if !aclGrantsSearch(entries, stat, granted) {
		t.Error("aclGrantsSearch = false for uid 957, want true: the ACL names it with --x")
	}

	stranger := &hypervisorIdentity{Name: "qemu", UID: 107, GIDs: map[uint32]bool{107: true}}
	if aclGrantsSearch(entries, stat, stranger) {
		t.Error("aclGrantsSearch = true for uid 107, want false: other:: is ---")
	}
}

// A named entry is capped by the mask, so a mask without execute revokes the
// grant even though the user entry still carries it.
func TestACL_AppliesTheMaskToANamedUserEntry(t *testing.T) {
	entries := []aclEntry{
		{Tag: tagUserObj, Perm: 0o7, ID: ^uint32(0)},
		{Tag: tagUser, Perm: permExecute, ID: 957},
		{Tag: tagGroupObj, Perm: 0, ID: ^uint32(0)},
		{Tag: tagMask, Perm: 0, ID: ^uint32(0)},
		{Tag: tagOther, Perm: 0, ID: ^uint32(0)},
	}
	stat := &syscall.Stat_t{Uid: 1000, Gid: 1000}
	identity := &hypervisorIdentity{Name: "libvirt-qemu", UID: 957, GIDs: map[uint32]bool{957: true}}

	if aclGrantsSearch(entries, stat, identity) {
		t.Error("aclGrantsSearch = true, want false: the mask removes execute from the named entry")
	}
}

func TestACL_RejectsAnAttributeItCannotDecode(t *testing.T) {
	for name, data := range map[string][]byte{
		"truncated header":   {0x02, 0x00},
		"unknown version":    {0x09, 0x00, 0x00, 0x00},
		"ragged entry array": append([]byte{0x02, 0x00, 0x00, 0x00}, 0x01, 0x00, 0x07),
	} {
		if _, err := parseACL(data); err == nil {
			t.Errorf("parseACL(%s) = nil error, want a decoding failure rather than a silent fallback", name)
		}
	}
}

func TestQEMUUserFromConf_ReadsAnExplicitSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qemu.conf")
	conf := `# The user for QEMU processes run by the system instance.
#user = "root"
user = "libvirt-qemu"
#group = "root"
`
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatalf("writing qemu.conf: %v", err)
	}

	if got := qemuUserFromConf(path); got != "libvirt-qemu" {
		t.Errorf("qemuUserFromConf = %q, want %q", got, "libvirt-qemu")
	}
}

func TestQEMUUserFromConf_IgnoresCommentedAndAbsentSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qemu.conf")
	if err := os.WriteFile(path, []byte("#user = \"root\"\ngroup = \"kvm\"\n"), 0o600); err != nil {
		t.Fatalf("writing qemu.conf: %v", err)
	}

	if got := qemuUserFromConf(path); got != "" {
		t.Errorf("qemuUserFromConf = %q, want \"\" so the distribution default applies", got)
	}
	if got := qemuUserFromConf(filepath.Join(t.TempDir(), "absent.conf")); got != "" {
		t.Errorf("qemuUserFromConf on a missing file = %q, want \"\"", got)
	}
}

// runDoctorAs runs doctor with a pinned hypervisor identity and state directory.
func runDoctorAs(t *testing.T, stateDir string, identity func() (*hypervisorIdentity, error), extraArgs ...string) doctorReport {
	t.Helper()
	var stdout, stderr bytes.Buffer

	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: healthyHost(),
		HypervisorIdentity: identity,
		KVMAccess:          allowKVM,
	}
	args := append([]string{
		"--state-dir", stateDir,
		"--config", filepath.Join(t.TempDir(), "absent.toml"),
		"--output", "json",
	}, extraArgs...)

	_ = app.run(context.Background(), append(args, "doctor"))

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor --output json produced unparseable output: %v\n%s", err, stdout.String())
	}
	return report
}

func TestDoctor_StateDirectoryUnreachableByTheHypervisorIsAFailure(t *testing.T) {
	root, leaf := tree(t, 0o700, 0o755)
	blocked := filepath.Join(root, "a")

	report := runDoctorAs(t, leaf, func() (*hypervisorIdentity, error) { return otherUser(), nil })

	got := find(t, report, "state directory access")
	if got.Status != statusFail {
		t.Fatalf("state directory access = %s (%s), want fail", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, blocked) {
		t.Errorf("detail does not name the blocking directory %s: %s", blocked, got.Detail)
	}
	if !strings.Contains(got.Remedy, "setfacl -m u:libvirt-qemu:x "+blocked) {
		t.Errorf("remedy is not a command the operator can run: %s", got.Remedy)
	}
	if report.OK {
		t.Error("report.OK = true, want false: a VM cannot start when its disk is unreachable")
	}
}

func TestDoctor_StateDirectoryReachableByTheHypervisorPasses(t *testing.T) {
	_, leaf := tree(t, 0o755, 0o755)

	report := runDoctorAs(t, leaf, func() (*hypervisorIdentity, error) { return otherUser(), nil })

	if got := find(t, report, "state directory access"); got.Status != statusPass {
		t.Errorf("state directory access = %s (%s), want pass", got.Status, got.Detail)
	}
}

// Session mode runs QEMU as the invoking user, so there is no second identity
// to satisfy and the check does not apply.
func TestDoctor_StateDirectoryAccessIsSkippedOnTheSessionURI(t *testing.T) {
	root, leaf := tree(t, 0o700, 0o755)
	_ = root

	fake := healthyHost()
	fake.Respond("virsh --connect qemu:///session version", hostexec.FakeResponse{Stdout: "libvirt 9.0.0\n"})
	fake.Respond("virsh --connect qemu:///session net-list --name --all", hostexec.FakeResponse{Stdout: "agent-vm-nat\n"})
	fake.Respond("virsh --connect qemu:///session net-list --name", hostexec.FakeResponse{Stdout: "agent-vm-nat\n"})

	var stdout, stderr bytes.Buffer
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: fake,
		HypervisorIdentity: func() (*hypervisorIdentity, error) {
			t.Error("doctor consulted the hypervisor identity on the session URI, where QEMU runs as the invoking user")
			return nil, nil
		},
		KVMAccess: allowKVM,
	}
	_ = app.run(context.Background(), []string{
		"--state-dir", leaf,
		"--config", filepath.Join(t.TempDir(), "absent.toml"),
		"--libvirt-uri", "qemu:///session",
		"--output", "json",
		"doctor",
	})

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unparseable output: %v\n%s", err, stdout.String())
	}
	if got := find(t, report, "state directory access"); got.Status != statusSkip {
		t.Errorf("state directory access = %s (%s), want skip", got.Status, got.Detail)
	}
}

// Guessing an account would produce a confident and wrong verdict, so an
// unidentifiable hypervisor user skips rather than fails.
func TestDoctor_StateDirectoryAccessIsSkippedWhenTheQEMUUserIsUnknown(t *testing.T) {
	_, leaf := tree(t, 0o700)

	report := runDoctorAs(t, leaf, undeterminableHypervisor)

	got := find(t, report, "state directory access")
	if got.Status != statusSkip {
		t.Errorf("state directory access = %s (%s), want skip", got.Status, got.Detail)
	}
	if report.OK != true {
		t.Error("report.OK = false, want true: an undeterminable identity is not a host failure")
	}
}

// The check only stats directories, so unlike the writability check it stays
// useful on a host being reviewed with --dry-run.
func TestDoctor_StateDirectoryAccessRunsUnderDryRun(t *testing.T) {
	root, leaf := tree(t, 0o700, 0o755)
	blocked := filepath.Join(root, "a")

	report := runDoctorAs(t, leaf, func() (*hypervisorIdentity, error) { return otherUser(), nil }, "--dry-run")

	got := find(t, report, "state directory access")
	if got.Status != statusFail {
		t.Fatalf("state directory access = %s (%s), want fail under --dry-run", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, blocked) {
		t.Errorf("detail does not name the blocking directory %s: %s", blocked, got.Detail)
	}
}
