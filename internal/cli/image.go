package cli

import (
	"bufio"
	"context"
	"fmt"
	"sort"
	"strings"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/image"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/image/distro"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/progress"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

func imageCommand() *command {
	return &command{
		name:    "image",
		summary: "build, list, inspect, and remove cached base images",
		usage:   "agent-vm image <build|list|inspect|rm> [arguments]",
		run:     runImage,
	}
}

func runImage(ctx context.Context, app *App, args []string) error {
	if len(args) == 0 {
		return exitf(ExitUsage, "image needs a subcommand: build, list, inspect, or rm")
	}

	switch args[0] {
	case "build":
		return runImageBuild(ctx, app, args[1:])
	case "list", "ls":
		return runImageList(ctx, app, args[1:])
	case "inspect":
		return runImageInspect(ctx, app, args[1:])
	case "rm":
		return runImageRemove(ctx, app, args[1:])
	default:
		return exitf(ExitUsage, "unknown image subcommand %q: expected build, list, inspect, or rm", args[0])
	}
}

// builder wires up the image builder for this invocation.
func (a *App) builder() (*image.Builder, error) {
	store, err := a.Store()
	if err != nil {
		return nil, err
	}
	return &image.Builder{
		Runner:         a.runner,
		Versions:       a.versions,
		Store:          store,
		Logger:         a.logger,
		AgentVMVersion: Version,
		Progress:       a.buildProgress(),
	}, nil
}

// buildProgress returns what an image build reports its steps to. A build is
// several minutes of other programs working silently, so the operator gets a
// bar rather than a still terminal — on stderr, with the rest of the progress
// output (docs/cli.md).
func (a *App) buildProgress() image.Progress {
	// --quiet asked for no progress, and --dry-run runs no build to report on.
	if a.quiet || a.dryRun {
		return nil
	}
	// --verbose is already logging to stderr; a line redrawn in place would be
	// overwritten by the next log record, so those runs get plain step lines.
	return progress.NewBar(a.Stderr, !a.verbose)
}

func runImageBuild(ctx context.Context, app *App, args []string) error {
	flags := newFlagSet("image build", "agent-vm image build <distro>[-slim][:<tag>] [flags]", app.Stderr)
	from := flags.String("from", "", "override the source OCI reference")
	platform := flags.String("platform", "", "image platform to pull (default: the host platform)")
	force := flags.Bool("force", false, "rebuild even if a cached image already exists")
	name, err := flags.parseNamed(args)
	if err != nil {
		return err
	}

	ref, err := distro.ParseRef(name)
	if err != nil {
		return &config.ValidationError{Field: "distro", Value: name, Err: err}
	}
	opts := image.BuildOptions{
		Ref:      ref,
		From:     *from,
		Platform: *platform,
		Force:    *force,
	}

	// A build's later steps read what its earlier steps wrote, so --dry-run
	// prints the pipeline rather than executing half of it — and, importantly,
	// creates nothing in the state directory.
	if app.dryRun {
		return app.printBuildPlan(opts)
	}

	builder, err := app.builder()
	if err != nil {
		return err
	}

	app.out.Progress("Building base image %s from %s\n", ref, sourceOf(ref, *from))
	manifest, err := builder.Build(ctx, opts)
	if err != nil {
		return err
	}

	if app.out.format == OutputJSON {
		return app.out.JSON(manifest)
	}
	app.out.Printf("Built %s\n", manifest.Ref())
	app.out.Printf("  source  %s\n  digest  %s\n  kernel  %s\n  cmdline %s\n",
		manifest.SourceRef, manifest.SourceDigest, manifest.KernelVersion, manifest.KernelCmdline)
	return nil
}

// printBuildPlan prints the invocations and file operations a build would
// perform. Values that only exist once the build has run — the source digest,
// the kernel file name — appear as placeholders rather than as guesses.
func (a *App) printBuildPlan(opts image.BuildOptions) error {
	// The store, not the configured path, because a remote hypervisor's
	// default state directory sits under *its* home directory.
	store, err := a.Store()
	if err != nil {
		return err
	}
	layout := store.Layout

	for _, cmd := range image.Plan(layout, opts) {
		// Rendered through the runner so that a plan for a remote hypervisor
		// shows the ssh invocations that would really run (ADR-0010).
		a.out.Printf("%s\n", a.runner.Render(cmd))
	}
	for _, note := range image.PlanNotes(layout, opts.Ref) {
		a.out.Printf("# %s\n", note)
	}
	return nil
}

func sourceOf(ref distro.Ref, from string) string {
	if from != "" {
		return from
	}
	return ref.SourceRef()
}

func runImageList(_ context.Context, app *App, args []string) error {
	flags := newFlagSet("image list", "agent-vm image list", app.Stderr)
	if err := flags.parse(args); err != nil {
		return err
	}

	store, err := app.Store()
	if err != nil {
		return err
	}
	manifests, err := store.ListImages()
	if err != nil {
		return err
	}

	if app.out.format == OutputJSON {
		if manifests == nil {
			manifests = []*state.Manifest{}
		}
		return app.out.JSON(manifests)
	}

	if len(manifests) == 0 {
		app.out.Printf("No base images cached. Build one with `agent-vm image build %s`.\n", distro.Default.Name)
		return nil
	}

	rows := [][]string{{"IMAGE", "SOURCE DIGEST", "KERNEL", "SIZE", "BUILT"}}
	for _, m := range manifests {
		size, err := store.DiskUsage(m.Distro, m.Tag)
		if err != nil {
			return err
		}
		rows = append(rows, []string{
			m.Ref(),
			shortDigest(m.SourceDigest),
			m.KernelVersion,
			size.Human(),
			m.BuiltAt.Local().Format("2006-01-02 15:04"),
		})
	}
	app.out.Table(rows)
	return nil
}

// shortDigest trims a digest for a table. The full value is always available
// from `image inspect` and the manifest, which are what provenance questions
// should be answered from.
func shortDigest(digest string) string {
	if len(digest) > 19 {
		return digest[:19]
	}
	return digest
}

func runImageInspect(_ context.Context, app *App, args []string) error {
	flags := newFlagSet("image inspect", "agent-vm image inspect <distro>[:<tag>]", app.Stderr)
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.usagef("missing argument")
	}

	ref, err := distro.ParseRef(flags.Arg(0))
	if err != nil {
		return &config.ValidationError{Field: "distro", Value: flags.Arg(0), Err: err}
	}
	store, err := app.Store()
	if err != nil {
		return err
	}
	manifest, err := store.LoadManifest(ref.ImageName(), ref.Tag)
	if err != nil {
		return err
	}

	if app.out.format == OutputJSON {
		return app.out.JSON(manifest)
	}

	rows := [][]string{
		{"image", manifest.Ref()},
		{"source", manifest.SourceRef},
		{"digest", manifest.SourceDigest},
		{"platform", manifest.Platform},
		{"kernel", manifest.KernelVersion},
		{"cmdline", manifest.KernelCmdline},
		{"built", manifest.BuiltAt.Local().Format("2006-01-02 15:04:05")},
		{"built by", "agent-vm " + manifest.AgentVMVersion},
		{"disk", config.Size(manifest.BaseDiskBytes).Human()},
		{"base disk", store.BaseDiskPath(manifest.Distro, manifest.Tag)},
		{"kernel path", store.KernelPath(manifest.Distro, manifest.Tag)},
		{"initrd path", store.InitrdPath(manifest.Distro, manifest.Tag)},
	}
	for _, tool := range sortedKeys(manifest.ToolVersions) {
		rows = append(rows, []string{"built with " + tool, manifest.ToolVersions[tool]})
	}
	app.out.Table(rows)
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func runImageRemove(ctx context.Context, app *App, args []string) error {
	flags := newFlagSet("image rm", "agent-vm image rm <distro>[:<tag>] [--force]", app.Stderr)
	force := flags.Bool("force", false, "remove even while VMs still use it as a backing file; their disks become unreadable")
	name, err := flags.parseNamed(args)
	if err != nil {
		return err
	}

	ref, err := distro.ParseRef(name)
	if err != nil {
		return &config.ValidationError{Field: "distro", Value: name, Err: err}
	}
	builder, err := app.builder()
	if err != nil {
		return err
	}

	if app.dryRun {
		store, err := app.Store()
		if err != nil {
			return err
		}
		app.out.Printf("# remove the directory %s\n", store.ImageDir(ref.ImageName(), ref.Tag))
		return nil
	}

	prompt := fmt.Sprintf("Remove cached base image %s?", ref)
	if *force {
		prompt = fmt.Sprintf("Remove cached base image %s even if VMs still depend on it? Their disks become unreadable.", ref)
	}
	confirmed, err := app.confirm(prompt)
	if err != nil {
		return err
	}
	if !confirmed {
		app.out.Progress("Cancelled.\n")
		return nil
	}

	if err := builder.Remove(ctx, ref, *force); err != nil {
		return err
	}
	app.out.Printf("Removed %s\n", ref)
	return nil
}

// confirm asks the operator to approve a destructive operation. --yes skips the
// question; a non-interactive run without --yes refuses rather than assuming
// consent.
func (a *App) confirm(prompt string) (bool, error) {
	if a.assumeYes {
		return true, nil
	}
	if a.Stdin == nil {
		return false, exitf(ExitUsage, "%s\n  This is a destructive operation and there is no terminal to confirm on. Re-run with --yes.", prompt)
	}

	_, _ = fmt.Fprintf(a.Stderr, "%s [y/N] ", prompt)
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}

	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
