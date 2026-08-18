package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1TfEt0YKXQ+eZmJHCcTKQ0lMSzQFm/kQGHMvhE7Hqx agent@example\n"

// createHost answers every tool a create runs, with output captured from the
// real tools where a parser reads it.
func createHost(t *testing.T) *hostexec.Fake {
	t.Helper()
	fake := healthyHost()
	fake.RespondPrefix("virsh --connect qemu:///system domifaddr", hostexec.FakeResponse{
		Stdout: readToolout(t, "virsh-domifaddr.txt"),
	})
	fake.RespondPrefix("virsh --connect qemu:///system domiflist", hostexec.FakeResponse{
		Stdout: readToolout(t, "virsh-domiflist.txt"),
	})
	fake.RespondPrefix("virsh --connect qemu:///system dumpxml", hostexec.FakeResponse{
		Stdout: "<domain type='kvm'><name>agent-01</name></domain>\n",
	})
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name", hostexec.FakeResponse{Stdout: "\n"})
	return fake
}

func readToolout(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "test", "toolout", name))
	if err != nil {
		t.Fatalf("reading the captured tool output %s: %v", name, err)
	}
	return string(contents)
}

// createEnv is a state directory with a cached base image and a key file, which
// is the situation `create` is designed for: the image is already there and the
// VM is made from it.
func createEnv(t *testing.T) (stateDir, keyPath string) {
	t.Helper()
	stateDir = t.TempDir()
	cachedImage(t, stateDir)

	keyPath = filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(keyPath, []byte(publicKey), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}
	return stateDir, keyPath
}

func createArgs(keyPath string, extra ...string) []string {
	return append([]string{"create", "agent-01", "--ssh-key", keyPath}, extra...)
}

func loadVM(t *testing.T, stateDir, name string) *state.VM {
	t.Helper()
	store, err := state.OpenExisting(stateDir)
	if err != nil {
		t.Fatalf("state.OpenExisting: %v", err)
	}
	vm, err := store.LoadVM(name)
	if err != nil {
		t.Fatalf("LoadVM: %v", err)
	}
	return vm
}

func TestCreate_RunsTheDocumentedPipeline(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)

	code, stdout, stderr := cliRun(t, fake, stateDir, createArgs(keyPath)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	argvs := strings.Join(fake.Argvs(), "\n")
	for _, want := range []string{
		"qemu-img create -f qcow2 -F qcow2 ",
		"virt-install --connect qemu:///system --name agent-01",
		"virsh --connect qemu:///system dumpxml agent-01",
	} {
		if !strings.Contains(argvs, want) {
			t.Errorf("create did not run %q:\n%s", want, argvs)
		}
	}
	if !strings.Contains(stdout, "192.168.122.3") {
		t.Errorf("the address is not reported:\n%s", stdout)
	}
}

func TestCreate_WritesTheVMRecordWithItsProvenance(t *testing.T) {
	stateDir, keyPath := createEnv(t)

	code, _, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	vm := loadVM(t, stateDir, "agent-01")
	// "What created this VM, from what?" has to be answerable from the state
	// directory alone.
	if vm.BaseImage.SourceDigest == "" {
		t.Error("the base image digest was not recorded")
	}
	if len(vm.CreatedBy.VirtInstallArgv) == 0 {
		t.Error("the virt-install argv was not recorded")
	}
	if vm.CreatedBy.VirtInstallArgv[0] != "virt-install" {
		t.Errorf("recorded argv = %v, want the invocation that ran", vm.CreatedBy.VirtInstallArgv)
	}
	if vm.Network.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC = %q, want the one libvirt assigned", vm.Network.MAC)
	}
	if vm.Guest.User == "" || len(vm.Guest.SSHKeyPaths) != 1 {
		t.Errorf("guest record = %+v, want the user and the key path", vm.Guest)
	}

	for _, path := range []string{vm.Paths.UserData, vm.Paths.DomainXML} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was not written: %v", path, err)
		}
	}
}

