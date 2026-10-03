package state

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
)

func sampleVM(store *Store, name string) *VM {
	vm := store.NewVM(name)
	vm.LibvirtURI = "qemu:///system"
	vm.Distro = "ubuntu:24.04"
	vm.BaseImage = BaseImageRef{
		Distro:       "ubuntu",
		Tag:          "24.04",
		SourceRef:    "docker.io/library/ubuntu:24.04",
		SourceDigest: "sha256:3f85b7caad41a95462cf5b787d8a04604c8262cdcdf9a472b8c52ef83375fe15",
		Path:         store.BaseDiskPath("ubuntu", "24.04"),
	}
	vm.Resources = VMResources{VCPUs: 2, Memory: 4 * config.GiB, Disk: 50 * config.GiB}
	vm.Network = VMNetwork{Mode: config.NetworkNAT, Name: "agent-vm-nat", MAC: "52:54:00:8f:2b:1c"}
	vm.Guest = VMGuest{User: "agent", SSHKeyPaths: []string{"/home/operator/.ssh/id_ed25519.pub"}}
	vm.CreatedBy = VMProvenance{
		AgentVMVersion:     "0.1.0",
		VirtInstallVersion: "4.1.0",
		VirtInstallArgv:    []string{"virt-install", "--import", "--name", name},
	}
	return vm
}

func TestSaveAndLoadVM_RoundTrips(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	want := sampleVM(store, "agent-01")

	if err := store.SaveVM(want); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}
	got, err := store.LoadVM("agent-01")
	if err != nil {
		t.Fatalf("LoadVM: %v", err)
	}

	if got.Name != want.Name || got.Distro != want.Distro {
		t.Errorf("identity = %s/%s, want %s/%s", got.Name, got.Distro, want.Name, want.Distro)
	}
	if got.Resources != want.Resources {
		t.Errorf("Resources = %+v, want %+v", got.Resources, want.Resources)
	}
	if got.Network != want.Network {
		t.Errorf("Network = %+v, want %+v", got.Network, want.Network)
	}
	if got.BaseImage.SourceDigest != want.BaseImage.SourceDigest {
		t.Errorf("SourceDigest = %s, want %s", got.BaseImage.SourceDigest, want.BaseImage.SourceDigest)
	}
	if got.SchemaVersion != VMSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, VMSchemaVersion)
	}
}

