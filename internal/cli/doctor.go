package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/network"
)

// MinFreeSpace is the free space doctor expects in the state directory. A base
// image is 2–3 GiB and overlays grow as guests write, so less than this is a
// problem waiting to happen rather than an immediate failure — it is reported
// as a warning.
const MinFreeSpace = 10 * config.GiB

// checkStatus is the outcome of one readiness check. Only fail makes doctor
// exit non-zero; warn is for something that works now but will bite later.
type checkStatus string

const (
	statusPass checkStatus = "pass"
	statusWarn checkStatus = "warn"
	statusFail checkStatus = "fail"
	statusSkip checkStatus = "skip"
)

// check is one line of doctor's report. The JSON shape is a public contract.
type check struct {
	Name   string      `json:"name"`
	Status checkStatus `json:"status"`
	Detail string      `json:"detail,omitempty"`
	Remedy string      `json:"remedy,omitempty"`
}

type doctorReport struct {
	OK     bool    `json:"ok"`
	Checks []check `json:"checks"`
}

func doctorCommand() *command {
	return &command{
		name:    "doctor",
		summary: "check that this host can run VMs",
		usage:   "agent-vm doctor [--output json]",
		run:     runDoctor,
	}
}

// runDoctor reports every check rather than stopping at the first failure: an
// operator setting up a host wants the whole list, not one problem at a time.
// Because this tool delegates almost everything to host tools, this check is
// load-bearing rather than a nicety.
func runDoctor(ctx context.Context, app *App, args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(app.Stderr)
	if err := flags.Parse(args); err != nil {
		return &ExitError{Code: ExitUsage, Err: err}
	}
	if flags.NArg() > 0 {
		return exitf(ExitUsage, "doctor takes no arguments, got %q", flags.Arg(0))
	}

	cfg, err := app.Config()
	if err != nil {
		return err
	}

	conn, err := app.Connection()
	if err != nil {
		return err
	}

	report := doctorReport{}
	// The transport is checked first and its result gates the rest: when the
	// hypervisor cannot be reached, every check below it would fail for the
	// same reason and say so eight times over.
	transport := checkHypervisor(ctx, app, conn)
	if transport != nil {
		report.Checks = append(report.Checks, *transport)
		if transport.Status == statusFail {
			return app.reportDoctor(report)
		}
	}
	report.Checks = append(report.Checks, checkKVM(ctx, app, conn))
	report.Checks = append(report.Checks, checkGroups(ctx, app, cfg, conn)...)
	report.Checks = append(report.Checks, checkTools(ctx, app)...)
	report.Checks = append(report.Checks, checkOptionalTools(ctx, app)...)
	report.Checks = append(report.Checks, checkAppliance(ctx, app, cfg))

	libvirt := checkLibvirt(ctx, app, cfg)
	report.Checks = append(report.Checks, libvirt)
	report.Checks = append(report.Checks, checkStateDir(app, cfg))
	report.Checks = append(report.Checks, checkStateDirTraversal(app, cfg))
	report.Checks = append(report.Checks, checkNATNetwork(ctx, app, cfg, libvirt.Status == statusPass))
	report.Checks = append(report.Checks, checkForwarding(cfg, conn))
	report.Checks = append(report.Checks, checkGuestServices(cfg, conn, app.natBridge(ctx, cfg, libvirt.Status == statusPass)))
	report.Checks = append(report.Checks, checkBridge(ctx, app, cfg)...)

	return app.reportDoctor(report)
}

// reportDoctor renders the checks and turns a failure into the documented exit
// code. It is a method rather than inline so that an early return — a
// hypervisor that cannot be reached at all — reports what it did learn.
func (a *App) reportDoctor(report doctorReport) error {
	report.OK = true
	for _, c := range report.Checks {
		if c.Status == statusFail {
			report.OK = false
		}
	}

	if err := a.renderDoctor(report); err != nil {
		return err
	}
	if !report.OK {
		return &ExitError{
			Code: ExitHostNotReady,
			Err:  errors.New("this host is not ready to run VMs; see the failing checks above"),
		}
	}
	return nil
}

