// Package image turns an OCI container image reference into a cached,
// immutable base artifact: a qcow2 root disk, the kernel and initramfs
// extracted from it, and a manifest recording where it came from.
//
// Every mechanical step is an existing tool doing what it already does
// (ADR-0009): podman pulls, builds and flattens; virt-make-fs writes the disk;
// virt-ls and virt-copy-out extract the boot artifacts; virt-sysprep
// generalizes the result. This package owns the sequence, the cache, and the
// guarantee that a half-built image can never be booted.
package image

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
	"github.com/snaphop/snaphop-agent-vm/templates"
)

// Timeouts for the long steps. A registry pull and a distro package
// installation are minutes-scale operations; the default two-minute timeout in
// hostexec would fail a perfectly healthy build.
const (
	registryTimeout = 15 * time.Minute
	buildTimeout    = 30 * time.Minute
	guestfsTimeout  = 20 * time.Minute
)

// diskSlack is how much free space the base filesystem gets beyond the size of
// the exported root filesystem. It only needs to cover first-boot writes —
// cloud-init, logs, SSH host keys — because everything a VM writes afterwards
// goes to its own overlay.
const diskSlack = "+1G"

// syspreptOperations generalize the image so that every VM built on it does not
// share an identity. Machine ID and SSH host keys are the ones that matter:
// duplicated across guests they break systemd's journal and make host key
// verification meaningless.
var sysprepOperations = []string{
	"machine-id",
	"ssh-hostkeys",
	"logfiles",
	"tmp-files",
	"net-hostname",
	"udev-persistent-net",
}

// buildContextFiles are the embedded files every family's Containerfile COPYs
// into the guest, written into the build context next to the recipe. They are
// guest configuration, not credentials: a base image is shared by every VM
// built on it, so nothing per-VM or secret may be added to this list
// (SECURITY.md).
var buildContextFiles = []string{
	"tmux.conf",
	"claude-settings.json",
	"codex-config.toml",
	"opencode.json",
	"grok-config.toml",
	"agent-aliases.sh",
	"chromium.sh",
	"mise.sh",
	"toolchains.sh",
	"nix.sh",
	"agent-tools.nix",
	"user-setup.sh",
	"codex-remote-control.sh",
	"herdr-server.sh",
	"tmux-menu.sh",
	"tmux-menu-profile.sh",
	"github-runner.sh",
	"github-runner-configure.sh",
	"runner-docker.sh",
}

// Builder produces base images.
type Builder struct {
	Runner         hostexec.Runner
	Versions       *hostexec.Versions
	Store          *state.Store
	Logger         *slog.Logger
	AgentVMVersion string
	// Progress, when set, is told which step the build has reached. It is
	// nil for --quiet and in tests.
	Progress Progress
}

// BuildOptions are the inputs to one build.
type BuildOptions struct {
	Ref distro.Ref
	// From overrides the source OCI reference. It is the supported escape
	// hatch for a custom image within a supported family (ADR-0006).
	From string
	// Platform is an OS/arch pair; empty means the host's platform.
	Platform string
	// Force rebuilds even when a cached image already exists.
	Force bool
}

