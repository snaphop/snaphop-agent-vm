package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// VirtAAHelperProfile is the AppArmor profile libvirt uses to decide which
// files virt-aa-helper may open while it builds a per-VM profile.
const VirtAAHelperProfile = "/etc/apparmor.d/usr.lib.libvirt.virt-aa-helper"

// VirtAAHelperLocal is the snippet that profile includes last, so a later
// package update does not wipe a local rule.
const VirtAAHelperLocal = "/etc/apparmor.d/local/usr.lib.libvirt.virt-aa-helper"

const (
	virtAABegin = "# BEGIN agent-vm setup"
	virtAAEnd   = "# END agent-vm setup"

	// The shipped profile allows image files anywhere, then denies everything
	// under a hidden directory in a home directory. @{HOME} is /home/*/ and
	// /root/, so the denied tree is /home/<user>/.* and /root/.*.
	virtAADeny = "deny @{HOME}/.*/**"
)

// StateDirUnderHiddenHome reports whether path is covered by virt-aa-helper's
// deny of @{HOME}/.*/**. A state directory anywhere else is already readable
// under the shipped allows, and setup leaves the profile alone.
func StateDirUnderHiddenHome(path string) bool {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	parts := strings.Split(cleaned, string(filepath.Separator))
	// ["", "home", user, hidden, ...] or ["", "root", hidden, ...].
	if len(parts) >= 4 && parts[1] == "home" && hiddenComponent(parts[3]) {
		return true
	}
	if len(parts) >= 3 && parts[1] == "root" && hiddenComponent(parts[2]) {
		return true
	}
	return false
}

func hiddenComponent(name string) bool {
	return name != "." && name != ".." && strings.HasPrefix(name, ".")
}

// VirtAAHelperProfileDeniesHiddenHome reports whether the profile text still
// carries the shipped deny. A comment that mentions the rule does not count.
func VirtAAHelperProfileDeniesHiddenHome(profile string) bool {
	for _, line := range strings.Split(profile, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, virtAADeny) {
			return true
		}
	}
	return false
}

// VirtAAHelperRule is the one allow setup writes. priority=1 overrides the
// shipped deny, which has the default priority of 0, and only where the two
// overlap. rk is read and lock: the helper opens qcow2 images and does not
// need to write them. The rule names the state directory and nothing above it.
func VirtAAHelperRule(stateDir string) (string, error) {
	cleaned := filepath.Clean(stateDir)
	if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
		return "", fmt.Errorf("state directory %q is not a directory virt-aa-helper can be allowed to read", stateDir)
	}
	if strings.ContainsAny(cleaned, " \t\r\n*?[]{}^\"',\\#") {
		return "", fmt.Errorf("state directory %q cannot be written as an AppArmor path because it contains a space or a glob character. Choose another state_dir, or add the rule to %s by hand", stateDir, VirtAAHelperLocal)
	}
	return "priority=1 " + cleaned + "/** rk,", nil
}

// VirtAAHelperLocalAllows reports whether local already contains rule as its
// own line. A prefix of a longer path does not count.
func VirtAAHelperLocalAllows(local, rule string) bool {
	for _, line := range strings.Split(local, "\n") {
		if strings.TrimSpace(line) == rule {
			return true
		}
	}
	return false
}

