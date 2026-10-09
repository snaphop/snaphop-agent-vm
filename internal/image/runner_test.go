package image

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeEmbeddedScript copies an embedded guest script out to a temp file so a
// test can run the copy the image build would ship.
func writeEmbeddedScript(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(readTemplate(t, "distro/"+name)), 0o755); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func runScript(t *testing.T, path string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{path}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return out.String(), errb.String(), 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), errb.String(), exit.ExitCode()
	}
	t.Fatalf("running %s: %v\n%s", path, err, errb.String())
	return "", "", -1
}

func assertRegistrationHidden(t *testing.T, stdout, stderr string) {
	t.Helper()
	const secret = "registration-value"
	if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
		t.Errorf("the registration value was printed\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

// reachedGuestBoundary is the first check that needs the guest itself: root,
// then the runner installed by the image. A test that gets here has finished
// argument checking. Neither message is a successful registration.
func reachedGuestBoundary(stderr string) bool {
	return strings.Contains(stderr, "run as root") || strings.Contains(stderr, "config.sh is missing")
}

// refuseRealRunner stops a root test short of a host that already has the
// runner installed. Past argument checking, the script would call config.sh.
func refuseRealRunner(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		return
	}
	if _, err := os.Stat("/opt/actions-runner/config.sh"); err == nil {
		t.Fatal("/opt/actions-runner/config.sh is installed on this host; refusing to run the configure script as root against it")
	}
}

func TestGitHubRunnerScripts_AreValidShell(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"github-runner.sh", "github-runner-configure.sh", "runner-docker.sh"} {
		path := writeEmbeddedScript(t, name)
		out, err := exec.Command("sh", "-n", path).CombinedOutput()
		if err != nil {
			t.Errorf("%s: sh -n: %v\n%s", name, err, out)
		}
	}
}

func TestGitHubRunnerInstall_PinsTheReleaseAndCarriesNoRegistration(t *testing.T) {
	t.Parallel()
	script := readTemplate(t, "distro/github-runner.sh")
	lower := strings.ToLower(script)

	if got := strings.Count(script, "2.337.0"); got != 1 {
		t.Errorf("runner version 2.337.0 appears %d times, want the one pinned assignment", got)
	}
	for _, want := range []string{
		"70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613",
		"9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393",
		"https://github.com/actions/runner/releases/download/v${version}/",
		"x64",
		"arm64",
		"/opt/actions-runner",
		".agent-vm-runner-version",
		"useradd --system --user-group",
		"--no-create-home github-runner",
		"id github-runner",
		"github-runner ALL=(ALL) NOPASSWD:ALL",
		"/etc/sudoers.d/github-runner",
		"chmod 0440",
		"visudo -c -f",
		"build-essential",
		"chown -R github-runner:github-runner",
		"chmod 0750",
		"installdependencies.sh",
		"icu openssl krb5 zlib lttng-ust",
		"no dependency install for this distro",
		"pacman -S --noconfirm --needed icu openssl krb5 zlib lttng-ust",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("github-runner.sh does not contain %q", want)
		}
	}
	// -Sy or -Su upgrades or resyncs every installed package. The slim recipe
	// has already built the initramfs this image direct-boots, and a later
	// kernel upgrade would leave that initramfs behind.
	if strings.Contains(script, "pacman -Sy") || strings.Contains(script, "pacman -Su") {
		t.Error("github-runner.sh must install the named Arch packages only")
	}
	for _, gone := range []string{"id runner ", "no-create-home runner", "chown -R runner:"} {
		if strings.Contains(script, gone) {
			t.Errorf("github-runner.sh still names the old account: %q", gone)
		}
	}
	// NOPASSWD is the sudoers rule, not a credential stored in the image.
	// Its lowercase form contains the substring "password".
	scrubbed := strings.ReplaceAll(lower, "nopasswd", "")
	for _, forbidden := range []string{"latest", "token", "secret", "password", "api_key", "bearer", "sk-"} {
		if strings.Contains(scrubbed, forbidden) {
			t.Errorf("github-runner.sh contains %q; the image build must not carry a registration or an unpinned release", forbidden)
		}
	}
	// The archive is extracted and its dependencies installed. config.sh is
	// only checked to be present: running it would configure this build.
	if strings.Count(script, "config.sh") != 1 {
		t.Errorf("github-runner.sh mentions config.sh %d times, want the one presence check", strings.Count(script, "config.sh"))
	}
	if strings.Contains(script, "./config.sh") || strings.Contains(script, "RUNNER_ALLOW_RUNASROOT") {
		t.Error("github-runner.sh executes config.sh during the image build")
	}
}

