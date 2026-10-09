package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// Host is the machine setup inspected before it changes anything.
type Host struct {
	Release
	Arch string
	User string
	// Root reports that the account agent-vm runs as is uid 0, so privileged
	// commands are not prefixed with sudo.
	Root bool
}

// Options selects what setup changes. StateDir is an absolute path on the
// hypervisor. Groups are the ones that account is added to. Session skips the
// search grant, because a session connection runs QEMU as the operator.
type Options struct {
	StateDir string
	Groups   []string
	GitHub   bool
	Session  bool
	// Output receives package-manager output as it is produced. Nil discards
	// it; the result still carries the tail if a command fails.
	Output io.Writer
	// Progress is called with one line per phase. Nil is fine.
	Progress func(string)
}

// Grant is one search-only ACL entry setup added.
type Grant struct {
	User string `json:"user"`
	Path string `json:"path"`
}

// Report is what setup did, or what --dry-run would do.
type Report struct {
	Family       string   `json:"family"`
	Version      string   `json:"version,omitempty"`
	Name         string   `json:"name"`
	Arch         string   `json:"arch"`
	User         string   `json:"user"`
	Packages     []string `json:"packages"`
	Units        []string `json:"units"`
	UnitsAssumed bool     `json:"unitsAssumed,omitempty"`
	Groups       []string `json:"groups"`
	GroupsAdded  []string `json:"groupsAdded"`
	Search       []Grant  `json:"search"`
	SearchNote   string   `json:"searchNote,omitempty"`
	Relogin      bool     `json:"relogin"`
	DryRun       bool     `json:"dryRun,omitempty"`
}

// PathError is a state directory setup cannot place on the hypervisor.
type PathError struct {
	Path string
}

func (e *PathError) Error() string {
	return fmt.Sprintf("state directory %q is not an absolute path; name it with --state-dir", e.Path)
}

// libvirtUnits is the monolithic daemon, preferred where the host has it, and
// the modular set used where it does not. Enabling both contends for the same
// socket, so setup enables exactly one of them.
var libvirtUnits = [][]string{
	{"libvirtd.service", "virtlogd.socket", "virtlockd.socket"},
	{"virtqemud.service", "virtnetworkd.service", "virtstoraged.service", "virtlogd.socket", "virtlockd.socket"},
}

// Detect reads the hypervisor's os-release, architecture, and the account
// agent-vm runs as. It changes nothing.
func Detect(ctx context.Context, run hostexec.Runner) (Host, error) {
	text, err := output(ctx, run, hostexec.Command{Name: "cat", Args: []string{"/etc/os-release"}, Effect: hostexec.Read})
	if err != nil {
		return Host{}, fmt.Errorf("reading /etc/os-release: %w", err)
	}
	release, err := MatchRelease(ParseOSRelease(text))
	if err != nil {
		return Host{}, err
	}

	archText, err := output(ctx, run, hostexec.Command{Name: "uname", Args: []string{"-m"}, Effect: hostexec.Read})
	if err != nil {
		return Host{}, fmt.Errorf("reading the host architecture: %w", err)
	}
	arch, err := normalizeArch(strings.TrimSpace(archText))
	if err != nil {
		return Host{}, err
	}

	user, err := output(ctx, run, hostexec.Command{Name: "id", Args: []string{"-un"}, Effect: hostexec.Read})
	if err != nil {
		return Host{}, fmt.Errorf("reading the account agent-vm runs as: %w", err)
	}
	uid, err := output(ctx, run, hostexec.Command{Name: "id", Args: []string{"-u"}, Effect: hostexec.Read})
	if err != nil {
		return Host{}, fmt.Errorf("reading the account agent-vm runs as: %w", err)
	}
	user = strings.TrimSpace(user)
	if user == "" || strings.ContainsAny(user, " \t\n:/") {
		return Host{}, fmt.Errorf("the account name %q cannot be passed to usermod", user)
	}

	return Host{
		Release: release,
		Arch:    arch,
		User:    user,
		Root:    strings.TrimSpace(uid) == "0",
	}, nil
}

