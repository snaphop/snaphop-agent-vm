package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

func TestLoadState_ReadsTheCapturedSystemctlShow(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(toolout(t, "systemctl-show-LoadState-loaded.txt")) != "loaded" {
		t.Fatal("the loaded fixture is not the word systemctl show prints")
	}
	if strings.TrimSpace(toolout(t, "systemctl-show-LoadState-not-found.txt")) != "not-found" {
		t.Fatal("the missing-unit fixture is not the word systemctl show prints")
	}
}

func TestApply_InstallsEnablesGroupsAndGrantsSearch(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{})
	host := ubuntuHost()

	report, err := Apply(context.Background(), fake, host, Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.Relogin != true || strings.Join(report.GroupsAdded, ",") != "kvm,libvirt" {
		t.Errorf("groups added = %v, relogin %v", report.GroupsAdded, report.Relogin)
	}
	if strings.Join(report.Units, " ") != "libvirtd.service virtlogd.socket virtlockd.socket" {
		t.Errorf("units = %v", report.Units)
	}
	if len(report.Search) != 1 || report.Search[0].Path != "/home/operator" || report.Search[0].User != "libvirt-qemu" {
		t.Errorf("search grants = %+v", report.Search)
	}

	joined := argvJoined(fake)
	for _, want := range []string{
		"sudo -n -- env DEBIAN_FRONTEND=noninteractive apt-get update",
		"sudo -n -- env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends qemu-system-x86",
		"sudo -n -- systemctl enable --now libvirtd.service virtlogd.socket virtlockd.socket",
		"sudo -n -- usermod -aG kvm,libvirt operator",
		"mkdir -p /home/operator/.local/share/agent-vm",
		"sudo -n -- setfacl -m u:libvirt-qemu:x /home/operator",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q\n%s", want, joined)
		}
	}
	for _, call := range fake.Calls() {
		name := commandName(call)
		switch name {
		case "ufw", "firewall-cmd", "nft", "nmcli", "modprobe", "ip", "sh":
			t.Errorf("setup invoked %s: %s", name, strings.Join(call.Argv(), " "))
		}
		if name == "env" {
			for _, arg := range call.Args {
				if arg == "sh" || arg == "-c" {
					t.Errorf("setup invoked a shell: %s", strings.Join(call.Argv(), " "))
				}
			}
		}
	}
}

func commandName(c hostexec.Command) string {
	argv := c.Argv()
	if c.Name == "sudo" {
		for i, arg := range argv {
			if arg == "--" && i+1 < len(argv) {
				return argv[i+1]
			}
		}
	}
	return c.Name
}

func TestApply_PrefersVirtqemudWhenLibvirtdIsAbsent(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{modular: true})

	report, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/var/lib/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.Units[0] != "virtqemud.service" {
		t.Errorf("units = %v, want virtqemud first", report.Units)
	}
	if strings.Contains(argvJoined(fake), "libvirtd.service virtlogd") {
		t.Error("setup enabled libvirtd on a modular host")
	}
}

func TestApply_DoesNotGrantSearchWhenQEMUCanAlreadyTraverse(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{searchable: true, groups: "operator kvm libvirt"})

	report, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(report.Search) != 0 || report.Relogin {
		t.Errorf("search = %+v, relogin %v; nothing needed doing", report.Search, report.Relogin)
	}
	if strings.Contains(argvJoined(fake), "setfacl") || strings.Contains(argvJoined(fake), "usermod") {
		t.Errorf("setup changed access that was already in place:\n%s", argvJoined(fake))
	}
}

func TestApply_SessionSkipsTheSearchGrantAndTheLibvirtGroup(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{})

	report, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm"},
		Session:  true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.SearchNote == "" || len(report.Search) != 0 {
		t.Errorf("session search = %+v note %q", report.Search, report.SearchNote)
	}
	joined := argvJoined(fake)
	if strings.Contains(joined, "setfacl") || strings.Contains(joined, "usermod -aG kvm,libvirt") {
		t.Errorf("session setup touched the libvirt group or an ACL:\n%s", joined)
	}
}

func TestApply_RootDoesNotCallSudo(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{searchable: true, groups: "root kvm libvirt"})
	host := ubuntuHost()
	host.User = "root"
	host.Root = true

	if _, err := Apply(context.Background(), fake, host, Options{
		StateDir: "/var/lib/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(argvJoined(fake), "sudo") {
		t.Errorf("root setup called sudo:\n%s", argvJoined(fake))
	}
	if !strings.Contains(argvJoined(fake), "apt-get install") {
		t.Errorf("root setup did not install packages:\n%s", argvJoined(fake))
	}
}

