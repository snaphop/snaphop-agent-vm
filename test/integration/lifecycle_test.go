//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

// The lifecycle tests drive the real binary as a subprocess rather than calling
// into internal/cli. That is deliberate: the exit codes, the JSON on stdout,
// and `agent-vm ssh` replacing itself with ssh are all part of the public
// contract (AGENTS.md §8), and only a subprocess exercises them the way an
// operator or an agent supervisor does.
//
//	go test -tags integration ./test/integration/... -run TestVMLifecycle -v
//
// A run creates a real VM, boots it, logs into it, and destroys it. Point
// -lifecycle-state-dir at a directory that already holds a built base image to
// skip the image build, which otherwise happens inside the first create and
// takes tens of minutes:
//
//	go test -tags integration ./test/integration/... -run TestVMLifecycle \
//	    -lifecycle-state-dir ~/.cache/agent-vm-test -timeout 90m
//
// In NAT mode the tool defines the libvirt network agent-vm-nat if it is
// absent. That network is host state shared with real use, so the tests leave
// it in place rather than removing something they may not have created.
//
// Bridged mode is the other half of the networking contract and must be run
// explicitly, because it puts the test guest directly on the operator's LAN
// (SECURITY.md, "Networking Boundaries") and needs a bridge that already
// exists — this suite never creates one:
//
//	go test -tags integration ./test/integration/... -run TestVMLifecycle \
//	    -lifecycle-network bridge -lifecycle-bridge br0
//
// A bridged guest gets its address from whatever DHCP server serves that
// segment rather than from libvirt, so a segment without one will fail the
// wait for SSH — that is a host-environment problem, not a defect in the tool.

var (
	lifecycleStateDir = flag.String("lifecycle-state-dir", "",
		"state directory for the lifecycle tests; defaults to a temporary one, which forces a base image build")
	lifecycleDistro = flag.String("lifecycle-distro", "ubuntu",
		"distro to boot in the lifecycle tests")
	lifecycleURI = flag.String("lifecycle-libvirt-uri", "qemu:///system",
		"libvirt connection the lifecycle tests use")
	lifecycleNetwork = flag.String("lifecycle-network", string(config.NetworkNAT),
		"network mode the lifecycle tests create VMs in: nat or bridge")
	lifecycleBridge = flag.String("lifecycle-bridge", "",
		"host bridge the lifecycle tests attach to; required with -lifecycle-network bridge")
)

const (
	// Every domain and state directory these tests touch carries this prefix,
	// so a leftover from a crashed run is identifiable at a glance and can
	// never collide with a VM someone cares about (AGENTS.md §4).
	testVMPrefix = "agent-vm-test-"

	lifecycleVM = testVMPrefix + "lifecycle"
	rollbackVM  = testVMPrefix + "rollback"

	// A create builds the base image if the cache misses, which dominates this
	// budget; the boot itself takes seconds.
	createTimeout  = 60 * time.Minute
	commandTimeout = 5 * time.Minute
)

// guestHostnameCommand asks the guest its own name. `uname -n` rather than
// `hostname` for the same reason templates/distro/user-setup.sh gives: Arch
// installs no hostname binary at all, while coreutils is present everywhere.
var guestHostnameCommand = []string{"uname", "-n"}

// harness runs the built agent-vm binary against a dedicated state directory.
type harness struct {
	binary   string
	stateDir string
	// configFile is an empty configuration file, passed to every invocation so
	// that the operator's own ~/.config/agent-vm/config.toml cannot decide what
	// these tests exercise. In NAT mode networkArgs deliberately passes no
	// --network, so a config setting bridge mode would silently test the other
	// mode and then fail the assertion that vm.json records NAT.
	configFile string
	// publicKey is the key authorized in the guest. Its private half sits
	// beside it, which is how `agent-vm ssh` finds it.
	publicKey string
}