// TestGitHubRunnerSudoersRule_IsAcceptedByVisudo runs the same check the image
// build runs. A rule visudo rejects would fail every runner image build, and
// a unit test is the place that failure shows up without a registry.
func TestGitHubRunnerSudoersRule_IsAcceptedByVisudo(t *testing.T) {
	t.Parallel()
	visudo, err := exec.LookPath("visudo")
	if err != nil {
		t.Fatal("visudo is not installed; the runner image build checks this rule with it")
	}
	const rule = "github-runner ALL=(ALL) NOPASSWD:ALL\n"
	script := readTemplate(t, "distro/github-runner.sh")
	if !strings.Contains(script, strings.TrimRight(rule, "\n")) {
		t.Fatalf("github-runner.sh does not grant %q", strings.TrimRight(rule, "\n"))
	}
	path := filepath.Join(t.TempDir(), "github-runner")
	if err := os.WriteFile(path, []byte(rule), 0o440); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(visudo, "-c", "-f", path).CombinedOutput()
	if err != nil {
		t.Fatalf("visudo rejected the sudoers rule: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "parsed OK") {
		t.Fatalf("visudo output:\n%s", out)
	}
}

func TestRunnerDocker_InstallsTheDistroDaemonAndAdmitsTheRunner(t *testing.T) {
	t.Parallel()
	script := readTemplate(t, "distro/runner-docker.sh")
	lower := strings.ToLower(script)

	for _, want := range []string{
		"docker.io",
		"docker-compose-v2",
		"docker-buildx",
		"moby-engine",
		"containerd",
		"docker-compose",
		"pacman -S --noconfirm --needed",
		"systemctl --root=/ enable docker.service containerd.service",
		"id github-runner",
		"usermod -aG docker github-runner",
		"command -v docker",
		"no Docker install for this distro",
		"the github-runner account is missing",
		"the docker group is missing",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("runner-docker.sh does not contain %q", want)
		}
	}
	// -Sy or -Su refreshes or upgrades every installed package. The slim
	// recipe has already built the initramfs this image direct-boots.
	if strings.Contains(script, "pacman -Sy") || strings.Contains(script, "pacman -Su") {
		t.Error("runner-docker.sh must install the named Arch packages only")
	}
	for _, gone := range []string{"id runner ", "docker runner"} {
		if strings.Contains(script, gone) {
			t.Errorf("runner-docker.sh still names the old account: %q", gone)
		}
	}
	// Foreign-architecture builds stay on the full and nix images. Pulling
	// qemu-user into the runner image is a different, larger decision.
	if strings.Contains(script, "qemu-user") || strings.Contains(script, "binfmt") {
		t.Error("runner-docker.sh installs user-mode QEMU; cross-architecture docker build is not part of the runner image")
	}
	scrubbed := strings.ReplaceAll(lower, "nopasswd", "")
	for _, forbidden := range []string{"latest", "token", "secret", "password", "api_key", "bearer", "sk-"} {
		if strings.Contains(scrubbed, forbidden) {
			t.Errorf("runner-docker.sh contains %q; the image build must not carry a registration or an unpinned release", forbidden)
		}
	}
}

