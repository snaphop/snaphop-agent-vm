// Package domain defines and drives libvirt domains. It builds the
// virt-install argument vector that creates a VM and runs the virsh commands
// that inspect and control one afterwards.
//
// No domain XML is written here. virt-install owns the translation from "2
// vCPUs, virtio disk, NAT" into XML, including the parts that differ per
// architecture (ADR-0009), and libvirt owns the XML once a domain is defined —
// `virsh edit` on a defined domain keeps working, and the domain.xml this tool
// captures is a record of what was defined, never an input.
//
// The argument vector below is a public contract pinned by golden files: it
// determines the guest's device topology, and changing it changes what an
// existing VM's replacement looks like (AGENTS.md §8).
package domain

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
)

// CreateOptions is everything virt-install needs to define one VM. Paths are
// absolute and have already been resolved inside the state directory by the
// caller; this package does not decide where anything lives.
type CreateOptions struct {
	Name       string
	LibvirtURI string

	VCPUs  int
	Memory config.Size

	// Arch is the architecture libvirt reports for the hypervisor, as `virsh
	// capabilities` names it. It selects the handful of settings that cannot
	// be the same everywhere — see featuresArg. An empty value means the
	// caller did not ask libvirt, and leaves virt-install's defaults alone.
	Arch string

	// MaxMemory, when greater than Memory, gives the guest a virtio-mem device
	// covering the difference so its RAM can be grown at runtime without a
	// reboot. Zero leaves the domain exactly as it was before this existed: a
	// fixed-size guest with no maxMemory, no guest NUMA node, and no memory
	// device.
	MaxMemory config.Size

	// OverlayPath is the VM's copy-on-write root disk. The base image it backs
	// onto is recorded in the overlay itself, not passed here.
	OverlayPath string

	// KernelPath, InitrdPath and KernelCmdline are the direct kernel boot
	// (ADR-0004). The base image carries no bootloader, so a VM that is defined
	// without these will not boot at all.
	KernelPath    string
	InitrdPath    string
	KernelCmdline string

	// SeedImagePath is the cloud-init seed disk built by CreateSeed. It is
	// attached as a read-only virtio disk that stays with the VM, rather than
	// as the removable CD-ROM virt-install --cloud-init would have built
	// (ADR-0011).
	SeedImagePath string

	Network    config.NetworkMode
	NATNetwork string
	Bridge     string

	// ConsoleLogPath receives everything the guest writes to its serial
	// console. It is the primary evidence for a VM that never became
	// reachable, so it is configured at define time rather than attached later.
	ConsoleLogPath string

	// ExtraArgs are --virt-install-arg values, passed through verbatim as the
	// escape hatch for anything this CLI does not expose.
	ExtraArgs []string
}

// osinfo is what virt-install optimizes the guest configuration for. Since 4.0
// it insists on being told, and "generic" is the honest answer: a base image
// built from an OCI image is not any of libosinfo's known installs, and every
// device that matters is specified explicitly below anyway.
const osinfo = "detect=off,name=generic"

// cpuModel is what every guest gets. The guest is short-lived and never
// migrated, so it may as well see the host CPU and its virtualization-friendly
// features.
const cpuModel = "host-passthrough"

// Architecture names as `virsh capabilities` reports them. libvirt uses
// "aarch64"; Go and OCI call the same architecture "arm64", so both are
// recognised rather than only the one this tool happens to ask for.
var armArches = map[string]bool{"aarch64": true, "arm64": true}

// memorySlots is the <maxMemory slots=> count declared when memory hotplug is
// enabled.
const memorySlots = 16

