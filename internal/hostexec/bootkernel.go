package hostexec

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// UnreadableKernelError is a /boot/vmlinuz-* the hypervisor user cannot read.
// supermin copies that file while it builds the libguestfs appliance. Ubuntu
// installs the kernel mode 0600, so the copy fails and virt-make-fs reports
// only that supermin exited 1.
type UnreadableKernelError struct {
	Path string
}

func (e *UnreadableKernelError) Error() string {
	return e.Path + " is not readable\n" +
		"  supermin copies a kernel from /boot to build the libguestfs appliance, and Ubuntu installs that file mode 0600.\n" +
		"  " + e.Remedy()
}

// Remedy is the operator action. The initrd stays mode 0600: it can hold
// secrets, and supermin does not need to read it. A kernel package update
// restores mode 0600 on the vmlinuz files, so the same command is the fix
// the next time too.
func (e *UnreadableKernelError) Remedy() string {
	return "Run `sudo chmod 0644 /boot/vmlinuz*` and then `agent-vm doctor`. Leave the initrd mode 0600. A kernel package update restores mode 0600."
}

// CheckBootKernels reports how many versioned kernel images /boot holds, or
// *UnreadableKernelError when one of them cannot be read. Zero kernels is not
// an error: supermin then looks under /lib/modules, and a configured
// appliance_kernel is the caller's concern.
func CheckBootKernels(ctx context.Context, runner Runner) (int, error) {
	res, err := runner.Run(ctx, Command{
		Name:   "find",
		Args:   []string{"/boot", "-maxdepth", "1", "-name", "vmlinuz-*", "-print"},
		Effect: Read,
	})
	if err != nil {
		return 0, err
	}

	n := 0
	for _, path := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		if !bootVmlinuz(path) {
			continue
		}
		n++
		readable, err := fileReadable(ctx, runner, path)
		if err != nil {
			return n, err
		}
		if !readable {
			return n, &UnreadableKernelError{Path: path}
		}
	}
	return n, nil
}

// bootVmlinuz accepts only a versioned kernel image directly in /boot. find's
// output is untrusted, and the path is later an argument to test.
func bootVmlinuz(path string) bool {
	return filepath.Dir(path) == "/boot" && strings.HasPrefix(filepath.Base(path), "vmlinuz-") && !strings.Contains(path, "..")
}

func fileReadable(ctx context.Context, runner Runner, path string) (bool, error) {
	_, err := runner.Run(ctx, Command{
		Name:   "test",
		Args:   []string{"-r", path},
		Effect: Read,
	})
	if err == nil {
		return true, nil
	}
	var tool *ToolError
	if errors.As(err, &tool) && tool.ExitCode != 0 {
		return false, nil
	}
	return false, err
}
