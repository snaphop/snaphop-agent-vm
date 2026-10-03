package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"syscall"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// healthyHost answers every tool probe with a version at or above the floor and
// reports a libvirt connection that works.
func healthyHost() *hostexec.Fake {
	fake := hostexec.NewFake()
	fake.Respond("virsh --version", hostexec.FakeResponse{Stdout: "9.0.0\n"}).
		Respond("virt-install --version", hostexec.FakeResponse{Stdout: "4.0.0\n"}).
		Respond("qemu-img --version", hostexec.FakeResponse{Stdout: "qemu-img version 8.2.1\n"}).
		Respond("podman --version", hostexec.FakeResponse{Stdout: "podman version 4.9.3\n"}).
		Respond("ip -V", hostexec.FakeResponse{Stdout: "ip utility, iproute2-6.5.0\n"}).
		Respond("ssh -V", hostexec.FakeResponse{Stderr: "OpenSSH_9.6p1, OpenSSL 3.0.13\n"}).
		Respond("gh --version", hostexec.FakeResponse{Stdout: "gh version 2.62.0 (2024-11-14)\n"}).
		Respond("virsh --connect qemu:///system version", hostexec.FakeResponse{Stdout: "Compiled against library: libvirt 9.0.0\n"}).
		Respond("virsh --connect qemu:///system net-list --name --all", hostexec.FakeResponse{Stdout: "agent-vm-nat\n"}).
		Respond("virsh --connect qemu:///system net-list --name", hostexec.FakeResponse{Stdout: "agent-vm-nat\n"})

	for _, tool := range []string{"virt-make-fs", "virt-ls", "virt-copy-out", "virt-sysprep"} {
		fake.Respond(tool+" --version", hostexec.FakeResponse{Stdout: tool + " 1.50.1\n"})
	}
	return fake
}

// undeterminableHypervisor stands in for a host whose QEMU account cannot be
// identified, which makes the traversal check skip.
func undeterminableHypervisor() (*hypervisorIdentity, error) { return nil, nil }

// allowKVM is the answer a host with usable hardware virtualization gives.
// Doctor's local check otherwise calls syscall.Access on the real /dev/kvm,
// which would make these tests fail on a machine that has none.
func allowKVM(string) error { return nil }

// runDoctorWith runs doctor against a fake host and returns its JSON report.
func runDoctorWith(t *testing.T, fake *hostexec.Fake, extraArgs ...string) (doctorReport, int) {
	t.Helper()
	return runDoctorWithEnv(t, fake, nil, extraArgs...)
}

// runDoctorWithEnv is the same, for the settings that have no flag and reach
// configuration through the environment instead.
func runDoctorWithEnv(t *testing.T, fake *hostexec.Fake, env map[string]string, extraArgs ...string) (doctorReport, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer

	// The hypervisor identity is pinned to "undeterminable" so the traversal
	// check skips. It would otherwise consult this host's passwd database and
	// judge a 0700 t.TempDir(), making these results depend on the machine the
	// tests run on. TestDoctor_StateDirectory* below cover that check directly.
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(name string) string { return env[name] }, Runner: fake,
		HypervisorIdentity: undeterminableHypervisor,
		KVMAccess:          allowKVM,
		// The state directory stays on this disk even when the test points at
		// a hypervisor on another machine, so these tests exercise the remote
		// branches without needing a fake to stand in for a filesystem too.
		StateFS: state.Local(),
	}
	args := append([]string{
		"--state-dir", t.TempDir(),
		"--config", t.TempDir() + "/absent.toml",
		"--output", "json",
	}, extraArgs...)
	args = append(args, "doctor")

	err := app.run(context.Background(), args)
	code := 0
	if err != nil {
		code = exitCodeFor(err)
	}

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor --output json produced unparseable output: %v\n%s", err, stdout.String())
	}
	return report, code
}

// find returns the check with the given name.
func find(t *testing.T, report doctorReport, name string) check {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	names := []string{}
	for _, c := range report.Checks {
		names = append(names, c.Name)
	}
	t.Fatalf("doctor has no check named %q; it reported: %s", name, strings.Join(names, ", "))
	return check{}
}

