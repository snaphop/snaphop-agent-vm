package domain

import (
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/golden"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/image/distro"
)

func natOptions() CreateOptions {
	return CreateOptions{
		Name:           "agent-01",
		LibvirtURI:     "qemu:///system",
		VCPUs:          2,
		Memory:         4 * config.GiB,
		OverlayPath:    "/home/operator/.local/share/agent-vm/vms/agent-01/root.qcow2",
		KernelPath:     "/home/operator/.local/share/agent-vm/images/ubuntu/24.04/vmlinuz",
		InitrdPath:     "/home/operator/.local/share/agent-vm/images/ubuntu/24.04/initrd",
		KernelCmdline:  distro.KernelCmdline,
		SeedImagePath:  "/home/operator/.local/share/agent-vm/vms/agent-01/seed.img",
		Network:        config.NetworkNAT,
		NATNetwork:     "agent-vm",
		ConsoleLogPath: "/home/operator/.local/share/agent-vm/vms/agent-01/console.log",
	}
}

func bridgeOptions() CreateOptions {
	opts := natOptions()
	opts.Network = config.NetworkBridge
	opts.Bridge = "br0"
	opts.NATNetwork = ""
	return opts
}

func TestVirtInstallArgs_NAT(t *testing.T) {
	args, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	golden.Assert(t, "virt-install-nat.argv", []byte(strings.Join(args, "\n")+"\n"))
}

func TestVirtInstallArgs_Bridge(t *testing.T) {
	args, err := VirtInstallArgs(bridgeOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	golden.Assert(t, "virt-install-bridge.argv", []byte(strings.Join(args, "\n")+"\n"))
}

// virtioMemOptions is a NAT VM that may grow from 4 GiB to 16 GiB.
func virtioMemOptions() CreateOptions {
	opts := natOptions()
	opts.MaxMemory = 16 * config.GiB
	return opts
}

func TestVirtInstallArgs_VirtioMem(t *testing.T) {
	args, err := VirtInstallArgs(virtioMemOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	golden.Assert(t, "virt-install-virtio-mem.argv", []byte(strings.Join(args, "\n")+"\n"))
}

func TestVirtInstallArgs_SizesTheMemoryDeviceToTheGrowthRoom(t *testing.T) {
	args, err := VirtInstallArgs(virtioMemOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}

	// 12 GiB, not 16: the 4 GiB the guest boots with is the NUMA cell's, and
	// libvirt adds the device on top of it. Asking for 16 here would give the
	// guest a 20 GiB ceiling.
	if value := flagValue(args, "--memdev"); value != "model=virtio-mem,target.node=0,target.block=2048,target.size=12288,target.requested=0" {
		t.Errorf("--memdev = %q, want a 12288 MiB device that starts unplugged", value)
	}
	if value := flagValue(args, "--memory"); value != "4096,maxMemory=16384,maxMemory.slots=16" {
		t.Errorf("--memory = %q, want the ceiling declared alongside the boot memory", value)
	}
}

func TestVirtInstallArgs_GivesTheMemoryDeviceANUMANodeCoveringEveryVCPU(t *testing.T) {
	// libvirt refuses a domain whose NUMA cells do not account for every vCPU,
	// and a memory device has to name a node that exists.
	for _, tc := range []struct {
		vcpus    int
		wantCPUs string
	}{
		{vcpus: 1, wantCPUs: "numa.cell0.cpus=0"},
		{vcpus: 2, wantCPUs: "numa.cell0.cpus=0-1"},
		{vcpus: 16, wantCPUs: "numa.cell0.cpus=0-15"},
	} {
		opts := virtioMemOptions()
		opts.VCPUs = tc.vcpus
		args, err := VirtInstallArgs(opts)
		if err != nil {
			t.Fatalf("VirtInstallArgs with %d vCPUs: %v", tc.vcpus, err)
		}
		cpu := flagValue(args, "--cpu")
		if !strings.Contains(cpu, tc.wantCPUs) {
			t.Errorf("--cpu = %q with %d vCPUs, want it to contain %q", cpu, tc.vcpus, tc.wantCPUs)
		}
		if !strings.Contains(cpu, "numa.cell0.memory=4096,numa.cell0.unit=MiB") {
			t.Errorf("--cpu = %q, want the cell to own the boot memory", cpu)
		}
	}
}

func TestVirtInstallArgs_WithoutMaxMemoryDefinesNoMemoryDevice(t *testing.T) {
	args, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	// The default topology is a public contract, so leaving --max-memory unset
	// must produce the domain it always did: no device, no NUMA topology, and
	// a bare --memory figure.
	if contains(args, "--memdev") {
		t.Errorf("a VM without --max-memory must get no memory device: %v", args)
	}
	if value := flagValue(args, "--memory"); value != "4096" {
		t.Errorf("--memory = %q, want the bare figure", value)
	}
	if value := flagValue(args, "--cpu"); value != "host-passthrough" {
		t.Errorf("--cpu = %q, want no NUMA topology", value)
	}
}

func TestVirtInstallArgs_RejectsAMemoryCeilingBelowTheGuestsMemory(t *testing.T) {
	opts := natOptions()
	opts.MaxMemory = 2 * config.GiB

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: a ceiling under the boot memory is not a ceiling")
	}
}

func TestVirtInstallArgs_RejectsGrowthRoomThatIsNotAWholeVirtioMemBlock(t *testing.T) {
	opts := natOptions()
	// 3 MiB of growth room: QEMU would reject the device for not being a
	// multiple of its 2 MiB block size.
	opts.MaxMemory = opts.Memory + 3*config.MiB

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: virtio-mem plugs memory in whole blocks")
	}
}

