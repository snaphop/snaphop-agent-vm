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

	// virtAADenyBegin marks the shipped hidden-home deny after setup has
	// moved it into the local include. A package upgrade restores the deny
	// lines and drops the marker; setup puts the carve-out back.
	virtAADenyBegin = "# BEGIN agent-vm setup deny"
	virtAADenyEnd   = "# END agent-vm setup deny"

	// The shipped profile allows image files anywhere, then denies everything
	// under a hidden directory in a home directory. @{HOME} is /home/*/ and
	// /root/, so the denied tree is /home/<user>/.* and /root/.*.
	virtAADeny = "deny @{HOME}/.*/**"
)

// priorityProbe is a profile whose only unusual token is priority=. Parsers
// that accept the qualifier (AppArmor 4.1 and later) load it; AppArmor 4.0.1
// reports a syntax error. -Q -K compiles it and does not touch the kernel.
const priorityProbe = "profile agent-vm-priority-probe /does/not/exist {\n  priority=1 /tmp/agent-vm-probe rk,\n}\n"

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

// VirtAAHelperRule is the allow setup writes when apparmor_parser accepts the
// priority= qualifier. priority=1 overrides the shipped deny, which has the
// default priority of 0, and only where the two overlap. rk is read and lock:
// the helper opens qcow2 images and does not need to write them. The rule
// names the state directory and nothing above it.
func VirtAAHelperRule(stateDir string) (string, error) {
	plain, err := VirtAAHelperPlainRule(stateDir)
	if err != nil {
		return "", err
	}
	return "priority=1 " + plain, nil
}

// VirtAAHelperPlainRule is the same allow without priority=. AppArmor 4.0.1,
// which Ubuntu 24.04 ships, rejects priority= as a syntax error. A plain allow
// does not override a deny on that parser, so it is only effective once the
// shipped hidden-home deny has been narrowed with hiddenHomeExceptDenies.
func VirtAAHelperPlainRule(stateDir string) (string, error) {
	cleaned, err := cleanAppArmorPath(stateDir)
	if err != nil {
		return "", err
	}
	return cleaned + "/** rk,", nil
}

func cleanAppArmorPath(stateDir string) (string, error) {
	cleaned := filepath.Clean(stateDir)
	if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
		return "", fmt.Errorf("state directory %q is not a directory virt-aa-helper can be allowed to read", stateDir)
	}
	if strings.ContainsAny(cleaned, " \t\r\n*?[]{}^\"',\\#") || !isASCIIPath(cleaned) {
		return "", fmt.Errorf("state directory %q cannot be written as an AppArmor path because it contains a space, a glob character, or a non-ASCII character. Choose another state_dir, or add the rule to %s by hand", stateDir, VirtAAHelperLocal)
	}
	return cleaned, nil
}

func isASCIIPath(path string) bool {
	for i := 0; i < len(path); i++ {
		if path[i] >= 0x7f {
			return false
		}
	}
	return true
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

// allowVirtAAHelper lets virt-aa-helper read the state directory when the
// shipped profile would deny it, and reloads the profile. It changes nothing
// when the helper is not installed, the profile does not deny the path, or
// the exception is already present. The returned path is the directory it
// allowed; the note explains a dry run that cannot see the profile yet.
//
// Parsers that accept priority= get one local allow at priority 1, which
// overrides the shipped deny only on that directory. AppArmor 4.0.1 rejects
// that qualifier, and a plain allow does not override a deny, so on that
// parser the shipped hidden-home deny is replaced with a deny of the same
// paths minus the state directory.
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
	denies := VirtAAHelperProfileDeniesHiddenHome(profile)
	narrowed := virtAAHelperDenyNarrowed(profile)
	if !denies && !narrowed {
		return "", "", nil
	}
	if _, err := cleanAppArmorPath(stateDir); err != nil {
		return "", "", err
	}
	existed, existing, err := e.readLocal(ctx)
	if err != nil {
		return "", "", err
	}
	if denies {
		accepts, err := e.apparmorAcceptsPriority(ctx)
		if err != nil {
			return "", "", err
		}
		if accepts {
			return e.allowVirtAAHelperWithPriority(ctx, stateDir, existed, existing)
		}
	}
	return e.allowVirtAAHelperByNarrowing(ctx, stateDir, profile, existed, existing)
}

