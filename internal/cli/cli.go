// Package cli implements the agent-vm subcommands. It orchestrates: it
// resolves configuration, calls the packages that own each concern, decides
// what the operator sees, and maps failures to the documented exit codes. It
// does not render XML, build argument vectors, or spawn processes itself.
//
// docs/cli.md is the specification this package must satisfy. When the two
// disagree, one of them is a bug and they are fixed together.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

// Version is the agent-vm version, set at build time by scripts/build-release.sh.
var Version = "0.1.0-dev"

// App is one invocation of the CLI.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	// Stdin is read only to confirm a destructive operation. When it is nil,
	// such an operation refuses instead of assuming consent.
	Stdin io.Reader
	Env   config.Environ
	// Runner substitutes the process runner. It is the seam tests use to stand
	// in for the host tools; in a real run it is nil and the real one is built.
	Runner hostexec.Runner
	// HypervisorIdentity substitutes the lookup of the user a privileged
	// libvirt runs QEMU as. It is the seam tests use in place of the host's
	// passwd database; in a real run it is nil and the host is consulted.
	HypervisorIdentity func() (*hypervisorIdentity, error)
	// StateFS substitutes the filesystem the state directory lives on. It is
	// the seam that lets a test exercise a remote-hypervisor run against a
	// real directory on this disk; in a real run it is nil and the machine the
	// libvirt URI names is used.
	StateFS state.FS

	// globals, populated from the global flags.
	configFile string
	stateDir   string
	libvirtURI string
	format     string
	verbose    bool
	quiet      bool
	assumeYes  bool
	dryRun     bool
	showVer    bool

	out      *output
	logger   *slog.Logger
	runner   hostexec.Runner
	versions *hostexec.Versions
	cfg      *config.Config
	conn     *config.Connection
	store    *state.Store
	// cleanup holds what the run has to undo before it exits — currently only
	// the directory holding the ssh connection-sharing socket.
	cleanup []func()
}

// command is one subcommand. Flags are registered per command so that
// `agent-vm <command> --help` shows exactly that command's flags.
type command struct {
	name    string
	summary string
	usage   string
	// hidden keeps a command out of the usage listing. It is for interfaces
	// meant for other programs — the shell completion helper — not for
	// commands an operator is expected to type.
	hidden bool
	run    func(ctx context.Context, app *App, args []string) error
}

func commands() map[string]*command {
	list := []*command{
		doctorCommand(),
		imageCommand(),
		createCommand(),
		listCommand(),
		infoCommand(),
		startCommand(),
		stopCommand(),
		restartCommand(),
		sshCommand(),
		updateCommand(),
		consoleCommand(),
		destroyCommand(),
		completionCommand(),
		completeCommand(),
	}

	byName := make(map[string]*command, len(list))
	for _, c := range list {
		byName[c.name] = c
	}
	return byName
}

// errUsagePrinted reports that the operator asked a command for its usage and
// got it. Nothing failed, so it exits 0; it is an error only so that a command
// can stop parsing and return.
var errUsagePrinted = errors.New("usage printed")

// flagSet is one subcommand's flags.
//
// Go's flag package spells a flag with a single dash, prints its own message
// when parsing fails, and then prints "Usage of <name>:". docs/cli.md, the
// help text, and every example spell flags with two dashes, and App.Main is
// what reports an error to the operator. So the flag package is kept silent
// here and this package renders both the message and the usage listing
// itself — otherwise `agent-vm image build --test` answers with "flag
// provided but not defined: -test", twice, in a spelling the tool does not
// accept.
type flagSet struct {
	*flag.FlagSet
	// usage is the documented invocation this command answers to — the same
	// string its command.usage field carries.
	usage string
	out   io.Writer
}

// newFlagSet builds the flag set for one subcommand. name is the command as an
// operator types it ("image build"), so that a usage error can point at that
// command's own --help rather than the global one.
func newFlagSet(name, usage string, out io.Writer) *flagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	return &flagSet{FlagSet: flags, usage: usage, out: out}
}

// parse parses a command that takes flags and no positional arguments.
func (f *flagSet) parse(args []string) error {
	if err := f.Parse(args); err != nil {
		return f.fail(err)
	}
	return nil
}