func TestSaveVM_WritesSizesInTheFormOperatorsRead(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	if err := store.SaveVM(sampleVM(store, "agent-01")); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}

	raw, err := store.ReadFile(filepath.Join(store.VMDir("agent-01"), VMRecordFile))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var decoded struct {
		Resources struct {
			Memory string `json:"memory"`
			Disk   string `json:"disk"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding vm.json: %v", err)
	}
	if decoded.Resources.Memory != "4G" || decoded.Resources.Disk != "50G" {
		t.Errorf("sizes = %s / %s, want 4G / 50G", decoded.Resources.Memory, decoded.Resources.Disk)
	}
}

func TestSaveVM_RecordsNoPrivateKeyMaterial(t *testing.T) {
	t.Parallel()
	// vm.json records the path of an authorized public key, never key material.
	store := newStore(t)
	if err := store.SaveVM(sampleVM(store, "agent-01")); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}

	raw, err := store.ReadFile(filepath.Join(store.VMDir("agent-01"), VMRecordFile))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, forbidden := range []string{"PRIVATE KEY", "ssh-ed25519 AAAA", "ssh-rsa AAAA"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("vm.json contains %q", forbidden)
		}
	}
}

func TestLoadVM_UnknownNameIsNotFound(t *testing.T) {
	t.Parallel()
	store := newStore(t)

	_, err := store.LoadVM("no-such-vm")

	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("got %v, want *NotFoundError", err)
	}
}

func TestLoadVM_RejectsAnInvalidNameBeforeUsingItAsAPath(t *testing.T) {
	t.Parallel()
	store := newStore(t)

	for _, name := range []string{"../etc", "Agent", "vm/../../escape"} {
		if _, err := store.LoadVM(name); err == nil {
			t.Errorf("LoadVM(%q) = nil, want an error", name)
		}
	}
}

func TestLoadVM_RefusesAnUnknownSchemaVersion(t *testing.T) {
	t.Parallel()
	// Guessing at a record written by another version is how state gets
	// corrupted; refusing and saying what to do is the contract.
	store := newStore(t)
	record := `{"schemaVersion": 99, "name": "agent-01"}`
	if err := store.WriteFile(filepath.Join(store.VMDir("agent-01"), VMRecordFile), []byte(record), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := store.LoadVM("agent-01")

	var serr *SchemaError
	if !errors.As(err, &serr) {
		t.Fatalf("got %v, want *SchemaError", err)
	}
	if serr.Found != 99 || serr.Known != VMSchemaVersion {
		t.Errorf("SchemaError = %+v", serr)
	}
	if serr.Remedy == "" {
		t.Error("SchemaError has no remedy; the operator is left with nothing to do")
	}
}

func TestListVMs_IgnoresDirectoriesWithoutARecord(t *testing.T) {
	t.Parallel()
	// A directory with no vm.json is the debris of an interrupted create, not a VM.
	store := newStore(t)
	if err := store.SaveVM(sampleVM(store, "agent-01")); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}
	if err := store.MkdirAll(store.VMDir("agent-02")); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	vms, err := store.ListVMs()
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "agent-01" {
		t.Fatalf("ListVMs returned %d VMs, want only agent-01", len(vms))
	}
}

func TestListVMs_EmptyStateDirectoryIsNotAnError(t *testing.T) {
	t.Parallel()
	store := newStore(t)

	vms, err := store.ListVMs()
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 0 {
		t.Errorf("ListVMs returned %d VMs, want 0", len(vms))
	}
}

func TestCreatesInProgress_NamesOnlyAnUnrecordedVMWhoseLockIsHeld(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// agent-01 is being created: its directory exists and its lock is held.
	// agent-02 is a create that was interrupted, agent-03 is recorded, and
	// lost+found is not a VM at all.
	for _, dir := range []string{"agent-01", "agent-02", "lost+found"} {
		if err := store.MkdirAll(filepath.Join(store.Root(), "vms", dir)); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}
	if err := store.SaveVM(sampleVM(store, "agent-03")); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}
	creating, err := store.TryLockVM("agent-01", "create")
	if err != nil {
		t.Fatalf("TryLockVM: %v", err)
	}
	defer func() { _ = creating.Release() }()
	recorded, err := store.TryLockVM("agent-03", "destroy")
	if err != nil {
		t.Fatalf("TryLockVM: %v", err)
	}
	defer func() { _ = recorded.Release() }()

	got, err := store.CreatesInProgress()
	if err != nil {
		t.Fatalf("CreatesInProgress: %v", err)
	}
	if strings.Join(got, ",") != "agent-01" {
		t.Errorf("CreatesInProgress = %v, want [agent-01]", got)
	}
	// Checking must not leave the interrupted create's lock held.
	again, err := store.TryLockVM("agent-02", "create")
	if err != nil {
		t.Fatalf("the check kept agent-02's lock: %v", err)
	}
	_ = again.Release()
}

// tamperVM saves a valid record for name and then rewrites it through edit, the
// way a corrupted or hand-edited vm.json would arrive.
func tamperVM(t *testing.T, store *Store, name string, edit func(vm *VM)) {
	t.Helper()
	vm := sampleVM(store, name)
	edit(vm)
	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("encoding vm.json: %v", err)
	}
	if err := store.WriteFile(filepath.Join(store.VMDir(name), VMRecordFile), data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoadVM_RefusesARecordThatDoesNotMatchItsDirectory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		field string
		edit  func(store *Store, vm *VM)
	}{
		{"directory is the state root", "paths.dir", func(store *Store, vm *VM) { vm.Paths.Dir = store.Root() }},
		{"directory is the vms directory", "paths.dir", func(store *Store, vm *VM) { vm.Paths.Dir = filepath.Join(store.Root(), "vms") }},
		{"directory is another VM's", "paths.dir", func(store *Store, vm *VM) { vm.Paths.Dir = store.VMDir("agent-02") }},
		{"directory is outside the state directory", "paths.dir", func(_ *Store, vm *VM) { vm.Paths.Dir = "/home/operator" }},
		{"name is another VM's", "name", func(_ *Store, vm *VM) { vm.Name = "agent-02" }},
		{"overlay is a base image", "paths.overlay", func(store *Store, vm *VM) { vm.Paths.Overlay = store.BaseDiskPath("ubuntu", "24.04") }},
		{"overlay climbs out", "paths.overlay", func(store *Store, vm *VM) {
			vm.Paths.Overlay = filepath.Join(store.VMDir("agent-01"), "..", "agent-02", OverlayFile)
		}},
		{"console log is elsewhere", "paths.consoleLog", func(_ *Store, vm *VM) { vm.Paths.ConsoleLog = "/etc/shadow" }},
		{"user-data is the directory itself", "paths.userData", func(store *Store, vm *VM) { vm.Paths.UserData = store.VMDir("agent-01") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newStore(t)
			tamperVM(t, store, "agent-01", func(vm *VM) { tt.edit(store, vm) })

			_, err := store.LoadVM("agent-01")

			var rerr *RecordError
			if !errors.As(err, &rerr) {
				t.Fatalf("LoadVM = %v, want *RecordError", err)
			}
			if rerr.Field != tt.field {
				t.Errorf("RecordError.Field = %s, want %s", rerr.Field, tt.field)
			}
		})
	}
}

func TestLoadVM_AcceptsARecordWithoutPathsItNeverHad(t *testing.T) {
	t.Parallel()
	// A record from before a path was recorded simply lacks it.
	store := newStore(t)
	tamperVM(t, store, "agent-01", func(vm *VM) {
		vm.Paths.SeedDir = ""
		vm.Paths.MetaData = ""
	})

	if _, err := store.LoadVM("agent-01"); err != nil {
		t.Fatalf("LoadVM = %v, want nil", err)
	}
}

func TestListVMs_SkipsDirectoriesThatAreNotVMNames(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	if err := store.SaveVM(sampleVM(store, "agent-01")); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}
	for _, dir := range []string{"lost+found", "old_vm", ".snap"} {
		path := filepath.Join(store.Root(), "vms", dir)
		if err := store.WriteFile(filepath.Join(path, VMRecordFile), []byte("{}"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	vms, err := store.ListVMs()
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "agent-01" {
		t.Fatalf("ListVMs returned %d VMs, want only agent-01", len(vms))
	}
	_, unreadable, err := store.ScanVMs()
	if err != nil {
		t.Fatalf("ScanVMs: %v", err)
	}
	if len(unreadable) != 0 {
		t.Errorf("ScanVMs reported %v as unreadable; they are not VMs", unreadable)
	}
}

func TestScanVMs_ReportsAnUnreadableRecordAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	for _, name := range []string{"agent-01", "agent-03"} {
		if err := store.SaveVM(sampleVM(store, name)); err != nil {
			t.Fatalf("SaveVM: %v", err)
		}
	}
	record := `{"schemaVersion": 99, "name": "agent-02"}`
	if err := store.WriteFile(filepath.Join(store.VMDir("agent-02"), VMRecordFile), []byte(record), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	vms, unreadable, err := store.ScanVMs()
	if err != nil {
		t.Fatalf("ScanVMs: %v", err)
	}
	if len(vms) != 2 || vms[0].Name != "agent-01" || vms[1].Name != "agent-03" {
		t.Errorf("ScanVMs returned %d readable VMs, want agent-01 and agent-03", len(vms))
	}
	if len(unreadable) != 1 || unreadable[0].Name != "agent-02" {
		t.Fatalf("unreadable = %v, want agent-02", unreadable)
	}
	var serr *SchemaError
	if !errors.As(unreadable[0].Err, &serr) {
		t.Errorf("unreadable error = %v, want *SchemaError", unreadable[0].Err)
	}
}

func TestVMsUsingImage_FailsClosedOnAnUnreadableRecord(t *testing.T) {
	t.Parallel()
	// The unreadable VM may well be backed by this image; removing it past a
	// record nobody could read would break that VM's disk.
	store := newStore(t)
	if err := store.SaveVM(sampleVM(store, "agent-01")); err != nil {
		t.Fatalf("SaveVM: %v", err)
	}
	record := `{"schemaVersion": 99, "name": "agent-02"}`
	if err := store.WriteFile(filepath.Join(store.VMDir("agent-02"), VMRecordFile), []byte(record), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := store.VMsUsingImage("ubuntu", "24.04")

	var serr *SchemaError
	if !errors.As(err, &serr) {
		t.Fatalf("VMsUsingImage = %v, want *SchemaError", err)
	}
}