func (e executor) readLocal(ctx context.Context) (bool, string, error) {
	existed, err := e.fileExists(ctx, VirtAAHelperLocal)
	if err != nil {
		return false, "", err
	}
	if !existed {
		return false, "", nil
	}
	existing, err := e.readFile(ctx, VirtAAHelperLocal)
	if err != nil {
		return false, "", fmt.Errorf("reading %s: %w", VirtAAHelperLocal, err)
	}
	return true, existing, nil
}

func (e executor) allowVirtAAHelperWithPriority(ctx context.Context, stateDir string, existed bool, existing string) (string, string, error) {
	rule, err := VirtAAHelperRule(stateDir)
	if err != nil {
		return "", "", err
	}
	updated, changed, err := mergeVirtAAHelperLocal(existing, virtAAAllowBlock(rule), VirtAAHelperLocalAllows(existing, rule))
	if err != nil {
		return "", "", err
	}
	if !changed {
		return "", "", nil
	}
	if err := e.writeLocal(ctx, stateDir, updated); err != nil {
		return "", "", err
	}
	if err := e.reloadVirtAAHelper(ctx, stateDir); err != nil {
		return "", "", e.restoreAfterReloadFailure(ctx, stateDir, err, false, "", existed, existing)
	}
	return stateDir, "", nil
}

func (e executor) allowVirtAAHelperByNarrowing(ctx context.Context, stateDir, profile string, existed bool, existing string) (string, string, error) {
	allow, err := VirtAAHelperPlainRule(stateDir)
	if err != nil {
		return "", "", err
	}
	denies, err := hiddenHomeExceptDenies(stateDir)
	if err != nil {
		return "", "", err
	}
	narrowed, profileChanged, err := narrowShippedHiddenDenies(profile, stateDir)
	if err != nil {
		return "", "", err
	}
	satisfied := !profileChanged && localHasRules(existing, append(append([]string{}, denies...), allow))
	updated, localChanged, err := mergeVirtAAHelperLocal(existing, virtAACarveBlock(denies, allow), satisfied)
	if err != nil {
		return "", "", err
	}
	if !localChanged && !profileChanged {
		return "", "", nil
	}
	if localChanged {
		if err := e.writeLocal(ctx, stateDir, updated); err != nil {
			return "", "", err
		}
	}
	if profileChanged {
		if err := e.runPrivileged(ctx, []string{"tee", VirtAAHelperProfile}, strings.NewReader(narrowed)); err != nil {
			restoreErr := error(nil)
			if localChanged {
				restoreErr = e.restoreVirtAAHelperLocal(ctx, existed, existing)
			}
			if restoreErr != nil {
				return "", "", fmt.Errorf("writing %s so virt-aa-helper can read %s: %w; restoring %s also failed: %v", VirtAAHelperProfile, stateDir, err, VirtAAHelperLocal, restoreErr)
			}
			return "", "", fmt.Errorf("writing %s so virt-aa-helper can read %s: %w", VirtAAHelperProfile, stateDir, err)
		}
	}
	if err := e.reloadVirtAAHelper(ctx, stateDir); err != nil {
		return "", "", e.restoreAfterReloadFailure(ctx, stateDir, err, profileChanged, profile, existed, existing)
	}
	return stateDir, "", nil
}

func (e executor) writeLocal(ctx context.Context, stateDir, body string) error {
	if err := e.runPrivileged(ctx, []string{"tee", VirtAAHelperLocal}, strings.NewReader(body)); err != nil {
		return fmt.Errorf("writing the virt-aa-helper rule for %s: %w", stateDir, err)
	}
	return nil
}

func (e executor) reloadVirtAAHelper(ctx context.Context, stateDir string) error {
	if err := e.runPrivileged(ctx, []string{"apparmor_parser", "-r", VirtAAHelperProfile}, nil); err != nil {
		return fmt.Errorf("reloading virt-aa-helper after allowing it to read %s: %w", stateDir, err)
	}
	return nil
}