func TestVirtInstallArgs_EqualMaxMemoryDefinesNoMemoryDevice(t *testing.T) {
	opts := natOptions()
	opts.MaxMemory = opts.Memory

	args, err := VirtInstallArgs(opts)
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	// A device with no growth room would be a zero-sized one, which QEMU
	// refuses, so the ceiling is treated as "no hotplug" instead.
	if contains(args, "--memdev") {
		t.Errorf("a ceiling equal to the boot memory leaves no room to grow: %v", args)
	}
}

func TestVirtInstallArgs_AttachesTheGuestToOneNetworkOnly(t *testing.T) {
	nat, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	if count := countFlag(nat, "--network"); count != 1 {
		t.Errorf("--network appears %d times, want exactly 1: %v", count, nat)
	}
	if value := flagValue(nat, "--network"); value != "network=agent-vm,model=virtio" {
		t.Errorf("--network = %q, want the NAT network", value)
	}

	// Bridged mode is the one that puts the guest on the operator's LAN, so the
	// argument must name a bridge and never a libvirt network.
	bridged, err := VirtInstallArgs(bridgeOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	if value := flagValue(bridged, "--network"); value != "bridge=br0,model=virtio" {
		t.Errorf("--network = %q, want the host bridge", value)
	}
	if strings.Contains(strings.Join(bridged, " "), "network=agent-vm") {
		t.Errorf("a bridged VM must not be attached to the NAT network: %v", bridged)
	}
}

func TestVirtInstallArgs_BootsTheKernelDirectlyWithItsCommandLine(t *testing.T) {
	args, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	want := "kernel=/home/operator/.local/share/agent-vm/images/ubuntu/24.04/vmlinuz," +
		"initrd=/home/operator/.local/share/agent-vm/images/ubuntu/24.04/initrd," +
		"kernel_args=" + distro.KernelCmdline
	if got := flagValue(args, "--boot"); got != want {
		t.Errorf("--boot = %q, want %q", got, want)
	}
	if !contains(args, "--import") {
		t.Error("a base image already contains a system; --import must be passed")
	}
}

// TestVirtInstallArgs_AttachesTheSeedAsAReadOnlyVirtioDisk is the regression
// for ADR-0011: virt-install --cloud-init attaches its seed as a USB CD-ROM on
// a machine type with no SATA bus, and USB mass storage is enumerated about a
// second after cloud-init has already chosen a datasource, so the guest booted
// with no login user and no authorized key.
func TestVirtInstallArgs_AttachesTheSeedAsAReadOnlyVirtioDisk(t *testing.T) {
	args, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}

	want := "path=/home/operator/.local/share/agent-vm/vms/agent-01/seed.img,format=raw,bus=virtio,readonly=on"
	var found bool
	for i, arg := range args {
		if arg == "--disk" && i+1 < len(args) && args[i+1] == want {
			found = true
		}
	}
	if !found {
		t.Errorf("no --disk %q in %v", want, args)
	}

	joined := strings.Join(args, " ")
	// The seed is ours now, so virt-install must not be asked to build one of
	// its own — a second cidata filesystem would race the first.
	if strings.Contains(joined, "--cloud-init") {
		t.Errorf("virt-install must not build a seed of its own: %v", args)
	}
	// root-password-generate and root-ssh-key would put credentials in the seed;
	// the generated user-data is the only channel into the guest.
	for _, forbidden := range []string{"root-password", "root-ssh-key", "clouduser-ssh-key"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("virt-install must not be asked to inject credentials (%s): %v", forbidden, args)
		}
	}
}