// Apply installs packages, starts libvirt, adds groups, creates the state
// directory, and grants search access. A command marked Mutate is printed and
// skipped under --dry-run; the read-only probes still run.
func Apply(ctx context.Context, run hostexec.Runner, host Host, opt Options) (*Report, error) {
	if !filepath.IsAbs(opt.StateDir) {
		return nil, &PathError{Path: opt.StateDir}
	}
	for _, group := range opt.Groups {
		if group == "" || strings.ContainsAny(group, ", \t\n") {
			return nil, fmt.Errorf("group %q cannot be passed to usermod", group)
		}
	}

	packages, err := Packages(host.Family, host.Version, host.Arch, opt.GitHub)
	if err != nil {
		return nil, err
	}
	steps, err := installArgv(host.Family, packages)
	if err != nil {
		return nil, err
	}

	report := &Report{
		Family:      host.Family,
		Version:     host.Version,
		Name:        host.Name,
		Arch:        host.Arch,
		User:        host.User,
		Packages:    packages,
		Groups:      append([]string{}, opt.Groups...),
		GroupsAdded: []string{},
		Search:      []Grant{},
		Units:       []string{},
	}
	ex := executor{run: run, root: host.Root, output: opt.Output}

	say(opt, "Installing packages")
	skipped, err := ex.install(ctx, steps)
	if err != nil {
		return nil, err
	}
	report.DryRun = skipped

	say(opt, "Starting libvirt")
	units, assumed, err := ex.libvirtUnits(ctx, skipped)
	if err != nil {
		return nil, err
	}
	report.Units = units
	report.UnitsAssumed = assumed
	if _, err := ex.mutate(ctx, append([]string{"systemctl", "enable", "--now"}, units...), 0); err != nil {
		return nil, err
	}

	say(opt, "Adding "+host.User+" to "+joinAnd(opt.Groups))
	added, err := ex.addGroups(ctx, host.User, opt.Groups)
	if err != nil {
		return nil, err
	}
	report.GroupsAdded = added
	report.Relogin = len(added) > 0

	resolved, err := ex.resolve(ctx, opt.StateDir)
	if err != nil {
		return nil, err
	}
	say(opt, "Creating the state directory")
	if _, err := ex.run.Run(ctx, hostexec.Command{
		Name: "mkdir", Args: []string{"-p", resolved}, Effect: hostexec.Mutate,
	}); err != nil {
		return nil, fmt.Errorf("creating the state directory %s: %w", resolved, err)
	}

	if opt.Session {
		report.SearchNote = "a session connection runs QEMU as " + host.User + ", so no search grant is needed"
		return report, nil
	}
	say(opt, "Checking whether QEMU can reach the state directory")
	grants, note, err := ex.grantSearch(ctx, resolved, skipped)
	if err != nil {
		return nil, err
	}
	report.Search = grants
	report.SearchNote = note
	return report, nil
}

// executor runs one setup's commands. root skips sudo; otherwise every
// privileged command is `sudo -n`, which fails instead of prompting.
type executor struct {
	run    hostexec.Runner
	root   bool
	output io.Writer
}

func (e executor) install(ctx context.Context, steps [][]string) (bool, error) {
	skipped := false
	for _, argv := range steps {
		res, err := e.mutate(ctx, argv, packageTimeout)
		if err != nil {
			return false, err
		}
		if res != nil && res.Skipped {
			skipped = true
		}
	}
	return skipped, nil
}

func (e executor) mutate(ctx context.Context, argv []string, timeout time.Duration) (*hostexec.Result, error) {
	cmd := hostexec.Command{
		Name:    argv[0],
		Args:    argv[1:],
		Effect:  hostexec.Mutate,
		Timeout: timeout,
		Output:  e.output,
	}
	if !e.root {
		cmd.Name = "sudo"
		cmd.Args = append([]string{"-n", "--"}, argv...)
	}
	res, err := e.run.Run(ctx, cmd)
	if err != nil {
		return res, privilegeError(err)
	}
	return res, nil
}