// parseNamed parses a command that takes exactly one name plus flags — a VM
// name for most commands, a distro reference for the image ones. Go's flag
// package stops at the first non-flag argument, so flags written after the
// name — the way docs/cli.md spells these commands — need a second pass over
// what is left.
//
// Every subcommand with both a name and flags of its own must go through here.
// Parsing directly leaves `agent-vm <verb> <name> --flag` failing with "flag
// provided but not defined", which is the documented spelling and the one
// operators reach for.
func (f *flagSet) parseNamed(args []string) (string, error) {
	if err := f.Parse(args); err != nil {
		return "", f.fail(err)
	}
	if f.NArg() == 0 {
		return "", f.usagef("usage: %s", f.usage)
	}
	name := f.Arg(0)

	if f.NArg() > 1 {
		if err := f.Parse(f.Args()[1:]); err != nil {
			return "", f.fail(err)
		}
		if f.NArg() != 0 {
			return "", f.usagef("usage: %s: unexpected argument %q", f.usage, f.Arg(0))
		}
	}
	return name, nil
}

// parseNames parses a command that takes any number of names plus flags,
// with the same tolerance for flags written after a name that parseNamed has.
func (f *flagSet) parseNames(args []string) ([]string, error) {
	if err := f.Parse(args); err != nil {
		return nil, f.fail(err)
	}

	names := []string{}
	for f.NArg() > 0 {
		names = append(names, f.Arg(0))
		if err := f.Parse(f.Args()[1:]); err != nil {
			return nil, f.fail(err)
		}
	}
	return names, nil
}

// fail turns a parse failure into what the operator sees: the usage listing
// when help was asked for, and otherwise a usage error spelled the way this
// tool accepts flags, pointing at this command's help rather than the global
// one.
func (f *flagSet) fail(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		f.printUsage()
		return errUsagePrinted
	}
	return &ExitError{Code: ExitUsage, Err: respellFlags(err), Help: f.helpCommand()}
}

// usagef builds a usage error for a wrong argument rather than a wrong flag,
// pointing at the same help.
func (f *flagSet) usagef(format string, args ...any) error {
	return &ExitError{Code: ExitUsage, Err: fmt.Errorf(format, args...), Help: f.helpCommand()}
}

func (f *flagSet) helpCommand() string { return "agent-vm " + f.Name() }

// printUsage prints the command's documented invocation and its own flags,
// spelled with the two dashes the tool accepts.
func (f *flagSet) printUsage() {
	_, _ = fmt.Fprintf(f.out, "Usage:\n  %s\n", f.usage)

	// VisitAll yields a flag set's flags in lexical order, which is the order
	// a listing wants.
	var names, help []string
	f.VisitAll(func(fl *flag.Flag) {
		valueName, usage := flag.UnquoteUsage(fl)
		spelled := "--" + fl.Name
		if valueName != "" {
			spelled += " <" + valueName + ">"
		}
		names = append(names, spelled)
		help = append(help, usage)
	})
	if len(names) == 0 {
		return
	}

	width := 0
	for _, name := range names {
		if len(name) > width {
			width = len(name)
		}
	}
	_, _ = fmt.Fprint(f.out, "\nFlags:\n")
	for i, name := range names {
		_, _ = fmt.Fprintf(f.out, "  %-*s  %s\n", width, name, help[i])
	}
}

// goFlagSpelling matches how the flag package writes a flag name in its own
// error messages — "…: -name", "… for -name", "… for flag -name" — which is
// the one spelling docs/cli.md never uses. The value in an "invalid value"
// message is quoted, so a leading dash inside it is not matched.
var goFlagSpelling = regexp.MustCompile(`(: |for (?:flag )?)-(\w)`)

// respellFlags rewrites the flag package's single-dash flag names as the
// double-dash ones this tool documents and accepts, so an operator is not told
// about a flag in a spelling they did not type and cannot use.
func respellFlags(err error) error {
	msg := err.Error()
	respelled := goFlagSpelling.ReplaceAllString(msg, "${1}--${2}")
	if respelled == msg {
		return err
	}
	return errors.New(respelled)
}

// Run parses global flags, dispatches to a subcommand, and returns the process
// exit code. It never panics out to the caller: every failure becomes a message
// on stderr and a documented exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	app := &App{Stdout: stdout, Stderr: stderr, Stdin: stdin, Env: os.Getenv}
	return app.Main(ctx, args)
}

// Main runs one invocation of an already-constructed App and returns its exit
// code. Reporting lives here rather than in Run so that a test exercises the
// same path an operator sees, message and code together.
func (a *App) Main(ctx context.Context, args []string) int {
	defer a.finish()
	err := a.run(ctx, args)
	// A command asked for its usage and printed it. Nothing failed.
	if err == nil || errors.Is(err, errUsagePrinted) {
		return ExitOK
	}

	code := exitCodeFor(err)
	_, _ = fmt.Fprintf(a.Stderr, "agent-vm: %v\n", err)
	if code == ExitUsage {
		_, _ = fmt.Fprintf(a.Stderr, "Run `%s --help` for usage.\n", helpCommandFor(err))
	}
	return code
}

