package image

import (
	"os"
	"path/filepath"
	"testing"
)

// toolout reads a fixture captured from a real tool. The digest parser is
// tested against podman's actual output rather than against a hand-written
// object, because the field it has to pick is only ambiguous in the real thing
// (AGENTS.md §7, test/toolout/README.md).
func toolout(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "toolout", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

// The digest this build pins to is the image's own `Digest`. Real output also
// carries a RepoDigests array, and for a multi-arch repository that array holds
// a second, different digest — the manifest list's. Pinning to that one would
// silently build from a different artifact than the one podman pulled, so this
// asserts the field, not just that something digest-shaped came back.
func TestParseImageDigest_ReadsTheImageDigestFromRealPodmanOutput(t *testing.T) {
	const (
		image    = "sha256:1cfa4e2b09e127b9c4ed43578d3f3c18e7d44ea47b9ea98475c0cbe9086525f8"
		manifest = "sha256:dc2d74b28e4cf8984fa52af1f39bc7c3d9c73760b41a74d629f5d11b1ab28616"
	)

	got, err := parseImageDigest(toolout(t, "podman-image-inspect.json"))
	if err != nil {
		t.Fatalf("parseImageDigest: %v", err)
	}
	if got == manifest {
		t.Fatalf("parseImageDigest read the other digest in RepoDigests (%s), not the image's own", got)
	}
	if got != image {
		t.Errorf("parseImageDigest = %s, want %s", got, image)
	}
}