// checkHypervisor confirms agent-vm can run a command on the machine libvirt is
// on. It is nil for a local connection, where there is nothing to reach.
//
// Everything below it depends on this working: with a remote URI, agent-vm
// builds images, creates disks, and defines domains by running the host tools
// there over ssh (ADR-0010), so an unreachable host is the only thing worth
// reporting.
func checkHypervisor(ctx context.Context, app *App, conn *config.Connection) *check {
	if !conn.Remote {
		return nil
	}
	name := "hypervisor host " + conn.SSHDestination

	// `true` is the smallest thing that proves a command ran there.
	if _, err := app.runner.Run(ctx, hostexec.Command{Name: "true", Effect: hostexec.Read}); err != nil {
		// The transport error already names the destination, quotes ssh, and
		// says what has to work, so it is the remedy rather than a preamble to
		// one.
		return &check{
			Name: name, Status: statusFail,
			Detail: "cannot run a command over ssh",
			Remedy: err.Error(),
		}
	}
	return &check{
		Name: name, Status: statusPass,
		Detail: "reachable over ssh; host tools and the state directory live there",
	}
}

func (a *App) renderDoctor(report doctorReport) error {
	if a.out.format == OutputJSON {
		return a.out.JSON(report)
	}

	rows := make([][]string, 0, len(report.Checks))
	for _, c := range report.Checks {
		rows = append(rows, []string{statusLabel(c.Status), c.Name, c.Detail})
	}
	a.out.Table(rows)

	for _, c := range report.Checks {
		if c.Remedy != "" && (c.Status == statusFail || c.Status == statusWarn) {
			a.out.Printf("\n%s: %s\n  %s\n", c.Name, c.Detail, c.Remedy)
		}
	}
	if report.OK {
		a.out.Printf("\nThis host can run VMs.\n")
	}
	return nil
}

func statusLabel(s checkStatus) string {
	switch s {
	case statusPass:
		return "  ok  "
	case statusWarn:
		return " warn "
	case statusFail:
		return " FAIL "
	default:
		return " skip "
	}
}

// checkKVM confirms hardware virtualization is available to this user.
// Software emulation is not an acceptable fallback: it is slow enough that a
// VM per task stops being viable.
func checkKVM(ctx context.Context, app *App, conn *config.Connection) check {
	const path = "/dev/kvm"
	if conn.Remote {
		return checkRemoteKVM(ctx, app, path)
	}
	if err := syscall.Access(path, unixReadWrite); err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return check{
				Name: "kvm", Status: statusFail,
				Detail: path + " does not exist",
				Remedy: "Enable virtualization in firmware and load the kvm_intel or kvm_amd module. Nested virtualization must be enabled if this host is itself a VM.",
			}
		}
		return check{
			Name: "kvm", Status: statusFail,
			Detail: path + " is not readable and writable by this user",
			Remedy: "Add your user to the kvm group and start a new login session: sudo usermod -aG kvm $USER",
		}
	}
	return check{Name: "kvm", Status: statusPass, Detail: path + " is available"}
}

// unixReadWrite is R_OK|W_OK for syscall.Access.
const unixReadWrite = 0x2 | 0x4

// checkRemoteKVM asks the same question on the hypervisor, where the answer
// concerns the account agent-vm logs in as there rather than this one. `test`
// is the shell builtin that answers exactly what syscall.Access does locally.
func checkRemoteKVM(ctx context.Context, app *App, path string) check {
	probe := func(args ...string) bool {
		_, err := app.runner.Run(ctx, hostexec.Command{Name: "test", Args: args, Effect: hostexec.Read})
		return err == nil
	}

	switch {
	case !probe("-e", path):
		return check{
			Name: "kvm", Status: statusFail,
			Detail: path + " does not exist on " + app.runner.HypervisorHost(),
			Remedy: "Enable virtualization in firmware on that host and load the kvm_intel or kvm_amd module. Nested virtualization must be enabled if it is itself a VM.",
		}
	case !probe("-r", path, "-a", "-w", path):
		return check{
			Name: "kvm", Status: statusFail,
			Detail: path + " is not readable and writable by the account agent-vm logs in as on " + app.runner.HypervisorHost(),
			Remedy: "On that host: sudo usermod -aG kvm <the account in the libvirt URI>, then let it start a new session.",
		}
	}
	return check{Name: "kvm", Status: statusPass, Detail: path + " is available on " + app.runner.HypervisorHost()}
}

