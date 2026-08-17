package state

import (
	"fmt"
	"syscall"
)

// freeBytes reports space available to an unprivileged user, which is what
// matters for a base image build or a growing overlay — not the total free
// space, which includes blocks reserved for root.
//
// syscall rather than golang.org/x/sys: this project targets Linux only and
// adding a dependency is a decision that needs approval (AGENTS.md §11).
func freeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("checking free space in %s: %w", path, err)
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