// result is one finished agent-vm invocation.
type result struct {
	code   int
	stdout string
	stderr string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	requireTools(t, "virsh", "virt-install", "qemu-img", "podman", "ssh", "ssh-keygen",
		"virt-make-fs", "virt-ls", "virt-copy-out", "virt-sysprep")
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skipf("/dev/kvm is not available; skipping the lifecycle test: %v", err)
	}

	stateDir := *lifecycleStateDir
	if stateDir == "" {
		stateDir = filepath.Join(testTempDir(t), "agent-vm-test-state")
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("creating the state directory: %v", err)
	}

	h := &harness{
		binary:     buildBinary(t),
		stateDir:   stateDir,
		configFile: emptyConfigFile(t),
		publicKey:  generateKeyPair(t),
	}

	// doctor is the host readiness check the tool itself relies on, so a host
	// it rejects is one where a lifecycle failure would say nothing about this
	// code. It is a skip, not a failure.
	if res := h.run(t, commandTimeout, "doctor"); res.code != 0 {
		t.Skipf("agent-vm doctor says this host cannot run VMs (exit %d); skipping:\n%s%s",
			res.code, res.stdout, res.stderr)
	}
	return h
}

// testTempDir is t.TempDir() with the whole path made searchable. Under
// qemu:///system the QEMU process runs as libvirt's own account, which has to
// search every directory between / and a VM's disk, and t.TempDir() creates
// its directories 0700. Without this the tool's own state-directory-access
// check fails, doctor exits 3, and every lifecycle test skips — leaving a run
// that reports PASS having booted nothing.
//
// 0711 grants search without read, the same narrow grant docs/host-setup.md
// recommends via setfacl: the hypervisor can traverse to the disk and still
// cannot list what else is in there.
func testTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	// Both levels matter: t.TempDir() returns a numbered subdirectory of a
	// directory it also created 0700, and the outer one blocks the path first.
	for _, path := range []string{filepath.Dir(dir), dir} {
		if err := os.Chmod(path, 0o711); err != nil {
			t.Fatalf("making %s searchable by the hypervisor: %v", path, err)
		}
	}
	return dir
}

// emptyConfigFile writes a configuration file with nothing in it, so that
// --config resolves to defaults rather than to the operator's own file.
func emptyConfigFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "empty-config.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("writing the empty config file: %v", err)
	}
	return path
}

// withoutAgentVMVars drops the AGENT_VM_* variables from an environment.
// They sit above the configuration file in the precedence order (docs/cli.md),
// so pinning --config alone would still leave an exported AGENT_VM_NETWORK
// deciding what a test exercises. Everything else is passed through: the binary
// needs PATH to find virsh, and HOME to find the ssh key.
func withoutAgentVMVars(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		if strings.HasPrefix(entry, "AGENT_VM_") {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// buildBinary builds agent-vm once per test binary run.
func buildBinary(t *testing.T) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "agent-vm")
	build := exec.Command("go", "build", "-o", binary, "./cmd/agent-vm")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building agent-vm: %v\n%s", err, out)
	}
	return binary
}

func repoRoot(t *testing.T) string {
	t.Helper()

	// The test runs in test/integration.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s does not look like the repository root: %v", root, err)
	}
	return root
}

// generateKeyPair makes a throwaway key for the guest. A test must never
// authorize the operator's own key in an untrusted guest, and the key is
// discarded with the temporary directory.
func generateKeyPair(t *testing.T) string {
	t.Helper()

	private := filepath.Join(t.TempDir(), "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "agent-vm-integration-test", "-f", private)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating a test SSH key: %v\n%s", err, out)
	}
	return private + ".pub"
}

// run invokes agent-vm with the global flags every test needs, and returns its
// exit code and streams rather than failing: most of these assertions are about
// which code came back.
func (h *harness) run(t *testing.T, timeout time.Duration, args ...string) result {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	full := append([]string{
		"--state-dir", h.stateDir,
		"--libvirt-uri", *lifecycleURI,
		// Every source of configuration this tool reads is pinned here, so that
		// what these tests exercise is the documented default rather than
		// whatever the machine running them happens to be configured for.
		"--config", h.configFile,
		// Destructive commands must not stop at a prompt in a test.
		"--yes",
	}, args...)

	cmd := exec.CommandContext(ctx, h.binary, full...)
	cmd.Env = withoutAgentVMVars(os.Environ())
	stdout := new(strings.Builder)
	stderr := new(strings.Builder)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	res := result{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running agent-vm %s: %v\n%s", strings.Join(args, " "), err, res.stderr)
		}
		res.code = exitErr.ExitCode()
	}

	if testing.Verbose() {
		t.Logf("agent-vm %s -> %d\n%s%s", strings.Join(args, " "), res.code, res.stdout, res.stderr)
	}
	return res
}

