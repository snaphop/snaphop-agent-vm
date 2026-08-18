package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
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

// runDoctorWith runs doctor against a fake host and returns its JSON report.
func runDoctorWith(t *testing.T, fake *hostexec.Fake, extraArgs ...string) (doctorReport, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer

	// The hypervisor identity is pinned to "undeterminable" so the traversal
	// check skips. It would otherwise consult this host's passwd database and
	// judge a 0700 t.TempDir(), making these results depend on the machine the
	// tests run on. TestDoctor_StateDirectory* below cover that check directly.
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: fake,
		HypervisorIdentity: undeterminableHypervisor,
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
	report, _ := runDoctorWith(t, healthyHost())

	for _, tool := range hostexec.RequiredTools() {
		if got := find(t, report, tool.Name); got.Status != statusPass {
			t.Errorf("check %s = %s (%s), want pass", tool.Name, got.Status, got.Detail)
		}
	}
}

func TestDoctor_ReportsAMissingToolAsAFailureWithItsPackage(t *testing.T) {
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
	fake := healthyHost()
	fake.Respond("ip -json link show type bridge", hostexec.FakeResponse{
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
	fake := healthyHost()
	fake.Missing["qemu-img"] = true

	var stdout, stderr bytes.Buffer
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: fake,
		HypervisorIdentity: undeterminableHypervisor,
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

// gh is optional: it is needed only by --github-ssh-key, so a host without it
// is still a ready host.
func TestDoctor_AMissingGHIsReportedWithoutFailingTheHost(t *testing.T) {
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
