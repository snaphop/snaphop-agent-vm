package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
)

// ManifestSchemaVersion is the version of the manifest.json schema this build
// writes and understands. Base images built by an older release must stay
// bootable by a newer one; when that cannot hold, this constant rises and the
// rebuild path is documented (AGENTS.md §8).
const ManifestSchemaVersion = 1

// Manifest records what a cached base image is and how it was produced. It is
// what makes an image identifiable after the fact: the source digest says what
// booted, and the tool versions say what built it.
type Manifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Distro        string    `json:"distro"`
	Tag           string    `json:"tag"`
	BuiltAt       time.Time `json:"builtAt"`
	Platform      string    `json:"platform"`

	// SourceRef is the OCI reference the operator named; SourceDigest is what
	// was actually pulled and is the authoritative identity.
	SourceRef    string `json:"sourceRef"`
	SourceDigest string `json:"sourceDigest"`

	KernelVersion string `json:"kernelVersion"`
	// KernelCmdline is part of the public contract: with direct kernel boot
	// (ADR-0004) the command line lives on the host, not in the guest, so it is
	// recorded here rather than being discoverable from inside the VM.
	KernelCmdline string `json:"kernelCmdline"`

	BaseDiskBytes int64 `json:"baseDiskBytes"`

	// ToolVersions maps tool name to the version that built this image, so an
	// artifact built by a known-bad tool version can be found later.
	ToolVersions map[string]string `json:"toolVersions"`

	AgentVMVersion string `json:"agentVmVersion"`
}

// Ref renders the image's identity as an operator writes it.
func (m *Manifest) Ref() string { return m.Distro + ":" + m.Tag }

// MarshalManifest encodes a manifest in the on-disk form. It is exported
// because a build assembles its manifest inside a temporary directory and
// renames it into place with the artifacts it describes, rather than writing it
// to the cache directly.
func MarshalManifest(m *Manifest) ([]byte, error) {
	m.SchemaVersion = ManifestSchemaVersion

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding manifest for %s: %w", m.Ref(), err)
	}
	return append(data, '\n'), nil
}

// SaveManifest writes images/<distro>/<tag>/manifest.json atomically.
func (s *Store) SaveManifest(m *Manifest) error {
	data, err := MarshalManifest(m)
	if err != nil {
		return err
	}
	return s.WriteFile(filepath.Join(s.ImageDir(m.Distro, m.Tag), ManifestFile), data, filePerm)
}

// LoadManifest reads one base image's manifest.
func (s *Store) LoadManifest(distro, tag string) (*Manifest, error) {
	path := filepath.Join(s.ImageDir(distro, tag), ManifestFile)
	data, err := s.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &NotFoundError{Kind: "base image", Name: distro + ":" + tag}
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if probe.SchemaVersion != ManifestSchemaVersion {
		return nil, &SchemaError{
			File: path, Found: probe.SchemaVersion, Known: ManifestSchemaVersion,
			Remedy: fmt.Sprintf("Rebuild the base image with `agent-vm image build %s:%s --force`.", distro, tag),
		}
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &m, nil
}

// HasImage reports whether a usable base image is cached. All three artifacts
// must be present: a manifest without its disk, kernel, or initrd is the
// remains of an interrupted build, not a cache hit.
func (s *Store) HasImage(distro, tag string) bool {
	for _, path := range []string{
		filepath.Join(s.ImageDir(distro, tag), ManifestFile),
		s.BaseDiskPath(distro, tag),
		s.KernelPath(distro, tag),
		s.InitrdPath(distro, tag),
	} {
		if found, err := s.fsys.Exists(path); err != nil || !found {
			return false
		}
	}
	return true
}

// ListImages returns the manifest of every cached base image.
//
// Only a directory whose manifest names the image that directory is the cache
// for is listed. Anything else under images/ is not a cached image: a build's
// temporary workspace or the copy of an old image a rebuild moves aside (both
// dot-prefixed, which no image name or tag can be), or a leftover directory
// holding some other image's manifest. Listing one of those under the ref its
// manifest names would show an image that `image rm` and `create` cannot
// reach.
func (s *Store) ListImages() ([]*Manifest, error) {
	imagesDir := filepath.Join(s.root, "images")
	distros, err := s.fsys.Subdirectories(imagesDir)
	if err != nil {
		return nil, fmt.Errorf("listing base images: %w", err)
	}

	var manifests []*Manifest
	for _, d := range distros {
		if strings.HasPrefix(d, ".") {
			continue
		}
		tags, err := s.fsys.Subdirectories(filepath.Join(imagesDir, d))
		if err != nil {
			return nil, fmt.Errorf("listing base images for %s: %w", d, err)
		}
		for _, tag := range tags {
			if strings.HasPrefix(tag, ".") || !s.HasImage(d, tag) {
				continue
			}
			m, err := s.LoadManifest(d, tag)
			if err != nil {
				return nil, err
			}
			if m.Distro != d || m.Tag != tag {
				continue
			}
			manifests = append(manifests, m)
		}
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].Ref() < manifests[j].Ref() })
	return manifests, nil
}

// VMsUsingImage returns the VMs whose overlay is backed by this base image.
// Deleting a base image while an overlay depends on it is refused, because a
// backing file is not optional (ADR-0004).
func (s *Store) VMsUsingImage(distro, tag string) ([]*VM, error) {
	vms, err := s.ListVMs()
	if err != nil {
		return nil, err
	}
	var users []*VM
	for _, vm := range vms {
		if vm.BaseImage.Distro == distro && vm.BaseImage.Tag == tag {
			users = append(users, vm)
		}
	}
	return users, nil
}

// DiskUsage reports the space a base image occupies on disk, for `image list`.
func (s *Store) DiskUsage(distro, tag string) (config.Size, error) {
	total, err := s.fsys.Usage(s.ImageDir(distro, tag))
	if err != nil {
		return 0, fmt.Errorf("measuring base image %s:%s: %w", distro, tag, err)
	}
	return config.Size(total), nil
}