// checkGroups reports group membership. It is a warning rather than a failure:
// a host may grant access through udev rules or ACLs instead, and the KVM and
// libvirt checks already test what actually matters.
func checkGroups(ctx context.Context, app *App, cfg *config.Config, conn *config.Connection) []check {
	if conn.Remote {
		return checkRemoteGroups(ctx, app, cfg)
	}
	current, err := user.Current()
	if err != nil {
		return []check{{Name: "groups", Status: statusSkip, Detail: fmt.Sprintf("cannot determine the current user: %v", err)}}
	}
	groupIDs, err := current.GroupIds()
	if err != nil {
		return []check{{Name: "groups", Status: statusSkip, Detail: fmt.Sprintf("cannot read group membership: %v", err)}}
	}

	names := map[string]bool{}
	for _, gid := range groupIDs {
		if group, err := user.LookupGroupId(gid); err == nil {
			names[group.Name] = true
		}
	}

	required := requiredGroups(cfg)
	checks := make([]check, 0, len(required))
	for _, group := range required {
		if names[group] {
			checks = append(checks, check{Name: "group " + group, Status: statusPass, Detail: current.Username + " is a member"})
			continue
		}
		checks = append(checks, check{
			Name: "group " + group, Status: statusWarn,
			Detail: current.Username + " is not a member",
			Remedy: fmt.Sprintf("sudo usermod -aG %s %s, then start a new login session. Ignore this if your host grants access another way and the other checks pass.", group, current.Username),
		})
	}
	return checks
}

// checkRemoteGroups reports the group membership of the account agent-vm logs
// in as on the hypervisor. `id` is asked rather than the passwd database,
// because that database is on the other machine.
func checkRemoteGroups(ctx context.Context, app *App, cfg *config.Config) []check {
	ask := func(args ...string) (string, error) {
		res, err := app.runner.Run(ctx, hostexec.Command{Name: "id", Args: args, Effect: hostexec.Read})
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(res.Stdout)), nil
	}

	username, err := ask("-un")
	if err != nil {
		return []check{{Name: "groups", Status: statusSkip, Detail: fmt.Sprintf("cannot determine the remote user: %v", err)}}
	}
	groups, err := ask("-nG")
	if err != nil {
		return []check{{Name: "groups", Status: statusSkip, Detail: fmt.Sprintf("cannot read remote group membership: %v", err)}}
	}

	names := map[string]bool{}
	for _, group := range strings.Fields(groups) {
		names[group] = true
	}

	checks := make([]check, 0, 2)
	for _, group := range requiredGroups(cfg) {
		where := username + " on " + app.runner.HypervisorHost()
		if names[group] {
			checks = append(checks, check{Name: "group " + group, Status: statusPass, Detail: where + " is a member"})
			continue
		}
		checks = append(checks, check{
			Name: "group " + group, Status: statusWarn,
			Detail: where + " is not a member",
			Remedy: fmt.Sprintf("On that host: sudo usermod -aG %s %s, then let it start a new session. Ignore this if the host grants access another way and the other checks pass.", group, username),
		})
	}
	return checks
}

// requiredGroups are the groups that grant the hypervisor access this
// configuration needs.
func requiredGroups(cfg *config.Config) []string {
	if cfg.SessionMode() {
		return []string{"kvm"}
	}
	return []string{"kvm", "libvirt"}
}

// checkTools verifies every tool this project delegates to, and its minimum
// version. Because the design orchestrates rather than reimplements, a missing
// or too-old tool is the most common reason the tool does not work.
func checkTools(ctx context.Context, app *App) []check {
	tools := hostexec.RequiredTools()
	checks := make([]check, 0, len(tools))

	for _, tool := range tools {
		version, err := app.versions.Require(ctx, tool)

		var (
			missing *hostexec.NotFoundError
			tooOld  *hostexec.VersionError
			parse   *hostexec.ParseError
		)
		switch {
		case errors.As(err, &missing):
			checks = append(checks, check{
				Name: tool.Name, Status: statusFail,
				Detail: "not found on PATH",
				Remedy: fmt.Sprintf("Install %s (package %s).", tool.Name, tool.Package),
			})
		case errors.As(err, &tooOld):
			checks = append(checks, check{
				Name: tool.Name, Status: statusFail,
				Detail: fmt.Sprintf("version %s is older than the required %s", tooOld.Found, tooOld.Minimum),
				Remedy: fmt.Sprintf("Upgrade %s to %s or newer.", tool.Name, tooOld.Minimum),
			})
		case errors.As(err, &parse):
			// The tool works but reports its version in a form we do not know.
			// That is our problem, not the operator's, so it is a warning.
			checks = append(checks, check{
				Name: tool.Name, Status: statusWarn,
				Detail: "installed, but its version could not be read",
				Remedy: "This is an agent-vm bug: please report the output of `" + tool.Name + " " + strings.Join(tool.VersionArgs(), " ") + "`.",
			})
		case err != nil:
			checks = append(checks, check{Name: tool.Name, Status: statusFail, Detail: err.Error()})
		default:
			detail := version.String()
			if !tool.Minimum.IsZero() {
				detail += fmt.Sprintf(" (minimum %s)", tool.Minimum)
			}
			checks = append(checks, check{Name: tool.Name, Status: statusPass, Detail: detail})
		}
	}
	return checks
}

