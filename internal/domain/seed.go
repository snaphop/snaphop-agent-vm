package domain

import (
	"context"
	"fmt"
	"path/filepath"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

// The NoCloud seed is built here rather than by `virt-install --cloud-init`
// (ADR-0011). virt-install attaches the seed it builds as a USB CD-ROM on any
// machine type with no IDE or SATA bus — which is every aarch64 `virt` guest —
// and USB mass storage is enumerated too late to be seen: the probe carries a
// one-second device-reset delay, so the seed appears at about 2.7s while
// cloud-init's local stage, which is where the datasource is chosen, has
// already run at about 1.6s. The guest boots with no datasource, no login user,
// and no authorized key. A virtio-blk disk is probed with the root disk instead,
// long before cloud-init looks.
//
// virt-make-fs still does the work of writing the filesystem (ADR-0009); this
// only decides what goes on it and which bus it is attached to.

// seedLabel is the filesystem label cloud-init's NoCloud datasource searches
// for. The name is cloud-init's, not ours. A vfat filesystem stores it
// uppercased, which is why cloud-init accepts either case.
const seedLabel = "cidata"

// seedFilesystem is vfat because it is what libguestfs can label, and because
// NoCloud accepts vfat and iso9660 alike. The seed holds two small text files,
// so nothing else about the filesystem matters.
const seedFilesystem = "vfat"

// seedSize is the size of the seed disk. cloud-init's user-data is a few
// kilobytes and an operator's own may be larger, so this is generous by orders
// of magnitude and still smaller than a rounding error on a 50 GiB root disk.
const seedSize = "8M"

// CreateSeed builds the VM's cloud-init seed disk from a directory holding the
// NoCloud files — user-data and meta-data — and nothing else. Every file in
// the directory lands on the filesystem the guest reads.
func (m *Manager) CreateSeed(ctx context.Context, seedDir, seedPath string) error {
	for what, path := range map[string]string{"seed directory": seedDir, "seed image": seedPath} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("the %s path %q must be absolute", what, path)
		}
	}

	cmd := hostexec.Command{
		Name: hostexec.VirtMakeFS.Name,
		Args: []string{
			"--type=" + seedFilesystem,
			"--label=" + seedLabel,
			"--size=" + seedSize,
			"--format=raw",
			seedDir,
			seedPath,
		},
		Effect: hostexec.Mutate,
	}
	if _, err := m.runner.Run(ctx, cmd); err != nil {
		return fmt.Errorf("building the cloud-init seed disk for %s: %w", filepath.Base(filepath.Dir(seedPath)), err)
	}
	return nil
}