func TestApply_FedoraAndArchUseTheirOwnPackageManagers(t *testing.T) {
	t.Parallel()
	fedora := newFakeHost(t, fakeHostOptions{searchable: true, groups: "operator kvm libvirt"})
	host := ubuntuHost()
	host.Family = "fedora"
	host.Version = "44"
	host.Name = "Fedora Linux 44"
	if _, err := Apply(context.Background(), fedora, host, Options{
		StateDir: "/var/lib/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	}); err != nil {
		t.Fatalf("fedora: %v", err)
	}
	if !strings.Contains(argvJoined(fedora), "sudo -n -- dnf -y install qemu-kvm") {
		t.Errorf("fedora install:\n%s", argvJoined(fedora))
	}

	arch := newFakeHost(t, fakeHostOptions{searchable: true, groups: "operator kvm libvirt"})
	host.Family = "arch"
	host.Version = ""
	host.Name = "Arch Linux"
	if _, err := Apply(context.Background(), arch, host, Options{
		StateDir: "/var/lib/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	}); err != nil {
		t.Fatalf("arch: %v", err)
	}
	got := argvJoined(arch)
	if !strings.Contains(got, "sudo -n -- pacman -Sy --noconfirm --needed qemu-base") || strings.Contains(got, "-Syu") {
		t.Errorf("arch install:\n%s", got)
	}
}

func TestApply_DryRunPlansLibvirtdWhenTheUnitsAreNotInstalledYet(t *testing.T) {
	t.Parallel()
	inner := newFakeHost(t, fakeHostOptions{noUnits: true, noQEMUUser: true})
	var printed bytes.Buffer
	dry := hostexec.NewDryRun(inner, &printed)

	report, err := Apply(context.Background(), dry, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !report.DryRun || !report.UnitsAssumed || report.Units[0] != "libvirtd.service" {
		t.Errorf("report = %+v", report)
	}
	if report.SearchNote == "" {
		t.Error("dry run on a host without libvirt did not say the search grant waits for the account")
	}
	if strings.Contains(argvJoined(inner), "apt-get") {
		t.Error("dry run executed the package install")
	}
	if !strings.Contains(printed.String(), "apt-get install") || !strings.Contains(printed.String(), "systemctl enable --now libvirtd.service") {
		t.Errorf("dry run did not print the plan:\n%s", printed.String())
	}
}

func TestApply_ExplainsASudoPasswordPrompt(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == "sudo" {
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "sudo: a password is required"}, true
		}
		if c.Name == "realpath" {
			return hostexec.FakeResponse{Stdout: c.Args[len(c.Args)-1] + "\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	_, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/var/lib/agent-vm",
		Groups:   []string{"kvm"},
	})
	if err == nil || !strings.Contains(err.Error(), "sudo -v") {
		t.Fatalf("error = %v, want a sudo -v remedy", err)
	}
}

func TestApply_RejectsARelativeStateDirectory(t *testing.T) {
	t.Parallel()
	_, err := Apply(context.Background(), hostexec.NewFake(), ubuntuHost(), Options{StateDir: "agent-vm"})
	var path *PathError
	if !errors.As(err, &path) {
		t.Fatalf("error = %v, want *PathError", err)
	}
}

func TestDetect_ReadsTheReleaseAndRefusesAnUnsupportedOne(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		switch strings.Join(c.Argv(), " ") {
		case "cat /etc/os-release":
			return hostexec.FakeResponse{Stdout: toolout(t, "os-release-ubuntu-26.04.txt")}, true
		case "uname -m":
			return hostexec.FakeResponse{Stdout: "x86_64\n"}, true
		case "id -un":
			return hostexec.FakeResponse{Stdout: "operator\n"}, true
		case "id -u":
			return hostexec.FakeResponse{Stdout: "1000\n"}, true
		default:
			return hostexec.FakeResponse{}, false
		}
	}
	host, err := Detect(context.Background(), fake)
	if err != nil {
		t.Fatal(err)
	}
	if host.Family != "ubuntu" || host.Version != "26.04" || host.User != "operator" || host.Root || host.Arch != "x86_64" {
		t.Errorf("host = %+v", host)
	}

	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if c.Name == "cat" {
			return hostexec.FakeResponse{Stdout: "ID=ubuntu\nVERSION_ID=22.04\nPRETTY_NAME=\"Ubuntu 22.04 LTS\"\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}
	if _, err := Detect(context.Background(), fake); err == nil {
		t.Fatal("Detect accepted Ubuntu 22.04")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Detect(ctx, fake); err == nil {
		t.Fatal("Detect ran after its context was canceled")
	}
}

func TestApply_StopsWhenNoLibvirtUnitExistsAfterInstall(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{noUnits: true})
	_, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/var/lib/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err == nil || !strings.Contains(err.Error(), "libvirtd") {
		t.Fatalf("error = %v", err)
	}
}

type fakeHostOptions struct {
	modular              bool
	searchable           bool
	groups               string
	noUnits              bool
	noQEMUUser           bool
	apparmorProfile      string
	apparmorLocal        string
	apparmorLocalPresent bool
	parserFails          bool
}

func newFakeHost(t *testing.T, opt fakeHostOptions) *hostexec.Fake {
	t.Helper()
	if opt.groups == "" {
		opt.groups = "operator"
	}
	loaded := strings.TrimSpace(toolout(t, "systemctl-show-LoadState-loaded.txt"))
	missing := strings.TrimSpace(toolout(t, "systemctl-show-LoadState-not-found.txt"))
	fake := hostexec.NewFake()
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		argv := strings.Join(c.Argv(), " ")
		switch {
		case c.Name == "realpath":
			return hostexec.FakeResponse{Stdout: c.Args[len(c.Args)-1] + "\n"}, true
		case strings.HasPrefix(argv, "systemctl show"):
			if opt.noUnits {
				return hostexec.FakeResponse{Stdout: missing + "\n"}, true
			}
			unit := c.Args[len(c.Args)-1]
			if opt.modular {
				if unit == "libvirtd.service" {
					return hostexec.FakeResponse{Stdout: missing + "\n"}, true
				}
				if strings.HasPrefix(unit, "virtqemu") || strings.HasPrefix(unit, "virtnetwork") || strings.HasPrefix(unit, "virtstorage") || strings.HasPrefix(unit, "virtlog") || strings.HasPrefix(unit, "virtlock") {
					return hostexec.FakeResponse{Stdout: loaded + "\n"}, true
				}
			}
			if unit == "libvirtd.service" || unit == "virtlogd.socket" || unit == "virtlockd.socket" {
				return hostexec.FakeResponse{Stdout: loaded + "\n"}, true
			}
			return hostexec.FakeResponse{Stdout: missing + "\n"}, true
		case strings.HasPrefix(argv, "id -nG"):
			return hostexec.FakeResponse{Stdout: opt.groups + "\n"}, true
		case argv == "cat /etc/libvirt/qemu.conf":
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "cat: /etc/libvirt/qemu.conf: No such file or directory"}, true
		case strings.HasPrefix(argv, "getent passwd"):
			if opt.noQEMUUser || !strings.HasSuffix(argv, "libvirt-qemu") {
				return hostexec.FakeResponse{ExitCode: 2}, true
			}
			return hostexec.FakeResponse{Stdout: "libvirt-qemu:x:64055:64055::/var/lib/libvirt:/usr/sbin/nologin\n"}, true
		case c.Name == "test" && len(c.Args) > 0 && c.Args[0] == "-d":
			return hostexec.FakeResponse{}, true
		case argv == "test -f "+VirtAAHelperProfile:
			if opt.apparmorProfile == "" {
				return hostexec.FakeResponse{ExitCode: 1}, true
			}
			return hostexec.FakeResponse{}, true
		case argv == "cat "+VirtAAHelperProfile:
			return hostexec.FakeResponse{Stdout: opt.apparmorProfile}, true
		case argv == "test -f "+VirtAAHelperLocal:
			if !opt.apparmorLocalPresent {
				return hostexec.FakeResponse{ExitCode: 1}, true
			}
			return hostexec.FakeResponse{}, true
		case argv == "cat "+VirtAAHelperLocal:
			return hostexec.FakeResponse{Stdout: opt.apparmorLocal}, true
		case strings.Contains(argv, "apparmor_parser"):
			if opt.parserFails {
				return hostexec.FakeResponse{ExitCode: 1, Stderr: "AppArmor parser error for the profile\n"}, true
			}
			return hostexec.FakeResponse{}, true
		case strings.Contains(argv, "/usr/bin/test -x"):
			if opt.searchable || c.Args[len(c.Args)-1] != "/home/operator" {
				return hostexec.FakeResponse{}, true
			}
			return hostexec.FakeResponse{ExitCode: 1}, true
		case c.Name == "sudo" || c.Name == "mkdir" || c.Name == "env" || c.Name == "dnf" || c.Name == "pacman" || c.Name == "apt-get" || c.Name == "usermod" || c.Name == "systemctl" || c.Name == "setfacl" || c.Name == "runuser":
			return hostexec.FakeResponse{}, true
		default:
			t.Errorf("unexpected command %s", argv)
			return hostexec.FakeResponse{ExitCode: 99, Stderr: "unexpected"}, true
		}
	}
	return fake
}

func ubuntuHost() Host {
	return Host{
		Release: Release{Family: "ubuntu", Version: "26.04", Name: "Ubuntu 26.04.1 LTS"},
		Arch:    "x86_64",
		User:    "operator",
	}
}

const shippedVirtAADeny = "  audit deny @{HOME}/.*/** mrwkl,\n"

func TestStateDirUnderHiddenHome_MatchesTheShippedDeny(t *testing.T) {
	t.Parallel()
	denied := []string{
		"/home/operator/.local/share/agent-vm",
		"/home/operator/.local",
		"/root/.local/share/agent-vm",
	}
	allowed := []string{
		"/var/lib/agent-vm",
		"/home/operator/vms",
		"/home/operator/work/.cache/agent-vm",
		"/opt/.local/agent-vm",
		"relative/.local",
	}
	for _, path := range denied {
		if !StateDirUnderHiddenHome(path) {
			t.Errorf("%s should be covered by @{HOME}/.*/**", path)
		}
	}
	for _, path := range allowed {
		if StateDirUnderHiddenHome(path) {
			t.Errorf("%s is not covered by the shipped deny", path)
		}
	}
}

func TestVirtAAHelperProfileDeniesHiddenHome_IgnoresComments(t *testing.T) {
	t.Parallel()
	if VirtAAHelperProfileDeniesHiddenHome("# audit deny @{HOME}/.*/** mrwkl,\n") {
		t.Fatal("a comment is not a deny rule")
	}
	if !VirtAAHelperProfileDeniesHiddenHome(shippedVirtAADeny) {
		t.Fatal("the shipped deny was not recognised")
	}
}

func TestApply_AllowsVirtAAHelperToReadAHiddenStateDirectory(t *testing.T) {
	t.Parallel()
	const stateDir = "/home/operator/.local/share/agent-vm"
	fake := newFakeHost(t, fakeHostOptions{
		searchable:      true,
		groups:          "operator kvm libvirt",
		apparmorProfile: shippedVirtAADeny + "@{HOME}/** r,\n",
	})

	report, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: stateDir,
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.AppArmor != stateDir {
		t.Errorf("AppArmor = %q, want %s", report.AppArmor, stateDir)
	}
	if !strings.Contains(argvJoined(fake), "sudo -n -- apparmor_parser -r "+VirtAAHelperProfile) {
		t.Errorf("profile was not reloaded:\n%s", argvJoined(fake))
	}
	got := stdins(t, fake, "tee")
	if len(got) != 1 {
		t.Fatalf("tee calls = %d, want 1", len(got))
	}
	rule, err := VirtAAHelperRule(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[0], "# BEGIN agent-vm setup") || !strings.Contains(got[0], rule) || !strings.Contains(got[0], "# END agent-vm setup") {
		t.Errorf("local profile =\n%s", got[0])
	}
	if strings.Contains(got[0], "priority=1 /home/operator/**") || strings.Contains(got[0], "priority=1 /**") {
		t.Errorf("rule is wider than the state directory:\n%s", got[0])
	}
}

func TestApply_DoesNotRewriteAVirtAAHelperRuleThatIsAlreadyPresent(t *testing.T) {
	t.Parallel()
	const stateDir = "/home/operator/.local/share/agent-vm"
	rule, err := VirtAAHelperRule(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeHost(t, fakeHostOptions{
		searchable:           true,
		groups:               "operator kvm libvirt",
		apparmorProfile:      shippedVirtAADeny,
		apparmorLocalPresent: true,
		apparmorLocal:        "# kept by hand\n" + rule + "\n",
	})

	report, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: stateDir,
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.AppArmor != "" {
		t.Errorf("AppArmor = %q, want nothing changed", report.AppArmor)
	}
	if strings.Contains(argvJoined(fake), "tee") || strings.Contains(argvJoined(fake), "apparmor_parser") {
		t.Errorf("setup rewrote a rule that was already present:\n%s", argvJoined(fake))
	}
}

func TestApply_ReplacesItsOwnVirtAAHelperBlockAndKeepsOtherLines(t *testing.T) {
	t.Parallel()
	const stateDir = "/root/.local/share/agent-vm"
	old, err := VirtAAHelperRule("/root/.local/old")
	if err != nil {
		t.Fatal(err)
	}
	local := "# site keep\n" + virtAABegin + "\n" + old + "\n" + virtAAEnd + "\n# site after\n"
	fake := newFakeHost(t, fakeHostOptions{
		searchable:           true,
		groups:               "operator kvm libvirt",
		apparmorProfile:      shippedVirtAADeny,
		apparmorLocalPresent: true,
		apparmorLocal:        local,
	})

	if _, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: stateDir,
		Groups:   []string{"kvm", "libvirt"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := stdins(t, fake, "tee")
	if len(got) != 1 {
		t.Fatalf("tee calls = %d, want 1", len(got))
	}
	rule, err := VirtAAHelperRule(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[0], "# site keep") || !strings.Contains(got[0], "# site after") || !strings.Contains(got[0], rule) {
		t.Errorf("local profile =\n%s", got[0])
	}
	if strings.Contains(got[0], old) {
		t.Errorf("old state directory is still allowed:\n%s", got[0])
	}
	if strings.Count(got[0], virtAABegin) != 1 {
		t.Errorf("marker repeated:\n%s", got[0])
	}
}

func TestApply_RestoresTheLocalFileWhenApparmorParserFails(t *testing.T) {
	t.Parallel()
	const stateDir = "/home/operator/.local/share/agent-vm"
	previous := "# site keep\n"
	fake := newFakeHost(t, fakeHostOptions{
		searchable:           true,
		groups:               "operator kvm libvirt",
		apparmorProfile:      shippedVirtAADeny,
		apparmorLocalPresent: true,
		apparmorLocal:        previous,
		parserFails:          true,
	})

	_, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: stateDir,
		Groups:   []string{"kvm", "libvirt"},
	})
	if err == nil || !strings.Contains(err.Error(), "apparmor_parser") {
		t.Fatalf("error = %v, want the parser failure", err)
	}
	got := stdins(t, fake, "tee")
	if len(got) != 2 {
		t.Fatalf("tee calls = %d, want the write and the restore", len(got))
	}
	if got[1] != previous {
		t.Errorf("restored file =\n%s\nwant\n%s", got[1], previous)
	}
}

func TestApply_RemovesALocalFileItCreatedWhenApparmorParserFails(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{
		searchable:      true,
		groups:          "operator kvm libvirt",
		apparmorProfile: shippedVirtAADeny,
		parserFails:     true,
	})

	_, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err == nil {
		t.Fatal("Apply succeeded after apparmor_parser failed")
	}
	if !strings.Contains(argvJoined(fake), "sudo -n -- rm -f "+VirtAAHelperLocal) {
		t.Errorf("the file setup created was left behind:\n%s", argvJoined(fake))
	}
}