// checkOptionalTools reports the tools only some flags need. A host without
// them is still a working host, so nothing here can fail the report — it exists
// so an operator can see in advance whether those flags will work.
func checkOptionalTools(ctx context.Context, app *App) []check {
	tools := hostexec.OptionalTools()
	checks := make([]check, 0, len(tools))

	for _, tool := range tools {
		version, err := app.versions.Require(ctx, tool)

		var missing *hostexec.NotFoundError
		switch {
		case errors.As(err, &missing):
			checks = append(checks, check{
				Name: tool.Name, Status: statusSkip,
				Detail: fmt.Sprintf("not installed; only `--github-ssh-key` needs it (package %s)", tool.Package),
			})
		case err != nil:
			checks = append(checks, check{
				Name: tool.Name, Status: statusWarn,
				Detail: err.Error(),
				Remedy: "`--github-ssh-key` will not work until this is fixed; every other command is unaffected.",
			})
		default:
			checks = append(checks, check{
				Name: tool.Name, Status: statusPass,
				Detail: fmt.Sprintf("%s (optional; used by --github-ssh-key)", version),
			})
		}
	}
	return checks
}

// checkLibvirt confirms the connection works. `virsh version` fails with a
// clear message when the daemon is not running or not reachable, so its exit
// status is the whole check — nothing needs parsing.
func checkLibvirt(ctx context.Context, app *App, cfg *config.Config) check {
	// virsh runs on the hypervisor, so it is given the URI as that machine
	// reads it (ADR-0010). The remedy below still names the URI the operator
	// configured, because that is the one they would type.
	uri, err := app.hypervisorURI()
	if err != nil {
		return check{Name: "libvirt connection", Status: statusFail, Detail: err.Error()}
	}
	_, err = app.runner.Run(ctx, hostexec.Command{
		Name:   hostexec.Virsh.Name,
		Args:   []string{"--connect", uri, "version"},
		Effect: hostexec.Read,
	})
	if err != nil {
		remedy := "Start libvirt with `sudo systemctl start libvirtd` (or `virtqemud`), and confirm your user may connect to " + cfg.LibvirtURI + "."
		if cfg.SessionMode() {
			remedy = "Session mode needs a running user session daemon: `systemctl --user start virtqemud`."
		}
		return check{
			Name: "libvirt connection", Status: statusFail,
			Detail: "cannot connect to " + cfg.LibvirtURI,
			Remedy: remedy,
		}
	}
	return check{Name: "libvirt connection", Status: statusPass, Detail: "connected to " + cfg.LibvirtURI}
}

func checkStateDir(app *App, cfg *config.Config) check {
	if app.dryRun {
		// This check works by writing a probe file, which --dry-run forbids.
		return check{Name: "state directory", Status: statusSkip, Detail: "skipped under --dry-run: the check writes a probe file"}
	}

	store, err := app.Store()
	if err != nil {
		return check{
			Name: "state directory", Status: statusFail,
			Detail: fmt.Sprintf("cannot use %s: %v", cfg.StateDir, err),
			Remedy: "Choose a writable location with --state-dir or the state_dir config key.",
		}
	}

	// Writability is tested by writing, not by inspecting permission bits,
	// which say nothing about ACLs, read-only mounts, or full filesystems.
	probe := store.Root() + "/.doctor-write-probe"
	if err := store.WriteFile(probe, []byte("agent-vm doctor\n"), 0o600); err != nil {
		return check{
			Name: "state directory", Status: statusFail,
			Detail: fmt.Sprintf("%s is not writable: %v", store.Root(), err),
			Remedy: "Choose a writable location with --state-dir or the state_dir config key.",
		}
	}
	if err := store.Remove(probe); err != nil {
		return check{Name: "state directory", Status: statusWarn, Detail: fmt.Sprintf("left %s behind: %v", probe, err)}
	}

	free, err := store.FreeBytes()
	if err != nil {
		return check{Name: "state directory", Status: statusWarn, Detail: err.Error()}
	}
	if config.Size(free) < MinFreeSpace {
		return check{
			Name: "state directory", Status: statusWarn,
			Detail: fmt.Sprintf("%s has %s free", store.Root(), config.Size(free).Human()),
			Remedy: fmt.Sprintf("A cached base image needs 2–3 GiB and overlays grow as guests write; %s free is recommended.", MinFreeSpace.Human()),
		}
	}
	return check{
		Name: "state directory", Status: statusPass,
		Detail: fmt.Sprintf("%s, %s free", store.Root(), config.Size(free).Human()),
	}
}

