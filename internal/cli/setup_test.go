package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/hostsetup"
)

func TestSetup_RejectsAnUnsupportedReleaseBeforeItChangesAnything(t *testing.T) {
	t.Parallel()
	fake := setupFake("ID=ubuntu\nVERSION_ID=22.04\nPRETTY_NAME=\"Ubuntu 22.04 LTS\"\n")

	code, _, stderr := cliRun(t, fake, t.TempDir(), "--yes", "setup")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "22.04") || !strings.Contains(stderr, "26.04") || !strings.Contains(stderr, "Arch") {
		t.Errorf("stderr does not name the host and the supported releases:\n%s", stderr)
	}
	if strings.Contains(argvText(fake), "apt-get") || strings.Contains(argvText(fake), "sudo") {
		t.Errorf("an unsupported host was modified:\n%s", argvText(fake))
	}
}

func TestSetup_RequiresConfirmation(t *testing.T) {
	t.Parallel()
	fake := setupFake(ubuntuRelease)
	stateDir := t.TempDir()

	code, _, stderr := cliRun(t, fake, stateDir, "setup")
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitUsage, stderr)
	}
	if !strings.Contains(stderr, "Ubuntu 26.04.1 LTS") || !strings.Contains(stderr, "kvm and libvirt") {
		t.Errorf("the prompt does not name the consequence:\n%s", stderr)
	}
	if strings.Contains(argvText(fake), "apt-get") {
		t.Error("setup installed packages without confirmation")
	}
}

func TestSetup_ADeclinedConfirmationChangesNothing(t *testing.T) {
	t.Parallel()
	fake := setupFake(ubuntuRelease)

	code, _, stderr := cliRunStdin(t, fake, t.TempDir(), "n\n", "setup")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "Cancelled.") {
		t.Errorf("stderr = %q, want Cancelled", stderr)
	}
	if strings.Contains(argvText(fake), "apt-get") {
		t.Error("a declined setup installed packages")
	}
}

func TestSetup_InstallsTheUbuntuPackages(t *testing.T) {
	t.Parallel()
	fake := setupFake(ubuntuRelease)
	stateDir := t.TempDir()

	code, stdout, stderr := cliRun(t, fake, stateDir, "--yes", "setup")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "qemu-system-x86") || !strings.Contains(stdout, "guestfs-tools") || !strings.Contains(stdout, "guestfish") || !strings.Contains(stdout, "uidmap") || !strings.Contains(stdout, "passt") {
		t.Errorf("stdout does not name the packages:\n%s", stdout)
	}
	if !strings.Contains(stdout, "docs/host-setup.md") || !strings.Contains(stdout, "new login") {
		t.Errorf("stdout does not say what was left alone or that a new login is required:\n%s", stdout)
	}
	if !strings.Contains(argvText(fake), "apt-get install") || strings.Contains(argvText(fake), "ufw") {
		t.Errorf("commands:\n%s", argvText(fake))
	}
}

func TestSetup_DryRunPrintsThePlanAndRunsNoInstall(t *testing.T) {
	t.Parallel()
	fake := setupFake(ubuntuRelease)

	code, stdout, stderr := cliRun(t, fake, t.TempDir(), "--dry-run", "setup")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "apt-get install") || !strings.Contains(stdout, "qemu-system-x86") {
		t.Errorf("dry run did not print the install:\n%s", stdout)
	}
	if strings.Contains(argvText(fake), "apt-get") {
		t.Errorf("dry run executed the install:\n%s", argvText(fake))
	}
}

func TestSetup_JSONReportsTheRelease(t *testing.T) {
	t.Parallel()
	fake := setupFake(ubuntuRelease)

	code, stdout, stderr := cliRun(t, fake, t.TempDir(), "--output", "json", "--yes", "setup")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	var report hostsetup.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if report.Family != "ubuntu" || report.Version != "26.04" || len(report.Packages) == 0 {
		t.Errorf("report = %+v", report)
	}
}