func TestVirtInstallArgs_LogsTheSerialConsoleToAFile(t *testing.T) {
	args, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	// The console log is the only evidence available for a VM that never became
	// reachable, so it is configured when the domain is defined.
	if got := flagValue(args, "--serial"); got != "pty,log.file="+natOptions().ConsoleLogPath {
		t.Errorf("--serial = %q, want a pty logging to console.log", got)
	}
	if !contains(args, "--noautoconsole") {
		t.Error("virt-install must return instead of waiting on a console nobody is attached to")
	}
}

func TestVirtInstallArgs_AppendsPassthroughArgumentsLast(t *testing.T) {
	opts := natOptions()
	opts.ExtraArgs = []string{"--tpm", "backend.type=emulator"}

	args, err := VirtInstallArgs(opts)
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	// Last is what makes --virt-install-arg an escape hatch: virt-install lets a
	// later argument win, so an operator can override what this tool chose.
	if got := args[len(args)-2:]; got[0] != "--tpm" || got[1] != "backend.type=emulator" {
		t.Errorf("passthrough arguments = %v, want them at the end of %v", got, args)
	}
}

func TestVirtInstallArgs_RejectsAnInvalidVMName(t *testing.T) {
	opts := natOptions()
	opts.Name = "Agent VM"

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: the name becomes a domain name, a hostname, and a path segment")
	}
}

func TestVirtInstallArgs_RejectsAPathVirtInstallWouldMisread(t *testing.T) {
	opts := natOptions()
	opts.OverlayPath = "/home/operator/vms/agent,01/root.qcow2"

	_, err := VirtInstallArgs(opts)
	if err == nil {
		t.Fatal("want a refusal: virt-install reads a comma as the start of the next suboption")
	}
	if !strings.Contains(err.Error(), "comma") {
		t.Errorf("the error should say what is wrong with the path: %v", err)
	}
}

func TestVirtInstallArgs_RejectsARelativePath(t *testing.T) {
	opts := natOptions()
	opts.KernelPath = "images/ubuntu/24.04/vmlinuz"

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: libvirt resolves paths from its own working directory, not ours")
	}
}

func TestVirtInstallArgs_RejectsBridgedModeWithNoBridge(t *testing.T) {
	opts := bridgeOptions()
	opts.Bridge = ""

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal rather than a silent fallback to NAT")
	}
}

func TestVirtInstallArgs_RejectsAnUnknownNetworkMode(t *testing.T) {
	opts := natOptions()
	opts.Network = "host"

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal for a network mode this tool does not implement")
	}
}

func TestVirtInstallArgs_RejectsMemoryThatRoundsToZeroMiB(t *testing.T) {
	opts := natOptions()
	opts.Memory = 512

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: virt-install takes whole MiB and would be given 0")
	}
}

func countFlag(args []string, flag string) int {
	count := 0
	for _, arg := range args {
		if arg == flag {
			count++
		}
	}
	return count
}

// flagValue returns the argument following flag, which is how every value in
// this argument vector is passed.
func flagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// An aarch64 host is the one architecture that needs a --features argument:
// libvirt refuses ACPI without UEFI there, and a directly booted kernel has no
// UEFI (featuresArg).
func TestVirtInstallArgs_AArch64TurnsOffACPI(t *testing.T) {
	opts := natOptions()
	opts.Arch = "aarch64"

	args, err := VirtInstallArgs(opts)
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	golden.Assert(t, "virt-install-aarch64.argv", []byte(strings.Join(args, "\n")+"\n"))
}

func TestVirtInstallArgs_KeepsACPIOnX8664(t *testing.T) {
	opts := natOptions()
	opts.Arch = "x86_64"

	args, err := VirtInstallArgs(opts)
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	if flagValue(args, "--features") != "" {
		t.Errorf("--features = %q, want no --features: x86_64 guests need ACPI",
			flagValue(args, "--features"))
	}
}
