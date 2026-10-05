package domain

import (
	"net"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/golden"
	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
)

func natOptions() CreateOptions {
	return CreateOptions{
		Name:       "agent-01",
		LibvirtURI: "qemu:///system",
		// The golden vectors are what an x86 create emits. create always asks
		// libvirt for the architecture; the empty-arch case is tested on its own.
		Arch:           "x86_64",
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
	t.Parallel()
	args, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	golden.Assert(t, "virt-install-nat.argv", []byte(strings.Join(args, "\n")+"\n"))
}

func TestVirtInstallArgs_Bridge(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	args, err := VirtInstallArgs(virtioMemOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	golden.Assert(t, "virt-install-virtio-mem.argv", []byte(strings.Join(args, "\n")+"\n"))
}

func TestVirtInstallArgs_SizesTheMemoryDeviceToTheGrowthRoom(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	if value := flagValue(args, "--cpu"); value != "host-passthrough,-hypervisor" {
		t.Errorf("--cpu = %q, want host-passthrough with the hypervisor flag disabled and no NUMA topology", value)
	}
}

func TestVirtInstallArgs_RejectsAMemoryCeilingBelowTheGuestsMemory(t *testing.T) {
	t.Parallel()
	opts := natOptions()
	opts.MaxMemory = 2 * config.GiB

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: a ceiling under the boot memory is not a ceiling")
	}
}

func TestVirtInstallArgs_RejectsGrowthRoomThatIsNotAWholeVirtioMemBlock(t *testing.T) {
	t.Parallel()
	opts := natOptions()
	// 3 MiB of growth room: QEMU would reject the device for not being a
	// multiple of its 2 MiB block size.
	opts.MaxMemory = opts.Memory + 3*config.MiB

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: virtio-mem plugs memory in whole blocks")
	}
}

func TestVirtInstallArgs_EqualMaxMemoryDefinesNoMemoryDevice(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	nat, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	if count := countFlag(nat, "--network"); count != 1 {
		t.Errorf("--network appears %d times, want exactly 1: %v", count, nat)
	}
	if value := flagValue(nat, "--network"); value != "network=agent-vm,model=virtio,mac="+hardwareMAC("agent-01", natOptions().OverlayPath) {
		t.Errorf("--network = %q, want the NAT network and the VM's own MAC", value)
	}

	// Bridged mode is the one that puts the guest on the operator's LAN, so the
	// argument must name a bridge and never a libvirt network.
	bridged, err := VirtInstallArgs(bridgeOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	if value := flagValue(bridged, "--network"); value != "bridge=br0,model=virtio,mac="+hardwareMAC("agent-01", bridgeOptions().OverlayPath) {
		t.Errorf("--network = %q, want the host bridge and the VM's own MAC", value)
	}
	if strings.Contains(strings.Join(bridged, " "), "network=agent-vm") {
		t.Errorf("a bridged VM must not be attached to the NAT network: %v", bridged)
	}
}

func TestVirtInstallArgs_BootsTheKernelDirectlyWithItsCommandLine(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	opts := natOptions()
	opts.Name = "Agent VM"

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: the name becomes a domain name, a hostname, and a path segment")
	}
}

func TestVirtInstallArgs_RejectsAPathVirtInstallWouldMisread(t *testing.T) {
	t.Parallel()
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

// virtinst splits suboptions with POSIX shlex, so a quote or a backslash in a
// path is quoting to it: an unpaired one fails, a paired one is stripped.
func TestVirtInstallArgs_RejectsAPathWithQuotingVirtInstallWouldMisread(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/home/o'brien/.local/share/agent-vm/vms/agent-01/root.qcow2",
		`/home/operator/"agent vms"/vms/agent-01/root.qcow2`,
		`/home/operator/agent\vms/vms/agent-01/root.qcow2`,
	} {
		for _, field := range []struct {
			name string
			set  func(*CreateOptions)
		}{
			{"root disk", func(o *CreateOptions) { o.OverlayPath = path }},
			{"kernel", func(o *CreateOptions) { o.KernelPath = path }},
			{"initrd", func(o *CreateOptions) { o.InitrdPath = path }},
			{"seed disk", func(o *CreateOptions) { o.SeedImagePath = path }},
			{"console log", func(o *CreateOptions) { o.ConsoleLogPath = path }},
		} {
			opts := natOptions()
			field.set(&opts)

			_, err := VirtInstallArgs(opts)
			if err == nil {
				t.Errorf("%s %q: want a refusal: virt-install's suboption parser reads it as quoting", field.name, path)
				continue
			}
			for _, want := range []string{field.name, "quote or a backslash", "state directory"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s %q: the error should mention %q: %v", field.name, path, want, err)
				}
			}
		}
	}
}