func TestCreate_AuthorizesTheKeyAndNeverWritesPrivateMaterial(t *testing.T) {
	stateDir, keyPath := createEnv(t)

	code, _, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	vm := loadVM(t, stateDir, "agent-01")
	userData, err := os.ReadFile(vm.Paths.UserData)
	if err != nil {
		t.Fatalf("reading the generated user-data: %v", err)
	}
	if !strings.Contains(string(userData), strings.TrimSpace(publicKey)) {
		t.Errorf("the key was not authorized:\n%s", userData)
	}
	if strings.Contains(string(userData), "PRIVATE KEY") {
		t.Errorf("private key material reached the seed:\n%s", userData)
	}
	// vm.json records where the key came from, never the key itself.
	record, err := os.ReadFile(filepath.Join(vm.Paths.Dir, state.VMRecordFile))
	if err != nil {
		t.Fatalf("reading vm.json: %v", err)
	}
	if strings.Contains(string(record), "AAAAC3Nza") {
		t.Errorf("vm.json contains key material:\n%s", record)
	}
}

func TestCreate_RejectsAnInvalidVMName(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)

	code, _, _ := cliRun(t, fake, stateDir, "create", "Agent VM", "--ssh-key", keyPath)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("an invalid name must be rejected before any tool runs:\n%s", fake)
	}
}

func TestCreate_RefusesNoStartAndSaysWhy(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--no-start")...)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	// An operator who asked for a stopped VM must not silently get a running one.
	if !strings.Contains(stderr, "cloud-init") || !strings.Contains(stderr, "agent-vm stop") {
		t.Errorf("the refusal should explain the constraint and the alternative:\n%s", stderr)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("nothing may run when the flags cannot be honored:\n%s", fake)
	}
}

func TestCreate_RefusesAVMThatAlreadyExists(t *testing.T) {
	stateDir, keyPath := createEnv(t)

	if code, _, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath)...); code != ExitOK {
		t.Fatalf("first create failed with %d: %s", code, stderr)
	}
	code, _, _ := cliRun(t, createHost(t), stateDir, createArgs(keyPath)...)
	if code != ExitConflict {
		t.Errorf("exit code = %d, want %d", code, ExitConflict)
	}
}

func TestCreate_RefusesALibvirtDomainItDoesNotOwn(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)
	// Someone else's domain happens to have the name we were asked for.
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name",
		hostexec.FakeResponse{Stdout: "agent-01\n"})

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath)...)
	if code != ExitConflict {
		t.Errorf("exit code = %d, want %d", code, ExitConflict)
	}
	if !strings.Contains(stderr, "no record of it") {
		t.Errorf("the refusal should say the domain is not ours:\n%s", stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "virt-install --connect") || strings.Contains(argv, "undefine") {
			t.Errorf("a domain this tool did not create must not be touched: %v", argv)
		}
	}
}

func TestCreate_RollsBackEverythingAfterAFailure(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)
	// The domain is defined and started, and then a later step fails.
	fake.RespondPrefix("virsh --connect qemu:///system dumpxml", hostexec.FakeResponse{
		ExitCode: 1, Stderr: "error: failed to get domain 'agent-01'",
	})

	code, _, _ := cliRun(t, fake, stateDir, createArgs(keyPath)...)
	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}

	argvs := strings.Join(fake.Argvs(), "\n")
	for _, want := range []string{
		"virsh --connect qemu:///system destroy agent-01",
		"virsh --connect qemu:///system undefine agent-01",
	} {
		if !strings.Contains(argvs, want) {
			t.Errorf("rollback did not run %q:\n%s", want, argvs)
		}
	}
	if strings.Contains(argvs, "--remove-all-storage") {
		t.Error("rollback must delete its own files, never hand the decision to libvirt")
	}
	// A failed create leaves no state directory behind.
	if _, err := os.Stat(filepath.Join(stateDir, "vms", "agent-01")); !os.IsNotExist(err) {
		t.Errorf("the VM state directory survived a failed create: %v", err)
	}
}

func TestCreate_RollsBackWithoutUndefiningADomainItNeverDefined(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)
	fake.RespondPrefix("virt-install", hostexec.FakeResponse{
		ExitCode: 1, Stderr: "ERROR    Requested operation is not valid",
	})

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath)...)
	if code != ExitFailure {
		t.Errorf("exit code = %d, want %d", code, ExitFailure)
	}
	// The failure has to carry the tool, its exit status, and its stderr.
	if !strings.Contains(stderr, "virt-install") || !strings.Contains(stderr, "not valid") {
		t.Errorf("the error does not explain what failed:\n%s", stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "undefine") {
			t.Errorf("nothing was defined, so nothing may be undefined: %v", argv)
		}
	}
}