func TestDoctor_ChecksEveryToolTheProjectDelegatesTo(t *testing.T) {
	t.Parallel()
	report, _ := runDoctorWith(t, healthyHost())

	for _, tool := range hostexec.RequiredTools() {
		if got := find(t, report, tool.Name); got.Status != statusPass {
			t.Errorf("check %s = %s (%s), want pass", tool.Name, got.Status, got.Detail)
		}
	}
}

func TestDoctor_ReportsAMissingToolAsAFailureWithItsPackage(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	fake.Missing["virt-install"] = true

	report, code := runDoctorWith(t, fake)

	got := find(t, report, "virt-install")
	if got.Status != statusFail {
		t.Errorf("check virt-install = %s, want fail", got.Status)
	}
	if !strings.Contains(got.Remedy, "virtinst") {
		t.Errorf("remedy %q does not name the package to install", got.Remedy)
	}
	if code != ExitHostNotReady {
		t.Errorf("exit code = %d, want %d", code, ExitHostNotReady)
	}
	if report.OK {
		t.Error("report says the host is ready while a required tool is missing")
	}
}

func TestDoctor_ReportsAToolBelowItsMinimumVersion(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	fake.Respond("virsh --version", hostexec.FakeResponse{Stdout: "8.10.0\n"})

	report, code := runDoctorWith(t, fake)

	got := find(t, report, "virsh")
	if got.Status != statusFail {
		t.Fatalf("check virsh = %s (%s), want fail", got.Status, got.Detail)
	}
	// Raising a floor can stop the tool working on a host where it worked
	// yesterday, so the report states both versions.
	for _, want := range []string{"8.10.0", "9.0.0"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not mention %s", got.Detail, want)
		}
	}
	if code != ExitHostNotReady {
		t.Errorf("exit code = %d, want %d", code, ExitHostNotReady)
	}
}

func TestDoctor_ReportsAFailedLibvirtConnection(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	fake.Respond("virsh --connect qemu:///system version", hostexec.FakeResponse{
		Stderr:   "error: failed to connect to the hypervisor\n",
		ExitCode: 1,
	})

	report, code := runDoctorWith(t, fake)

	if got := find(t, report, "libvirt connection"); got.Status != statusFail {
		t.Errorf("check libvirt connection = %s, want fail", got.Status)
	}
	if got := find(t, report, "NAT network agent-vm-nat"); got.Status != statusSkip {
		t.Errorf("NAT check = %s, want skip when there is no libvirt connection", got.Status)
	}
	if code != ExitHostNotReady {
		t.Errorf("exit code = %d, want %d", code, ExitHostNotReady)
	}
}

func TestDoctor_UndefinedNATNetworkIsNotAFailure(t *testing.T) {
	t.Parallel()
	// create defines the network on demand, so its absence is normal on a host
	// that has not created a VM yet.
	fake := healthyHost()
	fake.Respond("virsh --connect qemu:///system net-list --name --all", hostexec.FakeResponse{Stdout: "default\n"})
	fake.Respond("virsh --connect qemu:///system net-list --name", hostexec.FakeResponse{Stdout: "default\n"})

	report, _ := runDoctorWith(t, fake)

	if got := find(t, report, "NAT network agent-vm-nat"); got.Status != statusPass {
		t.Errorf("NAT check = %s (%s), want pass", got.Status, got.Detail)
	}
}

func TestDoctor_ValidatesAConfiguredBridge(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	fake.Respond("ip -d -json link show type bridge", hostexec.FakeResponse{
		Stdout: `[{"ifname":"br0","flags":["BROADCAST","MULTICAST","UP","LOWER_UP"],"operstate":"UP"}]`,
	})

	report, _ := runDoctorWith(t, fake, "--libvirt-uri", "qemu:///system")
	// No bridge is configured by default, so there should be no bridge check.
	for _, c := range report.Checks {
		if strings.HasPrefix(c.Name, "host bridge") {
			t.Errorf("doctor checked a bridge that is not configured: %+v", c)
		}
	}
}