// networkArgs turns the suite's network flags into create arguments. NAT is
// the documented default and passes nothing, so the default run exercises the
// same argument vector an operator gets from a bare `agent-vm create`.
func networkArgs(t *testing.T) []string {
	t.Helper()

	switch mode := config.NetworkMode(*lifecycleNetwork); mode {
	case config.NetworkNAT:
		return nil
	case config.NetworkBridge:
		if *lifecycleBridge == "" {
			t.Fatal("-lifecycle-network bridge needs -lifecycle-bridge; this suite never creates a host bridge")
		}
		return []string{"--network", string(mode), "--bridge", *lifecycleBridge}
	default:
		t.Fatalf("-lifecycle-network %q is not a network mode; want nat or bridge", *lifecycleNetwork)
		return nil
	}
}

// mustRun fails the test when the command did not succeed, quoting what the
// tool said. An integration failure is only useful with the tool's own
// diagnosis attached.
func (h *harness) mustRun(t *testing.T, timeout time.Duration, args ...string) result {
	t.Helper()

	res := h.run(t, timeout, args...)
	if res.code != 0 {
		t.Fatalf("agent-vm %s exited %d, want 0\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), res.code, res.stdout, res.stderr)
	}
	return res
}

// decode parses a command's JSON stdout, which is a public contract.
func decode[T any](t *testing.T, res result, args ...string) T {
	t.Helper()

	var value T
	if err := json.Unmarshal([]byte(res.stdout), &value); err != nil {
		t.Fatalf("agent-vm %s did not print valid JSON: %v\nstdout:\n%s",
			strings.Join(args, " "), err, res.stdout)
	}
	return value
}

// vmStatus mirrors the JSON that list, info, start, stop, and restart print.
// It is redeclared here rather than imported because internal/cli keeps it
// unexported: the contract is the JSON, and a test that reuses the producer's
// struct cannot notice a field being renamed.
type vmStatus struct {
	state.VM
	State   string `json:"state"`
	Address string `json:"address"`
	// Sizes cross the wire in the same human-readable form as every other size
	// in the contract ("50G", not a byte count), so they are decoded with the
	// type that defines that form rather than as a bare integer.
	Disk *struct {
		VirtualSize config.Size `json:"virtualSize"`
		ActualSize  config.Size `json:"actualSize"`
		BackingFile string      `json:"backingFile"`
	} `json:"disk"`
}

// TestVMLifecycle creates one VM and takes it through every state an operator
// puts it in, in order. The phases share a VM because creating one is the
// expensive part; each is a subtest so a failure names the step it happened in.
//
// This is the test that makes the claim in AGENTS.md §1 — that the VM lifecycle
// works against a real KVM host — checkable rather than assumed.
func TestVMLifecycle(t *testing.T) {
	h := newHarness(t)
	h.removeLeftoverVM(t, lifecycleVM)

	// Destroy runs even when a phase fails, so a broken run does not leave a
	// domain and a disk behind on the host.
	t.Cleanup(func() { h.removeLeftoverVM(t, lifecycleVM) })

	var created state.VM

	t.Run("create", func(t *testing.T) {
		args := append([]string{"--output", "json", "create", lifecycleVM,
			"--distro", *lifecycleDistro,
			"--ssh-key", h.publicKey,
			// Smaller than the documented default: the test needs a guest that
			// boots, not a realistic workstation.
			"--memory", "2G",
			"--vcpus", "2",
		}, networkArgs(t)...)
		res := h.mustRun(t, createTimeout, args...)
		created = decode[state.VM](t, res, "create")

		if created.Name != lifecycleVM {
			t.Errorf("create recorded name %q, want %q", created.Name, lifecycleVM)
		}
		if created.SchemaVersion != state.VMSchemaVersion {
			t.Errorf("vm.json schemaVersion = %d, want %d", created.SchemaVersion, state.VMSchemaVersion)
		}
		if created.Network.MAC == "" {
			t.Error("create recorded no MAC address; the domain's NIC was never read back")
		}
		// How the guest is attached is recorded so that exposure is auditable
		// after the fact (internal/state), which only holds if the record
		// matches the mode the VM was actually created in.
		if got, want := created.Network.Mode, config.NetworkMode(*lifecycleNetwork); got != want {
			t.Errorf("vm.json network.mode = %q, want %q", got, want)
		}
		switch created.Network.Mode {
		case config.NetworkBridge:
			if created.Network.Bridge != *lifecycleBridge {
				t.Errorf("vm.json network.bridge = %q, want %q", created.Network.Bridge, *lifecycleBridge)
			}
			// A bridged guest must be on the host bridge and not on a libvirt
			// network; recording a network name here would mean the attachment
			// silently fell back to NAT.
			if created.Network.Name != "" {
				t.Errorf("vm.json records the libvirt network %q for a bridged VM", created.Network.Name)
			}
			if !containsArg(created.CreatedBy.VirtInstallArgv, "bridge="+*lifecycleBridge) {
				t.Errorf("virt-install argv does not attach the guest to bridge %s: %v",
					*lifecycleBridge, created.CreatedBy.VirtInstallArgv)
			}
		case config.NetworkNAT:
			if created.Network.Name == "" {
				t.Error("vm.json records no libvirt network for a NAT VM")
			}
		}
		if !strings.HasPrefix(created.BaseImage.SourceDigest, "sha256:") {
			t.Errorf("baseImage.sourceDigest = %q, want the digest the image was pinned to",
				created.BaseImage.SourceDigest)
		}
		if len(created.CreatedBy.VirtInstallArgv) == 0 {
			t.Error("vm.json records no virt-install argv, so the VM's provenance is unanswerable")
		}

		// The artifacts an operator and a later destroy both depend on.
		for _, path := range []string{
			created.Paths.Overlay,
			created.Paths.UserData,
			created.Paths.DomainXML,
			filepath.Join(created.Paths.Dir, state.VMRecordFile),
		} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("create did not leave %s behind: %v", filepath.Base(path), err)
			}
		}

		// The generated user-data may hold operator-supplied content, so its
		// mode is part of the contract, not an accident (internal/cli).
		if info, err := os.Stat(created.Paths.UserData); err == nil {
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("user-data is mode %o, want 600", perm)
			}
		}
	})

	if t.Failed() {
		// Every phase below assumes a created VM; running them would report
		// failures that all have the same cause.
		t.Fatal("create failed; skipping the rest of the lifecycle")
	}

	t.Run("overlay is a thin copy on the base image", func(t *testing.T) {
		res := h.mustRun(t, commandTimeout, "--output", "json", "info", lifecycleVM)
		detail := decode[vmStatus](t, res, "info")

		if detail.Disk == nil {
			t.Fatal("info reported no disk detail for a VM whose overlay exists")
		}
		if detail.Disk.BackingFile != created.BaseImage.Path {
			t.Errorf("overlay backing file = %q, want the cached base image %q",
				detail.Disk.BackingFile, created.BaseImage.Path)
		}
		// A copy-on-write overlay that already occupies its full virtual size
		// is not an overlay, and would make create as slow as a copy.
		if detail.Disk.ActualSize >= detail.Disk.VirtualSize {
			t.Errorf("overlay occupies %s of its %s virtual size; it is not thin",
				detail.Disk.ActualSize, detail.Disk.VirtualSize)
		}
	})

	t.Run("list reports it running with an address", func(t *testing.T) {
		res := h.mustRun(t, commandTimeout, "--output", "json", "list")
		statuses := decode[[]vmStatus](t, res, "list")

		status, ok := findVM(statuses, lifecycleVM)
		if !ok {
			t.Fatalf("list does not include %s: %s", lifecycleVM, res.stdout)
		}
		if status.State != "running" {
			t.Errorf("state = %q, want running", status.State)
		}
		if status.Address == "" {
			t.Error("list reports no address for a VM that create said was reachable")
		}
	})

	t.Run("the guest is attached where the mode says it is", func(t *testing.T) {
		if config.NetworkMode(*lifecycleNetwork) != config.NetworkBridge {
			t.Skip("only bridged mode puts the guest somewhere the host can independently verify")
		}

		res := h.mustRun(t, commandTimeout, "--output", "json", "info", lifecycleVM)
		address := decode[vmStatus](t, res, "info").Address
		guest := net.ParseIP(address)
		if guest == nil {
			t.Fatalf("info reported %q, which is not an address", address)
		}

		// The point of bridged mode is that the guest is on the operator's LAN
		// rather than behind libvirt's NAT (SECURITY.md). The host bridge's own
		// subnet is what "the LAN" means here, and it is read from the host so
		// the assertion does not hard-code anyone's addressing.
		subnets := bridgeSubnets(t, *lifecycleBridge)
		if len(subnets) == 0 {
			t.Skipf("bridge %s has no address, so there is no subnet to check the guest against", *lifecycleBridge)
		}
		for _, subnet := range subnets {
			if subnet.Contains(guest) {
				return
			}
		}
		t.Errorf("the guest answered on %s, which is outside bridge %s's subnets %v; it is not on the bridged segment",
			address, *lifecycleBridge, subnets)
	})

	t.Run("ssh runs a command in the guest", func(t *testing.T) {
		// The hostname proves this is the VM under test and not another guest
		// that happens to answer on that address.
		if got := h.ssh(t, guestHostnameCommand...); got != lifecycleVM {
			t.Errorf("hostname in the guest = %q, want %q", got, lifecycleVM)
		}
		if got := h.ssh(t, "id", "-un"); got != created.Guest.User {
			t.Errorf("logged in as %q, want the recorded guest user %q", got, created.Guest.User)
		}
		// Root in the guest is the point of the product: an agent inside the VM
		// is meant to be able to do anything to it (AGENTS.md §1).
		if got := h.ssh(t, "sudo", "-n", "id", "-un"); got != "root" {
			t.Errorf("sudo id -un = %q, want root; the agent user cannot escalate in its own VM", got)
		}
	})

	t.Run("stop shuts the guest down", func(t *testing.T) {
		res := h.mustRun(t, commandTimeout, "--output", "json", "stop", lifecycleVM)
		status := decode[vmStatus](t, res, "stop")
		if status.State != "shut off" {
			t.Errorf("state after stop = %q, want shut off", status.State)
		}

		// Stopping twice is a state conflict, not a silent success.
		if res := h.run(t, commandTimeout, "stop", lifecycleVM); res.code != 5 {
			t.Errorf("stopping an already stopped VM exited %d, want 5 (conflict)\n%s", res.code, res.stderr)
		}
	})

	t.Run("start brings it back with SSH working", func(t *testing.T) {
		res := h.mustRun(t, commandTimeout, "--output", "json", "start", lifecycleVM)
		if status := decode[vmStatus](t, res, "start"); status.State != "running" {
			t.Errorf("state after start = %q, want running", status.State)
		}

		// A VM that boots once but is unreachable after a restart is a VM an
		// operator cannot use, so reachability is asserted rather than state.
		if got := h.sshWhenReachable(t, guestHostnameCommand...); got != lifecycleVM {
			t.Errorf("hostname after start = %q, want %q", got, lifecycleVM)
		}
	})

	t.Run("restart keeps it usable", func(t *testing.T) {
		res := h.mustRun(t, commandTimeout, "--output", "json", "restart", lifecycleVM)
		if status := decode[vmStatus](t, res, "restart"); status.State != "running" {
			t.Errorf("state after restart = %q, want running", status.State)
		}
		if got := h.sshWhenReachable(t, guestHostnameCommand...); got != lifecycleVM {
			t.Errorf("hostname after restart = %q, want %q", got, lifecycleVM)
		}
	})

	t.Run("destroy refuses a domain that is not the recorded one", func(t *testing.T) {
		// The guard is that the domain's disk must be this VM's overlay, not
		// that the names match. Pointing the record at another path is the
		// cheapest way to stand in for "a domain someone else created with the
		// same name" without defining a foreign domain on the host.
		record := filepath.Join(created.Paths.Dir, state.VMRecordFile)
		original, err := os.ReadFile(record)
		if err != nil {
			t.Fatalf("reading %s: %v", record, err)
		}
		t.Cleanup(func() {
			if err := os.WriteFile(record, original, 0o600); err != nil {
				t.Errorf("restoring %s: %v", record, err)
			}
		})

		tampered := strings.Replace(string(original), state.OverlayFile, "not-our-disk.qcow2", 1)
		if tampered == string(original) {
			t.Fatalf("could not point the record at another disk; %s is not in vm.json", state.OverlayFile)
		}
		if err := os.WriteFile(record, []byte(tampered), 0o600); err != nil {
			t.Fatalf("writing the tampered record: %v", err)
		}

		res := h.run(t, commandTimeout, "destroy", lifecycleVM)
		if res.code != 5 {
			t.Errorf("destroy exited %d, want 5 (conflict) for a domain using another disk\n%s", res.code, res.stderr)
		}
		if !h.domainExists(t, lifecycleVM) {
			t.Error("destroy undefined the domain it had just refused to act on")
		}
	})

	t.Run("destroy removes the domain and the state", func(t *testing.T) {
		h.mustRun(t, commandTimeout, "destroy", lifecycleVM)

		if h.domainExists(t, lifecycleVM) {
			t.Errorf("libvirt still has a domain named %s after destroy", lifecycleVM)
		}
		if _, err := os.Stat(created.Paths.Dir); !os.IsNotExist(err) {
			t.Errorf("the state directory %s survived destroy (%v)", created.Paths.Dir, err)
		}

		// The base image is a shared cache entry, not part of the VM:
		// destroying a VM must never invalidate it for the next create.
		if _, err := os.Stat(created.BaseImage.Path); err != nil {
			t.Errorf("destroy removed the shared base image %s: %v", created.BaseImage.Path, err)
		}

		if res := h.run(t, commandTimeout, "info", lifecycleVM); res.code != 4 {
			t.Errorf("info on a destroyed VM exited %d, want 4 (not found)\n%s", res.code, res.stderr)
		}
	})
}

