package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/tailscale"
)

const tailscaleCanary = "tskey-auth-test-CANARYKEY1234567890"

func writeTailscaleKey(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tskey")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing the auth key file: %v", err)
	}
	return path
}

// withTailscale answers the guest's `up` with an address. install, the script
// copy, and chmod succeed with no output, which is what a real installer
// looks like from this side once it has exited 0.
func withTailscale(fake *hostexec.Fake, ipv4 string) *hostexec.Fake {
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if isTailscaleUp(c) {
			return hostexec.FakeResponse{Stdout: ipv4 + "\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}
	return fake
}

func isTailscaleUp(c hostexec.Command) bool {
	if c.Name != hostexec.SSH.Name {
		return false
	}
	remote := sshRemote(c)
	for _, arg := range remote {
		if arg == "up" {
			return true
		}
	}
	return false
}

func sshRemote(c hostexec.Command) []string {
	for i, arg := range c.Args {
		if strings.Contains(arg, "@") && !strings.HasPrefix(arg, "-") {
			return c.Args[i+1:]
		}
	}
	return nil
}

func commandStdin(t *testing.T, c hostexec.Command) string {
	t.Helper()
	if c.Stdin == nil {
		return ""
	}
	got, err := io.ReadAll(c.Stdin)
	if err != nil {
		t.Fatalf("reading stdin: %v", err)
	}
	return string(got)
}

func TestCreate_JoinsATailscaleNetworkWithoutStoringTheAuthKey(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	authPath := writeTailscaleKey(t, tailscaleCanary+"\n")
	fake := withTailscale(createHost(t), "100.64.0.2")

	code, stdout, stderr := cliRun(t, fake, stateDir, createArgs(keyPath,
		"--tailscale-auth-key-file", authPath,
		"--tailscale-hostname", "build-01",
		"--tailscale-ephemeral",
		"--tailscale-ssh",
		"--tailscale-advertise-tag", "tag:ci",
		"--tailscale-advertise-tag", "tag:ci",
		"--tailscale-login-server", "https://headscale.example.com",
	)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "100.64.0.2") || !strings.Contains(stdout, "build-01") {
		t.Errorf("create did not report the tailnet join:\n%s", stdout)
	}

	joined := false
	scriptCopied := false
	for _, call := range fake.Calls() {
		argv := strings.Join(call.Argv(), " ")
		if strings.Contains(argv, tailscaleCanary) || strings.Contains(call.String(), tailscaleCanary) {
			t.Errorf("the auth key is in an argument vector: %s", argv)
		}
		stdin := commandStdin(t, call)
		remote := sshRemote(call)
		switch {
		case isTailscaleUp(call):
			joined = true
			if stdin != tailscaleCanary {
				t.Errorf("up stdin = %q, want the auth key", stdin)
			}
			got := strings.Join(remote, " ")
			for _, want := range []string{
				"--hostname=build-01",
				"--operator=agent",
				"--ephemeral",
				"--ssh",
				"--advertise-tags=tag:ci",
				"--login-server=https://headscale.example.com",
				"sudo -n " + tailscale.ScriptPath + " up",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("up command missing %q: %s", want, got)
				}
			}
			if strings.Contains(got, "tag:ci,tag:ci") {
				t.Errorf("duplicate tag was sent twice: %s", got)
			}
		case strings.Contains(stdin, tailscaleCanary):
			t.Errorf("the auth key was sent to %s", argv)
		case len(remote) >= 2 && remote[0] == "sudo" && remote[1] == "-n" && strings.Contains(strings.Join(remote, " "), "tee"):
			scriptCopied = true
			if !strings.Contains(stdin, "https://tailscale.com/install.sh") {
				t.Errorf("the script copied to the guest is not the join script:\n%s", stdin)
			}
			if strings.Contains(stdin, tailscaleCanary) {
				t.Errorf("the copied script contains the auth key")
			}
		}
	}
	if !joined {
		t.Fatal("tailscale up did not run")
	}
	if !scriptCopied {
		t.Fatal("the join script was not copied to the guest")
	}

	vm := loadVM(t, stateDir, "agent-01")
	if vm.Tailscale == nil || vm.Tailscale.IPv4 != "100.64.0.2" || vm.Tailscale.Hostname != "build-01" || !vm.Tailscale.Ephemeral || !vm.Tailscale.SSH {
		t.Fatalf("recorded join = %+v", vm.Tailscale)
	}
	if vm.Tailscale.LoginServer != "https://headscale.example.com" || len(vm.Tailscale.AdvertiseTags) != 1 {
		t.Fatalf("recorded join = %+v", vm.Tailscale)
	}
	record, err := os.ReadFile(vm.Paths.UserData)
	if err != nil {
		t.Fatalf("reading user-data: %v", err)
	}
	vmJSON, err := os.ReadFile(filepath.Join(vm.Paths.Dir, "vm.json"))
	if err != nil {
		t.Fatalf("reading vm.json: %v", err)
	}
	for _, blob := range []struct {
		name string
		text string
	}{
		{"stdout", stdout},
		{"stderr", stderr},
		{"user-data", string(record)},
		{"vm.json", string(vmJSON)},
	} {
		if strings.Contains(blob.text, tailscaleCanary) {
			t.Errorf("the auth key is in %s", blob.name)
		}
	}

	_, listOut, listErr := cliRun(t, createHost(t), stateDir, "list")
	if !strings.Contains(listOut, "nat+tailscale") {
		t.Errorf("list does not show the tailnet:\n%s\n%s", listOut, listErr)
	}
	_, infoOut, infoErr := cliRun(t, createHost(t), stateDir, "info", "agent-01")
	if !strings.Contains(infoOut, "100.64.0.2") || !strings.Contains(infoOut, "build-01") {
		t.Errorf("info does not show the tailnet:\n%s\n%s", infoOut, infoErr)
	}
}

func TestCreate_TailscaleInstallFailureDoesNotSendTheAuthKey(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	authPath := writeTailscaleKey(t, tailscaleCanary)
	fake := createHost(t)
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		remote := sshRemote(c)
		if len(remote) > 0 && remote[len(remote)-1] == "install" {
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "curl: connection refused\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, stdout, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--tailscale-auth-key-file", authPath)...)
	if code == ExitOK {
		t.Fatal("create succeeded after Tailscale failed to install")
	}
	if !strings.Contains(stderr, "still there") {
		t.Errorf("the error does not say the VM was kept:\n%s", stderr)
	}
	if strings.Contains(stdout+stderr, tailscaleCanary) {
		t.Errorf("the auth key leaked into the output:\n%s\n%s", stdout, stderr)
	}
	for _, call := range fake.Calls() {
		if strings.Contains(strings.Join(call.Argv(), " "), tailscaleCanary) || strings.Contains(commandStdin(t, call), tailscaleCanary) {
			t.Errorf("the auth key was sent before install finished: %s", strings.Join(call.Argv(), " "))
		}
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.Tailscale != nil {
		t.Fatalf("a failed join was recorded: %+v", vm.Tailscale)
	}
}

func TestCreate_TailscaleUpFailureKeepsTheVMAndTheKeyOutOfTheError(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	authPath := writeTailscaleKey(t, tailscaleCanary)
	fake := createHost(t)
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		if isTailscaleUp(c) {
			return hostexec.FakeResponse{ExitCode: 1, Stderr: "tailscale: authentication failed\n"}, true
		}
		return hostexec.FakeResponse{}, false
	}

	code, stdout, stderr := cliRun(t, fake, stateDir, createArgs(keyPath, "--tailscale-auth-key-file", authPath)...)
	if code == ExitOK {
		t.Fatal("create succeeded after tailscale up failed")
	}
	if strings.Contains(stdout+stderr, tailscaleCanary) {
		t.Errorf("the auth key leaked into the output:\n%s\n%s", stdout, stderr)
	}
	sent := false
	for _, call := range fake.Calls() {
		if strings.Contains(strings.Join(call.Argv(), " "), tailscaleCanary) {
			t.Errorf("the auth key is in argv: %s", strings.Join(call.Argv(), " "))
		}
		if isTailscaleUp(call) && commandStdin(t, call) == tailscaleCanary {
			sent = true
		}
	}
	if !sent {
		t.Fatal("the auth key was not passed on stdin")
	}
	if vm := loadVM(t, stateDir, "agent-01"); vm.Tailscale != nil {
		t.Fatalf("a failed join was recorded: %+v", vm.Tailscale)
	}
}

