package tailscale

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runJoin runs the embedded join script the way create copies it, with stub
// tailscale, tailscaled, and systemctl binaries ahead of the real PATH.
func runJoin(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int, logs map[string]string, dropin string) {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "agent-vm-tailscale-join")
	body, err := Script()
	if err != nil {
		t.Fatalf("reading the join script: %v", err)
	}
	if err := os.WriteFile(scriptPath, body, 0o755); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	logs = map[string]string{}
	logDir := filepath.Join(dir, "logs")
	if err := os.Mkdir(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tailscale", "systemctl"} {
		logPath := filepath.Join(logDir, name)
		logs[name] = logPath
		stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n"
		if name == "tailscale" {
			stub += "if [ \"$1\" = ip ]; then printf '%s\\n' 100.64.0.8; fi\n"
		}
		stub += "exit 0\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// command -v tailscaled must return an absolute path. The stub is never executed.
	if err := os.WriteFile(filepath.Join(bin, "tailscaled"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	keyfile := filepath.Join(dir, "authkey")
	dropin = filepath.Join(dir, "unit.d", "agent-vm-ephemeral.conf")
	cmd := exec.Command("sh", append([]string{scriptPath}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+":/usr/bin:/bin",
		"AGENT_VM_TAILSCALE_KEYFILE="+keyfile,
		"AGENT_VM_TAILSCALE_EPHEMERAL_UNIT="+dropin,
	)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	stdout, stderr = out.String(), errb.String()
	if err == nil {
		code = 0
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running the join script: %v\n%s", err, stderr)
		}
		code = exit.ExitCode()
	}
	if _, statErr := os.Stat(keyfile); !os.IsNotExist(statErr) {
		t.Errorf("auth key file remains at %s", keyfile)
	}
	return stdout, stderr, code, logs, dropin
}

func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(body)
}

func TestJoinScript_EphemeralRegistersWithMemState(t *testing.T) {
	t.Parallel()
	stdout, stderr, code, logs, dropin := runJoin(t, canaryKey+"\n",
		"up",
		"--hostname=snaphop-developer-3.agent",
		"--operator=agent",
		"--ephemeral",
		"--ssh",
		"--login-server=https://headscale.example.com",
		"--advertise-tags=tag:ci",
	)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if stdout != "100.64.0.8\n" {
		t.Errorf("stdout = %q, want the address from tailscale ip -4", stdout)
	}
	up := readLog(t, logs["tailscale"])
	for _, want := range []string{
		"up --auth-key=file:",
		"--hostname=snaphop-developer-3.agent",
		"--operator=agent",
		"--timeout=90s",
		"--reset",
		"--ssh",
		"--login-server=https://headscale.example.com",
		"--advertise-tags=tag:ci",
		"ip -4",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("tailscale log missing %q:\n%s", want, up)
		}
	}
	if strings.Contains(up, "--ephemeral") {
		t.Errorf("tailscale up was passed --ephemeral, which that command rejects:\n%s", up)
	}
	if strings.Contains(up, canaryKey) || strings.Contains(stdout, canaryKey) || strings.Contains(stderr, canaryKey) {
		t.Error("the auth key was printed")
	}

	unit, err := os.ReadFile(dropin)
	if err != nil {
		t.Fatalf("reading the ephemeral unit: %v", err)
	}
	text := string(unit)
	if strings.Contains(text, canaryKey) {
		t.Error("the ephemeral unit contains the auth key")
	}
	for _, want := range []string{"ExecStart=\n", "--state=mem:", "--socket=/run/tailscale/tailscaled.sock"} {
		if !strings.Contains(text, want) {
			t.Errorf("ephemeral unit missing %q:\n%s", want, text)
		}
	}
	sys := readLog(t, logs["systemctl"])
	for _, want := range []string{"daemon-reload", "enable tailscaled", "restart tailscaled"} {
		if !strings.Contains(sys, want) {
			t.Errorf("systemctl log missing %q:\n%s", want, sys)
		}
	}
}

func TestJoinScript_PersistentUpLeavesTailscaledAlone(t *testing.T) {
	t.Parallel()
	_, stderr, code, logs, dropin := runJoin(t, canaryKey+"\n",
		"up", "--hostname=build-01.agent", "--operator=agent",
	)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	up := readLog(t, logs["tailscale"])
	if strings.Contains(up, "--ephemeral") || strings.Contains(up, "--state") {
		t.Errorf("persistent up changed tailscaled settings:\n%s", up)
	}
	if _, err := os.Stat(dropin); !os.IsNotExist(err) {
		t.Errorf("persistent up wrote an ephemeral unit: %v", err)
	}
	sys := readLog(t, logs["systemctl"])
	if !strings.Contains(sys, "enable --now tailscaled") {
		t.Errorf("systemctl log = %q, want enable --now", sys)
	}
	if strings.Contains(sys, "daemon-reload") || strings.Contains(sys, "restart") {
		t.Errorf("persistent up restarted tailscaled:\n%s", sys)
	}
}

func TestJoinScript_EphemeralWithoutSystemdDoesNotRunTailscaleUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "agent-vm-tailscale-join")
	body, err := Script()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, body, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// cat writes the key and rm removes it. systemctl is absent on purpose:
	// a PATH that includes /usr/bin would find the host's systemctl.
	for _, tool := range []string{"cat", "rm"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(src, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(dir, "tailscale.log")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "tailscale"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tailscaled"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	keyfile := filepath.Join(dir, "authkey")
	cmd := exec.Command("sh", scriptPath, "up", "--hostname=build-01.agent", "--operator=agent", "--ephemeral")
	cmd.Stdin = strings.NewReader(canaryKey + "\n")
	cmd.Env = append(os.Environ(),
		"PATH="+bin,
		"AGENT_VM_TAILSCALE_KEYFILE="+keyfile,
		"AGENT_VM_TAILSCALE_EPHEMERAL_UNIT="+filepath.Join(dir, "unit.conf"),
	)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("exit %v, want 2\n%s", err, errb.String())
	}
	if !strings.Contains(errb.String(), "--state=mem:") {
		t.Errorf("stderr does not say why systemd is required:\n%s", errb.String())
	}
	if strings.Contains(errb.String(), canaryKey) {
		t.Error("the auth key was printed")
	}
	if got := readLog(t, logPath); got != "" {
		t.Errorf("tailscale up ran after the ephemeral setup failed:\n%s", got)
	}
	if _, statErr := os.Stat(keyfile); !os.IsNotExist(statErr) {
		t.Errorf("auth key file remains after the failed join")
	}
}