// checkStateDirTraversal reports whether the hypervisor can reach the state
// directory. Under a privileged libvirt, QEMU runs as its own user and must
// search every directory from / down to a VM's disk; the default state
// directory sits under a home directory, which is commonly 0700 or 0710. Only
// permission bits and ACLs are read, so this is safe under --dry-run.
func checkStateDirTraversal(app *App, cfg *config.Config) check {
	const name = "state directory access"

	if cfg.SessionMode() {
		return check{
			Name: name, Status: statusSkip,
			Detail: "not applicable: " + cfg.LibvirtURI + " runs QEMU as the invoking user",
		}
	}
	if cfg.RemoteHypervisor() {
		// The check reads the hypervisor's own passwd database and the
		// permission bits on every ancestor of the state directory. Both are
		// on the other machine, and a verdict from this machine's would be
		// confidently wrong.
		return check{
			Name: name, Status: statusSkip,
			Detail: "not checked for a remote hypervisor: it reads that host's accounts and directory permissions",
			Remedy: "Run `agent-vm doctor` on " + app.runner.HypervisorHost() +
				" if a VM fails to start with a permission error on its disk.",
		}
	}

	identity, err := app.hypervisorIdentity()
	if err != nil {
		return check{Name: name, Status: statusSkip, Detail: err.Error()}
	}
	if identity == nil {
		return check{
			Name: name, Status: statusSkip,
			Detail: "cannot determine which user this host runs QEMU as",
			Remedy: "Set `user` in " + qemuConfPath + " if a VM fails to start with a permission error on its disk.",
		}
	}

	blocker, err := firstUntraversable(cfg.StateDir, identity)
	if err != nil {
		return check{Name: name, Status: statusWarn, Detail: err.Error()}
	}
	if blocker != "" {
		return check{
			Name: name, Status: statusFail,
			Detail: fmt.Sprintf("QEMU runs as %s, which cannot search %s on the way to %s", identity.Name, blocker, cfg.StateDir),
			Remedy: fmt.Sprintf("Grant search permission without granting read: sudo setfacl -m u:%s:x %s. "+
				"Repeat for any other ancestor this check names. Alternatively move the state directory outside your home with --state-dir or the state_dir config key.",
				identity.Name, blocker),
		}
	}
	return check{
		Name: name, Status: statusPass,
		Detail: fmt.Sprintf("%s can reach %s", identity.Name, cfg.StateDir),
	}
}

// checkNATNetwork reports whether the NAT network is ready. A network that does
// not exist yet is not a failure: `create` defines it on demand.
func checkNATNetwork(ctx context.Context, app *App, cfg *config.Config, libvirtOK bool) check {
	name := "NAT network " + cfg.NATNetwork
	if !libvirtOK {
		return check{Name: name, Status: statusSkip, Detail: "skipped: no libvirt connection"}
	}

	uri, err := app.hypervisorURI()
	if err != nil {
		return check{Name: name, Status: statusFail, Detail: err.Error()}
	}
	defined, active, err := network.NATStatus(ctx, app.runner, uri, cfg.NATNetwork)
	switch {
	case err != nil:
		return check{Name: name, Status: statusFail, Detail: err.Error()}
	case defined && active:
		return check{Name: name, Status: statusPass, Detail: "defined and active"}
	case defined:
		return check{
			Name: name, Status: statusWarn, Detail: "defined but not active",
			Remedy: fmt.Sprintf("agent-vm will start it on the next create, or start it now with `virsh --connect %s net-start %s`.", cfg.LibvirtURI, cfg.NATNetwork),
		}
	default:
		return check{Name: name, Status: statusPass, Detail: "not defined yet; agent-vm will define it on the first create"}
	}
}