func TestApply_ReportsABrokenVirtAAHelperMarker(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{
		searchable:           true,
		groups:               "operator kvm libvirt",
		apparmorProfile:      shippedVirtAADeny,
		apparmorLocalPresent: true,
		apparmorLocal:        virtAABegin + "\npriority=1 /tmp/old rk,\n",
	})

	_, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err == nil || !strings.Contains(err.Error(), virtAAEnd) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(argvJoined(fake), "tee") {
		t.Errorf("setup wrote over a broken local file:\n%s", argvJoined(fake))
	}
}

func TestApply_RefusesAnAppArmorPathItCannotExpress(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{
		searchable:      true,
		groups:          "operator kvm libvirt",
		apparmorProfile: shippedVirtAADeny,
	})

	_, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/my vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err == nil || !strings.Contains(err.Error(), "my vm") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(argvJoined(fake), "tee") || strings.Contains(argvJoined(fake), "apparmor_parser") {
		t.Errorf("setup wrote a rule for a path it cannot express:\n%s", argvJoined(fake))
	}
}

func TestApply_SkipsVirtAAHelperWhenTheProfileDoesNotDenyHiddenHomes(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{
		searchable:      true,
		groups:          "operator kvm libvirt",
		apparmorProfile: "  @{HOME}/** r,\n  /**.qcow{,2} r,\n",
	})

	report, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.AppArmor != "" || strings.Contains(argvJoined(fake), "apparmor_parser") {
		t.Errorf("AppArmor = %q\n%s", report.AppArmor, argvJoined(fake))
	}
}

