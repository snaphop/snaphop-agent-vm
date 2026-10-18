package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	for _, name := range []string{"github-runner.sh", "github-runner-configure.sh", "runner-docker.sh", "runner-git.sh", "runner-cleanup.sh", "runner-disk-job-started.sh"} {
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
		"apt-get install -y --no-install-recommends \\\n        build-essential \\\n        libssl-dev \\\n        pkg-config",
		"dnf -y install \\\n        openssl-devel \\\n        pkgconf-pkg-config",
		"pacman -S --noconfirm --needed \\\n        openssl \\\n        pkgconf",
		"pkg-config --exists libssl",
		"pkg-config is not on PATH",
		"libssl is not visible to pkg-config",
		"no OpenSSL development install for this distro",
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

func TestRunnerGit_InstallsGitAndTheGitHubCLI(t *testing.T) {
	t.Parallel()
	script := readTemplate(t, "distro/runner-git.sh")
	lower := strings.ToLower(script)

	for _, want := range []string{
		"apt-get install -y --no-install-recommends \\\n        git \\\n        gh",
		"dnf -y install \\\n        git \\\n        gh",
		"pacman -S --noconfirm --needed \\\n        git \\\n        github-cli",
		"for cmd in git gh; do",
		`command -v "$cmd"`,
		`"$cmd" --version`,
		"no git install for this distro",
		"is not on PATH after the package install",
		"is installed but cannot run",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("runner-git.sh does not contain %q", want)
		}
	}
	// -Sy or -Su refreshes or upgrades every installed package. The slim
	// recipe has already built the initramfs this image direct-boots.
	if strings.Contains(script, "pacman -Sy") || strings.Contains(script, "pacman -Su") {
		t.Error("runner-git.sh must install the named Arch packages only")
	}
	// gh auth would store a login in the shared base image.
	if strings.Contains(script, "gh auth") || strings.Contains(script, "gh login") {
		t.Error("runner-git.sh signs gh in during the image build")
	}
	for _, forbidden := range []string{"latest", "token", "secret", "password", "api_key", "bearer", "sk-"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("runner-git.sh contains %q; the image build must not carry a login or an unpinned release", forbidden)
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
		"/usr/local/libexec/snaphop-runner",
		"ACTIONS_RUNNER_HOOK_JOB_STARTED",
		"SNAPHOP_RUNNER_ROOT",
		"ensure_job_hook",
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
	for _, want := range []string{"--url", "--token", "configure", "ephemeral", "registration token", "removal token", "github-runner", "sudo without a prompt", "job-started disk hook"} {
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

// stubChown records the configure script's ownership fix for .env. The test
// host has no github-runner account, so the real chown cannot run.
const stubChown = `#!/bin/sh
if [ -n "${AGENT_VM_RUNNER_PREFIX:-}" ]; then
  printf '%s\n' "$*" >> "$AGENT_VM_RUNNER_PREFIX/chown.log"
fi
exit 0
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
func runConfigureAgainstFakeRunner(t *testing.T, prefix, hookDir, script string, args ...string) (stdout, stderr string, code int, invocations [][]string) {
	t.Helper()
	bin := t.TempDir()
	writeStub(t, bin, "id", stubID)
	writeStub(t, bin, "runuser", stubRunuser)
	writeStub(t, bin, "chown", stubChown)
	if hookDir == "" {
		hookDir = t.TempDir()
	}
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "AGENT_VM_RUNNER_PREFIX=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env = append(env, "AGENT_VM_RUNNER_HOOK_DIR="+hookDir)
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
		stdout, stderr, code, got := runConfigureAgainstFakeRunner(t, "", "", script, configure...)
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
		stdout, stderr, code, got := runConfigureAgainstFakeRunner(t, dir, "", script, append(append([]string{}, configure...), "--replace")...)
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
		hook := t.TempDir()
		if err := os.WriteFile(filepath.Join(hook, "job-started.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		original := "LANG=C.UTF-8\nACTIONS_RUNNER_HOOK_JOB_STARTED=/old/job-started.sh\nSNAPHOP_RUNNER_ROOT=/not-this-prefix\n"
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code, got := runConfigureAgainstFakeRunner(t, dir, hook, script, "remove", "--token", removal)
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
		gotEnv, err := os.ReadFile(filepath.Join(dir, ".env"))
		if err != nil {
			t.Fatal(err)
		}
		if string(gotEnv) != original {
			t.Errorf(".env after remove =\n%s\nwant it unchanged:\n%s", gotEnv, original)
		}
	})

	t.Run("first configure", func(t *testing.T) {
		t.Parallel()
		dir := fakeRunnerDir(t, false)
		stdout, stderr, code, got := runConfigureAgainstFakeRunner(t, dir, "", script, configure...)
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

	t.Run("configure restores the disk hook", func(t *testing.T) {
		t.Parallel()
		dir := fakeRunnerDir(t, false)
		hook := t.TempDir()
		if err := os.WriteFile(filepath.Join(hook, "job-started.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		seed := "LANG=C.UTF-8\nACTIONS_RUNNER_HOOK_JOB_STARTED=/old/job-started.sh\nSNAPHOP_RUNNER_ROOT=/not-this-prefix\nEXTRA=1\n"
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code, _ := runConfigureAgainstFakeRunner(t, dir, hook, script, configure...)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		assertRegistrationHidden(t, stdout, stderr)
		got, err := os.ReadFile(filepath.Join(dir, ".env"))
		if err != nil {
			t.Fatal(err)
		}
		want := "LANG=C.UTF-8\nEXTRA=1\n" +
			"ACTIONS_RUNNER_HOOK_JOB_STARTED=" + filepath.Join(hook, "job-started.sh") + "\n" +
			"SNAPHOP_RUNNER_ROOT=" + dir + "\n"
		if string(got) != want {
			t.Errorf(".env =\n%s\nwant\n%s", got, want)
		}
		info, err := os.Stat(filepath.Join(dir, ".env"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf(".env mode = %o, want 644", info.Mode().Perm())
		}
		chownLog, err := os.ReadFile(filepath.Join(dir, "chown.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(chownLog), "github-runner:github-runner "+filepath.Join(dir, ".env")) {
			t.Errorf("chown log = %q", chownLog)
		}
	})
}

// The hashes are the copies of snaphop-build-deploy .github/actions/runner-disk
// at c68e4b2821de81f4d599fdbb48b162097e90b046. That is the commit
// snaphop-ai-stack's installer pins.
func TestRunnerDiskHook_IsThePinnedUpstreamCopy(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"runner-disk-headroom.py":    "c040d4753b3ebe122752be1d8372b3a564b54965254c7c23e70e83d74962904c",
		"runner-disk-job-started.py": "35f65abff156d639027263571dd2a208dcbcf8c4b7c3eefa3b16cee37e7312d1",
		"runner-disk-job-started.sh": "459d82c14cea0d085e49be8af4610ef9017d0bbab7f360763955c65a5a7a8a31",
	}
	for name, sum := range want {
		body := readTemplate(t, "distro/"+name)
		got := sha256.Sum256([]byte(body))
		if hex.EncodeToString(got[:]) != sum {
			t.Errorf("%s sha256 = %s, want the pinned copy %s", name, hex.EncodeToString(got[:]), sum)
		}
	}
	script := readTemplate(t, "distro/runner-cleanup.sh")
	for _, needle := range []string{
		"c68e4b2821de81f4d599fdbb48b162097e90b046",
		"/usr/local/libexec/snaphop-runner",
		"ACTIONS_RUNNER_HOOK_JOB_STARTED",
		"SNAPHOP_RUNNER_ROOT",
		"chown root:root",
		"chown github-runner:github-runner",
		"the hook must live outside",
		"python3 is not on PATH",
		"docker is not on PATH",
		"the github-runner account is missing",
		"RUNNER_DISK_PRUNE_BELOW_GIB=0",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("runner-cleanup.sh does not contain %q", needle)
		}
	}
	lower := strings.ToLower(script)
	for _, forbidden := range []string{"latest", "token", "secret", "password", "api_key", "bearer", "sk-"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("runner-cleanup.sh contains %q", forbidden)
		}
	}
}

func TestRunnerDiskHook_RemovesStaleWorkAndPrunesDockerWhenLow(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("python3 is not installed; the runner image runs the disk hook with it")
	}
	hook := writeInstalledHook(t)
	root := t.TempDir()
	work := filepath.Join(root, "_work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(work, "old-checkout", "src")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "main.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ageTree(t, filepath.Join(work, "old-checkout"), 73*time.Hour)

	busy := filepath.Join(work, "busy", "src")
	if err := os.MkdirAll(busy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(busy, "main.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	agePath(t, filepath.Join(work, "busy"), 73*time.Hour)
	agePath(t, busy, 73*time.Hour)

	current := filepath.Join(work, "demo")
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "keep.txt"), []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	ageTree(t, current, 73*time.Hour)

	actions := filepath.Join(work, "_actions")
	if err := os.MkdirAll(actions, 0o755); err != nil {
		t.Fatal(err)
	}
	ageTree(t, actions, 73*time.Hour)

	if err := os.WriteFile(filepath.Join(work, "notes.txt"), []byte("loose"), 0o644); err != nil {
		t.Fatal(err)
	}
	agePath(t, filepath.Join(work, "notes.txt"), 73*time.Hour)

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(outsideFile, []byte("stay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "linked")); err != nil {
		t.Fatal(err)
	}

	diag := filepath.Join(root, "_diag")
	if err := os.MkdirAll(diag, 0o755); err != nil {
		t.Fatal(err)
	}
	oldLog := filepath.Join(diag, "old.log")
	if err := os.WriteFile(oldLog, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	agePath(t, oldLog, 8*24*time.Hour)
	newLog := filepath.Join(diag, "new.log")
	if err := os.WriteFile(newLog, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkedLog := filepath.Join(diag, "linked.log")
	if err := os.Symlink(outsideFile, linkedLog); err != nil {
		t.Fatal(err)
	}
	agePath(t, outsideFile, 8*24*time.Hour)

	stdout, dockerLog := runJobStarted(t, hook, root, []string{
		"GITHUB_REPOSITORY=snaphop/demo",
		"RUNNER_DISK_PRUNE_BELOW_GIB=1000000",
		"RUNNER_DISK_FAIL_BELOW_GIB=1000000",
		"RUNNER_NAME=runner-test",
	})
	if _, err := os.Stat(filepath.Join(work, "old-checkout")); !os.IsNotExist(err) {
		t.Errorf("stale checkout still present: %v", err)
	}
	for _, keep := range []string{
		filepath.Join(work, "busy", "src", "main.txt"),
		filepath.Join(work, "demo", "keep.txt"),
		actions,
		filepath.Join(work, "notes.txt"),
		filepath.Join(work, "linked"),
		outsideFile,
		newLog,
		linkedLog,
	} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("hook removed %s: %v", keep, err)
		}
	}
	if _, err := os.Lstat(linkedLog); err != nil {
		t.Errorf("hook removed the diag symlink: %v", err)
	}
	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Errorf("old diag log still present: %v", err)
	}
	if !strings.Contains(stdout, "Removed checkout _work/old-checkout") {
		t.Errorf("stdout does not report the removed checkout:\n%s", stdout)
	}
	for _, want := range []string{
		"builder prune --force --filter until=72h",
		"image prune --force --filter until=72h",
		"volume prune --force --filter all=false",
		"builder prune --all --force",
	} {
		if !strings.Contains(dockerLog, want) {
			t.Errorf("docker log does not contain %q:\n%s", want, dockerLog)
		}
	}

	// Plenty of free space: files still age out, Docker is left alone.
	fresh := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fresh, "_work", "old-checkout"), 0o755); err != nil {
		t.Fatal(err)
	}
	ageTree(t, filepath.Join(fresh, "_work", "old-checkout"), 73*time.Hour)
	_, quiet := runJobStarted(t, hook, fresh, []string{
		"GITHUB_REPOSITORY=snaphop/other",
		"RUNNER_DISK_PRUNE_BELOW_GIB=0",
		"RUNNER_NAME=runner-test",
	})
	if strings.Contains(quiet, "builder prune") {
		t.Errorf("docker was pruned while free space was above the threshold:\n%s", quiet)
	}
	if _, err := os.Stat(filepath.Join(fresh, "_work", "old-checkout")); !os.IsNotExist(err) {
		t.Errorf("stale checkout kept when Docker pruning was skipped: %v", err)
	}
}

func TestRunnerDiskHook_ShellNeverFailsTheJob(t *testing.T) {
	t.Parallel()
	hook := writeInstalledHook(t)
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "python.log")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(hook, "job-started.sh"))
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("job-started.sh exit: %v\n%s", err, out)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), filepath.Join(hook, "job-started.py")) {
		t.Errorf("python3 was not pointed at the sibling script:\n%s", logged)
	}
}

func TestRunnerCleanup_InstallsTheHookOutsideTheRunnerDirectory(t *testing.T) {
	t.Parallel()
	script := writeEmbeddedScript(t, "runner-cleanup.sh")
	src := t.TempDir()
	for embedded, installed := range map[string]string{
		"runner-disk-headroom.py":    "agent-vm-runner-headroom.py",
		"runner-disk-job-started.py": "agent-vm-runner-job-started.py",
		"runner-disk-job-started.sh": "agent-vm-runner-job-started.sh",
	} {
		if err := os.WriteFile(filepath.Join(src, installed), []byte(readTemplate(t, "distro/"+embedded)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("installs and keeps other env lines", func(t *testing.T) {
		prefix := t.TempDir()
		dest := t.TempDir()
		bin, chownLog := runnerCleanupPATH(t, true, true)
		stdout, stderr, code := runCleanup(t, script, src, prefix, dest, bin, chownLog)
		if code != 0 {
			t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		for embedded, name := range map[string]string{
			"runner-disk-headroom.py":    "headroom.py",
			"runner-disk-job-started.py": "job-started.py",
			"runner-disk-job-started.sh": "job-started.sh",
		} {
			got, err := os.ReadFile(filepath.Join(dest, name))
			if err != nil {
				t.Fatalf("reading installed %s: %v", name, err)
			}
			if string(got) != readTemplate(t, "distro/"+embedded) {
				t.Errorf("installed %s differs from the embedded copy", name)
			}
		}
		shInfo, err := os.Stat(filepath.Join(dest, "job-started.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if shInfo.Mode().Perm() != 0o755 {
			t.Errorf("job-started.sh mode = %o, want 755", shInfo.Mode().Perm())
		}
		pyInfo, err := os.Stat(filepath.Join(dest, "headroom.py"))
		if err != nil {
			t.Fatal(err)
		}
		if pyInfo.Mode().Perm() != 0o644 {
			t.Errorf("headroom.py mode = %o, want 644", pyInfo.Mode().Perm())
		}
		envPath := filepath.Join(prefix, ".env")
		got, err := os.ReadFile(envPath)
		if err != nil {
			t.Fatal(err)
		}
		want := "ACTIONS_RUNNER_HOOK_JOB_STARTED=" + filepath.Join(dest, "job-started.sh") + "\n" +
			"SNAPHOP_RUNNER_ROOT=" + prefix + "\n"
		if string(got) != want {
			t.Errorf(".env =\n%s\nwant\n%s", got, want)
		}
		if err := os.WriteFile(envPath, []byte("LANG=C.UTF-8\n"+string(got)+"ACTIONS_RUNNER_HOOK_JOB_STARTED=/old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code = runCleanup(t, script, src, prefix, dest, bin, chownLog)
		if code != 0 {
			t.Fatalf("second exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		got, err = os.ReadFile(envPath)
		if err != nil {
			t.Fatal(err)
		}
		want = "LANG=C.UTF-8\n" + want
		if string(got) != want {
			t.Errorf(".env after a second install =\n%s\nwant\n%s", got, want)
		}
		if strings.Count(string(got), "ACTIONS_RUNNER_HOOK_JOB_STARTED=") != 1 {
			t.Errorf(".env repeats the hook line:\n%s", got)
		}
		chownLogBody, err := os.ReadFile(chownLog)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(chownLogBody), "root:root") || !strings.Contains(string(chownLogBody), "github-runner:github-runner") {
			t.Errorf("chown log = %q", chownLogBody)
		}
	})

	t.Run("refuses a hook inside the runner directory", func(t *testing.T) {
		prefix := t.TempDir()
		dest := filepath.Join(prefix, "inside")
		bin, chownLog := runnerCleanupPATH(t, true, true)
		_, stderr, code := runCleanup(t, script, src, prefix, dest, bin, chownLog)
		if code == 0 {
			t.Fatal("install inside the runner directory exited 0")
		}
		if !strings.Contains(stderr, "must live outside") {
			t.Errorf("stderr =\n%s", stderr)
		}
	})

	t.Run("refuses a missing runner account", func(t *testing.T) {
		prefix := t.TempDir()
		dest := t.TempDir()
		bin, chownLog := runnerCleanupPATH(t, true, false)
		_, stderr, code := runCleanup(t, script, src, prefix, dest, bin, chownLog)
		if code == 0 {
			t.Fatal("missing account exited 0")
		}
		if !strings.Contains(stderr, "github-runner account is missing") {
			t.Errorf("stderr =\n%s", stderr)
		}
	})

	t.Run("refuses a missing docker command", func(t *testing.T) {
		prefix := t.TempDir()
		dest := t.TempDir()
		bin, chownLog := runnerCleanupPATH(t, false, true)
		_, stderr, code := runCleanup(t, script, src, prefix, dest, bin, chownLog)
		if code == 0 {
			t.Fatal("missing docker exited 0")
		}
		if !strings.Contains(stderr, "docker is not on PATH") {
			t.Errorf("stderr =\n%s", stderr)
		}
	})
}

func writeInstalledHook(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for embedded, name := range map[string]string{
		"runner-disk-headroom.py":    "headroom.py",
		"runner-disk-job-started.py": "job-started.py",
		"runner-disk-job-started.sh": "job-started.sh",
	} {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(readTemplate(t, "distro/"+embedded)), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func agePath(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func ageTree(t *testing.T, root string, age time.Duration) {
	t.Helper()
	var paths []string
	if err := filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A child's timestamp updates its parent, so the parents go last.
	for i := len(paths) - 1; i >= 0; i-- {
		agePath(t, paths[i], age)
	}
}

func runJobStarted(t *testing.T, hook, root string, extra []string) (stdout, dockerLog string) {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	stub := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %s\ncase \"$1\" in\ninfo)\n  printf '%%s\\n' %s\n  ;;\nvolume)\n  if [ \"$2\" = prune ]; then printf '%%s\\n' 'Total reclaimed space: 0B'; fi\n  ;;\nbuilder|image)\n  printf '%%s\\n' 'Total reclaimed space: 0B'\n  ;;\nesac\nexit 0\n", shellQuote(logPath), shellQuote(root))
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", filepath.Join(hook, "job-started.py"))
	env := []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"SNAPHOP_RUNNER_ROOT=" + root,
	}
	env = append(env, extra...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("job-started.py: %v\n%s", err, out)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(out), string(logged)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func runnerCleanupPATH(t *testing.T, withDocker, withRunner bool) (dir, chownLog string) {
	t.Helper()
	dir = t.TempDir()
	chownLog = filepath.Join(t.TempDir(), "chown.log")
	for _, name := range []string{"python3", "install", "grep", "mktemp", "chmod", "cat", "rm", "mkdir", "touch", "env"} {
		real, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("%s is not installed; the disk-hook install runs it", name)
		}
		if err := os.Symlink(real, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	idBody := "#!/bin/sh\nexit 1\n"
	if withRunner {
		idBody = "#!/bin/sh\nif [ \"$1\" = github-runner ]; then exit 0; fi\nprintf '%s\\n' 'id stub: unexpected invocation' >&2\nexit 99\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "id"), []byte(idBody), 0o755); err != nil {
		t.Fatal(err)
	}
	chownBody := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(chownLog) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "chown"), []byte(chownBody), 0o755); err != nil {
		t.Fatal(err)
	}
	if withDocker {
		if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, chownLog
}

func runCleanup(t *testing.T, script, src, prefix, dest, bin, chownLog string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(script, src)
	cmd.Env = []string{
		"PATH=" + bin,
		"HOME=" + t.TempDir(),
		"AGENT_VM_RUNNER_PREFIX=" + prefix,
		"AGENT_VM_RUNNER_HOOK_DIR=" + dest,
		"CHOWN_LOG=" + chownLog,
	}
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
	t.Fatalf("running cleanup install: %v\n%s", err, errb.String())
	return "", "", -1
}