func TestGitHubRunnerConfigure_CarriesNoCredentialMaterial(t *testing.T) {
	t.Parallel()
	script := readTemplate(t, "distro/github-runner-configure.sh")
	lower := strings.ToLower(script)
	scrubbed := strings.ReplaceAll(lower, "nopasswd", "")
	for _, forbidden := range []string{"ghp_", "github_pat_", "gho_", "sk-", "bearer ", "api_key", "password"} {
		if strings.Contains(scrubbed, forbidden) {
			t.Errorf("github-runner-configure.sh contains %q", forbidden)
		}
	}
	for _, want := range []string{
		"run_svc install github-runner",
		"runuser -u github-runner --",
		"sudo --user github-runner --",
		"config.sh",
		"--runnergroup",
		"remove --local",
		"removal token",
		"prefix=${AGENT_VM_RUNNER_PREFIX:-/opt/actions-runner}",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("github-runner-configure.sh does not contain %q", want)
		}
	}
	for _, forbidden := range []string{"eval", "sh -c", "RUNNER_ALLOW_RUNASROOT"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("github-runner-configure.sh contains %q", forbidden)
		}
	}
	for _, gone := range []string{"runuser -u runner --", "sudo --user runner --", "run_svc install runner\n"} {
		if strings.Contains(script, gone) {
			t.Errorf("github-runner-configure.sh still names the old account: %q", gone)
		}
	}
}

func TestGitHubRunnerConfigure_HelpSucceedsAndPrintsTheRegistrationFlags(t *testing.T) {
	t.Parallel()
	path := writeEmbeddedScript(t, "github-runner-configure.sh")
	stdout, stderr, code := runScript(t, path, "--help")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, stderr)
	}
	for _, want := range []string{"--url", "--token", "configure", "ephemeral", "registration token", "removal token", "github-runner", "sudo without a prompt"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help does not mention %q:\n%s", want, stdout)
		}
	}
}

func TestGitHubRunnerConfigure_UsageOnNoArgs(t *testing.T) {
	t.Parallel()
	path := writeEmbeddedScript(t, "github-runner-configure.sh")
	_, stderr, code := runScript(t, path)
	if code == 0 {
		t.Fatal("no arguments exited 0")
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr does not carry usage:\n%s", stderr)
	}
}

func TestGitHubRunnerConfigure_RefusesBeforeItCouldRegister(t *testing.T) {
	t.Parallel()
	path := writeEmbeddedScript(t, "github-runner-configure.sh")
	const secret = "registration-value"

	world := filepath.Join(t.TempDir(), "world")
	if err := os.WriteFile(world, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(world, 0o644); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(t.TempDir(), "owned")
	if err := os.WriteFile(owned, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(owned, 0o600); err != nil {
		t.Fatal(err)
	}

	// These fail while parsing, so they never ask to be root and never read a
	// runner directory.
	early := []struct {
		name string
		args []string
		want string
	}{
		{"http url", []string{"configure", "--url", "http://github.com/org/repo", "--token", secret}, "--url must be an https URL"},
		{"token flag", []string{"configure", "--url", "https://github.com/org/repo", "--token", "-" + secret}, "must not start with -"},
		{"both token sources", []string{"configure", "--url", "https://github.com/org/repo", "--token", secret, "--token-file", owned}, "only one of --token and --token-file"},
		{"bad label", []string{"configure", "--url", "https://github.com/org/repo", "--token", secret, "--name", "runner1", "--labels", "bad label"}, "--labels"},
		{"missing token", []string{"configure", "--url", "https://github.com/org/repo"}, "--token or --token-file is required"},
		{"world-readable token file", []string{"configure", "--url", "https://github.com/org/repo", "--token-file", world, "--name", "runner1"}, "readable only by its owner"},
		{"remove with a url", []string{"remove", "--token", secret, "--url", "https://github.com/org/repo"}, "remove accepts only"},
		{"unknown command", []string{"frob"}, "unknown command"},
	}
	for _, tt := range early {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, code := runScript(t, path, tt.args...)
			if code == 0 {
				t.Fatalf("exited 0\n%s", stderr)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr does not contain %q:\n%s", tt.want, stderr)
			}
			assertRegistrationHidden(t, stdout, stderr)
		})
	}

	// A private token file is accepted as input. The next check is the guest.
	refuseRealRunner(t)
	stdout, stderr, code := runScript(t, path, "configure", "--url", "https://github.com/org/repo", "--token-file", owned, "--name", "runner1", "--ephemeral", "--replace")
	if code == 0 {
		t.Fatalf("configure exited 0\n%s", stderr)
	}
	if !reachedGuestBoundary(stderr) {
		t.Errorf("configure did not reach the guest check:\n%s", stderr)
	}
	assertRegistrationHidden(t, stdout, stderr)

	stdout, stderr, code = runScript(t, path, "remove", "--token", secret)
	if code == 0 {
		t.Fatalf("remove exited 0\n%s", stderr)
	}
	if !reachedGuestBoundary(stderr) {
		t.Errorf("remove did not reach the guest check:\n%s", stderr)
	}
	assertRegistrationHidden(t, stdout, stderr)
}

