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