// natBridge is the bridge device libvirt allocated for the NAT network, for
// the checks that must name an interface. It answers "" rather than an error
// for every failure: the bridge only sharpens a firewall verdict, and a
// network that is not defined yet is the normal state of a fresh host.
func (a *App) natBridge(ctx context.Context, cfg *config.Config, libvirtOK bool) string {
	if !libvirtOK || cfg.BridgeMode() {
		return ""
	}
	uri, err := a.hypervisorURI()
	if err != nil {
		return ""
	}
	bridge, err := network.NATBridge(ctx, a.runner, uri, cfg.NATNetwork)
	if err != nil {
		return ""
	}
	return bridge
}

// checkBridge validates the configured host bridge, and reports the
// combinations that cannot work at all.
func checkBridge(ctx context.Context, app *App, cfg *config.Config) []check {
	if cfg.Bridge == "" {
		return nil
	}
	name := "host bridge " + cfg.Bridge

	if cfg.SessionMode() {
		return []check{{
			Name: name, Status: statusWarn,
			Detail: "bridged networking is not supported on " + cfg.LibvirtURI,
			Remedy: "Use --libvirt-uri qemu:///system for bridged networking. NAT mode works either way.",
		}}
	}

	warning, err := network.ValidateBridge(ctx, app.runner, cfg.Bridge)
	if err != nil {
		var berr *network.BridgeError
		if errors.As(err, &berr) {
			return []check{{
				Name: name, Status: statusFail,
				Detail: fmt.Sprintf("%s %s", cfg.Bridge, berr.Reason),
				Remedy: "Create or bring up the bridge with your network manager. agent-vm never modifies host network configuration.",
			}}
		}
		return []check{{Name: name, Status: statusFail, Detail: err.Error()}}
	}
	// A spanning tree forward delay is not a broken host — the guest boots and
	// is reachable — so it is a warning with the command that removes it,
	// rather than a refusal to use a bridge the operator asked for.
	if warning != nil {
		return []check{{
			Name: name, Status: statusWarn,
			Detail: warning.Detail(),
			Remedy: warning.Remedy(),
		}}
	}
	return []check{{Name: name, Status: statusPass, Detail: "exists and is up"}}
}

// applianceKernelSymbols are the kernel configuration options QEMU's `virt`
// board depends on. libguestfs builds its appliance around the host's own
// kernel, so a kernel built for one machine rather than for machines in
// general can be missing them — and when it is, the appliance boots to silence
// and every image build fails with libguestfs reporting only that the
// appliance "closed the connection unexpectedly". Diagnosing that from the
// failure takes a kernel config and a hand-run QEMU; diagnosing it here takes
// one line of doctor's output.
var applianceKernelSymbols = []struct {
	symbol   string
	provides string
}{
	{"CONFIG_SERIAL_AMBA_PL011", "the PL011 serial port the appliance's console is on"},
	{"CONFIG_PCI_HOST_GENERIC", "the PCIe host bridge the appliance's disks are behind"},
}

// applianceRemedy names the way out, and is the same whichever symbol is
// missing: give libguestfs a general-purpose kernel to build the appliance
// from. That kernel is never booted by the host, only inside QEMU, so it does
// not have to support the host's hardware at all.
const applianceRemedy = "The host kernel cannot boot a libguestfs appliance, so image builds will fail. " +
	"Unpack a general-purpose kernel package into a directory named after its version, " +
	"holding the kernel as \"Image\" and a depmod'd module tree as \"modules\", and set " +
	"appliance_kernel in config.toml (or AGENT_VM_APPLIANCE_KERNEL) to that directory. " +
	"See docs/host-setup.md, \"Hosts whose kernel cannot boot the appliance\"."