func (a *App) run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("agent-vm", flag.ContinueOnError)
	// The flag package prints nothing for itself: a bad global flag is
	// reported once, by Main, and --help prints the usage below.
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}

	flags.StringVar(&a.configFile, "config", "", "configuration file (default ~/.config/agent-vm/config.toml)")
	flags.StringVar(&a.stateDir, "state-dir", "", "root of all VM and image state (default ~/.local/share/agent-vm)")
	flags.StringVar(&a.libvirtURI, "libvirt-uri", "", "libvirt connection URI; qemu+ssh://user@host/system runs everything on that host (default qemu:///system)")
	flags.StringVar(&a.format, "output", string(OutputText), "output format: text or json")
	flags.BoolVar(&a.verbose, "verbose", false, "debug-level logging to stderr")
	flags.BoolVar(&a.quiet, "quiet", false, "suppress progress output")
	flags.BoolVar(&a.assumeYes, "yes", false, "skip confirmation for destructive operations")
	flags.BoolVar(&a.dryRun, "dry-run", false, "print the tool invocations this would run, and exit without changing anything")
	flags.BoolVar(&a.showVer, "version", false, "print agent-vm and host tool versions")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.printUsage()
			return nil
		}
		return &ExitError{Code: ExitUsage, Err: respellFlags(err)}
	}

	format := OutputFormat(a.format)
	if format != OutputText && format != OutputJSON {
		return exitf(ExitUsage, "invalid --output %q: expected text or json", a.format)
	}
	a.out = &output{stdout: a.Stdout, stderr: a.Stderr, format: format, quiet: a.quiet}
	a.logger = a.newLogger()
	if err := a.newRunner(); err != nil {
		return err
	}
	a.versions = hostexec.NewVersions(a.runner)

	if a.showVer {
		return a.printVersions(ctx)
	}

	rest := flags.Args()
	if len(rest) == 0 {
		a.printUsage()
		return &ExitError{Code: ExitUsage, Err: errors.New("no command given")}
	}

	cmd, ok := commands()[rest[0]]
	if !ok {
		return exitf(ExitUsage, "unknown command %q", rest[0])
	}
	return cmd.run(ctx, a, rest[1:])
}

func (a *App) newLogger() *slog.Logger {
	level := slog.LevelInfo
	if a.verbose {
		level = slog.LevelDebug
	}
	if a.quiet {
		level = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(a.Stderr, &slog.HandlerOptions{Level: level}))
}

// newRunner builds the process runner for this invocation.
//
// Three decisions are layered here, innermost first. Processes are spawned by
// hostexec.Exec. Under --dry-run, mutating commands are printed to stdout
// instead of being run, while read-only ones still execute so the printed plan
// reflects the real host. And when the libvirt URI names another machine, every
// hypervisor-located command is wrapped in ssh, so what --dry-run prints is the
// ssh invocation that would actually run — which is what an operator debugging
// a remote host needs to see.
func (a *App) newRunner() error {
	runner := a.Runner
	if runner == nil {
		runner = hostexec.New(a.logger)
	}
	if a.dryRun {
		runner = hostexec.NewDryRun(runner, a.Stdout)
	}

	// A substituted Runner is a test standing in for the whole process
	// boundary, including the transport, so it is never wrapped.
	if a.Runner != nil {
		a.runner = a.withApplianceKernel(runner)
		return nil
	}

	conn, err := a.Connection()
	if err != nil {
		// A configuration this bad cannot say where the hypervisor is, so the
		// local runner stands in. Nothing is lost: every command resolves the
		// same configuration and reports the same error as a usage failure
		// before it runs anything.
		a.runner = runner
		return nil
	}
	if !conn.Remote {
		a.runner = a.withApplianceKernel(runner)
		return nil
	}

	dir, err := hostexec.ControlDir()
	if err != nil {
		return err
	}
	a.cleanup = append(a.cleanup, func() { _ = os.RemoveAll(dir) })

	remote := hostexec.NewRemote(runner, a.logger, conn.SSHDestination, dir)
	remote.Port = conn.SSHPort
	remote.IdentityFile = conn.IdentityFile
	remote.NoVerify = conn.NoVerify
	a.runner = a.withApplianceKernel(remote)
	return nil
}