// TestCreate_RollsBackAfterAFailedDefine checks the promise that a failed
// create leaves nothing behind (docs/cli.md): no libvirt domain, and no
// half-written state directory. The failure is injected through
// --virt-install-arg, which is the only way to make the real virt-install fail
// after the overlay and the user-data have already been written.
func TestCreate_RollsBackAfterAFailedDefine(t *testing.T) {
	h := newHarness(t)
	h.removeLeftoverVM(t, rollbackVM)
	t.Cleanup(func() { h.removeLeftoverVM(t, rollbackVM) })

	args := append([]string{"create", rollbackVM,
		"--distro", *lifecycleDistro,
		"--ssh-key", h.publicKey,
		"--virt-install-arg", "--not-a-real-virt-install-flag",
	}, networkArgs(t)...)
	res := h.run(t, createTimeout, args...)
	if res.code == 0 {
		t.Fatal("create succeeded despite an invalid virt-install argument")
	}
	// Exit 7 means the rollback itself could not finish, which is the failure
	// this test exists to catch.
	if res.code == 7 {
		t.Errorf("create left host state behind (exit 7):\n%s", res.stderr)
	}

	if h.domainExists(t, rollbackVM) {
		t.Errorf("a failed create left the libvirt domain %s defined", rollbackVM)
	}
	vmDir := state.NewLayout(h.stateDir).VMDir(rollbackVM)
	if _, err := os.Stat(vmDir); !os.IsNotExist(err) {
		t.Errorf("a failed create left the state directory %s behind (%v)", vmDir, err)
	}

	// The name is free again: a rolled-back create must not poison it.
	if res := h.run(t, commandTimeout, "info", rollbackVM); res.code != 4 {
		t.Errorf("info after a rolled-back create exited %d, want 4 (not found)\n%s", res.code, res.stderr)
	}
}