// VirtInstallArgs builds the argument vector that defines and starts one VM.
//
// virt-install always boots a guest that has cloud-init data, because the seed
// is attached only to that first boot and removed from the XML it defines. That
// is the tool's design, and it is why this package has no "define without
// starting" variant to offer.
func VirtInstallArgs(opts CreateOptions) ([]string, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	network, err := opts.networkArg()
	if err != nil {
		return nil, err
	}

	args := []string{
		"--connect", opts.LibvirtURI,
		"--name", opts.Name,
		"--memory", opts.memoryArg(),
		"--vcpus", strconv.Itoa(opts.VCPUs),
		"--cpu", opts.cpuArg(),
		"--virt-type", "kvm",
		"--osinfo", osinfo,
		// --import: there is no installation. The root disk already contains a
		// system, built from an OCI image (ADR-0003).
		"--import",
		"--disk", "path=" + opts.OverlayPath + ",format=qcow2,bus=virtio",
		// The seed is a virtio disk because it has to be visible before
		// cloud-init picks a datasource, which a USB CD-ROM is not on a
		// machine type with no SATA bus (ADR-0011). readonly is both honest —
		// nothing in the guest may rewrite its own cloud-init data — and what
		// keeps a second boot reading what the first one did.
		"--disk", "path=" + opts.SeedImagePath + ",format=raw,bus=virtio,readonly=on",
		"--boot", strings.Join([]string{
			"kernel=" + opts.KernelPath,
			"initrd=" + opts.InitrdPath,
			"kernel_args=" + opts.KernelCmdline,
		}, ","),
		"--network", network,
		// The guest agent is how `virsh domifaddr --source agent` learns the
		// guest's address without waiting for a DHCP lease to appear.
		"--channel", "unix,target.type=virtio,target.name=org.qemu.guest_agent.0",
		// One serial device serves both purposes: a pty for `agent-vm console`,
		// and a log file that is written whether or not anyone is attached.
		"--serial", "pty,log.file=" + opts.ConsoleLogPath,
		"--rng", "/dev/urandom",
		"--memballoon", "virtio",
		// No graphics: the guest is reached over SSH and the serial console.
		"--graphics", "none",
		// virt-install must return once the domain is running. Without this it
		// waits on a console nobody is attached to.
		"--noautoconsole",
	}
	if features, ok := opts.featuresArg(); ok {
		args = append(args, "--features", features)
	}
	if opts.hotplugsMemory() {
		args = append(args, "--memdev", opts.memdevArg())
	}
	return append(args, opts.ExtraArgs...), nil
}

// featuresArg renders --features, and reports whether the argument is needed
// at all. It is needed on one architecture only.
//
// On aarch64 libvirt refuses a domain that has ACPI but no UEFI firmware
// ("unsupported configuration: ACPI requires UEFI on this architecture"), and
// virt-install turns ACPI on by default. A directly booted kernel (ADR-0004)
// has no firmware to run: QEMU's virt machine hands the guest a device tree
// instead, which describes the same PCIe, serial and virtio devices ACPI would
// have. Adding UEFI to get ACPI back would mean a per-VM NVRAM file and a
// firmware boot on every start, for a guest that already knows exactly which
// kernel it boots — so ACPI is turned off instead.
//
// x86_64 keeps ACPI: there the guest needs it to see its PCI devices at all.
func (o CreateOptions) featuresArg() (string, bool) {
	if armArches[o.Arch] {
		return "acpi=off", true
	}
	return "", false
}

// hotplugsMemory reports whether this VM gets a virtio-mem device. The device
// exists only to cover growth room, so an unset or equal MaxMemory means no
// device rather than a zero-sized one, which QEMU would reject.
func (o CreateOptions) hotplugsMemory() bool {
	return o.MaxMemory > o.Memory
}

// memoryArg renders --memory. Without hotplug it is the bare figure it has
// always been; with it, maxMemory declares the ceiling libvirt will let the
// domain reach.
func (o CreateOptions) memoryArg() string {
	mib := strconv.FormatInt(o.Memory.MiBValue(), 10)
	if !o.hotplugsMemory() {
		return mib
	}
	// libvirt wants a slot count alongside maxMemory. One virtio-mem device
	// needs one, and the spare slots cost nothing while leaving room for an
	// operator to attach another by hand with `virsh attach-device`.
	return strings.Join([]string{
		mib,
		"maxMemory=" + strconv.FormatInt(o.MaxMemory.MiBValue(), 10),
		"maxMemory.slots=" + strconv.Itoa(memorySlots),
	}, ",")
}

// cpuArg renders --cpu. A memory device has to be attached to a guest NUMA
// node, and a guest has no NUMA topology unless one is asked for, so enabling
// hotplug means declaring a single cell that owns every vCPU and all of the
// boot memory. libvirt rejects a topology whose cells do not add up to the
// vCPU count, so the cpus range is derived from VCPUs rather than fixed.
func (o CreateOptions) cpuArg() string {
	if !o.hotplugsMemory() {
		return cpuModel
	}
	cpus := "0"
	if o.VCPUs > 1 {
		cpus = "0-" + strconv.Itoa(o.VCPUs-1)
	}
	return strings.Join([]string{
		cpuModel,
		"numa.cell0.id=0",
		"numa.cell0.cpus=" + cpus,
		"numa.cell0.memory=" + strconv.FormatInt(o.Memory.MiBValue(), 10),
		"numa.cell0.unit=MiB",
	}, ",")
}

