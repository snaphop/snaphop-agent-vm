package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/hostsetup"
)

func setupCommand() *command {
	return &command{
		name:    "setup",
		summary: "prepare this host to run agent-vm",
		usage:   "agent-vm setup [--github]",
		run:     runSetup,
	}
}

func runSetup(ctx context.Context, app *App, args []string) error {
	flags := newFlagSet("setup", "agent-vm setup [--github]", app.Stderr)
	github := flags.Bool("github", false, "also install the GitHub CLI")
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return flags.usagef("setup takes no arguments, got %q", flags.Arg(0))
	}

	cfg, err := app.Config()
	if err != nil {
		return err
	}
	conn, err := app.Connection()
	if err != nil {
		return err
	}
	stateDir := cfg.StateDir
	if conn.Remote {
		stateDir, err = app.remoteStateDir(cfg)
		if err != nil {
			return err
		}
	}

	host, err := hostsetup.Detect(ctx, app.runner)
	if err != nil {
		return setupInputError(err)
	}

	groups := requiredGroups(cfg)
	// gh runs on the client. On a remote hypervisor the package install would
	// put it on the machine that does not call GitHub.
	installGitHub := *github && !conn.Remote
	if *github && conn.Remote {
		app.out.Warn("gh runs on this machine, not on %s. Install it here; setup does not install it on the hypervisor.\n", conn.SSHDestination)
	}
	if !app.dryRun {
		confirmed, err := app.confirm(setupPrompt(host, groups, installGitHub))
		if err != nil {
			return err
		}
		if !confirmed {
			app.out.Progress("Cancelled.\n")
			return nil
		}
	}

	stream := app.Stderr
	if app.quiet {
		stream = nil
	}
	report, err := hostsetup.Apply(ctx, app.runner, host, hostsetup.Options{
		StateDir: stateDir,
		Groups:   groups,
		GitHub:   installGitHub,
		Session:  cfg.SessionMode(),
		Output:   stream,
		Progress: func(msg string) { app.out.Progress("%s\n", msg) },
	})
	if err != nil {
		return setupInputError(err)
	}
	return app.renderSetup(report)
}

func setupPrompt(host hostsetup.Host, groups []string, github bool) string {
	extra := ""
	if github {
		extra = " It also installs the GitHub CLI."
	}
	return fmt.Sprintf(
		"Set up this %s (%s) host to run agent-vm? This installs QEMU, libvirt, podman, and libguestfs, starts libvirt, and adds %s to %s.%s",
		host.Name, host.Arch, host.User, groupPhrase(groups), extra,
	)
}

func groupPhrase(groups []string) string {
	switch len(groups) {
	case 0:
		return "no groups"
	case 1:
		return "the " + groups[0] + " group"
	default:
		return "the " + strings.Join(groups[:len(groups)-1], ", ") + " and " + groups[len(groups)-1] + " groups"
	}
}

func setupInputError(err error) error {
	var (
		unsupported *hostsetup.UnsupportedOSError
		arch        *hostsetup.UnsupportedArchError
		path        *hostsetup.PathError
	)
	if errors.As(err, &unsupported) || errors.As(err, &arch) || errors.As(err, &path) {
		return exitf(ExitUsage, "%s", err.Error())
	}
	return err
}

func (a *App) renderSetup(report *hostsetup.Report) error {
	if a.out.format == OutputJSON {
		return a.out.JSON(report)
	}
	if report.UnitsAssumed {
		a.out.Progress("libvirt units are chosen after the packages install. This plan enables libvirtd; virtqemud is used when libvirtd is absent.\n")
	}
	if report.SearchNote != "" {
		a.out.Warn("%s\n", report.SearchNote)
	}
	if a.dryRun {
		return nil
	}

	a.out.Printf("Set up %s (%s) for agent-vm.\n", report.Name, report.Arch)
	a.out.Printf("  packages  %s\n", strings.Join(report.Packages, " "))
	a.out.Printf("  libvirt   %s\n", strings.Join(report.Units, " "))
	if len(report.Groups) > 0 {
		a.out.Printf("  groups    %s is in %s\n", report.User, strings.Join(report.Groups, ", "))
	}
	for _, grant := range report.Search {
		a.out.Printf("  access    %s can search %s\n", grant.User, grant.Path)
	}
	if report.Relogin {
		a.out.Printf("\nStart a new login so the new groups apply, then run `agent-vm doctor`.\n")
	} else {
		a.out.Printf("\nRun `agent-vm doctor` to confirm this host.\n")
	}
	a.out.Printf("Firewall rules, bridges, and host confinement were not changed. See docs/host-setup.md.\n")
	return nil
}