// TestDestroy_RefusesAVMItHasNoRecordOf checks that destroy resolves a name
// through the state directory and never through libvirt. This host may well
// have domains that are not ours, and none of them may be reachable by name
// (SECURITY.md).
func TestDestroy_RefusesAVMItHasNoRecordOf(t *testing.T) {
	h := newHarness(t)

	// Every domain libvirt knows about on this connection, none of which this
	// state directory recorded.
	for _, name := range h.definedDomains(t) {
		res := h.run(t, commandTimeout, "destroy", name)
		if res.code != 4 {
			t.Errorf("destroy %s exited %d, want 4 (not found) for a domain this tool never created\n%s",
				name, res.code, res.stderr)
		}
		if !h.domainExists(t, name) {
			t.Fatalf("destroy removed the domain %s, which this tool did not create", name)
		}
	}

	if res := h.run(t, commandTimeout, "destroy", testVMPrefix+"never-created"); res.code != 4 {
		t.Errorf("destroy of an unknown name exited %d, want 4 (not found)\n%s", res.code, res.stderr)
	}
}

// bridgeSubnets reads the networks the host bridge itself sits on, so a
// bridged guest's address can be checked against the segment it was supposed
// to land on without the test knowing anything about the host's addressing.
func bridgeSubnets(t *testing.T, iface string) []*net.IPNet {
	t.Helper()

	out, err := exec.Command("ip", "-json", "addr", "show", iface).Output()
	if err != nil {
		t.Fatalf("reading the addresses of bridge %s: %v", iface, err)
	}

	var links []struct {
		AddrInfo []struct {
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(out, &links); err != nil {
		t.Fatalf("parsing `ip -json addr show %s`: %v\n%s", iface, err, out)
	}

	var subnets []*net.IPNet
	for _, link := range links {
		for _, addr := range link.AddrInfo {
			_, subnet, err := net.ParseCIDR(fmt.Sprintf("%s/%d", addr.Local, addr.PrefixLen))
			if err != nil {
				continue
			}
			subnets = append(subnets, subnet)
		}
	}
	return subnets
}

// containsArg reports whether any recorded virt-install argument carries the
// given fragment. The argv pairs a flag with a comma-separated value, so the
// attachment is a substring of one element rather than an element of its own.
func containsArg(argv []string, fragment string) bool {
	for _, arg := range argv {
		if strings.Contains(arg, fragment) {
			return true
		}
	}
	return false
}

// ssh runs one command in the guest through `agent-vm ssh` and returns its
// trimmed stdout.
func (h *harness) ssh(t *testing.T, command ...string) string {
	t.Helper()

	args := append([]string{"ssh", lifecycleVM, "--"}, command...)
	return strings.TrimSpace(h.mustRun(t, commandTimeout, args...).stdout)
}

// sshWhenReachable retries until the guest accepts SSH. After a start or a
// restart the domain is running before sshd is listening, and that gap is a
// boot delay rather than a failure.
func (h *harness) sshWhenReachable(t *testing.T, command ...string) string {
	t.Helper()

	args := append([]string{"ssh", lifecycleVM, "--"}, command...)
	deadline := time.Now().Add(2 * time.Minute)
	var last result
	for time.Now().Before(deadline) {
		last = h.run(t, commandTimeout, args...)
		if last.code == 0 {
			return strings.TrimSpace(last.stdout)
		}
		time.Sleep(5 * time.Second)
	}

	t.Fatalf("the guest never accepted SSH within 2 minutes; last attempt exited %d\n%s",
		last.code, last.stderr)
	return ""
}

// domainExists asks libvirt directly rather than through agent-vm: after a
// destroy there is no record left to ask about, and the question is precisely
// whether libvirt still has the domain.
func (h *harness) domainExists(t *testing.T, name string) bool {
	t.Helper()

	cmd := exec.Command("virsh", "--connect", *lifecycleURI, "domstate", name)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false
		}
		t.Fatalf("running virsh domstate %s: %v", name, err)
	}
	return true
}

