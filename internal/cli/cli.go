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
	store    *state.Store
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

// parseNamed parses a command that takes exactly one VM name plus flags. Go's
// flag package stops at the first non-flag argument, so flags written after the
// name — the way docs/cli.md spells these commands — need a second pass over
// what is left.
func parseNamed(flags *flag.FlagSet, args []string, usage string) (string, error) {
	if err := flags.Parse(args); err != nil {
		return "", &ExitError{Code: ExitUsage, Err: err}
	}
	if flags.NArg() == 0 {
		return "", exitf(ExitUsage, "usage: %s", usage)
	}
	name := flags.Arg(0)

	if flags.NArg() > 1 {
		if err := flags.Parse(flags.Args()[1:]); err != nil {
			return "", &ExitError{Code: ExitUsage, Err: err}
		}
		if flags.NArg() != 0 {
			return "", exitf(ExitUsage, "usage: %s: unexpected argument %q", usage, flags.Arg(0))
		}
	}
	return name, nil
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
	err := a.run(ctx, args)
	if err == nil {
		return ExitOK
	}

	code := exitCodeFor(err)
	_, _ = fmt.Fprintf(a.Stderr, "agent-vm: %v\n", err)
	if code == ExitUsage {
		_, _ = fmt.Fprintf(a.Stderr, "Run `agent-vm --help` for usage.\n")
	}
	return code
}

func (a *App) run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("agent-vm", flag.ContinueOnError)
	flags.SetOutput(a.Stderr)
	flags.Usage = func() { a.printUsage() }

	flags.StringVar(&a.configFile, "config", "", "configuration file (default ~/.config/agent-vm/config.toml)")
	flags.StringVar(&a.stateDir, "state-dir", "", "root of all VM and image state (default ~/.local/share/agent-vm)")
	flags.StringVar(&a.libvirtURI, "libvirt-uri", "", "libvirt connection URI (default qemu:///system)")
	flags.StringVar(&a.format, "output", string(OutputText), "output format: text or json")
	flags.BoolVar(&a.verbose, "verbose", false, "debug-level logging to stderr")
	flags.BoolVar(&a.quiet, "quiet", false, "suppress progress output")
	flags.BoolVar(&a.assumeYes, "yes", false, "skip confirmation for destructive operations")
	flags.BoolVar(&a.dryRun, "dry-run", false, "print the tool invocations this would run, and exit without changing anything")
	flags.BoolVar(&a.showVer, "version", false, "print agent-vm and host tool versions")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &ExitError{Code: ExitUsage, Err: err}
	}

	format := OutputFormat(a.format)
	if format != OutputText && format != OutputJSON {
		return exitf(ExitUsage, "invalid --output %q: expected text or json", a.format)
	}
	a.out = &output{stdout: a.Stdout, stderr: a.Stderr, format: format, quiet: a.quiet}
	a.logger = a.newLogger()
	a.runner = a.newRunner()
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

// newRunner returns the process runner for this invocation. Under --dry-run,
// mutating commands are printed to stdout instead of being run, while read-only
// ones still execute so the printed plan reflects the real host.
func (a *App) newRunner() hostexec.Runner {
	runner := a.Runner
	if runner == nil {
		runner = hostexec.New(a.logger)
	}
	if a.dryRun {
		runner = hostexec.NewDryRun(runner, a.Stdout)
	}
	return runner
}

// Config resolves configuration on first use. Flags beat environment variables,
// which beat the configuration file, which beats the built-in defaults.
func (a *App) Config() (*config.Config, error) {
	return a.ConfigWith(config.Overrides{})
}

// ConfigWith resolves configuration with a command's own flags applied on top
// of the global ones. Commands that take resource or network flags use it;
// everything else uses Config.
func (a *App) ConfigWith(overrides config.Overrides) (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
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

// Store opens the state directory on first use.
func (a *App) Store() (*state.Store, error) {
	if a.store != nil {
		return a.store, nil
	}
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	open := state.Open
	if a.dryRun {
		// Creating the state directory tree is a change to the host, and a dry
		// run makes none.
		open = state.OpenExisting
	}
	store, err := open(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	a.store = store
	return store, nil
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
  --libvirt-uri <uri>    libvirt connection URI (default qemu:///system)
  --output <text|json>   output format (default text)
  --verbose              debug-level logging to stderr
  --quiet                suppress progress output
  --yes                  skip confirmation for destructive operations
  --dry-run              print the tool invocations this would run, without running them
  --version              print agent-vm and host tool versions

The full command-line contract is documented in docs/cli.md.
`)
}
