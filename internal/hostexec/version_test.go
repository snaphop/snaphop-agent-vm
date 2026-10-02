package hostexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// toolout reads a fixture captured from a real tool. See test/toolout/README.md.
func toolout(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "toolout", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseToolVersion_FromCapturedToolOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		tool    Tool
		fixture string
		want    Version
	}{
		{"virsh", Virsh, "virsh--version.txt", Version{Major: 12, Minor: 6}},
		{"virt-install", VirtInstall, "virt-install--version.txt", Version{Major: 5, Minor: 1}},
		{"qemu-img", QemuImg, "qemu-img--version.txt", Version{Major: 11, Minor: 1}},
		{"ip", IP, "ip-V.txt", Version{Major: 7, Minor: 1}},
		{"ssh", SSH, "ssh-V.txt", Version{Major: 10, Minor: 5}},
		{"podman", Podman, "podman--version.txt", Version{Major: 6, Minor: 1}},
		{"virt-make-fs", VirtMakeFS, "virt-make-fs--version.txt", Version{Major: 1, Minor: 56}},
		{"virt-ls", VirtLs, "virt-ls--version.txt", Version{Major: 1, Minor: 56}},
		{"virt-copy-out", VirtCopyOut, "virt-copy-out--version.txt", Version{Major: 1, Minor: 60, Patch: 1}},
		{"virt-sysprep", VirtSysprep, "virt-sysprep--version.txt", Version{Major: 1, Minor: 56}},
		// gh prints a release URL on a second line, so this also covers the
		// parser not assuming single-line output.
		{"gh", GH, "gh--version.txt", Version{Major: 2, Minor: 97}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseToolVersion(tt.tool, toolout(t, tt.fixture))
			if err != nil {
				t.Fatalf("parseToolVersion(%s): %v", tt.tool.Name, err)
			}
			if got != tt.want {
				t.Errorf("parseToolVersion(%s) = %v, want %v", tt.tool.Name, got, tt.want)
			}
		})
	}
}

func TestParseToolVersion_UnrecognizedOutputIsAnError(t *testing.T) {
	t.Parallel()
	// A tool changing its output must fail loudly rather than be read as
	// version 0.0.0, which would look like "too old" instead of "unparseable".
	_, err := parseToolVersion(QemuImg, "qemu-img: the world has moved on\n")
	var perr *ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("got %v, want *ParseError", err)
	}
	if perr.Tool != "qemu-img" {
		t.Errorf("ParseError.Tool = %q, want %q", perr.Tool, "qemu-img")
	}
}

func TestVersionAtLeast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		got, min string
		want     bool
	}{
		{"9.0.0", "9.0.0", true},
		{"12.6.0", "9.0.0", true},
		{"8.10.0", "9.0.0", false},
		{"9.0.0", "9.1.0", false},
		{"1.50.1", "1.50.0", true},
		{"1.49.9", "1.50.0", false},
	}
	for _, tt := range tests {
		got, err := ParseVersion(tt.got)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", tt.got, err)
		}
		min, err := ParseVersion(tt.min)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", tt.min, err)
		}
		if got.AtLeast(min) != tt.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", tt.got, tt.min, !tt.want, tt.want)
		}
	}
}

func TestParseVersion_IgnoresTrailingQualifier(t *testing.T) {
	t.Parallel()
	got, err := ParseVersion("1.50.1rc2")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if want := (Version{Major: 1, Minor: 50, Patch: 1}); got != want {
		t.Errorf("ParseVersion = %v, want %v", got, want)
	}
}

func TestVersionsRequire_RejectsToolBelowMinimum(t *testing.T) {
	t.Parallel()
	fake := NewFake().Respond("virsh --version", FakeResponse{Stdout: "8.10.0\n"})
	_, err := NewVersions(fake).Require(context.Background(), Virsh)

	var verr *VersionError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want *VersionError", err)
	}
	if verr.Found.String() != "8.10.0" || verr.Minimum.String() != "9.0.0" {
		t.Errorf("VersionError = %+v, want found 8.10.0 minimum 9.0.0", verr)
	}
}

func TestVersionsRequire_AcceptsToolAtMinimum(t *testing.T) {
	t.Parallel()
	fake := NewFake().Respond("virt-install --version", FakeResponse{Stdout: "4.0.0\n"})
	got, err := NewVersions(fake).Require(context.Background(), VirtInstall)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if got.String() != "4.0.0" {
		t.Errorf("version = %s, want 4.0.0", got)
	}
}

func TestVersionsGet_ReportsMissingToolWithItsPackage(t *testing.T) {
	t.Parallel()
	fake := NewFake()
	fake.Missing["virt-make-fs"] = true

	_, err := NewVersions(fake).Get(context.Background(), VirtMakeFS)

	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("got %v, want *NotFoundError", err)
	}
	if nf.Package != "libguestfs-tools" {
		t.Errorf("NotFoundError.Package = %q, want %q", nf.Package, "libguestfs-tools")
	}
}

func TestVersionsGet_ProbesEachToolOnce(t *testing.T) {
	t.Parallel()
	fake := NewFake().Respond("virsh --version", FakeResponse{Stdout: "12.6.0\n"})
	versions := NewVersions(fake)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := versions.Get(ctx, Virsh); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}
	if got := len(fake.Calls()); got != 1 {
		t.Errorf("probed %d times, want 1\n%s", got, fake)
	}
}
