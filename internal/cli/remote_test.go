package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

const remoteURI = "qemu+ssh://kvm@hv.example.com/system"

// asRemote adds the answers a hypervisor on another machine gives: /dev/kvm
// and the group membership are that host's, asked over the transport, and the
// tools it runs report themselves the same way a local host's do.
func asRemote(fake *hostexec.Fake) *hostexec.Fake {
	fake.Hypervisor = "kvm@hv.example.com"
	return fake.
		Respond("id -un", hostexec.FakeResponse{Stdout: "kvm\n"}).
		Respond("id -nG", hostexec.FakeResponse{Stdout: "kvm libvirt users\n"})
}

func remoteHost() *hostexec.Fake { return asRemote(healthyHost()) }

func runRemoteDoctor(t *testing.T, fake *hostexec.Fake) (doctorReport, int) {
	t.Helper()
	return runDoctorWith(t, fake, "--libvirt-uri", remoteURI)
}

func checkNamed(t *testing.T, report doctorReport, name string) check {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, report.Checks)
	return check{}
}

// virsh runs on the hypervisor, so it is given the URI as that machine reads
// it. Handing it the remote URI would send it back over ssh to the machine it
// is already on (ADR-0010).
func TestRemote_VirshIsGivenTheHypervisorsOwnURI(t *testing.T) {
	t.Parallel()
	fake := remoteHost()
	runRemoteDoctor(t, fake)

	argvs := strings.Join(fake.Argvs(), "\n")
	if !strings.Contains(argvs, "virsh --connect qemu:///system version") {
		t.Errorf("virsh was not given the hypervisor's own URI:\n%s", argvs)
	}
	if strings.Contains(argvs, "--connect "+remoteURI) {
		t.Errorf("virsh was given the remote URI, which would connect back over ssh:\n%s", argvs)
	}
}

// /dev/kvm on this machine says nothing about the hypervisor's, so the question
// is asked there.
func TestDoctor_RemoteKVMIsCheckedOnTheHypervisor(t *testing.T) {
	t.Parallel()
	fake := remoteHost()
	report, _ := runRemoteDoctor(t, fake)

	kvm := checkNamed(t, report, "kvm")
	if kvm.Status != statusPass {
		t.Errorf("kvm = %s (%s), want pass", kvm.Status, kvm.Detail)
	}
	if !strings.Contains(kvm.Detail, "kvm@hv.example.com") {
		t.Errorf("kvm detail does not name the host: %s", kvm.Detail)
	}
	if !strings.Contains(strings.Join(fake.Argvs(), "\n"), "test -e /dev/kvm") {
		t.Errorf("/dev/kvm was not probed on the hypervisor: %s", fake)
	}
}

func TestDoctor_RemoteKVMMissingFails(t *testing.T) {
	t.Parallel()
	fake := remoteHost()
	fake.Respond("test -e /dev/kvm", hostexec.FakeResponse{ExitCode: 1})
	report, code := runRemoteDoctor(t, fake)

	kvm := checkNamed(t, report, "kvm")
	if kvm.Status != statusFail {
		t.Errorf("kvm = %s, want fail", kvm.Status)
	}
	if code != ExitHostNotReady {
		t.Errorf("exit = %d, want %d", code, ExitHostNotReady)
	}
}

func TestDoctor_RemoteGroupsAreTheHypervisorsAccount(t *testing.T) {
	t.Parallel()
	fake := remoteHost()
	fake.Respond("id -nG", hostexec.FakeResponse{Stdout: "kvm users\n"})
	report, _ := runRemoteDoctor(t, fake)

	libvirt := checkNamed(t, report, "group libvirt")
	if libvirt.Status != statusWarn {
		t.Errorf("group libvirt = %s, want warn", libvirt.Status)
	}
	if !strings.Contains(libvirt.Detail, "kvm on kvm@hv.example.com") {
		t.Errorf("detail does not name the remote account: %s", libvirt.Detail)
	}
}

// The firewall and the state directory's ancestors that matter are the
// hypervisor's. A verdict computed from this machine's would be confidently
// wrong, so the checks say they did not run instead.
func TestDoctor_LocalOnlyChecksAreSkippedForARemoteHypervisor(t *testing.T) {
	t.Parallel()
	fake := remoteHost()
	report, _ := runRemoteDoctor(t, fake)

	for _, name := range []string{"host firewall forwarding", "host firewall guest services", "state directory access"} {
		got := checkNamed(t, report, name)
		if got.Status != statusSkip {
			t.Errorf("%s = %s (%s), want skip", name, got.Status, got.Detail)
		}
		if got.Remedy == "" {
			t.Errorf("%s skipped without saying what to do instead", name)
		}
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "sudo ") {
			t.Errorf("remote doctor ran sudo for this machine's firewall: %s", argv)
		}
	}
}

// Everything below the transport depends on it, so an unreachable hypervisor is
// reported once rather than as eight failures with the same cause.
func TestDoctor_UnreachableHypervisorStopsAtTheTransport(t *testing.T) {
	t.Parallel()
	fake := remoteHost()
	fake.Respond("true", hostexec.FakeResponse{
		Err: &hostexec.TransportError{Destination: "kvm@hv.example.com", Stderr: "Permission denied (publickey)."},
	})

	report, code := runRemoteDoctor(t, fake)

	if code != ExitHostNotReady {
		t.Errorf("exit = %d, want %d", code, ExitHostNotReady)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("reported %d checks, want only the transport: %+v", len(report.Checks), report.Checks)
	}
	transport := report.Checks[0]
	if transport.Status != statusFail || !strings.Contains(transport.Name, "kvm@hv.example.com") {
		t.Errorf("transport check = %+v", transport)
	}
	if !strings.Contains(transport.Remedy, "ssh kvm@hv.example.com true") {
		t.Errorf("remedy does not say how to fix it: %s", transport.Remedy)
	}
}