func TestVirtInstallArgs_RejectsARelativePath(t *testing.T) {
	t.Parallel()
	opts := natOptions()
	opts.KernelPath = "images/ubuntu/24.04/vmlinuz"

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal: libvirt resolves paths from its own working directory, not ours")
	}
}

func TestVirtInstallArgs_RejectsBridgedModeWithNoBridge(t *testing.T) {
	t.Parallel()
	opts := bridgeOptions()
	opts.Bridge = ""

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal rather than a silent fallback to NAT")
	}
}

func TestVirtInstallArgs_RejectsAnUnknownNetworkMode(t *testing.T) {
	t.Parallel()
	opts := natOptions()
	opts.Network = "host"

	if _, err := VirtInstallArgs(opts); err == nil {
		t.Fatal("want a refusal for a network mode this tool does not implement")
	}
}

func TestVirtInstallArgs_RejectsMemoryThatRoundsToZeroMiB(t *testing.T) {
	t.Parallel()
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

// An aarch64 host turns ACPI off: libvirt refuses ACPI without UEFI there, and
// a directly booted kernel has no UEFI (featuresArg). The x86 hypervisor-hiding
// flags are not valid on this architecture.
func TestVirtInstallArgs_AArch64TurnsOffACPI(t *testing.T) {
	t.Parallel()
	opts := natOptions()
	opts.Arch = "aarch64"

	args, err := VirtInstallArgs(opts)
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	golden.Assert(t, "virt-install-aarch64.argv", []byte(strings.Join(args, "\n")+"\n"))
}

func TestVirtInstallArgs_KeepsACPIOnX8664(t *testing.T) {
	t.Parallel()
	opts := natOptions()
	opts.Arch = "x86_64"

	args, err := VirtInstallArgs(opts)
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	if got := flagValue(args, "--features"); got != "kvm.hidden.state=on" {
		t.Errorf("--features = %q, want kvm.hidden.state=on and ACPI left on", got)
	}
}

// TestVirtInstallArgs_PresentsTheGuestAsAPhysicalDesktop is the firmware and
// CPUID half of the disguise. The strings QEMU would have reported are absent,
// the x86 hypervisor flag is disabled, and the NIC address is not the QEMU prefix.
func TestVirtInstallArgs_PresentsTheGuestAsAPhysicalDesktop(t *testing.T) {
	t.Parallel()
	args, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	sysinfo := flagValue(args, "--sysinfo")
	for _, banned := range []string{"QEMU", "SeaBIOS", "Bochs", "BOCHS", "KVM", "Standard PC"} {
		if strings.Contains(sysinfo, banned) {
			t.Errorf("--sysinfo contains %q: %s", banned, sysinfo)
		}
	}
	for _, want := range []string{
		"type=smbios",
		"bios.vendor=" + firmwareBIOSVendor,
		"bios.version=" + firmwareBIOSVersion,
		"bios.date=" + firmwareBIOSDate,
		"system.serial=" + hardwareSerial("agent-01"),
		"chassis.asset=" + firmwareOEM,
		"baseBoard.manufacturer=" + firmwareOEM,
	} {
		if !strings.Contains(sysinfo, want) {
			t.Errorf("--sysinfo = %q, want it to contain %q", sysinfo, want)
		}
	}
	for _, part := range strings.Split(sysinfo, ",") {
		key, val, ok := strings.Cut(part, "=")
		if !ok || key == "" || val == "" {
			t.Errorf("sysinfo suboption %q is not key=value", part)
		}
		if strings.ContainsAny(val, `"'=\`) {
			t.Errorf("sysinfo value %q contains a character virt-install's parser treats specially", val)
		}
	}
	if got := flagValue(args, "--cpu"); !strings.Contains(got, "-hypervisor") {
		t.Errorf("--cpu = %q, want the hypervisor flag disabled", got)
	}
	if got := flagValue(args, "--features"); got != "kvm.hidden.state=on" {
		t.Errorf("--features = %q, want the KVM signature hidden", got)
	}
	network := flagValue(args, "--network")
	mac := hardwareMAC(natOptions().Name, natOptions().OverlayPath)
	if !strings.Contains(network, "mac="+mac) {
		t.Errorf("--network = %q, want mac=%s", network, mac)
	}
	if strings.Contains(network, "52:54:00:") {
		t.Errorf("--network = %q, want no QEMU MAC prefix", network)
	}
}

func TestVirtInstallArgs_HidesTheHypervisorOnEveryX86Name(t *testing.T) {
	t.Parallel()
	for _, arch := range []string{"x86_64", "amd64", "i686", "386"} {
		opts := natOptions()
		opts.Arch = arch
		args, err := VirtInstallArgs(opts)
		if err != nil {
			t.Fatalf("VirtInstallArgs arch %s: %v", arch, err)
		}
		if !strings.Contains(flagValue(args, "--cpu"), "-hypervisor") {
			t.Errorf("arch %s --cpu = %q, want -hypervisor", arch, flagValue(args, "--cpu"))
		}
		if got := flagValue(args, "--features"); got != "kvm.hidden.state=on" {
			t.Errorf("arch %s --features = %q, want kvm.hidden.state=on", arch, got)
		}
	}
}

func TestVirtInstallArgs_LeavesX86HypervisorFlagsOffARM(t *testing.T) {
	t.Parallel()
	for _, arch := range []string{"aarch64", "arm64"} {
		opts := natOptions()
		opts.Arch = arch
		args, err := VirtInstallArgs(opts)
		if err != nil {
			t.Fatalf("VirtInstallArgs arch %s: %v", arch, err)
		}
		cpu := flagValue(args, "--cpu")
		if strings.Contains(cpu, "hypervisor") {
			t.Errorf("arch %s --cpu = %q, want no hypervisor feature", arch, cpu)
		}
		if got := flagValue(args, "--features"); got != "acpi=off" {
			t.Errorf("arch %s --features = %q, want acpi=off only", arch, got)
		}
		if flagValue(args, "--sysinfo") == "" {
			t.Errorf("arch %s has no --sysinfo", arch)
		}
	}
}

func TestVirtInstallArgs_OmitsX86FlagsWhenTheArchitectureIsUnknown(t *testing.T) {
	t.Parallel()
	opts := natOptions()
	opts.Arch = ""
	args, err := VirtInstallArgs(opts)
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	if got := flagValue(args, "--cpu"); got != "host-passthrough" {
		t.Errorf("--cpu = %q, want host-passthrough with no x86-only feature", got)
	}
	if got := flagValue(args, "--features"); got != "" {
		t.Errorf("--features = %q, want none when the architecture is unknown", got)
	}
	if flagValue(args, "--sysinfo") == "" {
		t.Error("firmware identity does not depend on the architecture")
	}
}

func TestHardwareIdentity_IsStableAndNotAQEMUAddress(t *testing.T) {
	t.Parallel()
	const (
		name    = "agent-01"
		overlay = "/home/operator/.local/share/agent-vm/vms/agent-01/root.qcow2"
	)
	serial := hardwareSerial(name)
	if serial != hardwareSerial(name) {
		t.Fatal("serial changed for the same name")
	}
	if serial == hardwareSerial("agent-02") {
		t.Fatal("two VM names share a serial")
	}
	if strings.ContainsAny(serial, ",='\"\\ ") {
		t.Fatalf("serial %q is not safe inside a virt-install suboption", serial)
	}

	mac := hardwareMAC(name, overlay)
	if mac != hardwareMAC(name, overlay) {
		t.Fatal("MAC changed for the same name and disk")
	}
	if mac == hardwareMAC("agent-02", overlay) {
		t.Fatal("two VM names share a MAC")
	}
	if mac == hardwareMAC(name, overlay+"-other") {
		t.Fatal("two disk paths share a MAC")
	}
	if strings.HasPrefix(mac, "52:54:00:") {
		t.Fatalf("MAC %s uses the QEMU prefix", mac)
	}
	parsed, err := net.ParseMAC(mac)
	if err != nil {
		t.Fatalf("MAC %s: %v", mac, err)
	}
	if parsed[0]&0x01 != 0 {
		t.Fatalf("MAC %s is multicast", mac)
	}
	if parsed[0]&0x02 == 0 {
		t.Fatalf("MAC %s is not locally administered", mac)
	}
}