func TestSetup_GitHubCLIInstallsOnTheLocalHostOnly(t *testing.T) {
	t.Parallel()
	local := setupFake(ubuntuRelease)
	code, _, stderr := cliRun(t, local, t.TempDir(), "--yes", "setup", "--github")
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	if !strings.Contains(argvText(local), "openssh-client acl gh") {
		t.Errorf("local setup did not install gh:\n%s", argvText(local))
	}

	remote := setupFake(ubuntuRelease)
	code, _, stderr = cliRun(t, remote, t.TempDir(),
		"--libvirt-uri", "qemu+ssh://kvm@hypervisor.lan/system", "--yes", "setup", "--github")
	if code != ExitOK {
		t.Fatalf("remote exit code = %d: %s", code, stderr)
	}
	if strings.Contains(argvText(remote), " gh") {
		t.Errorf("remote setup installed gh on the hypervisor:\n%s", argvText(remote))
	}
	if !strings.Contains(stderr, "hypervisor.lan") || !strings.Contains(stderr, "this machine") {
		t.Errorf("stderr does not say where gh belongs:\n%s", stderr)
	}
}

func TestSetup_UsesTheSameQEMUAccountsAsDoctor(t *testing.T) {
	t.Parallel()
	if strings.Join(qemuUserCandidates, ",") != strings.Join(hostsetup.QEMUUserCandidates, ",") {
		t.Errorf("doctor probes %v, setup probes %v", qemuUserCandidates, hostsetup.QEMUUserCandidates)
	}
}

func TestComplete_OffersTheSetupFlag(t *testing.T) {
	t.Parallel()
	got := completeLines(t, t.TempDir(), "setup", "-")
	if strings.Join(got, " ") != "--github" {
		t.Errorf("setup flags = %v, want [--github]", got)
	}
}

const ubuntuRelease = "PRETTY_NAME=\"Ubuntu 26.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"26.04\"\nID=ubuntu\n"

func setupFake(osRelease string) *hostexec.Fake {
	fake := hostexec.NewFake()
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		argv := strings.Join(c.Argv(), " ")
		switch {
		case argv == "cat /etc/os-release":
			return hostexec.FakeResponse{Stdout: osRelease}, true
		case argv == "uname -m":
			return hostexec.FakeResponse{Stdout: "x86_64\n"}, true
		case argv == "id -un":
			return hostexec.FakeResponse{Stdout: "operator\n"}, true
		case argv == "id -u":
			return hostexec.FakeResponse{Stdout: "1000\n"}, true
		case strings.HasPrefix(argv, "id -nG"):
			return hostexec.FakeResponse{Stdout: "operator\n"}, true
		case c.Name == "realpath":
			return hostexec.FakeResponse{Stdout: c.Args[len(c.Args)-1] + "\n"}, true
		case strings.HasPrefix(argv, "systemctl show"):
			unit := c.Args[len(c.Args)-1]
			if unit == "libvirtd.service" || unit == "virtlogd.socket" || unit == "virtlockd.socket" {
				return hostexec.FakeResponse{Stdout: "loaded\n"}, true
			}
			return hostexec.FakeResponse{Stdout: "not-found\n"}, true
		case argv == "cat /etc/libvirt/qemu.conf":
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "No such file"}, true
		case strings.HasPrefix(argv, "getent passwd"):
			if strings.HasSuffix(argv, "libvirt-qemu") {
				return hostexec.FakeResponse{Stdout: "libvirt-qemu:x:64055:64055::/:/usr/sbin/nologin\n"}, true
			}
			return hostexec.FakeResponse{ExitCode: 2}, true
		case c.Name == "test":
			return hostexec.FakeResponse{}, true
		case strings.Contains(argv, "/usr/bin/test -x"):
			return hostexec.FakeResponse{}, true
		default:
			return hostexec.FakeResponse{}, true
		}
	}
	return fake
}

func argvText(fake *hostexec.Fake) string {
	var lines []string
	for _, call := range fake.Calls() {
		lines = append(lines, strings.Join(call.Argv(), " "))
	}
	return strings.Join(lines, "\n")
}