// allowVirtAAHelper writes the state-directory rule when the shipped profile
// would deny it, and reloads the profile. It changes nothing when the helper
// is not installed, the profile does not deny the path, or the rule is
// already present. The returned path is the directory it allowed; the note
// explains a dry run that cannot see the profile yet.
func (e executor) allowVirtAAHelper(ctx context.Context, stateDir string, packagesSkipped bool) (string, string, error) {
	if !StateDirUnderHiddenHome(stateDir) {
		return "", "", nil
	}
	exists, err := e.fileExists(ctx, VirtAAHelperProfile)
	if err != nil {
		return "", "", err
	}
	if !exists {
		if packagesSkipped {
			return "", "the virt-aa-helper rule is applied after libvirt is installed, once its AppArmor profile exists", nil
		}
		return "", "", nil
	}
	profile, err := e.readFile(ctx, VirtAAHelperProfile)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", VirtAAHelperProfile, err)
	}
	if !VirtAAHelperProfileDeniesHiddenHome(profile) {
		return "", "", nil
	}
	rule, err := VirtAAHelperRule(stateDir)
	if err != nil {
		return "", "", err
	}
	existed, err := e.fileExists(ctx, VirtAAHelperLocal)
	if err != nil {
		return "", "", err
	}
	existing := ""
	if existed {
		existing, err = e.readFile(ctx, VirtAAHelperLocal)
		if err != nil {
			return "", "", fmt.Errorf("reading %s: %w", VirtAAHelperLocal, err)
		}
	}
	updated, changed, err := mergeVirtAAHelperLocal(existing, rule)
	if err != nil {
		return "", "", err
	}
	if !changed {
		return "", "", nil
	}
	if err := e.runPrivileged(ctx, []string{"tee", VirtAAHelperLocal}, strings.NewReader(updated)); err != nil {
		return "", "", fmt.Errorf("writing the virt-aa-helper rule for %s: %w", stateDir, err)
	}
	if err := e.runPrivileged(ctx, []string{"apparmor_parser", "-r", VirtAAHelperProfile}, nil); err != nil {
		restoreErr := e.restoreVirtAAHelperLocal(ctx, existed, existing)
		if restoreErr != nil {
			return "", "", fmt.Errorf("reloading virt-aa-helper after allowing it to read %s: %w; restoring %s also failed: %v", stateDir, err, VirtAAHelperLocal, restoreErr)
		}
		return "", "", fmt.Errorf("reloading virt-aa-helper after allowing it to read %s: %w", stateDir, err)
	}
	return stateDir, "", nil
}

func (e executor) restoreVirtAAHelperLocal(ctx context.Context, existed bool, previous string) error {
	if !existed {
		return e.runPrivileged(ctx, []string{"rm", "-f", VirtAAHelperLocal}, nil)
	}
	return e.runPrivileged(ctx, []string{"tee", VirtAAHelperLocal}, strings.NewReader(previous))
}

func mergeVirtAAHelperLocal(existing, rule string) (string, bool, error) {
	if VirtAAHelperLocalAllows(existing, rule) {
		return existing, false, nil
	}
	block := virtAABegin + "\n" +
		"# virt-aa-helper reads each disk so the per-VM profile can name its backing file.\n" +
		"# The shipped profile denies @{HOME}/.*/**, which covers this state directory.\n" +
		rule + "\n" +
		virtAAEnd
	if i := strings.Index(existing, virtAABegin); i >= 0 {
		rest := existing[i:]
		j := strings.Index(rest, virtAAEnd)
		if j < 0 {
			return "", false, fmt.Errorf("%s contains %q without %q. Remove the broken marker or close it, then run agent-vm setup again", VirtAAHelperLocal, virtAABegin, virtAAEnd)
		}
		end := i + j + len(virtAAEnd)
		return existing[:i] + block + existing[end:], true, nil
	}
	updated := existing
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	if updated != "" && !strings.HasSuffix(updated, "\n\n") {
		updated += "\n"
	}
	return updated + block + "\n", true, nil
}

func (e executor) fileExists(ctx context.Context, path string) (bool, error) {
	_, err := e.run.Run(ctx, hostexec.Command{
		Name: "test", Args: []string{"-f", path}, Effect: hostexec.Read,
	})
	if err == nil {
		return true, nil
	}
	var tool *hostexec.ToolError
	if errors.As(err, &tool) && tool.ExitCode == 1 && strings.TrimSpace(tool.Stderr) == "" {
		return false, nil
	}
	return false, err
}

func (e executor) readFile(ctx context.Context, path string) (string, error) {
	return output(ctx, e.run, hostexec.Command{
		Name: "cat", Args: []string{path}, Effect: hostexec.Read,
	})
}

func (e executor) runPrivileged(ctx context.Context, argv []string, stdin io.Reader) error {
	cmd := hostexec.Command{
		Name:   argv[0],
		Args:   argv[1:],
		Effect: hostexec.Mutate,
		Stdin:  stdin,
	}
	if !e.root {
		cmd.Name = "sudo"
		cmd.Args = append([]string{"-n", "--"}, argv...)
	}
	_, err := e.run.Run(ctx, cmd)
	if err != nil {
		return privilegeError(err)
	}
	return nil
}