func TestDoctor_NeverChangesHostState(t *testing.T) {
	t.Parallel()
	// doctor is a diagnostic. It may inspect anything and must mutate nothing,
	// which is why every command it issues is declared read-only.
	fake := healthyHost()
	runDoctorWith(t, fake)

	for _, call := range fake.Calls() {
		if call.Effect != hostexec.Read {
			t.Errorf("doctor ran a mutating command: %v", call.Argv())
		}
	}
}

func TestDoctor_TextOutputExplainsFailuresAndRemedies(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	fake.Missing["qemu-img"] = true

	var stdout, stderr bytes.Buffer
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: fake,
		HypervisorIdentity: undeterminableHypervisor,
		KVMAccess:          allowKVM,
		// The state directory stays on this disk even when the test points at
		// a hypervisor on another machine, so these tests exercise the remote
		// branches without needing a fake to stand in for a filesystem too.
		StateFS: state.Local(),
	}
	_ = app.run(context.Background(), []string{
		"--state-dir", t.TempDir(),
		"--config", t.TempDir() + "/absent.toml",
		"doctor",
	})

	out := stdout.String()
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "qemu-img") {
		t.Errorf("text output does not show the failing check:\n%s", out)
	}
	if !strings.Contains(out, "qemu-utils") {
		t.Errorf("text output does not tell the operator what to install:\n%s", out)
	}
}

func TestDoctor_RejectsArguments(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: healthyHost(),
		HypervisorIdentity: undeterminableHypervisor,
	}

	err := app.run(context.Background(), []string{"--state-dir", t.TempDir(), "doctor", "extra"})

	if got := exitCodeFor(err); got != ExitUsage {
		t.Errorf("exit code = %d, want %d", got, ExitUsage)
	}
}

func TestCheckKVM_ReportsTheSubstitutedAccessResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status checkStatus
		detail string
		remedy string
	}{
		{
			name:   "available",
			status: statusPass,
			detail: "/dev/kvm is available",
		},
		{
			name:   "missing",
			err:    syscall.ENOENT,
			status: statusFail,
			detail: "/dev/kvm does not exist",
			remedy: "kvm_intel",
		},
		{
			name:   "not usable by this user",
			err:    syscall.EACCES,
			status: statusFail,
			detail: "/dev/kvm is not readable and writable by this user",
			remedy: "usermod -aG kvm",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var path string
			app := &App{KVMAccess: func(got string) error {
				path = got
				return test.err
			}}

			got := checkKVM(context.Background(), app, &config.Connection{})

			if path != "/dev/kvm" {
				t.Errorf("checked %q, want /dev/kvm", path)
			}
			if got.Status != test.status {
				t.Errorf("status = %s, want %s (%s)", got.Status, test.status, got.Detail)
			}
			if got.Detail != test.detail {
				t.Errorf("detail = %q, want %q", got.Detail, test.detail)
			}
			if test.remedy != "" && !strings.Contains(got.Remedy, test.remedy) {
				t.Errorf("remedy %q does not mention %q", got.Remedy, test.remedy)
			}
		})
	}
}

// A missing device has to fail the whole report. The other doctor tests
// substitute a passing check, so this is the one that keeps the failure wired
// through to the exit code.
func TestDoctor_MissingKVMFailsTheHost(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	var stdout, stderr bytes.Buffer
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env:                func(string) string { return "" },
		Runner:             fake,
		HypervisorIdentity: undeterminableHypervisor,
		KVMAccess:          func(string) error { return syscall.ENOENT },
		StateFS:            state.Local(),
	}

	err := app.run(context.Background(), []string{
		"--state-dir", t.TempDir(),
		"--config", t.TempDir() + "/absent.toml",
		"--output", "json",
		"doctor",
	})

	if got := exitCodeFor(err); got != ExitHostNotReady {
		t.Fatalf("exit code = %d, want %d\n%s", got, ExitHostNotReady, stdout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor --output json produced unparseable output: %v\n%s", err, stdout.String())
	}
	if got := find(t, report, "kvm"); got.Status != statusFail {
		t.Errorf("kvm = %s (%s), want fail", got.Status, got.Detail)
	}
	if report.OK {
		t.Error("report.OK = true, want false")
	}
}