// Build produces the base image for a distro and tag, and returns its manifest.
//
// The whole build happens in a temporary directory that is renamed into place
// only on success, so an interrupted or failed build can never leave a
// partially written base image that a VM could boot from (SECURITY.md).
func (b *Builder) Build(ctx context.Context, opts BuildOptions) (built *state.Manifest, err error) {
	name, tag := opts.Ref.ImageName(), opts.Ref.Tag

	// One build per base image at a time. A second build of the same image
	// waits rather than racing; different distros build concurrently. Each
	// variant is a different image, so building one neither waits for nor
	// collides with another variant of the same family and tag.
	lock, err := b.Store.LockImage(ctx, name, tag, "image build")
	if err != nil {
		return nil, err
	}
	// A lock we cannot release is host state the operator needs to know about,
	// so it is reported rather than dropped (AGENTS.md §6).
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()

	if err := b.recoverInterruptedBuilds(name, tag); err != nil {
		return nil, err
	}

	cached, err := b.Store.HasImage(name, tag)
	if err != nil {
		return nil, err
	}
	if cached && !opts.Force {
		return b.Store.LoadManifest(name, tag)
	}
	if cached {
		// A rebuild replaces the backing file in place, and an overlay on top
		// of a different base disk is a corrupt filesystem (ADR-0004).
		if err := b.refuseWhileInUse(opts.Ref, "rebuild", false); err != nil {
			return nil, err
		}
	}

	sourceRef := opts.Ref.SourceRef()
	if opts.From != "" {
		sourceRef = opts.From
	}
	platform := opts.Platform
	if platform == "" {
		platform = "linux/" + runtime.GOARCH
	}

	// From here on the build actually does something, so it is worth
	// reporting. A cache hit above returns without a single step.
	steps := reporter{to: b.Progress}
	defer func() { steps.finish(err) }()

	work, err := b.newWorkspace(name, tag)
	if err != nil {
		return nil, err
	}
	// A failed build leaves nothing behind: the workspace is inside the state
	// directory and is removed whether the failure was ours or a tool's.
	defer func() {
		if work.dir != "" {
			if rmErr := b.Store.Remove(work.dir); rmErr != nil {
				b.Logger.Warn("could not remove the build workspace", "dir", work.dir, "error", rmErr)
			}
		}
	}()

	manifest, err := b.buildInto(ctx, steps, work, opts, sourceRef, platform)
	if err != nil {
		return nil, err
	}

	steps.at(stepCommit)
	if err := b.commit(work, name, tag); err != nil {
		return nil, err
	}
	work.dir = "" // committed; nothing left to clean up
	return manifest, nil
}

// workspace is the temporary directory one build assembles its artifacts in.
type workspace struct {
	dir      string
	tarPath  string
	diskPath string
}

// workspacePrefix starts the name of every build workspace. A workspace sits
// beside the image it will become, in images/<image>/, and is named
// ".build-<tag>-<pid>". The leading dot keeps it from ever being a tag
// (distro.ParseRef refuses one), and a PID cannot contain a hyphen, so the tag
// a workspace belongs to is exactly what lies between the prefix and the last
// hyphen.
const workspacePrefix = ".build-"

func workspaceDir(layout state.Layout, imageName, tag, pid string) string {
	return filepath.Join(filepath.Dir(layout.ImageDir(imageName, tag)), workspacePrefix+tag+"-"+pid)
}