// memdevArg renders the virtio-mem device. Its size is the growth room, not
// the ceiling: the boot memory is already accounted for by the NUMA cell, and
// libvirt adds the device's size on top of it.
//
// target.block is stated rather than left to QEMU's default because libvirt
// refuses to define a virtio-mem device without one ("block size must be a
// power of two"). It is the granularity memory is plugged in at, and it is why
// the growth room has to be a multiple of config.VirtioMemBlock.
//
// The two target sizes are in different units, which is virt-install's
// convention and not a mistake here: target.size is scaled like every other
// memory figure it takes (MiB), while target.block is written through to the
// XML as-is, where libvirt reads a bare figure as KiB.
//
// target.requested=0 means the device starts with nothing plugged in, so the
// guest boots with exactly the memory it was asked for. Growing it afterwards
// is `virsh update-memory-device`, an operator action this tool does not take
// on its own.
func (o CreateOptions) memdevArg() string {
	growth := o.MaxMemory - o.Memory
	return strings.Join([]string{
		"model=virtio-mem",
		"target.node=0",
		"target.block=" + strconv.FormatInt(int64(config.VirtioMemBlock/config.KiB), 10),
		"target.size=" + strconv.FormatInt(growth.MiBValue(), 10),
		"target.requested=0",
	}, ",")
}

// networkArg renders the --network value. The two modes differ in exposure,
// not just in syntax: bridged puts the guest on the operator's LAN, so it is
// never inferred and never a fallback (SECURITY.md).
func (o CreateOptions) networkArg() (string, error) {
	switch o.Network {
	case config.NetworkNAT:
		return "network=" + o.NATNetwork + ",model=virtio", nil
	case config.NetworkBridge:
		return "bridge=" + o.Bridge + ",model=virtio", nil
	default:
		return "", fmt.Errorf("unknown network mode %q", o.Network)
	}
}

func (o CreateOptions) validate() error {
	if err := config.ValidateVMName(o.Name); err != nil {
		return err
	}
	if o.LibvirtURI == "" {
		return fmt.Errorf("virt-install needs a libvirt URI")
	}
	if o.VCPUs < 1 {
		return fmt.Errorf("virt-install needs at least one vCPU, got %d", o.VCPUs)
	}
	if o.Memory.MiBValue() < 1 {
		return fmt.Errorf("virt-install takes memory in MiB, and %s rounds to zero", o.Memory)
	}
	if o.MaxMemory != 0 && o.MaxMemory < o.Memory {
		return fmt.Errorf("the memory ceiling %s is below the guest's memory %s", o.MaxMemory, o.Memory)
	}
	if o.hotplugsMemory() {
		if growth := o.MaxMemory - o.Memory; growth%config.VirtioMemBlock != 0 {
			return fmt.Errorf("virtio-mem plugs memory in %s blocks, and the growth room %s is not a multiple of one",
				config.VirtioMemBlock, growth)
		}
	}

	// Every one of these becomes a suboption in a comma-separated argument.
	// A path containing a comma would be read by virt-install as the start of
	// the next suboption, so it is refused rather than escaped — these are all
	// paths this tool chose inside the state directory, and none of them has
	// any business containing one.
	paths := map[string]string{
		"root disk":   o.OverlayPath,
		"kernel":      o.KernelPath,
		"initrd":      o.InitrdPath,
		"seed disk":   o.SeedImagePath,
		"console log": o.ConsoleLogPath,
	}
	for what, path := range paths {
		if path == "" {
			return fmt.Errorf("virt-install needs a %s path", what)
		}
		if !filepath.IsAbs(path) {
			return fmt.Errorf("the %s path %q must be absolute", what, path)
		}
		if strings.Contains(path, ",") {
			return fmt.Errorf("the %s path %q contains a comma, which virt-install reads as a suboption separator", what, path)
		}
	}

	if o.KernelCmdline == "" {
		return fmt.Errorf("a directly booted kernel needs a command line")
	}
	if strings.ContainsAny(o.KernelCmdline, ",") {
		// virt-install splits --boot on commas, so a comma in the command line
		// would silently truncate it and the guest would boot without the rest.
		return fmt.Errorf("the kernel command line %q contains a comma, which virt-install reads as a suboption separator", o.KernelCmdline)
	}
	return o.validateNetworkValues()
}

func (o CreateOptions) validateNetworkValues() error {
	switch o.Network {
	case config.NetworkNAT:
		if o.NATNetwork == "" {
			return fmt.Errorf("NAT mode needs the name of a libvirt network")
		}
		return validInterfaceValue("NAT network name", o.NATNetwork)
	case config.NetworkBridge:
		if o.Bridge == "" {
			return fmt.Errorf("bridged mode needs a host bridge interface")
		}
		return validInterfaceValue("bridge interface", o.Bridge)
	default:
		return fmt.Errorf("unknown network mode %q", o.Network)
	}
}

// validInterfaceValue keeps a network or bridge name from carrying suboption
// syntax into the --network argument.
func validInterfaceValue(what, value string) error {
	if strings.ContainsAny(value, ",= \t") {
		return fmt.Errorf("the %s %q contains a character virt-install reads as a suboption separator", what, value)
	}
	return nil
}