// gh is optional: it is needed only by --github-ssh-key, so a host without it
// is still a ready host.
func TestDoctor_AMissingGHIsReportedWithoutFailingTheHost(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	fake.Missing["gh"] = true

	report, code := runDoctorWith(t, fake)
	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0: a missing gh must not fail doctor", code)
	}
	if got := find(t, report, "gh").Status; got != statusSkip {
		t.Errorf("gh status = %q, want %q", got, statusSkip)
	}
}

// A gh that is installed but unusable is worth telling the operator about
// before `--github-ssh-key` fails, but it still must not fail the host: the
// check warns, which docs/cli.md describes.
func TestDoctor_AnUnusableGHWarnsWithoutFailingTheHost(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		stdout string
	}{
		{"too old", "gh version 1.14.0 (2021-08-04)\n"},
		{"version unreadable", "a wrapper script that prints no version\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := healthyHost()
			fake.Respond("gh --version", hostexec.FakeResponse{Stdout: tt.stdout})

			report, code := runDoctorWith(t, fake)
			if code != ExitOK {
				t.Fatalf("exit code = %d, want 0: an unusable gh must not fail doctor", code)
			}
			got := find(t, report, "gh")
			if got.Status != statusWarn {
				t.Errorf("gh status = %q, want %q (detail: %s)", got.Status, statusWarn, got.Detail)
			}
			if !strings.Contains(got.Remedy, "--github-ssh-key") {
				t.Errorf("remedy %q does not say what is affected", got.Remedy)
			}
		})
	}
}

// aarch64Host is a host whose kernel doctor will actually look at: the
// appliance symbols are ARM-only, so the check has nothing to say elsewhere.
func aarch64Host(kernelConfig string) *hostexec.Fake {
	fake := healthyHost()
	fake.Respond("uname -m", hostexec.FakeResponse{Stdout: "aarch64\n"}).
		Respond("uname -r", hostexec.FakeResponse{Stdout: "6.8.0-31-generic\n"}).
		Respond("cat /boot/config-6.8.0-31-generic", hostexec.FakeResponse{Stdout: kernelConfig})
	return fake
}

// generalPurposeKernelConfig is the shape of a distribution kernel's config:
// both options the appliance depends on are present, one built in and one a
// module, which is how they usually differ.
const generalPurposeKernelConfig = `CONFIG_ARM64=y
CONFIG_SERIAL_AMBA_PL011=y
CONFIG_SERIAL_AMBA_PL011_CONSOLE=y
CONFIG_PCI_HOST_GENERIC=m
CONFIG_VIRTIO_PCI=m
`

// hardwareSpecificKernelConfig is the shape that produces the failure this
// check exists for: a kernel built for one machine, with no PL011 and no
// generic PCIe host bridge, so QEMU's virt board gives the appliance neither a
// console nor its disks.
const hardwareSpecificKernelConfig = `CONFIG_ARM64=y
CONFIG_ARCH_APPLE=y
# CONFIG_SERIAL_AMBA_PL011 is not set
# CONFIG_PCI_HOST_GENERIC is not set
CONFIG_VIRTIO_PCI=m
`