// restoreAfterReloadFailure puts the profile back, then the local file. The
// profile is restored first so the shipped deny is in place before the local
// carve-out is removed. If the profile cannot be restored, the local carve-out
// stays: it is what keeps every other hidden path denied.
func (e executor) restoreAfterReloadFailure(ctx context.Context, stateDir string, reloadErr error, profileChanged bool, previousProfile string, localExisted bool, previousLocal string) error {
	if profileChanged {
		if err := e.runPrivileged(ctx, []string{"tee", VirtAAHelperProfile}, strings.NewReader(previousProfile)); err != nil {
			return fmt.Errorf("%w; restoring %s also failed: %v. The rules in %s are still in place", reloadErr, VirtAAHelperProfile, err, VirtAAHelperLocal)
		}
	}
	if err := e.restoreVirtAAHelperLocal(ctx, localExisted, previousLocal); err != nil {
		return fmt.Errorf("%w; restoring %s also failed: %v", reloadErr, VirtAAHelperLocal, err)
	}
	return reloadErr
}

func (e executor) restoreVirtAAHelperLocal(ctx context.Context, existed bool, previous string) error {
	if !existed {
		return e.runPrivileged(ctx, []string{"rm", "-f", VirtAAHelperLocal}, nil)
	}
	return e.runPrivileged(ctx, []string{"tee", VirtAAHelperLocal}, strings.NewReader(previous))
}

func virtAAHelperDenyNarrowed(profile string) bool {
	return strings.Contains(profile, virtAADenyBegin)
}

// apparmorAcceptsPriority reports whether this host's apparmor_parser accepts
// the priority= qualifier. The probe is a read: --dry-run runs it, and it is
// not wrapped in sudo because -Q -K only parses. The profile arrives on stdin.
// A bare "-" is "File - not found", and giving the parser no path makes it
// ignore stdin and exit 0. /dev/stdin is the path that actually reads the
// profile, including over the ssh transport to a remote hypervisor.
func (e executor) apparmorAcceptsPriority(ctx context.Context) (bool, error) {
	_, err := e.run.Run(ctx, hostexec.Command{
		Name:   "apparmor_parser",
		Args:   []string{"-Q", "-K", "/dev/stdin"},
		Effect: hostexec.Read,
		Stdin:  strings.NewReader(priorityProbe),
	})
	if err == nil {
		return true, nil
	}
	var tool *hostexec.ToolError
	if errors.As(err, &tool) && tool.ExitCode == 1 && strings.Contains(tool.Stderr, "syntax error") {
		return false, nil
	}
	return false, fmt.Errorf("checking whether apparmor_parser accepts priority=: %w", err)
}

func virtAAAllowBlock(rule string) string {
	return virtAABegin + "\n" +
		"# virt-aa-helper reads each disk so the per-VM profile can name its backing file.\n" +
		"# The shipped profile denies @{HOME}/.*/**, which covers this state directory.\n" +
		rule + "\n" +
		virtAAEnd
}

func virtAACarveBlock(denies []string, allow string) string {
	return virtAABegin + "\n" +
		"# virt-aa-helper reads each disk so the per-VM profile can name its backing file.\n" +
		"# The shipped profile denies @{HOME}/.*/**, which covers this state directory.\n" +
		"# This AppArmor parser rejects priority=. The denies below are the shipped\n" +
		"# hidden-home deny with this state directory left out.\n" +
		strings.Join(denies, "\n") + "\n" +
		allow + "\n" +
		virtAAEnd
}

const virtAADenyNote = "# The hidden-home deny is in /etc/apparmor.d/local/usr.lib.libvirt.virt-aa-helper, excluding the state directory."