func TestCreate_ReportsHostStateItCouldNotCleanUp(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)
	fake.RespondPrefix("virsh --connect qemu:///system dumpxml", hostexec.FakeResponse{
		ExitCode: 1, Stderr: "error: failed to get domain",
	})
	fake.RespondPrefix("virsh --connect qemu:///system undefine", hostexec.FakeResponse{
		ExitCode: 1, Stderr: "error: Failed to undefine domain",
	})

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath)...)
	if code != ExitCleanup {
		t.Errorf("exit code = %d, want %d", code, ExitCleanup)
	}
	if !strings.Contains(stderr, "still present") {
		t.Errorf("an incomplete cleanup must list what is left:\n%s", stderr)
	}
}

func TestCreate_ABootTimeoutLeavesTheVMInPlace(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)
	// The guest never gets an address: it is booting slowly, or not at all.
	fake.RespondPrefix("virsh --connect qemu:///system domifaddr", hostexec.FakeResponse{
		Stdout: " Name       MAC address         Protocol   Address\n" +
			"-------------------------------------------------------------\n\n",
	})

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--wait-for-ssh", "1ns")...)
	if code != ExitTimeout {
		t.Errorf("exit code = %d, want %d\n%s", code, ExitTimeout, stderr)
	}
	// The VM stays: "it booted slowly" and "it failed to boot" need the same
	// evidence, and destroying it would throw that away.
	vm := loadVM(t, stateDir, "agent-01")
	if vm.Name != "agent-01" {
		t.Errorf("vm.json = %+v, want the VM still recorded", vm)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "undefine") {
			t.Errorf("a boot timeout must not undefine the VM: %v", argv)
		}
	}
}

func TestCreate_WaitingCanBeDisabled(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)

	code, stdout, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--wait-for-ssh", "0")...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "ssh ") {
			t.Errorf("--wait-for-ssh 0 must not probe SSH: %v", argv)
		}
	}
	if strings.Contains(stdout, "192.168.122.3") {
		t.Errorf("no wait means no address to report:\n%s", stdout)
	}
}

func TestCreate_BridgeModeValidatesTheBridgeAndCreatesNothing(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)
	fake.Respond("ip -json link show type bridge", hostexec.FakeResponse{
		Stdout: readToolout(t, "ip-json-link-show-type-bridge.txt"),
	})

	code, _, stderr := cliRun(t, fake, stateDir,
		createArgs(keyPath, "--network", "bridge", "--bridge", "br40")...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	argvs := strings.Join(fake.Argvs(), "\n")
	if !strings.Contains(argvs, "ip -json link show type bridge") {
		t.Errorf("the bridge was not validated:\n%s", argvs)
	}
	// Host networking belongs to the operator: a bridge is checked, never made.
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "ip link add") || strings.Contains(argv, "net-define") {
			t.Errorf("bridged mode must not change host networking: %v", argv)
		}
	}
	if !strings.Contains(argvs, "--network bridge=br40,model=virtio") {
		t.Errorf("the guest was not attached to the bridge:\n%s", argvs)
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.Network.Bridge != "br40" {
		// The mode is recorded so a guest's exposure is auditable afterwards.
		t.Errorf("vm.json network = %+v, want the bridge recorded", vm.Network)
	}
	// A bridged guest is on the operator's LAN and on no libvirt network at
	// all. Naming one here would make the record describe an isolation the
	// guest does not have, which is the reading the field exists to prevent.
	if vm.Network.Name != "" {
		t.Errorf("vm.json records the libvirt network %q for a bridged VM: %+v", vm.Network.Name, vm.Network)
	}
}

// TestCreate_NATModeRecordsTheNetworkAndNoBridge is the mirror of the bridged
// case: a bridge can be configured — through the config file or AGENT_VM_BRIDGE
// — while NAT stays the mode, and recording it would claim a LAN attachment the
// guest does not have.
func TestCreate_NATModeRecordsTheNetworkAndNoBridge(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)
	t.Setenv("AGENT_VM_BRIDGE", "br40")

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	vm := loadVM(t, stateDir, "agent-01")
	if vm.Network.Mode != config.NetworkNAT {
		t.Fatalf("network.mode = %q, want nat", vm.Network.Mode)
	}
	if vm.Network.Bridge != "" {
		t.Errorf("vm.json records bridge %q for a NAT VM: %+v", vm.Network.Bridge, vm.Network)
	}
	if vm.Network.Name == "" {
		t.Errorf("vm.json records no libvirt network for a NAT VM: %+v", vm.Network)
	}
}

