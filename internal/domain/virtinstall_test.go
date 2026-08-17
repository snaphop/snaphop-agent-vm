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
		UserDataPath:   "/home/operator/.local/share/agent-vm/vms/agent-01/user-data",
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

func TestVirtInstallArgs_PassesTheGeneratedUserDataAndNothingElse(t *testing.T) {
	args, err := VirtInstallArgs(natOptions())
	if err != nil {
		t.Fatalf("VirtInstallArgs: %v", err)
	}
	want := "user-data=/home/operator/.local/share/agent-vm/vms/agent-01/user-data"
	if got := flagValue(args, "--cloud-init"); got != want {
		t.Errorf("--cloud-init = %q, want %q", got, want)
	}
	// root-password-generate and root-ssh-key would put credentials in the seed;
	// the generated user-data is the only channel into the guest.
	joined := strings.Join(args, " ")
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
