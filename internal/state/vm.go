package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
)

// VMSchemaVersion is the version of the vm.json schema this build writes and
// understands. It is a public contract: raise it only for a change that an
// older binary could not read correctly, and document the upgrade or rebuild
// path in the same change (AGENTS.md §8).
const VMSchemaVersion = 1

// VM is the record of one VM, stored as vms/<name>/vm.json. It is the system of
// record for a VM's configuration and provenance; libvirt remains the system of
// record for its runtime state, which is why nothing here caches domain state.
type VM struct {
	SchemaVersion int       `json:"schemaVersion"`
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"createdAt"`
	LibvirtURI    string    `json:"libvirtUri"`

	Distro    string       `json:"distro"`
	BaseImage BaseImageRef `json:"baseImage"`
	Resources VMResources  `json:"resources"`
	Network   VMNetwork    `json:"network"`
	Guest     VMGuest      `json:"guest"`
	Paths     VMPaths      `json:"paths"`
	CreatedBy VMProvenance `json:"createdBy"`
}

// BaseImageRef identifies the base image a VM was created from, by digest as
// well as by name — a tag can be rebuilt, a digest cannot.
type BaseImageRef struct {
	Distro       string `json:"distro"`
	Tag          string `json:"tag"`
	SourceRef    string `json:"sourceRef"`
	SourceDigest string `json:"sourceDigest"`
	Path         string `json:"path"`
}

// VMResources is what the domain was defined with.
type VMResources struct {
	VCPUs  int         `json:"vcpus"`
	Memory config.Size `json:"memory"`
	Disk   config.Size `json:"disk"`
}

// VMNetwork records how the guest is attached. The mode is recorded so that
// exposure is auditable after the fact (SECURITY.md, "Networking Boundaries").
type VMNetwork struct {
	Mode   config.NetworkMode `json:"mode"`
	Name   string             `json:"name,omitempty"`
	Bridge string             `json:"bridge,omitempty"`
	MAC    string             `json:"mac,omitempty"`
}

// VMGuest is the guest-side contract: who to log in as, and which public keys
// were authorized. Only paths are recorded — no key material.
type VMGuest struct {
	User        string   `json:"user"`
	SSHKeyPaths []string `json:"sshKeyPaths"`
}

// VMPaths are absolute paths inside the state directory.
type VMPaths struct {
	Dir        string `json:"dir"`
	Overlay    string `json:"overlay"`
	UserData   string `json:"userData"`
	DomainXML  string `json:"domainXml"`
	ConsoleLog string `json:"consoleLog"`
}

// VMProvenance answers "what created this?" from the state directory alone.
type VMProvenance struct {
	AgentVMVersion     string   `json:"agentVmVersion"`
	VirtInstallVersion string   `json:"virtInstallVersion"`
	VirtInstallArgv    []string `json:"virtInstallArgv"`
}

// SchemaError is a stored record this build does not understand. The tool
// refuses it and says what to do, rather than guessing at the contents.
type SchemaError struct {
	File   string
	Found  int
	Known  int
	Remedy string
}

func (e *SchemaError) Error() string {
	msg := fmt.Sprintf("%s has schemaVersion %d, but this build of agent-vm understands version %d", e.File, e.Found, e.Known)
	if e.Remedy != "" {
		msg += "\n  " + e.Remedy
	}
	return msg
}

// NewVM builds a record with the paths and schema version filled in.
func (s *Store) NewVM(name string) *VM {
	dir := s.VMDir(name)
	return &VM{
		SchemaVersion: VMSchemaVersion,
		Name:          name,
		CreatedAt:     time.Now().UTC(),
		Paths: VMPaths{
			Dir:        dir,
			Overlay:    filepath.Join(dir, OverlayFile),
			UserData:   filepath.Join(dir, UserDataFile),
			DomainXML:  filepath.Join(dir, DomainXMLFile),
			ConsoleLog: filepath.Join(dir, ConsoleLog),
		},
	}
}

// SaveVM writes vms/<name>/vm.json atomically.
func (s *Store) SaveVM(vm *VM) error {
	if err := config.ValidateVMName(vm.Name); err != nil {
		return err
	}
	vm.SchemaVersion = VMSchemaVersion

	data, err := json.MarshalIndent(vm, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding vm.json for %s: %w", vm.Name, err)
	}
	return s.WriteFile(filepath.Join(s.VMDir(vm.Name), VMRecordFile), append(data, '\n'), filePerm)
}

// LoadVM reads one VM's record. A name that is not a valid VM name is rejected
// before it is used as a path segment.
func (s *Store) LoadVM(name string) (*VM, error) {
	if err := config.ValidateVMName(name); err != nil {
		return nil, err
	}

	path := filepath.Join(s.VMDir(name), VMRecordFile)
	data, err := s.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &NotFoundError{Kind: "VM", Name: name}
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	// The schema version is read before the rest of the record, so an
	// unreadable future format is reported as a version problem rather than as
	// a parse error.
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if probe.SchemaVersion != VMSchemaVersion {
		return nil, &SchemaError{
			File: path, Found: probe.SchemaVersion, Known: VMSchemaVersion,
			Remedy: "This VM was created by a different version of agent-vm. Destroy it with that version, or remove its state directory by hand.",
		}
	}

	var vm VM
	if err := json.Unmarshal(data, &vm); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &vm, nil
}

// HasVM reports whether a VM record exists, without reading it.
func (s *Store) HasVM(name string) bool {
	_, err := os.Stat(filepath.Join(s.VMDir(name), VMRecordFile))
	return err == nil
}

// ListVMs returns every VM this state directory has a record of, by name.
// Domains that exist in libvirt but have no record here are deliberately not
// discoverable: this tool acts only on VMs it created.
func (s *Store) ListVMs() ([]*VM, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "vms"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing VMs: %w", err)
	}

	vms := make([]*VM, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		vm, err := s.LoadVM(entry.Name())
		if err != nil {
			var notFound *NotFoundError
			if errors.As(err, &notFound) {
				// A directory without a record is a create that failed partway
				// or was interrupted; it is not a VM.
				continue
			}
			return nil, err
		}
		vms = append(vms, vm)
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
	return vms, nil
}
