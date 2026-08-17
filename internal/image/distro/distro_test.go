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
