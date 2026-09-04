package distro

import (
	"strings"
	"testing"
)

func TestParseRef_AppliesTheFamilyDefaultTag(t *testing.T) {
	// These pairings are ADR-0006's decision table; changing one changes what
	// `agent-vm create --distro fedora` boots.
	tests := []struct {
		in        string
		want      string
		sourceRef string
	}{
		{"ubuntu", "ubuntu:24.04", "docker.io/library/ubuntu:24.04"},
		{"fedora", "fedora:42", "registry.fedoraproject.org/fedora:42"},
		{"arch", "arch:base", "docker.io/library/archlinux:base"},
		{"ubuntu:22.04", "ubuntu:22.04", "docker.io/library/ubuntu:22.04"},
	}
	for _, tt := range tests {
		ref, err := ParseRef(tt.in)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", tt.in, err)
			continue
		}
		if ref.String() != tt.want {
			t.Errorf("ParseRef(%q) = %s, want %s", tt.in, ref, tt.want)
		}
		if ref.SourceRef() != tt.sourceRef {
			t.Errorf("ParseRef(%q).SourceRef() = %s, want %s", tt.in, ref.SourceRef(), tt.sourceRef)
		}
	}
}

func TestParseRef_RejectsAnUnsupportedFamilyAndSaysWhatIsSupported(t *testing.T) {
	_, err := ParseRef("alpine")
	if err == nil {
		t.Fatal("ParseRef accepted an unsupported distro")
	}
	for _, want := range []string{"ubuntu", "fedora", "arch", "ADR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestParseRef_RejectsTagsThatWouldEscapeTheStateDirectory(t *testing.T) {
	// A tag becomes a path segment under images/<distro>/<tag>/.
	for _, in := range []string{
		"ubuntu:../../etc",
		"ubuntu:",
		"ubuntu:with spaces",
		"ubuntu:/absolute",
		"ubuntu:" + strings.Repeat("x", 65),
	} {
		if ref, err := ParseRef(in); err == nil {
			t.Errorf("ParseRef(%q) = %s, want an error", in, ref)
		}
	}
}

func TestDefault_IsUbuntu(t *testing.T) {
	if Default.Name != "ubuntu" {
		t.Errorf("Default = %s, want ubuntu (docs/cli.md and ADR-0006)", Default.Name)
	}
}

func TestNames_ListsEverySupportedFamilyInAStableOrder(t *testing.T) {
	got := strings.Join(Names(), ",")
	if got != "arch,fedora,ubuntu" {
		t.Errorf("Names() = %s, want arch,fedora,ubuntu", got)
	}
}

func TestToolingUpdate_UpgradesBothAccountsMiseInstallations(t *testing.T) {
	steps := ToolingUpdate("agent")

	var perUser []UpdateStep
	for _, step := range steps {
		if !step.Root {
			perUser = append(perUser, step)
		}
	}
	if len(perUser) != 1 {
		t.Fatalf("want exactly one unelevated step, the guest user's own mise: %+v", perUser)
	}
	if got := strings.Join(perUser[0].Argv, " "); got != "mise upgrade --yes" {
		t.Errorf("the guest user's step = %q, want mise upgrade --yes", got)
	}
	if !strings.Contains(perUser[0].Name, "agent") {
		t.Errorf("the step name should say whose tools it upgrades: %q", perUser[0].Name)
	}
}

func TestToolingUpdate_DoesNotUpgradeRootsMiseTwiceForARootGuest(t *testing.T) {
	steps := ToolingUpdate("root")
	upgrades := 0
	for _, step := range steps {
		if strings.Join(step.Argv, " ") == "env HOME=/root TMPDIR="+rootMiseTmpDir+" mise upgrade --yes" {
			upgrades++
		}
		if !step.Root {
			t.Errorf("a guest whose user is root has no separate per-user installation: %+v", step)
		}
	}
	if upgrades != 1 {
		t.Errorf("root's mise tools were upgraded %d times, want 1", upgrades)
	}
}

func TestToolingUpdate_SkipsWhatAGuestDoesNotHave(t *testing.T) {
	for _, step := range ToolingUpdate("agent") {
		if step.Requires == "" {
			t.Errorf("%q would fail an update in a guest built from an image without it", step.Name)
		}
	}
}

// TestToolingUpdate_KeepsRootOutOfTheGuestUsersMiseLockDirectory covers the
// ordering hazard in an update: root's mise runs first, and mise's npm backend
// locks each install under $TMPDIR/fslock -- a directory owned by whoever
// created it, at mode 0755. Left at the default /tmp, root's steps create it
// and the guest user's `mise upgrade` after them fails with "failed to acquire
// project lock: Permission denied" before installing anything.
func TestToolingUpdate_KeepsRootOutOfTheGuestUsersMiseLockDirectory(t *testing.T) {
	for _, step := range ToolingUpdate("agent") {
		if step.Requires != "mise" || !step.Root {
			continue
		}
		if !containsArg(step.Argv, "TMPDIR="+rootMiseTmpDir) {
			t.Errorf("%q runs mise as root with the default TMPDIR: %v", step.Name, step.Argv)
		}
	}
	if strings.HasPrefix(rootMiseTmpDir, "/tmp/") || rootMiseTmpDir == "/tmp" {
		t.Errorf("rootMiseTmpDir = %q, which is the shared directory the guest user's mise locks in", rootMiseTmpDir)
	}
}

func containsArg(argv []string, want string) bool {
	for _, arg := range argv {
		if arg == want {
			return true
		}
	}
	return false
}

func TestParseRef_ReadsTheSlimVariantOfAFamily(t *testing.T) {
	// A slim image is the same family and the same source image, built from
	// the family's slim recipe and cached under its own name.
	tests := []struct {
		in            string
		want          string
		imageName     string
		containerfile string
		sourceRef     string
	}{
		{"ubuntu-slim", "ubuntu-slim:24.04", "ubuntu-slim", "ubuntu-slim.Containerfile", "docker.io/library/ubuntu:24.04"},
		{"fedora-slim:41", "fedora-slim:41", "fedora-slim", "fedora-slim.Containerfile", "registry.fedoraproject.org/fedora:41"},
		{"arch-slim", "arch-slim:base", "arch-slim", "arch-slim.Containerfile", "docker.io/library/archlinux:base"},
	}
	for _, tt := range tests {
		ref, err := ParseRef(tt.in)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", tt.in, err)
			continue
		}
		if !ref.Slim {
			t.Errorf("ParseRef(%q) did not select the slim variant", tt.in)
		}
		if ref.String() != tt.want {
			t.Errorf("ParseRef(%q) = %s, want %s", tt.in, ref, tt.want)
		}
		if ref.ImageName() != tt.imageName {
			t.Errorf("ParseRef(%q).ImageName() = %s, want %s", tt.in, ref.ImageName(), tt.imageName)
		}
		if ref.Containerfile() != tt.containerfile {
			t.Errorf("ParseRef(%q).Containerfile() = %s, want %s", tt.in, ref.Containerfile(), tt.containerfile)
		}
		// The slim variant is a different recipe, not a different source: the
		// image it is built from is the family's.
		if ref.SourceRef() != tt.sourceRef {
			t.Errorf("ParseRef(%q).SourceRef() = %s, want %s", tt.in, ref.SourceRef(), tt.sourceRef)
		}
	}
}