func (e executor) libvirtUnits(ctx context.Context, packagesSkipped bool) ([]string, bool, error) {
	var masked bool
	for _, stack := range libvirtUnits {
		state, err := e.loadState(ctx, stack[0])
		if err != nil {
			if packagesSkipped && missingUnitProbe(err) {
				return append([]string{}, libvirtUnits[0]...), true, nil
			}
			return nil, false, err
		}
		if state == "masked" {
			masked = true
		}
		if state != "loaded" {
			continue
		}
		chosen := []string{stack[0]}
		for _, unit := range stack[1:] {
			st, err := e.loadState(ctx, unit)
			if err != nil {
				return nil, false, err
			}
			if st == "loaded" {
				chosen = append(chosen, unit)
			}
		}
		return chosen, false, nil
	}
	if packagesSkipped {
		return append([]string{}, libvirtUnits[0]...), true, nil
	}
	msg := "libvirt installed neither libvirtd.service nor virtqemud.service, so setup cannot start a daemon"
	if masked {
		msg += ". A unit is masked; unmask it with `sudo systemctl unmask` and run setup again"
	}
	return nil, false, errors.New(msg)
}

func (e executor) loadState(ctx context.Context, unit string) (string, error) {
	text, err := output(ctx, e.run, hostexec.Command{
		Name:   "systemctl",
		Args:   []string{"show", "-p", "LoadState", "--value", unit},
		Effect: hostexec.Read,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func (e executor) addGroups(ctx context.Context, user string, groups []string) ([]string, error) {
	if len(groups) == 0 {
		return []string{}, nil
	}
	text, err := output(ctx, e.run, hostexec.Command{
		Name: "id", Args: []string{"-nG", user}, Effect: hostexec.Read,
	})
	if err != nil {
		return nil, fmt.Errorf("reading the groups of %s: %w", user, err)
	}
	member := map[string]bool{}
	for _, name := range strings.Fields(text) {
		member[name] = true
	}
	var missing []string
	for _, group := range groups {
		if !member[group] {
			missing = append(missing, group)
		}
	}
	if len(missing) == 0 {
		return []string{}, nil
	}
	if _, err := e.mutate(ctx, []string{"usermod", "-aG", strings.Join(groups, ","), user}, 0); err != nil {
		return nil, err
	}
	return missing, nil
}

func (e executor) resolve(ctx context.Context, path string) (string, error) {
	text, err := output(ctx, e.run, hostexec.Command{
		Name: "realpath", Args: []string{"-m", path}, Effect: hostexec.Read,
	})
	if err != nil {
		// realpath is coreutils, which these hosts have. A failure here is
		// reported rather than papered over with a path QEMU would not walk.
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	resolved := strings.TrimSpace(text)
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("realpath reported %q for %s, which is not an absolute path", resolved, path)
	}
	return resolved, nil
}

func (e executor) grantSearch(ctx context.Context, stateDir string, packagesSkipped bool) ([]Grant, string, error) {
	user, err := e.qemuUser(ctx)
	if err != nil {
		return nil, "", err
	}
	if user == "" {
		if packagesSkipped {
			return []Grant{}, "the search grant is applied after libvirt is installed, once its account exists", nil
		}
		return []Grant{}, "could not determine the account QEMU runs as, so no search permission was granted. Run `agent-vm doctor` on the hypervisor", nil
	}

	var grants []Grant
	for _, dir := range ancestors(stateDir) {
		exists, err := e.isDir(ctx, dir)
		if err != nil {
			return nil, "", err
		}
		if !exists {
			continue
		}
		ok, err := e.canSearch(ctx, user, dir)
		if err != nil {
			return nil, "", err
		}
		if ok {
			continue
		}
		// x without r lets QEMU traverse the directory without listing it.
		if _, err := e.mutate(ctx, []string{"setfacl", "-m", "u:" + user + ":x", dir}, 0); err != nil {
			return nil, "", fmt.Errorf("granting %s search on %s: %w", user, dir, err)
		}
		grants = append(grants, Grant{User: user, Path: dir})
	}
	if grants == nil {
		grants = []Grant{}
	}
	return grants, "", nil
}

func (e executor) qemuUser(ctx context.Context) (string, error) {
	text, err := output(ctx, e.run, hostexec.Command{
		Name: "cat", Args: []string{"/etc/libvirt/qemu.conf"}, Effect: hostexec.Read,
	})
	if err != nil {
		var tool *hostexec.ToolError
		if !errors.As(err, &tool) {
			return "", err
		}
		text = ""
	}
	if name := qemuUserFromConf(text); name != "" {
		ok, err := e.accountExists(ctx, name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("qemu.conf sets user = %q, and that account does not exist", name)
		}
		return name, nil
	}
	for _, candidate := range QEMUUserCandidates {
		ok, err := e.accountExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if ok {
			return candidate, nil
		}
	}
	return "", nil
}

func (e executor) accountExists(ctx context.Context, name string) (bool, error) {
	_, err := output(ctx, e.run, hostexec.Command{
		Name: "getent", Args: []string{"passwd", name}, Effect: hostexec.Read,
	})
	if err == nil {
		return true, nil
	}
	var tool *hostexec.ToolError
	if errors.As(err, &tool) && (tool.ExitCode == 2 || (tool.ExitCode == 1 && strings.TrimSpace(tool.Stderr) == "")) {
		return false, nil
	}
	return false, err
}

func (e executor) isDir(ctx context.Context, path string) (bool, error) {
	_, err := e.run.Run(ctx, hostexec.Command{
		Name: "test", Args: []string{"-d", path}, Effect: hostexec.Read,
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

func (e executor) canSearch(ctx context.Context, user, path string) (bool, error) {
	cmd := hostexec.Command{Effect: hostexec.Read}
	if e.root {
		cmd.Name = "runuser"
		cmd.Args = []string{"-u", user, "--", "/usr/bin/test", "-x", path}
	} else {
		cmd.Name = "sudo"
		cmd.Args = []string{"-n", "-u", user, "--", "/usr/bin/test", "-x", path}
	}
	_, err := e.run.Run(ctx, cmd)
	if err == nil {
		return true, nil
	}
	var tool *hostexec.ToolError
	if errors.As(err, &tool) && tool.ExitCode == 1 && strings.TrimSpace(tool.Stderr) == "" {
		return false, nil
	}
	return false, privilegeError(err)
}

func qemuUserFromConf(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "user" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

func ancestors(path string) []string {
	path = filepath.Clean(path)
	var reversed []string
	for {
		reversed = append(reversed, path)
		if path == string(filepath.Separator) {
			break
		}
		next := filepath.Dir(path)
		if next == path {
			break
		}
		path = next
	}
	out := make([]string, len(reversed))
	for i := range reversed {
		out[i] = reversed[len(reversed)-1-i]
	}
	return out
}

func output(ctx context.Context, run hostexec.Runner, cmd hostexec.Command) (string, error) {
	res, err := run.Run(ctx, cmd)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	return string(res.Stdout), nil
}

func missingUnitProbe(err error) bool {
	var missing *hostexec.NotFoundError
	if errors.As(err, &missing) {
		return true
	}
	var tool *hostexec.ToolError
	return errors.As(err, &tool)
}

func privilegeError(err error) error {
	var tool *hostexec.ToolError
	if !errors.As(err, &tool) || tool.Tool != "sudo" {
		return err
	}
	low := strings.ToLower(tool.Stderr)
	if strings.Contains(low, "password") || strings.Contains(low, "a terminal is required") || strings.Contains(low, "no tty") {
		return fmt.Errorf("setup needs root, and sudo did not run without a password: %w\n  Run `sudo -v` in this terminal, then run `agent-vm setup` again", err)
	}
	return err
}

func say(opt Options, msg string) {
	if opt.Progress != nil {
		opt.Progress(msg)
	}
}
