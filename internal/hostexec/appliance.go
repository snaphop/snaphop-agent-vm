package hostexec

import (
	"context"
	"path/filepath"
)

// Appliance kernel layout. libguestfs boots a small "appliance" VM to do its
// work, and supermin builds that appliance around the *host's* kernel. That is
// fine on a general-purpose distribution kernel and wrong on a
// hardware-specific one: an Apple Silicon (Asahi) kernel, for instance, is
// built for that machine alone and has neither the PL011 serial port nor the
// generic PCIe host bridge QEMU's `virt` board provides, so the appliance
// boots to silence and libguestfs reports only that it "closed the connection
// unexpectedly".
//
// The fix is to hand supermin a general-purpose kernel to build the appliance
// from, which it takes through these three variables. The kernel does not have
// to be one the host could boot — it only ever runs inside QEMU.
const (
	// ApplianceKernelFile is the kernel image inside the configured directory.
	ApplianceKernelFile = "Image"
	// ApplianceModulesDir is the depmod'd module tree inside it.
	ApplianceModulesDir = "modules"
)

// ApplianceKernelEnv is the environment that points supermin at the kernel in
// dir. The directory is named after the kernel version, so all three values
// follow from the one path an operator configures.
func ApplianceKernelEnv(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{
		"SUPERMIN_KERNEL=" + filepath.Join(dir, ApplianceKernelFile),
		"SUPERMIN_MODULES=" + filepath.Join(dir, ApplianceModulesDir),
		"SUPERMIN_KERNEL_VERSION=" + filepath.Base(dir),
	}
}

// ApplianceKernel wraps a Runner so that every libguestfs invocation carries
// the appliance kernel environment, wherever in the codebase it was built.
//
// It is a decorator rather than an argument threaded through internal/image
// and internal/domain because the choice belongs to the host, not to any one
// call site: which kernel libguestfs boots its appliance with is no more the
// concern of the code building a seed than which machine the command runs on
// (ADR-0010). It wraps outermost, so the transport below it renders the
// environment into the remote command line and --dry-run prints it.
type ApplianceKernel struct {
	Inner Runner
	// Dir reports the versioned appliance kernel directory on the hypervisor,
	// or "" when the host's own kernel is fine. It is a function because the
	// runner is built before configuration is resolved: asking for the setting
	// any earlier than the command that needs it would resolve — and cache —
	// configuration without that command's own flags applied.
	Dir func() string
}

// NewApplianceKernel returns a Runner that adds the appliance kernel
// environment to libguestfs commands. A dir that reports "" leaves every
// command untouched, which is the case on any host whose own kernel can boot
// the appliance.
func NewApplianceKernel(inner Runner, dir func() string) Runner {
	if dir == nil {
		return inner
	}
	return &ApplianceKernel{Inner: inner, Dir: dir}
}

// apply adds the environment to a libguestfs command and leaves anything else
// alone. Only libguestfs reads these variables, and setting them on every tool
// would put three lines of noise on every --dry-run.
func (a *ApplianceKernel) apply(c Command) Command {
	if !IsLibguestfsTool(c.Name) {
		return c
	}
	env := ApplianceKernelEnv(a.Dir())
	if len(env) == 0 {
		return c
	}
	c.Env = append(append([]string(nil), c.Env...), env...)
	return c
}

// Run executes c with the appliance kernel environment applied.
func (a *ApplianceKernel) Run(ctx context.Context, c Command) (*Result, error) {
	return a.Inner.Run(ctx, a.apply(c))
}

// Start runs c without waiting, with the environment applied.
func (a *ApplianceKernel) Start(ctx context.Context, c Command) (*Process, error) {
	return a.Inner.Start(ctx, a.apply(c))
}

// Become replaces this process with c, with the environment applied.
func (a *ApplianceKernel) Become(c Command) error { return a.Inner.Become(a.apply(c)) }

// Render shows the command as it would run, environment included.
func (a *ApplianceKernel) Render(c Command) string { return a.Inner.Render(a.apply(c)) }

// LookPath delegates; an environment variable does not move a tool on PATH.
func (a *ApplianceKernel) LookPath(name string, loc Location) (string, error) {
	return a.Inner.LookPath(name, loc)
}

// HypervisorHost reports what the wrapped runner would use.
func (a *ApplianceKernel) HypervisorHost() string { return a.Inner.HypervisorHost() }
