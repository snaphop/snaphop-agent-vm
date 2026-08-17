package state

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
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
	store := newStore(t)

	_, err := store.LoadVM("no-such-vm")

	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("got %v, want *NotFoundError", err)
	}
}

func TestLoadVM_RejectsAnInvalidNameBeforeUsingItAsAPath(t *testing.T) {
	store := newStore(t)

	for _, name := range []string{"../etc", "Agent", "vm/../../escape"} {
		if _, err := store.LoadVM(name); err == nil {
			t.Errorf("LoadVM(%q) = nil, want an error", name)
		}
	}
}

func TestLoadVM_RefusesAnUnknownSchemaVersion(t *testing.T) {
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
	store := newStore(t)

	vms, err := store.ListVMs()
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 0 {
		t.Errorf("ListVMs returned %d VMs, want 0", len(vms))
	}
}