func TestParseRef_KeepsAFamilyAndItsSlimVariantApart(t *testing.T) {
	full, err := ParseRef("ubuntu")
	if err != nil {
		t.Fatalf("ParseRef(ubuntu): %v", err)
	}
	slim, err := ParseRef("ubuntu-slim")
	if err != nil {
		t.Fatalf("ParseRef(ubuntu-slim): %v", err)
	}
	// They share a family but never a cache directory or a recipe: both may be
	// cached at once, and rebuilding one must not touch the other.
	if full.ImageName() == slim.ImageName() {
		t.Errorf("ubuntu and ubuntu-slim share the image name %q", full.ImageName())
	}
	if full.Containerfile() == slim.Containerfile() {
		t.Errorf("ubuntu and ubuntu-slim share the recipe %q", full.Containerfile())
	}
	if full.Slim {
		t.Error("ParseRef(ubuntu) selected the slim variant")
	}
}

func TestLookupImage_ReadsBackTheNameARecordCarries(t *testing.T) {
	// vm.json and manifest.json record only the image name, and `agent-vm
	// update` has to find the family from it again.
	for _, tt := range []struct {
		name   string
		family string
		slim   bool
	}{
		{"ubuntu", "ubuntu", false},
		{"ubuntu-slim", "ubuntu", true},
		{"fedora-slim", "fedora", true},
	} {
		d, slim, ok := LookupImage(tt.name)
		if !ok {
			t.Errorf("LookupImage(%q) found nothing", tt.name)
			continue
		}
		if d.Name != tt.family || slim != tt.slim {
			t.Errorf("LookupImage(%q) = %s slim=%v, want %s slim=%v", tt.name, d.Name, slim, tt.family, tt.slim)
		}
	}
	for _, name := range []string{"alpine-slim", "slim", "-slim", "ubuntu-slim-slim"} {
		if d, _, ok := LookupImage(name); ok {
			t.Errorf("LookupImage(%q) = %s, want no match", name, d.Name)
		}
	}
}

func TestImageNames_ListEveryBuildableImage(t *testing.T) {
	got := strings.Join(ImageNames(), " ")
	want := "arch arch-slim fedora fedora-slim ubuntu ubuntu-slim"
	if got != want {
		t.Errorf("ImageNames() = %q, want %q", got, want)
	}
}