// definedDomains lists the domains on the test connection, which on a
// developer's host include ones that are none of this tool's business.
func (h *harness) definedDomains(t *testing.T) []string {
	t.Helper()

	out, err := exec.Command("virsh", "--connect", *lifecycleURI, "list", "--all", "--name").Output()
	if err != nil {
		t.Fatalf("listing libvirt domains: %v", err)
	}

	names := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// removeLeftoverVM destroys a test VM if it is still around, so a run that
// crashed halfway does not block the next one. It refuses to touch anything
// without the test prefix.
func (h *harness) removeLeftoverVM(t *testing.T, name string) {
	t.Helper()

	if !strings.HasPrefix(name, testVMPrefix) {
		t.Fatalf("refusing to clean up %q: the integration suite only removes %s* VMs", name, testVMPrefix)
	}
	record := filepath.Join(state.NewLayout(h.stateDir).VMDir(name), state.VMRecordFile)
	if _, err := os.Stat(record); err != nil {
		return
	}

	// --force skips the graceful shutdown: this is cleanup, and there is
	// nothing in a test guest worth waiting to flush.
	if res := h.run(t, commandTimeout, "destroy", name, "--force"); res.code != 0 {
		t.Errorf("could not clean up the leftover VM %s (exit %d); remove it by hand\n%s",
			name, res.code, res.stderr)
	}
}

func findVM(statuses []vmStatus, name string) (vmStatus, bool) {
	for _, status := range statuses {
		if status.Name == name {
			return status, true
		}
	}
	return vmStatus{}, false
}