// narrowShippedHiddenDenies replaces the shipped hidden-home deny with a
// marker. The denies themselves move into the local include, which is where
// the state directory is left out. A profile that already carries the marker
// and no longer has the deny is returned unchanged. A package update that
// restores the deny underneath an old marker is narrowed again.
func narrowShippedHiddenDenies(profile, stateDir string) (string, bool, error) {
	denies := VirtAAHelperProfileDeniesHiddenHome(profile)
	if strings.Contains(profile, virtAADenyBegin) && !denies {
		if !strings.Contains(profile, virtAADenyEnd) {
			return "", false, fmt.Errorf("%s contains %q without %q. Restore the profile from its package, then run agent-vm setup again", VirtAAHelperProfile, virtAADenyBegin, virtAADenyEnd)
		}
		return profile, false, nil
	}
	if !denies {
		return profile, false, nil
	}
	lines := strings.Split(profile, "\n")
	out := make([]string, 0, len(lines)+3)
	removed := 0
	inserted := false
	for _, line := range lines {
		if isShippedHiddenDeny(line) {
			removed++
			if !inserted {
				out = append(out, "  "+virtAADenyBegin, "  "+virtAADenyNote, "  "+virtAADenyEnd)
				inserted = true
			}
			continue
		}
		if isDenyMarkerLine(line) {
			continue
		}
		out = append(out, line)
	}
	if removed == 0 {
		return "", false, fmt.Errorf("virt-aa-helper's profile denies hidden home directories, but the deny is not the shipped three lines, so setup cannot exclude %s. Move the state directory out of the hidden home directory, or edit %s by hand", stateDir, VirtAAHelperProfile)
	}
	updated := strings.Join(out, "\n")
	return updated, updated != profile, nil
}

func isShippedHiddenDeny(line string) bool {
	switch strings.TrimSpace(line) {
	case "audit deny @{HOME}/.* mrwkl,",
		"deny @{HOME}/.* mrwkl,",
		"audit deny @{HOME}/.*/ rw,",
		"deny @{HOME}/.*/ rw,",
		"audit deny @{HOME}/.*/** mrwkl,",
		"deny @{HOME}/.*/** mrwkl,":
		return true
	default:
		return false
	}
}

func isDenyMarkerLine(line string) bool {
	switch strings.TrimSpace(line) {
	case virtAADenyBegin, virtAADenyNote, virtAADenyEnd:
		return true
	default:
		return false
	}
}

func localHasRules(local string, rules []string) bool {
	for _, rule := range rules {
		if !VirtAAHelperLocalAllows(local, rule) {
			return false
		}
	}
	return true
}

// mergeVirtAAHelperLocal inserts block between the agent-vm markers, or
// appends it. satisfied means the file already grants this state directory,
// so the file is left byte-for-byte alone. The markers are matched as whole
// lines: the deny marker in the main profile starts with the same words and
// must not be treated as this block.
func mergeVirtAAHelperLocal(existing, block string, satisfied bool) (string, bool, error) {
	if satisfied {
		return existing, false, nil
	}
	var updated string
	if i := findLineMarker(existing, virtAABegin); i >= 0 {
		j := findLineMarker(existing[i+len(virtAABegin):], virtAAEnd)
		if j < 0 {
			return "", false, fmt.Errorf("%s contains %q without %q. Remove the broken marker or close it, then run agent-vm setup again", VirtAAHelperLocal, virtAABegin, virtAAEnd)
		}
		j += i + len(virtAABegin)
		end := j + len(virtAAEnd)
		updated = existing[:i] + block + existing[end:]
	} else {
		updated = existing
		if updated != "" && !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		if updated != "" && !strings.HasSuffix(updated, "\n\n") {
			updated += "\n"
		}
		updated += block + "\n"
	}
	return updated, updated != existing, nil
}

// findLineMarker reports the index of marker when it occupies a whole line.
// A longer line that only begins with marker does not count.
func findLineMarker(text, marker string) int {
	from := 0
	for {
		var pos int
		if from == 0 && strings.HasPrefix(text, marker) {
			pos = 0
		} else {
			rel := strings.Index(text[from:], "\n"+marker)
			if rel < 0 {
				return -1
			}
			pos = from + rel + 1
		}
		end := pos + len(marker)
		if end == len(text) || text[end] == '\n' {
			return pos
		}
		from = pos + 1
		if from > len(text) {
			return -1
		}
	}
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