// checkAppliance reports whether libguestfs will be able to boot the appliance
// it does all of its work in.
func checkAppliance(ctx context.Context, app *App, cfg *config.Config) check {
	const name = "libguestfs appliance"
	if cfg.ApplianceKernel != "" {
		return checkApplianceKernelDir(ctx, app, name, cfg.ApplianceKernel)
	}

	// The symbols below are ARM-only, so on any other architecture their
	// absence says nothing. General-purpose kernels are also the only ones in
	// practical use there, which is why this check exists for aarch64 alone.
	arch, err := hostOutput(ctx, app, "uname", "-m")
	if err != nil {
		return check{Name: name, Status: statusSkip, Detail: "cannot determine the host architecture: " + err.Error()}
	}
	if arch != "aarch64" && arch != "arm64" {
		return check{Name: name, Status: statusPass, Detail: "the host kernel can boot the appliance"}
	}

	kernelConfig, err := hostKernelConfig(ctx, app)
	if err != nil {
		// A kernel that does not publish its configuration is not a broken
		// one, and guessing either way would be worse than saying so.
		return check{
			Name: name, Status: statusSkip,
			Detail: "cannot read the host kernel configuration: " + err.Error(),
			Remedy: "If image builds fail with \"the appliance closed the connection unexpectedly\", see " + applianceRemedy,
		}
	}

	missing := make([]string, 0, len(applianceKernelSymbols))
	for _, want := range applianceKernelSymbols {
		if !kernelConfigEnabled(kernelConfig, want.symbol) {
			missing = append(missing, want.symbol+" ("+want.provides+")")
		}
	}
	if len(missing) > 0 {
		return check{
			Name: name, Status: statusFail,
			Detail: "the host kernel is missing " + strings.Join(missing, " and "),
			Remedy: applianceRemedy,
		}
	}
	return check{Name: name, Status: statusPass, Detail: "the host kernel can boot the appliance"}
}

// checkApplianceKernelDir confirms the configured directory holds what
// supermin will be pointed at. A path that is merely wrong would otherwise
// surface as the same silent appliance failure it was configured to fix.
func checkApplianceKernelDir(ctx context.Context, app *App, name, dir string) check {
	kernel := filepath.Join(dir, hostexec.ApplianceKernelFile)
	modules := filepath.Join(dir, hostexec.ApplianceModulesDir)

	probe := func(args ...string) bool {
		_, err := app.runner.Run(ctx, hostexec.Command{Name: "test", Args: args, Effect: hostexec.Read})
		return err == nil
	}
	switch {
	case !probe("-f", kernel):
		return check{
			Name: name, Status: statusFail,
			Detail: "appliance_kernel is set but " + kernel + " is not a file",
			Remedy: "Put the kernel image at that path, or unset appliance_kernel to use the host's own kernel.",
		}
	case !probe("-f", filepath.Join(modules, "modules.dep")):
		return check{
			Name: name, Status: statusFail,
			Detail: "appliance_kernel is set but " + modules + " is not a module tree",
			Remedy: "Unpack the kernel's modules there and index them: depmod -b <the directory holding lib/modules> " +
				filepath.Base(dir),
		}
	}
	return check{
		Name: name, Status: statusPass,
		Detail: "libguestfs builds its appliance from the kernel in " + dir,
	}
}

// hostKernelConfig returns the hypervisor kernel's configuration, from
// whichever of the two places a distribution puts it.
func hostKernelConfig(ctx context.Context, app *App) (string, error) {
	if release, err := hostOutput(ctx, app, "uname", "-r"); err == nil {
		if out, err := hostOutput(ctx, app, "cat", "/boot/config-"+release); err == nil {
			return out, nil
		}
	}
	// /proc/config.gz is the other convention, and is compressed, which is why
	// this asks zcat rather than reading the file.
	return hostOutput(ctx, app, "zcat", "/proc/config.gz")
}

// hostOutput runs a read-only command on the hypervisor and returns its
// trimmed standard output. It goes through the runner rather than reading a
// file directly so that the answer describes the machine libvirt is on, which
// with a remote URI is not this one (ADR-0010).
func hostOutput(ctx context.Context, app *App, tool string, args ...string) (string, error) {
	res, err := app.runner.Run(ctx, hostexec.Command{Name: tool, Args: args, Effect: hostexec.Read})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// kernelConfigEnabled reports whether a symbol is built in or built as a
// module. Both boot an appliance; only "is not set" does not.
func kernelConfigEnabled(config, symbol string) bool {
	for _, line := range strings.Split(config, "\n") {
		switch strings.TrimSpace(line) {
		case symbol + "=y", symbol + "=m":
			return true
		}
	}
	return false
}
