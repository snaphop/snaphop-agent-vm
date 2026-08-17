package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

// The overlay lives here rather than in internal/image because it belongs to a
// VM, not to the cache: the base image is immutable and shared, and this is the
// per-VM copy-on-write file that makes creating a VM a near-instant operation
// (ADR-0004).

// CreateOverlay creates the VM's root disk as a copy-on-write overlay on an
// immutable base image. Nothing is copied — the overlay starts near-empty and
// grows only with what the guest writes — which is what lets a VM be created in
// seconds and destroyed without touching the base.
//
// size is the guest-visible size of the disk. It must be at least as large as
// the base image, and qemu-img enforces that.
func (m *Manager) CreateOverlay(ctx context.Context, basePath, overlayPath string, size config.Size) error {
	for what, path := range map[string]string{"base image": basePath, "overlay": overlayPath} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("the %s path %q must be absolute", what, path)
		}
	}

	cmd := hostexec.Command{
		Name: hostexec.QemuImg.Name,
		Args: []string{
			"create",
			"-f", "qcow2",
			// The backing format is stated explicitly because qemu refuses to
			// guess it, and a guessed format is how an image gets misread.
			"-F", "qcow2",
			"-b", basePath,
			overlayPath,
			strconv.FormatInt(int64(size), 10),
		},
		Effect: hostexec.Mutate,
	}
	if _, err := m.runner.Run(ctx, cmd); err != nil {
		return fmt.Errorf("creating the root disk overlay for %s: %w", filepath.Base(overlayPath), err)
	}
	return nil
}

// DiskInfo is what qemu-img reports about a disk image.
type DiskInfo struct {
	// VirtualSize is the size the guest sees.
	VirtualSize config.Size
	// ActualSize is what the overlay currently occupies on the host, which is
	// the number an operator actually wants when a state directory fills up.
	ActualSize config.Size
	// BackingFile is the base image this overlay was created from, empty for a
	// standalone image.
	BackingFile string
}

// qemuImgInfo is qemu-img's JSON output. Only the fields this tool uses are
// declared; qemu-img reports many more and adds to them between releases.
type qemuImgInfo struct {
	VirtualSize     int64  `json:"virtual-size"`
	ActualSize      int64  `json:"actual-size"`
	BackingFilename string `json:"backing-filename"`
	Format          string `json:"format"`
}

// InspectDisk reads a disk image's sizes and backing file.
//
// --output=json is used rather than the human-readable form: qemu-img's default
// output is meant for people and has changed shape between releases, while the
// JSON keys are stable (AGENTS.md §6).
func (m *Manager) InspectDisk(ctx context.Context, path string) (*DiskInfo, error) {
	res, err := m.runner.Run(ctx, hostexec.Command{
		Name:   hostexec.QemuImg.Name,
		Args:   []string{"info", "--output=json", path},
		Effect: hostexec.Read,
	})
	if err != nil {
		return nil, fmt.Errorf("reading disk information for %s: %w", path, err)
	}
	return parseDiskInfo(res.Stdout)
}

func parseDiskInfo(out []byte) (*DiskInfo, error) {
	var parsed qemuImgInfo
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("unreadable qemu-img info output: %w", err)
	}
	if parsed.Format == "" {
		return nil, fmt.Errorf("qemu-img info reported no format for the image")
	}
	return &DiskInfo{
		VirtualSize: config.Size(parsed.VirtualSize),
		ActualSize:  config.Size(parsed.ActualSize),
		BackingFile: strings.TrimSpace(parsed.BackingFilename),
	}, nil
}