// stubConfig records each invocation and, on a configure, writes the svc.sh a
// successful config.sh would have written. remove leaves the directory alone.
const stubConfig = `#!/bin/sh
sep=$(printf '\037')
line="config${sep}"
for arg in "$@"; do
  line="${line}${arg}${sep}"
done
printf '%s\n' "$line" >> argv.log
if [ "${1:-}" = "remove" ]; then
  exit 0
fi
cat > svc.sh << 'EOF'
#!/bin/sh
sep=$(printf '\037')
line="svc${sep}"
for arg in "$@"; do
  line="${line}${arg}${sep}"
done
printf '%s\n' "$line" >> argv.log
exit 0
EOF
chmod 0755 svc.sh
exit 0
`

// stubSvc is the service script present after an earlier registration. The
// configure command uninstalls it before clearing a local configuration.
const stubSvc = `#!/bin/sh
sep=$(printf '\037')
line="svc${sep}"
for arg in "$@"; do
  line="${line}${arg}${sep}"
done
printf '%s\n' "$line" >> argv.log
exit 0
`

const stubRunuser = `#!/bin/sh
# The configure script switches to github-runner through runuser. This host
# has no such account; record nothing and run the command as we are.
if [ "$1" != "-u" ] || [ "$2" != "github-runner" ] || [ "$3" != "--" ]; then
  printf '%s\n' "runuser stub: unexpected invocation" >&2
  exit 99
fi
shift 3
exec "$@"
`

// stubID answers the configure script's root check. GitHub-hosted runners
// refuse unprivileged user namespaces, so the test cannot reach this check
// by becoming root.
const stubID = `#!/bin/sh
if [ "$1" = "-u" ]; then
  printf '%s\n' 0
  exit 0
fi
printf '%s\n' "id stub: unexpected invocation" >&2
exit 99
`

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// fakeRunnerDir is a runner install the configure script can be pointed at.
// configured reports a guest that already has .runner and svc.sh.
func fakeRunnerDir(t *testing.T, configured bool) string {
	t.Helper()
	dir := t.TempDir()
	writeStub(t, dir, "config.sh", stubConfig)
	if configured {
		writeStub(t, dir, "svc.sh", stubSvc)
		if err := os.WriteFile(filepath.Join(dir, ".runner"), []byte("configured\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func parseArgvLog(log []byte) [][]string {
	if len(bytes.TrimSpace(log)) == 0 {
		return nil
	}
	var got [][]string
	for _, line := range bytes.Split(bytes.TrimSuffix(log, []byte("\n")), []byte("\n")) {
		parts := bytes.Split(line, []byte{0x1f})
		if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
			parts = parts[:len(parts)-1]
		}
		fields := make([]string, len(parts))
		for i, part := range parts {
			fields[i] = string(part)
		}
		got = append(got, fields)
	}
	return got
}

// runConfigureAgainstFakeRunner runs the configure script with id and runuser
// stubbed on PATH. prefix is the runner directory the script should use; an
// empty prefix leaves the default, /opt/actions-runner.
func runConfigureAgainstFakeRunner(t *testing.T, prefix, script string, args ...string) (stdout, stderr string, code int, invocations [][]string) {
	t.Helper()
	bin := t.TempDir()
	writeStub(t, bin, "id", stubID)
	writeStub(t, bin, "runuser", stubRunuser)
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "AGENT_VM_RUNNER_PREFIX=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if prefix != "" {
		env = append(env, "AGENT_VM_RUNNER_PREFIX="+prefix)
	}
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running configure script: %v\n%s", err, errb.String())
		}
		code = exit.ExitCode()
	}
	logPath := "/opt/actions-runner/argv.log"
	if prefix != "" {
		logPath = filepath.Join(prefix, "argv.log")
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	return out.String(), errb.String(), code, parseArgvLog(log)
}