// withApplianceKernel adds the configured appliance kernel to libguestfs
// invocations. It wraps outermost so that the transport underneath renders the
// environment into the remote command line, and so that --dry-run prints the
// libguestfs commands exactly as they would run.
func (a *App) withApplianceKernel(runner hostexec.Runner) hostexec.Runner {
	return hostexec.NewApplianceKernel(runner, func() string {
		// Resolved when a libguestfs command is actually built, never here:
		// the runner exists before flags are parsed, and resolving
		// configuration this early would cache it without the running
		// command's own flags applied.
		cfg, err := a.Config()
		if err != nil {
			// An unresolvable configuration is reported as a usage error by
			// the command itself, before it runs anything.
			return ""
		}
		return cfg.ApplianceKernel
	})
}

// become replaces this process with cmd, after releasing what the run holds.
//
// The release has to happen first: replacing the process image discards every
// deferred cleanup with it, so an `agent-vm ssh` or `agent-vm console` that
// left it to finish would orphan the ssh control socket's directory every time
// it ran.
func (a *App) become(cmd hostexec.Command) error {
	a.finish()
	return a.runner.Become(cmd)
}

// finish releases what the run holds. It runs on every exit path, including a
// failing one, because the ssh control socket outlives this process otherwise.
func (a *App) finish() {
	for i := len(a.cleanup) - 1; i >= 0; i-- {
		a.cleanup[i]()
	}
	a.cleanup = nil
}

// Connection resolves the libvirt URI on first use. It is separate from Config
// because the runner has to be built before any command runs, and the runner
// depends on whether the hypervisor is this machine.
func (a *App) Connection() (*config.Connection, error) {
	if a.conn != nil {
		return a.conn, nil
	}
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	conn, err := cfg.Connection()
	if err != nil {
		return nil, err
	}
	a.conn = conn
	return conn, nil
}

// remoteStateDir resolves where the state directory is on the hypervisor.
//
// The built-in default is a path under a home directory, and this machine's
// home directory says nothing about the account agent-vm logs in as over there
// — the usernames need not even match. So when nothing named a state directory
// explicitly, the hypervisor is asked for its own: an ssh session starts in the
// home directory of the account it logged in as, which makes `pwd` the answer.
//
// A state directory the operator did name is used verbatim, because they were
// naming a path on that host.
func (a *App) remoteStateDir(cfg *config.Config) (string, error) {
	if !cfg.StateDirIsDefault || a.StateFS != nil {
		return cfg.StateDir, nil
	}

	res, err := a.runner.Run(context.Background(), hostexec.Command{Name: "pwd", Effect: hostexec.Read})
	if err != nil {
		return "", fmt.Errorf("asking %s where its home directory is, to place the default state directory: %w",
			a.runner.HypervisorHost(), err)
	}
	home := strings.TrimSpace(string(res.Stdout))
	if !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("%s reported %q as its home directory, which is not an absolute path;\n"+
			"  name the state directory explicitly with --state-dir or the state_dir config key",
			a.runner.HypervisorHost(), home)
	}
	return filepath.Join(home, config.DefaultStateDirSuffix), nil
}

// sshJump is the machine a guest connection is made through, or "" when the
// guest is reachable from here.
//
// A NAT guest sits on a bridge that exists only on the hypervisor, so from
// another machine there is no route to it at all. Rather than tunnel or
// forward a port, the connection is made through the hypervisor the way ssh
// already knows how: -J, with the same destination agent-vm runs host tools on.
func (a *App) sshJump() string {
	conn, err := a.Connection()
	if err != nil || !conn.Remote {
		return ""
	}
	if conn.SSHPort != 0 {
		return fmt.Sprintf("%s:%d", conn.SSHDestination, conn.SSHPort)
	}
	return conn.SSHDestination
}

// hypervisorURI is the libvirt URI as virsh and virt-install see it. They run
// on the hypervisor, so a remote URI would send them back over ssh to the
// machine they are already on (ADR-0010).
func (a *App) hypervisorURI() (string, error) {
	conn, err := a.Connection()
	if err != nil {
		return "", err
	}
	return conn.HypervisorURI(), nil
}

// Config resolves configuration on first use. Flags beat environment variables,
// which beat the configuration file, which beats the built-in defaults.
func (a *App) Config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	return a.ConfigWith(config.Overrides{})
}

