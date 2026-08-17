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

	// OverlayPath is the VM's copy-on-write root disk. The base image it backs
	// onto is recorded in the overlay itself, not passed here.
	OverlayPath string

	// KernelPath, InitrdPath and KernelCmdline are the direct kernel boot
	// (ADR-0004). The base image carries no bootloader, so a VM that is defined
	// without these will not boot at all.
	KernelPath    string
	InitrdPath    string
	KernelCmdline string

	// UserDataPath is the generated cloud-init user-data. virt-install builds
	// the NoCloud seed from it and attaches it for the first boot only.
	UserDataPath string

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
		"--memory", strconv.FormatInt(opts.Memory.MiBValue(), 10),
		"--vcpus", strconv.Itoa(opts.VCPUs),
		// The guest is short-lived and never migrated, so it may as well see
		// the host CPU and its virtualization-friendly features.
		"--cpu", "host-passthrough",
		"--virt-type", "kvm",
		"--osinfo", osinfo,
		// --import: there is no installation. The root disk already contains a
		// system, built from an OCI image (ADR-0003).
		"--import",
		"--disk", "path=" + opts.OverlayPath + ",format=qcow2,bus=virtio",
		"--boot", strings.Join([]string{
			"kernel=" + opts.KernelPath,
			"initrd=" + opts.InitrdPath,
			"kernel_args=" + opts.KernelCmdline,
		}, ","),
		"--network", network,
		"--cloud-init", "user-data=" + opts.UserDataPath,
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
	return append(args, opts.ExtraArgs...), nil
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

	// Every one of these becomes a suboption in a comma-separated argument.
	// A path containing a comma would be read by virt-install as the start of
	// the next suboption, so it is refused rather than escaped — these are all
	// paths this tool chose inside the state directory, and none of them has
	// any business containing one.
	paths := map[string]string{
		"root disk":   o.OverlayPath,
		"kernel":      o.KernelPath,
		"initrd":      o.InitrdPath,
		"user-data":   o.UserDataPath,
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