// isWorkspaceOf reports whether a directory name is a build workspace for this
// tag. It compares exactly, so tag 24.04 never claims the workspace of tag
// 24.04.1 or 24.04-1.
func isWorkspaceOf(name, tag string) bool {
	rest, ok := strings.CutPrefix(name, workspacePrefix)
	if !ok {
		return false
	}
	cut := strings.LastIndex(rest, "-")
	if cut < 0 || rest[:cut] != tag {
		return false
	}
	pid := rest[cut+1:]
	if pid == "" {
		return false
	}
	for _, r := range pid {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// backupDir is where a rebuild moves the image it replaces while the new one is
// installed. The leading dot means it can never be the directory of a real
// image: distro.ParseRef refuses a tag that starts with one, so no other cached
// image can share the name and be deleted by mistake.
func backupDir(layout state.Layout, imageName, tag string) string {
	return filepath.Join(filepath.Dir(layout.ImageDir(imageName, tag)), "."+tag+".previous")
}

// recoverInterruptedBuilds clears up after a build of this image that was
// killed — SIGKILL, the OOM killer, a reboot — before it could clean up after
// itself. It runs under the image's lock, which every build of the image holds
// for its whole duration, so anything it finds belongs to a build that is no
// longer running.
//
// A workspace left behind is removed: it is gigabytes of root filesystem tar
// and disk image that nothing would ever use. A backup left by an interrupted
// commit is put back when the image itself is missing, because it is then the
// last good image; when the image is present, the new one was installed and
// the backup is only a leftover.
func (b *Builder) recoverInterruptedBuilds(imageName, tag string) error {
	parent := filepath.Dir(b.Store.ImageDir(imageName, tag))
	entries, err := b.Store.Subdirectories(parent)
	if err != nil {
		return fmt.Errorf("looking for interrupted builds of %s:%s: %w", imageName, tag, err)
	}
	for _, entry := range entries {
		if !isWorkspaceOf(entry, tag) {
			continue
		}
		dir := filepath.Join(parent, entry)
		if err := b.Store.Remove(dir); err != nil {
			return fmt.Errorf("removing %s, left by an interrupted build of %s:%s: %w", dir, imageName, tag, err)
		}
		if b.Logger != nil {
			b.Logger.Info("removed the workspace of an interrupted build", "dir", dir)
		}
	}

	backup := backupDir(b.Store.Layout, imageName, tag)
	hasBackup, err := b.Store.Exists(backup)
	if err != nil || !hasBackup {
		return err
	}
	final := b.Store.ImageDir(imageName, tag)
	hasImage, err := b.Store.Exists(final)
	if err != nil {
		return err
	}
	if hasImage {
		if err := b.Store.Remove(backup); err != nil {
			return fmt.Errorf("removing %s, left by an interrupted rebuild of %s:%s: %w", backup, imageName, tag, err)
		}
		return nil
	}
	if err := b.Store.Rename(backup, final); err != nil {
		return fmt.Errorf("restoring %s:%s from %s, left by an interrupted rebuild: %w", imageName, tag, backup, err)
	}
	if b.Logger != nil {
		b.Logger.Info("restored the image an interrupted rebuild had moved aside", "image", imageName+":"+tag)
	}
	return nil
}

func (b *Builder) newWorkspace(imageName, tag string) (*workspace, error) {
	dir := workspaceDir(b.Store.Layout, imageName, tag, strconv.Itoa(os.Getpid()))
	if err := b.Store.Remove(dir); err != nil {
		return nil, err
	}
	if err := b.Store.MkdirAll(dir); err != nil {
		return nil, err
	}
	return &workspace{
		dir:      dir,
		tarPath:  filepath.Join(dir, "rootfs.tar"),
		diskPath: filepath.Join(dir, state.BaseDiskFile),
	}, nil
}

func (b *Builder) buildInto(ctx context.Context, steps reporter, work *workspace, opts BuildOptions, sourceRef, platform string) (*state.Manifest, error) {
	d, tag := opts.Ref.Distro, opts.Ref.Tag

	steps.at(stepPull)
	if err := b.pull(ctx, sourceRef, platform); err != nil {
		return nil, err
	}
	steps.at(stepDigest)
	digest, err := b.resolveDigest(ctx, sourceRef)
	if err != nil {
		return nil, err
	}
	pinned, err := pinnedRef(repoOf(sourceRef), digest)
	if err != nil {
		return nil, err
	}

	containerfile, err := b.writeBuildContext(work, opts.Ref)
	if err != nil {
		return nil, err
	}
	localTag := fmt.Sprintf("agent-vm/%s:%s", opts.Ref.ImageName(), tag)
	steps.at(stepLayers)
	if err := b.build(ctx, containerfile, work.dir, localTag, pinned, platform); err != nil {
		return nil, err
	}
	steps.at(stepExport)
	if err := b.export(ctx, localTag, work.tarPath); err != nil {
		return nil, err
	}

	steps.at(stepDisk)
	if err := b.makeDisk(ctx, work); err != nil {
		return nil, err
	}
	steps.at(stepKernelVersion)
	kernelVersion, err := b.kernelVersion(ctx, work.diskPath)
	if err != nil {
		return nil, err
	}
	steps.at(stepBootArtifacts)
	if err := b.extractBootArtifacts(ctx, work, d); err != nil {
		return nil, err
	}
	steps.at(stepSysprep)
	if err := b.sysprep(ctx, work.diskPath); err != nil {
		return nil, err
	}

	// The exported tar is the largest thing in the workspace and is of no use
	// once the disk exists.
	if err := b.Store.Remove(work.tarPath); err != nil {
		return nil, err
	}

	steps.at(stepManifest)
	return b.writeManifest(ctx, work, opts, sourceRef, digest, platform, kernelVersion)
}

// writeBuildContext places the embedded per-distro recipe and the files it
// copies into the workspace, which doubles as podman's build context, and
// returns the path of the recipe.
//
// Every family's full recipe COPYs the same guest dotfiles, so they are
// written from one embedded copy rather than repeated as heredocs in three
// Containerfiles. They are written for every variant, including a slim build
// that COPYs none of them, a runner build that COPYs the two runner scripts,
// and a nix build that COPYs a different subset: podman ignores what a recipe
// does not reference, and writing the same context every time keeps the build
// from having to know which recipe it is running.
func (b *Builder) writeBuildContext(work *workspace, ref distro.Ref) (string, error) {
	contents, err := templates.FS.ReadFile("distro/" + ref.Containerfile())
	if err != nil {
		return "", fmt.Errorf("no build recipe for %s: %w", ref.ImageName(), err)
	}
	path := filepath.Join(work.dir, "Containerfile")
	if err := b.Store.WriteFile(path, contents, 0o644); err != nil {
		return "", err
	}

	for _, name := range buildContextFiles {
		contents, err := templates.FS.ReadFile("distro/" + name)
		if err != nil {
			return "", fmt.Errorf("no embedded %s for the build context: %w", name, err)
		}
		if err := b.Store.WriteFile(filepath.Join(work.dir, name), contents, 0o644); err != nil {
			return "", err
		}
	}
	return path, nil
}

// makeDisk turns the exported root filesystem into a bootable qcow2. The image
// is partitioned so the guest's root is /dev/vda1, which is what the recorded
// kernel command line expects.
func (b *Builder) makeDisk(ctx context.Context, work *workspace) error {
	_, err := b.run(ctx, hostexec.Command{
		Name: hostexec.VirtMakeFS.Name,
		Args: []string{
			"--type=ext4",
			"--format=qcow2",
			"--partition",
			"--size=" + diskSlack,
			work.tarPath,
			work.diskPath,
		},
		Effect:  hostexec.Mutate,
		Timeout: guestfsTimeout,
	})
	if err != nil {
		return fmt.Errorf("building the base disk: %w", err)
	}
	return nil
}

// kernelVersion reads the kernel version from the modules directory, which is
// more reliable than parsing it out of a kernel filename and is the same on all
// three supported families.
func (b *Builder) kernelVersion(ctx context.Context, diskPath string) (string, error) {
	entries, err := b.virtLs(ctx, diskPath, distro.ModulesDir)
	if err != nil {
		return "", err
	}
	versions := []string{}
	for _, entry := range entries {
		if kernelVersionPattern.MatchString(entry) {
			versions = append(versions, entry)
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("no kernel found in %s of the built image: the build recipe for this distro did not install a kernel", distro.ModulesDir)
	}
	// More than one kernel means the recipe installed two; the newest is the
	// one whose initramfs was generated last. Choosing by version keeps the
	// choice deterministic instead of depending on directory order.
	return newestByVersion(versions), nil
}

// newestByVersion returns the name that sorts last when runs of digits are
// compared as numbers, so 6.10.0 is newer than 6.9.0 — which plain string
// order gets backwards.
func newestByVersion(names []string) string {
	sorted := append([]string(nil), names...)
	sort.SliceStable(sorted, func(i, j int) bool { return compareVersions(sorted[i], sorted[j]) < 0 })
	return sorted[len(sorted)-1]
}

// compareVersions orders two names chunk by chunk: a run of digits against a
// run of digits by numeric value, anything else byte by byte.
func compareVersions(a, b string) int {
	for a != "" && b != "" {
		chunkA, restA := versionChunk(a)
		chunkB, restB := versionChunk(b)
		if isDigit(chunkA[0]) && isDigit(chunkB[0]) {
			// Compared as digit strings, not parsed, so a long run cannot
			// overflow: without leading zeros, the longer one is larger.
			numA, numB := strings.TrimLeft(chunkA, "0"), strings.TrimLeft(chunkB, "0")
			if len(numA) != len(numB) {
				return len(numA) - len(numB)
			}
			if c := strings.Compare(numA, numB); c != 0 {
				return c
			}
		} else if c := strings.Compare(chunkA, chunkB); c != 0 {
			return c
		}
		a, b = restA, restB
	}
	return len(a) - len(b)
}

// versionChunk splits off the leading run of digits, or of non-digits.
func versionChunk(s string) (chunk, rest string) {
	digits := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == digits {
		i++
	}
	return s[:i], s[i:]
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// kernelVersionPattern is deliberately strict: this value is used to build
// paths passed to another tool.
var kernelVersionPattern = regexp.MustCompile(`^[0-9][a-zA-Z0-9._+-]{0,127}$`)

// extractBootArtifacts copies the kernel and initramfs out of the built disk.
// Under direct kernel boot they live on the host, not in the guest (ADR-0004).
func (b *Builder) extractBootArtifacts(ctx context.Context, work *workspace, d distro.Distro) error {
	entries, err := b.virtLs(ctx, work.diskPath, "/boot")
	if err != nil {
		return err
	}

	kernel, err := matchOne(entries, d.KernelPattern, "kernel", d.Name)
	if err != nil {
		return err
	}
	initrd, err := matchOne(entries, d.InitrdPattern, "initramfs", d.Name)
	if err != nil {
		return err
	}

	if _, err := b.run(ctx, hostexec.Command{
		Name: hostexec.VirtCopyOut.Name,
		Args: []string{
			"-a", work.diskPath,
			"/boot/" + kernel,
			"/boot/" + initrd,
			work.dir,
		},
		Effect:  hostexec.Mutate,
		Timeout: guestfsTimeout,
	}); err != nil {
		return fmt.Errorf("extracting the kernel and initramfs: %w", err)
	}

	// virt-copy-out keeps the guest's file names; the manifest promises fixed
	// ones, so that virt-install's --boot arguments do not vary per distro.
	for _, rename := range []struct{ from, to string }{
		{kernel, state.KernelFile},
		{initrd, state.InitrdFile},
	} {
		if rename.from == rename.to {
			continue
		}
		if err := b.Store.Rename(filepath.Join(work.dir, rename.from), filepath.Join(work.dir, rename.to)); err != nil {
			return fmt.Errorf("renaming %s to %s: %w", rename.from, rename.to, err)
		}
	}
	if err := b.unpackUnifiedKernel(ctx, work.dir, filepath.Join(work.dir, state.KernelFile)); err != nil {
		return err
	}
	return nil
}

// matchOne finds exactly one /boot entry matching a distro's pattern. Entry
// names come from the built image and are untrusted, so a name that is not a
// plain file name is refused rather than being passed to another tool.
func matchOne(entries []string, pattern, what, distroName string) (string, error) {
	matches := []string{}
	for _, entry := range entries {
		if strings.Contains(entry, "/") || strings.HasPrefix(entry, "-") || strings.HasPrefix(entry, ".") {
			continue
		}
		if ok, err := path.Match(pattern, entry); err == nil && ok {
			matches = append(matches, entry)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no %s matching %q in /boot of the built %s image: check templates/distro/%s.Containerfile",
			what, pattern, distroName, distroName)
	case 1:
		return matches[0], nil
	default:
		// Two kernels means the recipe installed two. The newest is taken, by
		// the same version order kernelVersion uses, so the kernel copied out
		// and the version the manifest records agree.
		return newestByVersion(matches), nil
	}
}

// virtLs lists a directory inside a disk image.
func (b *Builder) virtLs(ctx context.Context, diskPath, dir string) ([]string, error) {
	res, err := b.run(ctx, hostexec.Command{
		Name:         hostexec.VirtLs.Name,
		Args:         []string{"-a", diskPath, dir},
		Effect:       hostexec.Read,
		Timeout:      guestfsTimeout,
		DryRunStdout: "",
	})
	if err != nil {
		return nil, fmt.Errorf("listing %s in the built image: %w", dir, err)
	}

	entries := []string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if entry := strings.TrimSpace(line); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// sysprep clears the identity a shared base image must not carry.
func (b *Builder) sysprep(ctx context.Context, diskPath string) error {
	_, err := b.run(ctx, hostexec.Command{
		Name: hostexec.VirtSysprep.Name,
		Args: []string{
			"-a", diskPath,
			"--operations", strings.Join(sysprepOperations, ","),
		},
		Effect:  hostexec.Mutate,
		Timeout: guestfsTimeout,
	})
	if err != nil {
		return fmt.Errorf("generalizing the base image: %w", err)
	}
	return nil
}

func (b *Builder) writeManifest(ctx context.Context, work *workspace, opts BuildOptions, sourceRef, digest, platform, kernelVersion string) (*state.Manifest, error) {
	manifest := &state.Manifest{
		SchemaVersion:  state.ManifestSchemaVersion,
		Distro:         opts.Ref.ImageName(),
		Tag:            opts.Ref.Tag,
		BuiltAt:        time.Now().UTC(),
		Platform:       platform,
		SourceRef:      sourceRef,
		SourceDigest:   digest,
		KernelVersion:  kernelVersion,
		KernelCmdline:  distro.KernelCmdline,
		ToolVersions:   b.toolVersions(ctx),
		AgentVMVersion: b.AgentVMVersion,
	}
	if size, err := b.Store.FileSize(work.diskPath); err == nil {
		manifest.BaseDiskBytes = size
	}

	// The manifest is written into the workspace, so it is renamed into place
	// together with the artifacts it describes.
	data, err := state.MarshalManifest(manifest)
	if err != nil {
		return nil, err
	}
	if err := b.Store.WriteFile(filepath.Join(work.dir, state.ManifestFile), data, 0o644); err != nil {
		return nil, err
	}
	return manifest, nil
}

// toolVersions records what built this image, so an artifact produced by a
// known-bad tool version can be identified later.
func (b *Builder) toolVersions(ctx context.Context) map[string]string {
	tools := []hostexec.Tool{hostexec.Podman, hostexec.VirtMakeFS, hostexec.VirtLs, hostexec.VirtCopyOut, hostexec.VirtSysprep}
	versions := make(map[string]string, len(tools))
	for _, tool := range tools {
		if version, err := b.Versions.Get(ctx, tool); err == nil {
			versions[tool.Name] = version.String()
		}
	}
	return versions
}

// commit moves the finished artifacts into the cache. The rename is the moment
// the image becomes visible and bootable, and it is the last step.
func (b *Builder) commit(work *workspace, distroName, tag string) error {
	final := b.Store.ImageDir(distroName, tag)
	if err := b.Store.MkdirAll(filepath.Dir(final)); err != nil {
		return err
	}

	// A rebuild replaces an existing image. The old directory is moved aside
	// first and removed only after the new one is in place, so a failure here
	// leaves the previous image intact rather than nothing at all.
	previous := backupDir(b.Store.Layout, distroName, tag)
	existed, err := b.Store.Exists(final)
	if err != nil {
		return err
	}
	if existed {
		if err := b.Store.Remove(previous); err != nil {
			return err
		}
		if err := b.Store.Rename(final, previous); err != nil {
			return fmt.Errorf("moving the previous %s:%s image aside: %w", distroName, tag, err)
		}
	}

	if err := b.Store.Rename(work.dir, final); err != nil {
		if existed {
			// Put the previous image back rather than leaving the cache empty.
			if restoreErr := b.Store.Rename(previous, final); restoreErr != nil {
				return fmt.Errorf("installing the new %s:%s image failed (%w), and the previous one could not be restored: %v",
					distroName, tag, err, restoreErr)
			}
		}
		return fmt.Errorf("installing the new %s:%s image: %w", distroName, tag, err)
	}

	if err := b.Store.Remove(previous); err != nil {
		return fmt.Errorf("removing the previous %s:%s image: %w", distroName, tag, err)
	}
	return nil
}

// run executes a host tool, logging the step for --verbose.
func (b *Builder) run(ctx context.Context, cmd hostexec.Command) (*hostexec.Result, error) {
	if b.Logger != nil {
		b.Logger.Debug("image build step", "tool", cmd.Name)
	}
	return b.Runner.Run(ctx, cmd)
}

// repoOf strips the tag or digest from an OCI reference, leaving the
// repository that a digest can be appended to.
func repoOf(ref string) string {
	if at := strings.Index(ref, "@"); at >= 0 {
		return ref[:at]
	}
	// A colon in the final path segment is a tag; a colon earlier is a
	// registry port and must be left alone.
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon]
	}
	return ref
}

// Remove deletes a cached base image, refusing while any VM's overlay still
// uses it as a backing file. A backing file is not optional: removing it makes
// those VMs' disks unreadable (ADR-0004).
func (b *Builder) Remove(ctx context.Context, ref distro.Ref, force bool) (err error) {
	lock, err := b.Store.LockImage(ctx, ref.ImageName(), ref.Tag, "image rm")
	if err != nil {
		return err
	}
	// A lock we cannot release is host state the operator needs to know about,
	// so it is reported rather than dropped (AGENTS.md §6).
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()

	// An image an interrupted rebuild left only as its backup is still the
	// operator's image, so it is put back first and is then removable.
	if err := b.recoverInterruptedBuilds(ref.ImageName(), ref.Tag); err != nil {
		return err
	}
	cached, err := b.Store.HasImage(ref.ImageName(), ref.Tag)
	if err != nil {
		return err
	}
	if !cached {
		return &state.NotFoundError{Kind: "base image", Name: ref.String()}
	}

	if err := b.refuseWhileInUse(ref, "remove", force); err != nil {
		return err
	}
	return b.Store.Remove(b.Store.ImageDir(ref.ImageName(), ref.Tag))
}

// refuseWhileInUse refuses to remove or replace a base image that an overlay
// depends on. It runs under the image's lock. A create in progress is refused
// even with force: its overlay may already be on the image, and its record —
// the only thing that would show the dependency — is not written yet. Such a
// create cannot be missed, because create makes its VM directory before it
// looks the image up, and that lookup waits for this lock (EnsureImage).
func (b *Builder) refuseWhileInUse(ref distro.Ref, action string, force bool) error {
	creating, err := b.Store.CreatesInProgress()
	if err != nil {
		return err
	}
	if len(creating) > 0 {
		return &state.BusyError{
			Resource: "base image " + ref.String(),
			Holder:   "a create is in progress for " + strings.Join(creating, ", ") + "; try again once it finishes",
		}
	}

	users, err := b.Store.VMsUsingImage(ref.ImageName(), ref.Tag)
	if err != nil {
		return err
	}
	if len(users) == 0 || force {
		return nil
	}
	names := make([]string, 0, len(users))
	for _, vm := range users {
		names = append(names, vm.Name)
	}
	remedy := "Destroy them first, or pass --force to remove it anyway and make their disks unreadable"
	if action == "rebuild" {
		remedy = "Destroy them first: rebuilding it in place would leave their disks on a different base, which corrupts them"
	}
	return fmt.Errorf("cannot %s base image %s: it is still the backing file for %d VM(s): %s\n  %s",
		action, ref, len(users), strings.Join(names, ", "), remedy)
}

// EnsureImage returns the cached base image for a reference, building it if the
// cache misses. It is what `create` calls.
//
// A cache hit still takes the image's lock, through Build. That orders the
// lookup against `image rm` and `image build --force`: one of them either
// finishes before the lookup, or finds the create's VM directory and refuses
// (refuseWhileInUse).
func (b *Builder) EnsureImage(ctx context.Context, ref distro.Ref) (*state.Manifest, error) {
	return b.Build(ctx, BuildOptions{Ref: ref})
}