// ConfigWith resolves configuration with a command's own flags applied on top
// of the global ones. Commands that take resource or network flags use it;
// everything else uses Config.
//
// It always resolves rather than returning what Config may already have
// cached, and replaces the cache with the result. Setting up a run reads
// configuration before any subcommand does — deciding which machine the
// hypervisor is on needs the libvirt URI — so a cache returned here would be
// one resolved without this command's own flags, and every `create` flag would
// be silently ignored.
func (a *App) ConfigWith(overrides config.Overrides) (*config.Config, error) {
	overrides.ConfigFile = a.configFile
	overrides.StateDir = a.stateDir
	overrides.LibvirtURI = a.libvirtURI

	cfg, err := config.Load(a.Env, overrides)
	if err != nil {
		return nil, err
	}
	a.cfg = cfg
	return cfg, nil
}

// Store opens the state directory on first use, creating its tree unless this
// is a dry run.
func (a *App) Store() (*state.Store, error) {
	if a.store != nil {
		return a.store, nil
	}
	// Creating the state directory tree is a change to the host, and a dry run
	// makes none.
	store, err := a.openStore(!a.dryRun)
	if err != nil {
		return nil, err
	}
	a.store = store
	return store, nil
}

// openStore resolves where the state directory is and opens it there.
func (a *App) openStore(create bool) (*state.Store, error) {
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	conn, err := a.Connection()
	if err != nil {
		return nil, err
	}
	// The state directory is on the machine the hypervisor is on: everything
	// in it is something QEMU has to open (ADR-0010).
	fsys := state.Local()
	dir := cfg.StateDir
	if conn.Remote {
		fsys = state.Remote(a.runner)
		if dir, err = a.remoteStateDir(cfg); err != nil {
			return nil, err
		}
	}
	if a.StateFS != nil {
		fsys = a.StateFS
	}
	return state.OpenOn(fsys, dir, create)
}

// printVersions implements --version: this tool, plus the detected versions of
// the tools it delegates to. A tool that is missing or unreadable is reported
// as such rather than omitted.
func (a *App) printVersions(ctx context.Context) error {
	type toolVersion struct {
		Tool    string `json:"tool"`
		Version string `json:"version"`
		Error   string `json:"error,omitempty"`
	}
	result := struct {
		AgentVM string        `json:"agentVm"`
		Tools   []toolVersion `json:"tools"`
	}{AgentVM: Version}

	for _, tool := range hostexec.RequiredTools() {
		version, err := a.versions.Get(ctx, tool)
		entry := toolVersion{Tool: tool.Name, Version: version.String()}
		if err != nil {
			entry.Version = ""
			entry.Error = err.Error()
		}
		result.Tools = append(result.Tools, entry)
	}

	if a.out.format == OutputJSON {
		return a.out.JSON(result)
	}

	a.out.Printf("agent-vm %s\n", result.AgentVM)
	rows := [][]string{}
	for _, tool := range result.Tools {
		value := tool.Version
		if tool.Error != "" {
			value = "not available"
		}
		rows = append(rows, []string{"  " + tool.Tool, value})
	}
	a.out.Table(rows)
	return nil
}

func (a *App) printUsage() {
	_, _ = fmt.Fprintf(a.Stderr, `agent-vm — disposable QEMU/KVM virtual machines for AI coding agents

Usage:
  agent-vm [global flags] <command> [arguments] [flags]

Commands:
`)

	byName := commands()
	names := make([]string, 0, len(byName))
	for name, cmd := range byName {
		if cmd.hidden {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	w := new(strings.Builder)
	for _, name := range names {
		fmt.Fprintf(w, "  %-10s %s\n", name, byName[name].summary)
	}
	_, _ = fmt.Fprint(a.Stderr, w.String())

	_, _ = fmt.Fprintf(a.Stderr, `
Global flags:
  --config <path>        configuration file (default ~/.config/agent-vm/config.toml)
  --state-dir <path>     root of all VM and image state (default ~/.local/share/agent-vm)
  --libvirt-uri <uri>    libvirt connection URI (default qemu:///system).
                         qemu+ssh://user@host/system drives a hypervisor on
                         another machine: base images, disks, and the state
                         directory all live there, and guests are reached
                         through it.
  --output <text|json>   output format (default text)
  --verbose              debug-level logging to stderr
  --quiet                suppress progress output
  --yes                  skip confirmation for destructive operations
  --dry-run              print the tool invocations this would run, without running them
  --version              print agent-vm and host tool versions
  --help                 print this message; after a command, that command's flags

The full command-line contract is documented in docs/cli.md.
`)
}
