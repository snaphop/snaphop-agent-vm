package image

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// The OCI half of the pipeline: pull a source image, pin it to a digest, build
// the per-distro Containerfile on top of it, and flatten the result to a tar.
// Every step is podman doing what podman already does (ADR-0009).

// digestPattern is what a valid content digest looks like. Registry metadata is
// untrusted input (SECURITY.md), and this value ends up in a manifest, in an
// image reference, and in an argument vector.
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// containerIDPattern constrains the ID podman hands back before it is used as
// an argument to the next command.
var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

// pull fetches the source image. It is the only operation in the whole tool
// that needs network access to a registry.
func (b *Builder) pull(ctx context.Context, ref, platform string) error {
	args := []string{"pull"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	args = append(args, ref)

	if _, err := b.run(ctx, hostexec.Command{
		Name:    hostexec.Podman.Name,
		Args:    args,
		Effect:  hostexec.Mutate,
		Timeout: registryTimeout,
	}); err != nil {
		return fmt.Errorf("pulling %s: %w", ref, err)
	}
	return nil
}

// resolveDigest reads back the digest of what was actually pulled. The image is
// identified by this digest from here on: a tag can be rebuilt underneath us,
// and Arch's rolling tags certainly will be.
func (b *Builder) resolveDigest(ctx context.Context, ref string) (string, error) {
	res, err := b.run(ctx, hostexec.Command{
		Name:   hostexec.Podman.Name,
		Args:   []string{"image", "inspect", "--format", "json", ref},
		Effect: hostexec.Read,
	})
	if err != nil {
		return "", fmt.Errorf("inspecting %s: %w", ref, err)
	}
	return parseImageDigest(res.Stdout)
}

// parseImageDigest reads `podman image inspect --format json` output. A parse
// failure is an error, never an empty digest: an unpinned build is exactly what
// the provenance rules forbid.
func parseImageDigest(out []byte) (string, error) {
	var images []struct {
		Digest string `json:"Digest"`
	}
	if err := json.Unmarshal(out, &images); err != nil {
		return "", &hostexec.ParseError{Tool: "podman", What: "image inspect output", Output: string(out)}
	}
	if len(images) == 0 {
		return "", &hostexec.ParseError{Tool: "podman", What: "image inspect output (no image in the result)", Output: string(out)}
	}
	digest := strings.TrimSpace(images[0].Digest)
	if !digestPattern.MatchString(digest) {
		return "", &hostexec.ParseError{Tool: "podman", What: "image digest", Output: digest}
	}
	return digest, nil
}

// pinnedRef rewrites a reference to name the digest instead of the tag, so the
// build is reproducible from the manifest alone.
func pinnedRef(repo, digest string) (string, error) {
	if !digestPattern.MatchString(digest) {
		return "", fmt.Errorf("refusing to build from %q: not a valid content digest", digest)
	}
	return repo + "@" + digest, nil
}

// build runs the embedded per-distro Containerfile on top of the pinned source
// image, producing a local image tagged for this distro and tag.
func (b *Builder) build(ctx context.Context, containerfile, contextDir, localTag, baseImage, platform string) error {
	args := []string{"build", "--file", containerfile, "--tag", localTag, "--build-arg", "BASE_IMAGE=" + baseImage}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	args = append(args, contextDir)

	if _, err := b.run(ctx, hostexec.Command{
		Name:    hostexec.Podman.Name,
		Args:    args,
		Effect:  hostexec.Mutate,
		Timeout: buildTimeout,
	}); err != nil {
		return fmt.Errorf("building the %s image: %w", localTag, err)
	}
	return nil
}

// export flattens the built image into a tar of its root filesystem, which is
// what virt-make-fs turns into a disk. A container is created but never run:
// nothing from the image executes on the host.
func (b *Builder) export(ctx context.Context, localTag, tarPath string) (err error) {
	res, err := b.run(ctx, hostexec.Command{
		Name:         hostexec.Podman.Name,
		Args:         []string{"create", localTag},
		Effect:       hostexec.Mutate,
		DryRunStdout: "0000000000dryrun0000000000\n",
	})
	if err != nil {
		return fmt.Errorf("creating a container from %s: %w", localTag, err)
	}

	containerID := strings.TrimSpace(string(res.Stdout))
	if !containerIDPattern.MatchString(containerID) {
		return &hostexec.ParseError{Tool: "podman", What: "container ID", Output: containerID}
	}

	// The container exists only to be exported. Removing it is part of this
	// operation's cleanup, and a failure to remove it is reported rather than
	// hidden — it is host state we created.
	defer func() {
		if _, rmErr := b.run(ctx, hostexec.Command{
			Name:   hostexec.Podman.Name,
			Args:   []string{"rm", "--force", containerID},
			Effect: hostexec.Mutate,
		}); rmErr != nil && err == nil {
			err = fmt.Errorf("removing the temporary container %s: %w", containerID, rmErr)
		}
	}()

	if _, err := b.run(ctx, hostexec.Command{
		Name:    hostexec.Podman.Name,
		Args:    []string{"export", "--output", tarPath, containerID},
		Effect:  hostexec.Mutate,
		Timeout: buildTimeout,
	}); err != nil {
		return fmt.Errorf("exporting the root filesystem of %s: %w", localTag, err)
	}
	return nil
}
