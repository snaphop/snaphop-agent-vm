package image

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// Placeholders stand for values that only exist once the build has actually
// run. They are deliberately conspicuous: a printed plan must not look like it
// knows a digest it cannot know.
const (
	PlaceholderDigest        = "<source-digest>"
	PlaceholderContainerID   = "<container-id>"
	PlaceholderKernel        = "<kernel>"
	PlaceholderInitrd        = "<initrd>"
	PlaceholderKernelVersion = "<kernel-version>"
)

// Plan returns the tool invocations a build would run, without running any of
// them and without creating anything on disk.
//
// It exists because the pipeline's later steps read what its earlier steps
// wrote: the digest comes from the pull, the kernel file name comes from the
// built disk. Executing the read-only half against a host where the mutations
// were skipped would report confusing failures instead of a plan, and running
// the build's own file writes would make --dry-run change the state directory.
func Plan(layout state.Layout, opts BuildOptions) []hostexec.Command {
	name, tag := opts.Ref.ImageName(), opts.Ref.Tag

	sourceRef := opts.Ref.SourceRef()
	if opts.From != "" {
		sourceRef = opts.From
	}
	platform := opts.Platform
	if platform == "" {
		platform = "linux/" + runtime.GOARCH
	}

	// The real workspace name carries the PID of the process doing the build;
	// a plan has no build to name, so it shows the shape instead.
	work := workspaceDir(layout, name, tag, "<pid>")
	tarPath := filepath.Join(work, "rootfs.tar")
	diskPath := filepath.Join(work, state.BaseDiskFile)
	localTag := fmt.Sprintf("agent-vm/%s:%s", name, tag)
	pinned := repoOf(sourceRef) + "@" + PlaceholderDigest

	podman := func(args ...string) hostexec.Command {
		return hostexec.Command{Name: hostexec.Podman.Name, Args: args, Effect: hostexec.Mutate}
	}

	return []hostexec.Command{
		podman("pull", "--platform", platform, sourceRef),
		{Name: hostexec.Podman.Name, Args: []string{"image", "inspect", "--format", "json", sourceRef}, Effect: hostexec.Read},
		podman("build", "--file", filepath.Join(work, "Containerfile"),
			"--tag", localTag, "--build-arg", "BASE_IMAGE="+pinned,
			"--platform", platform, work),
		podman("create", localTag),
		podman("export", "--output", tarPath, PlaceholderContainerID),
		podman("rm", "--force", PlaceholderContainerID),
		{
			Name:   hostexec.VirtMakeFS.Name,
			Args:   []string{"--type=ext4", "--format=qcow2", "--partition", "--size=" + diskSlack, tarPath, diskPath},
			Effect: hostexec.Mutate,
		},
		{Name: hostexec.VirtLs.Name, Args: []string{"-a", diskPath, distro.ModulesDir}, Effect: hostexec.Read},
		{Name: hostexec.VirtLs.Name, Args: []string{"-a", diskPath, "/boot"}, Effect: hostexec.Read},
		{
			Name:   hostexec.VirtCopyOut.Name,
			Args:   []string{"-a", diskPath, "/boot/" + PlaceholderKernel, "/boot/" + PlaceholderInitrd, work},
			Effect: hostexec.Mutate,
		},
		{
			Name:   hostexec.VirtSysprep.Name,
			Args:   []string{"-a", diskPath, "--operations", strings.Join(sysprepOperations, ",")},
			Effect: hostexec.Mutate,
		},
	}
}

// PlanNotes describe what a plan does beyond running tools, so an operator
// reading --dry-run output sees the file operations too.
func PlanNotes(layout state.Layout, ref distro.Ref) []string {
	final := layout.ImageDir(ref.ImageName(), ref.Tag)
	return []string{
		fmt.Sprintf("write the embedded %s build recipe into the temporary build directory", ref.Containerfile()),
		fmt.Sprintf("write manifest.json recording the source digest, kernel %s, cmdline %q, and the tool versions used",
			PlaceholderKernelVersion, distro.KernelCmdline),
		"delete the exported root filesystem tar",
		fmt.Sprintf("rename the temporary build directory to %s, which is the moment the image becomes usable", final),
	}
}
