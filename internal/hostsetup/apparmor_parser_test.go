package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHiddenHomeExceptDenies_ParseOnThisApparmorParser(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("apparmor_parser"); err != nil {
		t.Skip("apparmor_parser is not installed")
	}
	var rules []string
	for _, stateDir := range []string{
		"/home/wensington/.local/share/agent-vm",
		"/home/foo-bar/.local/share/agent-vm",
		"/root/.cache/agent-vm",
		"/home/a/.b/c",
	} {
		denies, err := hiddenHomeExceptDenies(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		allow, err := VirtAAHelperPlainRule(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		rules = append(rules, denies...)
		rules = append(rules, allow)
	}
	path := filepath.Join(t.TempDir(), "carve.profile")
	body := "#include <tunables/global>\n\nprofile agent-vm-test-aa-parse /does/not/exist {\n  #include <abstractions/base>\n  " +
		strings.Join(rules, "\n  ") + "\n}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "apparmor_parser", "-Q", "-K", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apparmor_parser rejected the carve: %v\n%s", err, out)
	}
}

// TestHiddenHomeExceptDenies_LetsTheKernelReadOnlyTheStateDirectory loads the
// carve onto a copy of cat. It does not attach the profile to virt-aa-helper
// or to /bin/cat. Loading a profile changes process-global kernel state, so
// this test does not call t.Parallel.
func TestHiddenHomeExceptDenies_LetsTheKernelReadOnlyTheStateDirectory(t *testing.T) {
	if _, err := exec.LookPath("apparmor_parser"); err != nil {
		t.Skip("apparmor_parser is not installed")
	}
	if _, err := exec.LookPath("aa-exec"); err != nil {
		t.Skip("aa-exec is not installed")
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Skip("sudo -n is not available")
	}
	enabled, err := os.ReadFile("/sys/module/apparmor/parameters/enabled")
	if err != nil || strings.TrimSpace(string(enabled)) != "Y" {
		t.Skip("AppArmor is not enabled")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(home, ".local", "share", "agent-vm")
	if !StateDirUnderHiddenHome(stateDir) {
		t.Skipf("home %s is not covered by virt-aa-helper's hidden-home deny", home)
	}
	denies, err := hiddenHomeExceptDenies(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	allow, err := VirtAAHelperPlainRule(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	catPath := filepath.Join(dir, "cat")
	if err := copyFile(catPath, "/bin/cat", 0o755); err != nil {
		t.Fatal(err)
	}
	const profileName = "agent-vm-test-aa-carve"
	profilePath := filepath.Join(dir, "carve.profile")
	body := fmt.Sprintf("#include <tunables/global>\n\nprofile %s %s {\n  #include <abstractions/base>\n  capability dac_override,\n  capability dac_read_search,\n  %s mr,\n  @{HOME}/ r,\n  @{HOME}/** r,\n  %s\n  %s\n}\n",
		profileName, catPath, catPath, strings.Join(denies, "\n  "), allow)
	if err := os.WriteFile(profilePath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	allowed := filepath.Join(stateDir, "aa-agentvm-probe-allow")
	visible := filepath.Join(home, "aa-agentvm-probe-visible")
	hidden := filepath.Join(home, ".aa-agentvm-probe", "secret")
	localSibling := filepath.Join(home, ".local", "aa-agentvm-probe-secret")
	shareSibling := filepath.Join(home, ".local", "share", "aa-agentvm-probe-secret")
	cleanup, err := writeProbeFiles(map[string]string{
		allowed:      "agent-vm probe allow\n",
		visible:      "agent-vm probe visible\n",
		hidden:       "agent-vm probe hidden\n",
		localSibling: "agent-vm probe local\n",
		shareSibling: "agent-vm probe share\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	load := exec.CommandContext(ctx, "sudo", "-n", "apparmor_parser", "-r", "-K", profilePath)
	if out, err := load.CombinedOutput(); err != nil {
		msg := string(out)
		if strings.Contains(msg, "syntax error") || strings.Contains(msg, "AppArmor parser error") {
			t.Fatalf("generated profile did not parse: %v\n%s", err, msg)
		}
		t.Skipf("cannot load a throwaway AppArmor profile: %v\n%s", err, msg)
	}
	t.Cleanup(func() {
		unload := exec.Command("sudo", "-n", "apparmor_parser", "-R", profilePath)
		if out, err := unload.CombinedOutput(); err != nil {
			t.Errorf("unloading %s: %v\n%s", profileName, err, out)
		}
	})

	code, out := runConfined(t, profileName, catPath, allowed)
	if code != 0 || !strings.Contains(out, "agent-vm probe allow") {
		t.Fatalf("state file: exit %d\n%s", code, out)
	}
	code, out = runConfined(t, profileName, catPath, visible)
	if code != 0 || !strings.Contains(out, "agent-vm probe visible") {
		t.Fatalf("visible file: exit %d\n%s", code, out)
	}
	for _, path := range []string{hidden, localSibling, shareSibling} {
		code, out = runConfined(t, profileName, catPath, path)
		if code == 0 || !strings.Contains(out, "Permission denied") {
			t.Fatalf("%s: exit %d, want Permission denied\n%s", path, code, out)
		}
	}
}

func writeProbeFiles(files map[string]string) (func(), error) {
	var createdDirs []string
	var createdFiles []string
	cleanup := func() {
		for _, path := range createdFiles {
			_ = os.Remove(path)
		}
		for i := len(createdDirs) - 1; i >= 0; i-- {
			_ = os.Remove(createdDirs[i])
		}
	}
	for path, body := range files {
		if _, err := os.Stat(path); err == nil {
			cleanup()
			return nil, fmt.Errorf("refusing to overwrite %s", path)
		}
		if err := ensureProbeDir(filepath.Dir(path), &createdDirs); err != nil {
			cleanup()
			return nil, err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			cleanup()
			return nil, err
		}
		createdFiles = append(createdFiles, path)
	}
	return cleanup, nil
}

// ensureProbeDir creates path and any missing parent, and records only the
// directories this test created. An existing directory is left for its owner.
func ensureProbeDir(path string, created *[]string) error {
	if path == "" || path == string(filepath.Separator) {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureProbeDir(filepath.Dir(path), created); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return err
	}
	*created = append(*created, path)
	return nil
}

func copyFile(dst, src string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func runConfined(t *testing.T, profile, bin, path string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", "-n", "aa-exec", "-p", profile, "--", bin, path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), string(out)
	}
	t.Fatalf("aa-exec %s: %v\n%s", path, err, out)
	return -1, string(out)
}