func TestApply_SkipsVirtAAHelperForAStateDirectoryTheProfileAlreadyAllows(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{
		searchable:      true,
		groups:          "operator kvm libvirt",
		apparmorProfile: shippedVirtAADeny,
	})

	if _, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/home/operator/vms",
		Groups:   []string{"kvm", "libvirt"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(argvJoined(fake), "apparmor") || strings.Contains(argvJoined(fake), VirtAAHelperProfile) {
		t.Errorf("setup inspected AppArmor for a path the shipped profile allows:\n%s", argvJoined(fake))
	}
}

func TestApply_SessionStillAllowsVirtAAHelper(t *testing.T) {
	t.Parallel()
	fake := newFakeHost(t, fakeHostOptions{apparmorProfile: shippedVirtAADeny})

	report, err := Apply(context.Background(), fake, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm"},
		Session:  true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.AppArmor == "" || strings.Contains(argvJoined(fake), "setfacl") {
		t.Errorf("AppArmor = %q\n%s", report.AppArmor, argvJoined(fake))
	}
}

func TestApply_DryRunPrintsTheVirtAAHelperRuleWithoutWritingIt(t *testing.T) {
	t.Parallel()
	const stateDir = "/home/operator/.local/share/agent-vm"
	inner := newFakeHost(t, fakeHostOptions{
		searchable:      true,
		groups:          "operator kvm libvirt",
		apparmorProfile: shippedVirtAADeny,
	})
	var printed bytes.Buffer
	dry := hostexec.NewDryRun(inner, &printed)

	report, err := Apply(context.Background(), dry, ubuntuHost(), Options{
		StateDir: stateDir,
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.AppArmor != stateDir {
		t.Errorf("AppArmor = %q, want the planned directory", report.AppArmor)
	}
	plan := printed.String()
	if !strings.Contains(plan, "tee "+VirtAAHelperLocal) || !strings.Contains(plan, "apparmor_parser -r "+VirtAAHelperProfile) {
		t.Errorf("plan =\n%s", plan)
	}
	if strings.Contains(argvJoined(inner), "tee") || strings.Contains(argvJoined(inner), "apparmor_parser") {
		t.Errorf("dry run wrote the profile:\n%s", argvJoined(inner))
	}
}

func TestApply_DryRunDefersTheVirtAAHelperRuleUntilTheProfileExists(t *testing.T) {
	t.Parallel()
	inner := newFakeHost(t, fakeHostOptions{noUnits: true, noQEMUUser: true})
	dry := hostexec.NewDryRun(inner, &bytes.Buffer{})

	report, err := Apply(context.Background(), dry, ubuntuHost(), Options{
		StateDir: "/home/operator/.local/share/agent-vm",
		Groups:   []string{"kvm", "libvirt"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.AppArmor != "" || !strings.Contains(report.AppArmorNote, "once its AppArmor profile exists") {
		t.Errorf("AppArmor = %q note %q", report.AppArmor, report.AppArmorNote)
	}
	if strings.Contains(argvJoined(inner), "tee") || strings.Contains(argvJoined(inner), "apparmor_parser") {
		t.Errorf("dry run wrote the profile:\n%s", argvJoined(inner))
	}
}

func stdins(t *testing.T, fake *hostexec.Fake, tool string) []string {
	t.Helper()
	var out []string
	for _, call := range fake.Calls() {
		if commandName(call) != tool {
			continue
		}
		if call.Stdin == nil {
			out = append(out, "")
			continue
		}
		body, err := io.ReadAll(call.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(body))
	}
	return out
}

func argvJoined(fake *hostexec.Fake) string {
	var lines []string
	for _, call := range fake.Calls() {
		lines = append(lines, strings.Join(call.Argv(), " "))
	}
	return strings.Join(lines, "\n")
}