func TestDoctor_PassesTheApplianceCheckOnAGeneralPurposeKernel(t *testing.T) {
	t.Parallel()
	report, code := runDoctorWith(t, aarch64Host(generalPurposeKernelConfig))

	if got := find(t, report, "libguestfs appliance"); got.Status != statusPass {
		t.Errorf("check = %s (%s), want pass", got.Status, got.Detail)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// Without this check the same host fails much later, inside an image build,
// with libguestfs reporting only that its appliance "closed the connection
// unexpectedly" — which names neither the cause nor the fix.
func TestDoctor_FailsWhenTheHostKernelCannotBootTheAppliance(t *testing.T) {
	t.Parallel()
	report, code := runDoctorWith(t, aarch64Host(hardwareSpecificKernelConfig))

	got := find(t, report, "libguestfs appliance")
	if got.Status != statusFail {
		t.Fatalf("check = %s (%s), want fail", got.Status, got.Detail)
	}
	for _, want := range []string{"CONFIG_SERIAL_AMBA_PL011", "CONFIG_PCI_HOST_GENERIC"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not name the missing %s", got.Detail, want)
		}
	}
	if !strings.Contains(got.Remedy, "appliance_kernel") {
		t.Errorf("remedy %q does not name the setting that fixes it", got.Remedy)
	}
	if code != ExitHostNotReady {
		t.Errorf("exit code = %d, want %d", code, ExitHostNotReady)
	}
	if report.OK {
		t.Error("report says the host is ready while no image can be built on it")
	}
}

// The symbols are ARM-only, so their absence on any other architecture means
// nothing at all and must not fail a working host.
func TestDoctor_SkipsTheKernelSymbolsOnNonARMHosts(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	fake.Respond("uname -m", hostexec.FakeResponse{Stdout: "x86_64\n"})

	report, code := runDoctorWith(t, fake)

	if got := find(t, report, "libguestfs appliance"); got.Status != statusPass {
		t.Errorf("check = %s (%s), want pass", got.Status, got.Detail)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// A kernel that publishes no configuration is not a broken one. Guessing
// either way would be worse than saying the question could not be answered.
func TestDoctor_SkipsTheApplianceCheckWhenTheKernelConfigIsUnreadable(t *testing.T) {
	t.Parallel()
	fake := healthyHost()
	fake.Respond("uname -m", hostexec.FakeResponse{Stdout: "aarch64\n"}).
		Respond("uname -r", hostexec.FakeResponse{Stdout: "6.8.0-31-generic\n"}).
		Respond("cat /boot/config-6.8.0-31-generic", hostexec.FakeResponse{ExitCode: 1}).
		Respond("zcat /proc/config.gz", hostexec.FakeResponse{ExitCode: 1})

	report, code := runDoctorWith(t, fake)

	if got := find(t, report, "libguestfs appliance"); got.Status != statusSkip {
		t.Errorf("check = %s (%s), want skip", got.Status, got.Detail)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// A configured directory replaces the host kernel entirely, so the host
// kernel's own shortcomings stop mattering — but a path that is merely wrong
// must not pass as if it were configured correctly.
func TestDoctor_ChecksTheConfiguredApplianceKernelInsteadOfTheHostKernel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() + "/6.8.0-31-generic"
	fake := aarch64Host(hardwareSpecificKernelConfig)
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name != "test" {
			return hostexec.FakeResponse{}, false
		}
		// `test -f` is answered for the two paths a correct directory holds.
		want := map[string]bool{
			dir + "/Image":               true,
			dir + "/modules/modules.dep": true,
		}
		return hostexec.FakeResponse{ExitCode: 1}, !want[c.Args[len(c.Args)-1]]
	}

	report, code := runDoctorWithEnv(t, fake, map[string]string{"AGENT_VM_APPLIANCE_KERNEL": dir})

	got := find(t, report, "libguestfs appliance")
	if got.Status != statusPass {
		t.Errorf("check = %s (%s), want pass", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, dir) {
		t.Errorf("detail %q does not name the directory in use", got.Detail)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestDoctor_FailsWhenTheConfiguredApplianceKernelIsNotThere(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() + "/6.8.0-31-generic"
	fake := aarch64Host(generalPurposeKernelConfig)
	fake.RespondPrefix("test", hostexec.FakeResponse{ExitCode: 1})

	report, code := runDoctorWithEnv(t, fake, map[string]string{"AGENT_VM_APPLIANCE_KERNEL": dir})

	got := find(t, report, "libguestfs appliance")
	if got.Status != statusFail {
		t.Fatalf("check = %s (%s), want fail", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, dir+"/Image") {
		t.Errorf("detail %q does not name the path that is missing", got.Detail)
	}
	if code != ExitHostNotReady {
		t.Errorf("exit code = %d, want %d", code, ExitHostNotReady)
	}
}
