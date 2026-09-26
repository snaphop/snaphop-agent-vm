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
	sawMise := false
	for _, step := range ToolingUpdate("agent") {
		// The mkdir that creates the directory is a mise prerequisite, not a
		// mise invocation, and it does not itself consult TMPDIR.
		if step.Requires != "mise" || !step.Root || !containsArg(step.Argv, "mise") {
			continue
		}
		sawMise = true
		if !containsArg(step.Argv, "TMPDIR="+rootMiseTmpDir) {
			t.Errorf("%q runs mise as root with the default TMPDIR: %v", step.Name, step.Argv)
		}
	}
	if !sawMise {
		t.Fatal("root's mise steps disappeared")
	}
	if strings.HasPrefix(rootMiseTmpDir, "/tmp/") || rootMiseTmpDir == "/tmp" {
		t.Errorf("rootMiseTmpDir = %q, which is the shared directory the guest user's mise locks in", rootMiseTmpDir)
	}
}

// TestToolingUpdate_CreatesTheMiseTmpDirBeforeSelfUpdate covers mise
// self-update failing with "No such file or directory" at
// $TMPDIR/.tmpXXXXXX. The self_update crate creates that tempfile directly in
// $TMPDIR and does not create the directory, and a guest has no
// /root/.cache/mise-tmp until an update makes one.
func TestToolingUpdate_CreatesTheMiseTmpDirBeforeSelfUpdate(t *testing.T) {
	steps := ToolingUpdate("agent")
	mkdirAt, selfUpdateAt := -1, -1
	for i, step := range steps {
		joined := strings.Join(step.Argv, " ")
		if joined == "mkdir -p -- "+rootMiseTmpDir {
			mkdirAt = i
			if !step.Root {
				t.Errorf("creating %s has to run as root, which is the only account that can write under /root", rootMiseTmpDir)
			}
			if step.Requires != "mise" {
				t.Errorf("the directory is only needed in a guest that has mise; Requires = %q", step.Requires)
			}
		}
		if strings.Contains(joined, "mise self-update") {
			selfUpdateAt = i
		}
	}
	if mkdirAt < 0 {
		t.Fatalf("root's TMPDIR %s is never created", rootMiseTmpDir)
	}
	if selfUpdateAt < 0 {
		t.Fatal("no mise self-update step")
	}
	if mkdirAt > selfUpdateAt {
		t.Errorf("mkdir at step %d runs after mise self-update at step %d", mkdirAt, selfUpdateAt)
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

func TestParseRef_ReadsTheVariantsOfAFamily(t *testing.T) {
	// A variant is the same family and the same source image, built from that
	// family's variant recipe and cached under its own name.
	tests := []struct {
		in            string
		variant       Variant
		want          string
		imageName     string
		containerfile string
		sourceRef     string
	}{
		{"ubuntu-slim", Slim, "ubuntu-slim:24.04", "ubuntu-slim", "ubuntu-slim.Containerfile", "docker.io/library/ubuntu:24.04"},
		{"fedora-slim:41", Slim, "fedora-slim:41", "fedora-slim", "fedora-slim.Containerfile", "registry.fedoraproject.org/fedora:41"},
		{"arch-slim", Slim, "arch-slim:base", "arch-slim", "arch-slim.Containerfile", "docker.io/library/archlinux:base"},
		{"ubuntu-nix", Nix, "ubuntu-nix:24.04", "ubuntu-nix", "ubuntu-nix.Containerfile", "docker.io/library/ubuntu:24.04"},
		{"fedora-nix:41", Nix, "fedora-nix:41", "fedora-nix", "fedora-nix.Containerfile", "registry.fedoraproject.org/fedora:41"},
		{"arch-nix", Nix, "arch-nix:base", "arch-nix", "arch-nix.Containerfile", "docker.io/library/archlinux:base"},
	}
	for _, tt := range tests {
		ref, err := ParseRef(tt.in)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", tt.in, err)
			continue
		}
		if ref.Variant != tt.variant {
			t.Errorf("ParseRef(%q).Variant = %q, want %q", tt.in, ref.Variant, tt.variant)
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
		// A variant is a different recipe, not a different source: the image
		// it is built from is the family's.
		if ref.SourceRef() != tt.sourceRef {
			t.Errorf("ParseRef(%q).SourceRef() = %s, want %s", tt.in, ref.SourceRef(), tt.sourceRef)
		}
	}
}

func TestParseRef_KeepsAFamilyAndItsVariantsApart(t *testing.T) {
	// Every variant of a family shares the family but never a cache directory
	// or a recipe: all three may be cached at once, and rebuilding one must not
	// touch the others.
	names, recipes := map[string]string{}, map[string]string{}
	for _, name := range []string{"ubuntu", "ubuntu-slim", "ubuntu-nix"} {
		ref, err := ParseRef(name)
		if err != nil {
			t.Fatalf("ParseRef(%s): %v", name, err)
		}
		if other, seen := names[ref.ImageName()]; seen {
			t.Errorf("%s and %s share the image name %q", other, name, ref.ImageName())
		}
		if other, seen := recipes[ref.Containerfile()]; seen {
			t.Errorf("%s and %s share the recipe %q", other, name, ref.Containerfile())
		}
		names[ref.ImageName()], recipes[ref.Containerfile()] = name, name
	}

	full, err := ParseRef("ubuntu")
	if err != nil {
		t.Fatalf("ParseRef(ubuntu): %v", err)
	}
	if full.Variant != Full {
		t.Errorf("ParseRef(ubuntu).Variant = %q, want the full recipe", full.Variant)
	}
}

func TestLookupImage_ReadsBackTheNameARecordCarries(t *testing.T) {
	// vm.json and manifest.json record only the image name, and `agent-vm
	// update` has to find the family from it again.
	for _, tt := range []struct {
		name    string
		family  string
		variant Variant
	}{
		{"ubuntu", "ubuntu", Full},
		{"ubuntu-slim", "ubuntu", Slim},
		{"fedora-slim", "fedora", Slim},
		{"ubuntu-nix", "ubuntu", Nix},
		{"arch-nix", "arch", Nix},
	} {
		d, variant, ok := LookupImage(tt.name)
		if !ok {
			t.Errorf("LookupImage(%q) found nothing", tt.name)
			continue
		}
		if d.Name != tt.family || variant != tt.variant {
			t.Errorf("LookupImage(%q) = %s variant=%q, want %s variant=%q", tt.name, d.Name, variant, tt.family, tt.variant)
		}
	}
	for _, name := range []string{"alpine-slim", "slim", "-slim", "ubuntu-slim-slim", "nix", "-nix", "alpine-nix", "ubuntu-nix-nix"} {
		if d, _, ok := LookupImage(name); ok {
			t.Errorf("LookupImage(%q) = %s, want no match", name, d.Name)
		}
	}
}

func TestImageNames_ListEveryBuildableImage(t *testing.T) {
	got := strings.Join(ImageNames(), " ")
	want := "arch arch-slim arch-nix fedora fedora-slim fedora-nix ubuntu ubuntu-slim ubuntu-nix"
	if got != want {
		t.Errorf("ImageNames() = %q, want %q", got, want)
	}
}