// A local connection has no transport to report, so nothing about a hypervisor
// host appears.
func TestDoctor_LocalConnectionReportsNoTransport(t *testing.T) {
	t.Parallel()
	report, _ := runDoctorWith(t, healthyHost())

	for _, c := range report.Checks {
		if strings.HasPrefix(c.Name, "hypervisor host") {
			t.Errorf("a local run reported a transport check: %+v", c)
		}
	}
}

// A guest sits on a network that exists only on the hypervisor, so it is
// reached through that host rather than directly.
func TestSSH_RemoteGuestIsReachedThroughTheHypervisor(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	fake := asRemote(createHost(t))
	if code, _, stderr := cliRun(t, fake, stateDir,
		"--libvirt-uri", remoteURI, "create", "web", "--ssh-key", keyPath); code != ExitOK {
		t.Fatalf("create failed with %d: %s", code, stderr)
	}

	fake.RespondPrefix("virsh --connect qemu:///system list --all --name", hostexec.FakeResponse{Stdout: "web\n"})
	fake.RespondPrefix("virsh --connect qemu:///system domstate", hostexec.FakeResponse{Stdout: "running\n"})

	if code, _, stderr := cliRun(t, fake, stateDir, "--libvirt-uri", remoteURI, "ssh", "web"); code != ExitOK {
		t.Fatalf("ssh failed with %d: %s", code, stderr)
	}

	became := fake.Became()
	if len(became) != 1 {
		t.Fatalf("became %d commands, want 1: %s", len(became), fake)
	}
	argv := strings.Join(became[0].Argv(), " ")
	if !strings.Contains(argv, "-J kvm@hv.example.com") {
		t.Errorf("ssh does not jump through the hypervisor: %s", argv)
	}
	if became[0].Location != hostexec.Client {
		t.Errorf("the guest connection was not made from this machine: %+v", became[0])
	}
}

// With a local hypervisor the guest is directly reachable, so nothing is
// jumped through.
func TestSSH_LocalGuestIsReachedDirectly(t *testing.T) {
	t.Parallel()
	stateDir, _ := createdVM(t, "agent-01")
	fake := runningHost(t, "agent-01")

	if code, _, stderr := cliRun(t, fake, stateDir, "ssh", "agent-01"); code != ExitOK {
		t.Fatalf("ssh failed with %d: %s", code, stderr)
	}
	if argv := strings.Join(fake.Became()[0].Argv(), " "); strings.Contains(argv, "-J") {
		t.Errorf("a local guest was reached through a jump host: %s", argv)
	}
}

// A VM created against a remote hypervisor records the URI the operator gave,
// transport included: that record is what makes a VM's provenance auditable,
// and it is localized only where virsh is actually invoked.
func TestRemote_VMRecordKeepsTheOperatorsURI(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	fake := asRemote(createHost(t))
	if code, _, stderr := cliRun(t, fake, stateDir,
		"--libvirt-uri", remoteURI, "create", "web", "--ssh-key", keyPath); code != ExitOK {
		t.Fatalf("create failed with %d: %s", code, stderr)
	}

	store, err := state.Open(stateDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	vm, err := store.LoadVM("web")
	if err != nil {
		t.Fatalf("LoadVM: %v", err)
	}
	if vm.LibvirtURI != remoteURI {
		t.Errorf("vm.json records libvirtUri %q, want %q", vm.LibvirtURI, remoteURI)
	}
}

// Replacing this process discards every deferred cleanup with it, so the ssh
// control socket's directory has to be released before `agent-vm ssh` or
// `agent-vm console` execs — otherwise each one orphans a directory in /tmp.
func TestRemote_BecomingACommandReleasesTheRunsResourcesFirst(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	fake := asRemote(createHost(t))
	if code, _, stderr := cliRun(t, fake, stateDir,
		"--libvirt-uri", remoteURI, "create", "web", "--ssh-key", keyPath); code != ExitOK {
		t.Fatalf("create failed with %d: %s", code, stderr)
	}
	fake.RespondPrefix("virsh --connect qemu:///system list --all --name", hostexec.FakeResponse{Stdout: "web\n"})
	fake.RespondPrefix("virsh --connect qemu:///system domstate", hostexec.FakeResponse{Stdout: "running\n"})

	var stdout, stderr bytes.Buffer
	released := false
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: fake, StateFS: state.Local(),
	}
	app.cleanup = append(app.cleanup, func() { released = true })

	if err := app.run(context.Background(), []string{
		"--state-dir", stateDir, "--config", stateDir + "/absent.toml",
		"--libvirt-uri", remoteURI, "ssh", "web",
	}); err != nil {
		t.Fatalf("ssh: %v", err)
	}

	if len(fake.Became()) != 1 {
		t.Fatalf("became %d commands, want 1", len(fake.Became()))
	}
	if !released {
		t.Error("the run's resources were not released before the process was replaced")
	}
}

// An unsupported transport reaches libvirt but gives agent-vm no shell on the
// hypervisor, so it is refused as a usage error before anything runs.
func TestRemote_RejectsATransportWithoutAShell(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	app := &App{
		Stdout: &stdout, Stderr: &stderr,
		Env: func(string) string { return "" }, Runner: healthyHost(),
	}
	dir := t.TempDir()

	code := app.Main(context.Background(), []string{
		"--state-dir", dir, "--config", dir + "/absent.toml",
		"--libvirt-uri", "qemu+tls://hv.example.com/system", "list",
	})

	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "qemu+ssh") {
		t.Errorf("the refusal does not name the supported form: %s", stderr.String())
	}
}