func TestGitHubRunnerConfigure_ReplaceClearsLocalFilesAndRemoveUsesItsOwnToken(t *testing.T) {
	t.Parallel()
	script := writeEmbeddedScript(t, "github-runner-configure.sh")
	const registration = "registration-value"
	const removal = "removal-value"
	configure := []string{"configure", "--url", "https://github.com/org/repo", "--token", registration, "--name", "runner1"}

	t.Run("default prefix", func(t *testing.T) {
		t.Parallel()
		if _, err := os.Stat("/opt/actions-runner/config.sh"); err == nil {
			t.Fatal("/opt/actions-runner/config.sh is installed on this host; refusing to run the configure script against it")
		}
		stdout, stderr, code, got := runConfigureAgainstFakeRunner(t, "", script, configure...)
		if code == 0 {
			t.Fatal("configure exited 0 with no runner install")
		}
		if !strings.Contains(stderr, "/opt/actions-runner/config.sh is missing") {
			t.Errorf("stderr does not name the default install path:\n%s", stderr)
		}
		assertRegistrationHidden(t, stdout, stderr)
		if len(got) != 0 {
			t.Errorf("config.sh was invoked without an install: %q", got)
		}
	})

	t.Run("replace", func(t *testing.T) {
		t.Parallel()
		dir := fakeRunnerDir(t, true)
		stdout, stderr, code, got := runConfigureAgainstFakeRunner(t, dir, script, append(append([]string{}, configure...), "--replace")...)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		assertRegistrationHidden(t, stdout, stderr)
		want := [][]string{
			{"svc", "uninstall"},
			{"config", "remove", "--local"},
			{"config", "--unattended", "--url", "https://github.com/org/repo", "--name", "runner1", "--token", registration, "--replace"},
			{"svc", "install", "github-runner"},
			{"svc", "start"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("invocations:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("remove", func(t *testing.T) {
		t.Parallel()
		dir := fakeRunnerDir(t, true)
		stdout, stderr, code, got := runConfigureAgainstFakeRunner(t, dir, script, "remove", "--token", removal)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		assertRegistrationHidden(t, stdout, stderr)
		if strings.Contains(stdout, removal) || strings.Contains(stderr, removal) {
			t.Errorf("the removal value was printed\nstdout: %s\nstderr: %s", stdout, stderr)
		}
		want := [][]string{
			{"svc", "uninstall"},
			{"config", "remove", "--token", removal},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("invocations:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("first configure", func(t *testing.T) {
		t.Parallel()
		dir := fakeRunnerDir(t, false)
		stdout, stderr, code, got := runConfigureAgainstFakeRunner(t, dir, script, configure...)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		assertRegistrationHidden(t, stdout, stderr)
		want := [][]string{
			{"config", "--unattended", "--url", "https://github.com/org/repo", "--name", "runner1", "--token", registration},
			{"svc", "install", "github-runner"},
			{"svc", "start"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("invocations:\n got %q\nwant %q", got, want)
		}
	})
}