func TestCreate_TailscaleRequiresABootWaitAndAKeyFile(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	authPath := writeTailscaleKey(t, tailscaleCanary)

	fake := createHost(t)
	code, _, stderr := cliRun(t, fake, stateDir, createArgs(keyPath,
		"--tailscale-auth-key-file", authPath, "--wait-for-ssh", "0")...)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d: %s", code, ExitUsage, stderr)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("the host was touched by a rejected create:\n%s", fake)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "vms", "agent-01")); !os.IsNotExist(err) {
		t.Fatalf("a refused create still wrote the VM: %v", err)
	}

	code, _, stderr = cliRun(t, createHost(t), stateDir, createArgs(keyPath, "--tailscale-ephemeral")...)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d for a join with no key file: %s", code, ExitUsage, stderr)
	}

	code, stdout, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath,
		"--tailscale-auth-key-file", filepath.Join(t.TempDir(), "missing"))...)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d for a missing key file: %s", code, ExitUsage, stderr)
	}
	if strings.Contains(stdout+stderr, tailscaleCanary) {
		t.Errorf("output contains the canary:\n%s\n%s", stdout, stderr)
	}

	bad := writeTailscaleKey(t, "two lines\n"+tailscaleCanary)
	code, stdout, stderr = cliRun(t, createHost(t), stateDir, createArgs(keyPath, "--tailscale-auth-key-file", bad)...)
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want %d for a malformed key file: %s", code, ExitUsage, stderr)
	}
	if strings.Contains(stdout+stderr, tailscaleCanary) || strings.Contains(stdout+stderr, "two lines") {
		t.Errorf("the malformed file was echoed:\n%s\n%s", stdout, stderr)
	}
}

func TestCreate_TailscaleDryRunPrintsThePlanAndNotTheKey(t *testing.T) {
	t.Parallel()
	stateDir, keyPath := createEnv(t)
	authPath := writeTailscaleKey(t, tailscaleCanary+"\n")
	fake := createHost(t)

	code, stdout, stderr := cliRun(t, fake, stateDir, append([]string{"--dry-run"}, createArgs(keyPath,
		"--tailscale-auth-key-file", authPath, "--tailscale-hostname", "build-01")...)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d: %s", code, stderr)
	}
	for _, want := range []string{
		tailscale.ScriptPath + " install",
		tailscale.ScriptPath + " up",
		"--hostname=build-01",
		authPath,
		"the key is not printed",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout+stderr, tailscaleCanary) {
		t.Errorf("dry-run printed the auth key:\n%s\n%s", stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "vms", "agent-01")); !os.IsNotExist(err) {
		t.Errorf("--dry-run created the VM: %v", err)
	}
	for _, argv := range fake.Argvs() {
		if strings.HasPrefix(argv, "virt-install ") || strings.Contains(argv, tailscaleCanary) {
			t.Errorf("--dry-run ran %s", argv)
		}
	}
}