func TestCreate_RefusesBridgeModeWithNoBridge(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--network", "bridge")...)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "--bridge") {
		t.Errorf("the refusal should name the missing flag:\n%s", stderr)
	}
}

func TestCreate_RejectsAPrivateKeyBeforeChangingAnything(t *testing.T) {
	stateDir, _ := createEnv(t)
	fake := createHost(t)
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}

	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath)...)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "private key") {
		t.Errorf("the refusal should say what the file is:\n%s", stderr)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("nothing may run before the inputs are accepted:\n%s", fake)
	}
}

func TestCreate_RejectsCloudInitDataCloudInitWouldIgnore(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	extra := filepath.Join(t.TempDir(), "extra.yaml")
	if err := os.WriteFile(extra, []byte("packages:\n  - ripgrep\n"), 0o600); err != nil {
		t.Fatalf("writing the user-data file: %v", err)
	}

	code, _, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath, "--cloud-init", extra)...)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if strings.Contains(stderr, "ripgrep") {
		t.Errorf("operator-supplied user-data must never be echoed:\n%s", stderr)
	}
}

func TestCreate_MergesOperatorCloudInitData(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	extra := filepath.Join(t.TempDir(), "extra.yaml")
	if err := os.WriteFile(extra, []byte("#cloud-config\npackages:\n  - ripgrep\n"), 0o600); err != nil {
		t.Fatalf("writing the user-data file: %v", err)
	}

	code, _, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath, "--cloud-init", extra)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}

	vm := loadVM(t, stateDir, "agent-01")
	userData, err := os.ReadFile(vm.Paths.UserData)
	if err != nil {
		t.Fatalf("reading the generated user-data: %v", err)
	}
	if !strings.Contains(string(userData), "ripgrep") {
		t.Errorf("the operator's user-data was not merged:\n%s", userData)
	}
	if !strings.Contains(string(userData), strings.TrimSpace(publicKey)) {
		t.Errorf("merging must not drop the authorized key:\n%s", userData)
	}
}

func TestCreate_PassesVirtInstallArgumentsThrough(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)

	code, _, stderr := cliRun(t, fake, stateDir,
		createArgs(keyPath, "--virt-install-arg", "--tpm", "--virt-install-arg", "backend.type=emulator")...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(strings.Join(fake.Argvs(), "\n"), "--tpm backend.type=emulator") {
		t.Errorf("the passthrough arguments did not reach virt-install:\n%s", fake)
	}
}

func TestCreate_DryRunPrintsThePlanAndChangesNothing(t *testing.T) {
	stateDir, keyPath := createEnv(t)
	fake := createHost(t)

	code, stdout, stderr := cliRun(t, fake, stateDir, append([]string{"--dry-run"}, createArgs(keyPath)...)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, want := range []string{"qemu-img create", "virt-install --connect", "user-data"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "vms", "agent-01")); !os.IsNotExist(err) {
		t.Errorf("--dry-run created state: %v", err)
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "virt-install --connect") || strings.HasPrefix(argv, "qemu-img create") {
			t.Errorf("--dry-run must not run a mutating command: %v", argv)
		}
	}
}

func TestCreate_JSONOutputIsTheVMRecord(t *testing.T) {
	stateDir, keyPath := createEnv(t)

	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr, Env: func(string) string { return "" }, Runner: createHost(t)}
	code := app.Main(context.Background(), append([]string{
		"--state-dir", stateDir,
		"--config", t.TempDir() + "/absent.toml",
		"--output", "json",
	}, createArgs(keyPath)...))
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr.String())
	}

	var vm state.VM
	if err := json.Unmarshal(stdout.Bytes(), &vm); err != nil {
		t.Fatalf("stdout is not the VM record: %v\n%s", err, stdout.String())
	}
	if vm.Name != "agent-01" || vm.SchemaVersion == 0 {
		t.Errorf("record = %+v, want the stored vm.json", vm)
	}
}
