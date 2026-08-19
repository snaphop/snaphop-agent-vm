package image

// Progress observes a build as it moves from step to step. A build is a
// sequence of other programs doing minutes of work in silence — a registry
// pull, a distro package installation, a libguestfs appliance boot — so the
// steps are reported rather than left to be guessed at from an idle terminal.
//
// The builder only says where it is; internal/cli decides what that looks like.
// A nil Progress reports nothing, which is what --quiet passes.
type Progress interface {
	// Step reports that a 1-based numbered step out of total has begun.
	Step(number, total int, name string)
	// Finish reports that the build ended, with the error that ended it or nil.
	Finish(err error)
}

// buildStep identifies one step of the pipeline. The steps are named
// constants rather than a running counter so that reordering the pipeline
// cannot silently mislabel what the operator is watching.
type buildStep int

const (
	stepPull buildStep = iota
	stepDigest
	stepLayers
	stepExport
	stepDisk
	stepKernelVersion
	stepBootArtifacts
	stepSysprep
	stepManifest
	stepCommit
)

// buildStepNames are what an operator reads. They describe the work, not the
// tool doing it: --verbose and --dry-run are where tool names belong.
var buildStepNames = [...]string{
	stepPull:          "pulling the source image",
	stepDigest:        "resolving the source digest",
	stepLayers:        "building the image layers",
	stepExport:        "exporting the root filesystem",
	stepDisk:          "creating the base disk",
	stepKernelVersion: "reading the kernel version",
	stepBootArtifacts: "extracting the kernel and initramfs",
	stepSysprep:       "generalizing the image",
	stepManifest:      "writing the manifest",
	stepCommit:        "installing into the cache",
}

// reporter adapts the step constants to the Progress interface and tolerates
// the common case of there being no reporter at all.
type reporter struct{ to Progress }

func (r reporter) at(step buildStep) {
	if r.to != nil {
		r.to.Step(int(step)+1, len(buildStepNames), buildStepNames[step])
	}
}

func (r reporter) finish(err error) {
	if r.to != nil {
		r.to.Finish(err)
	}
}
