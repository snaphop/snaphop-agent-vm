package config

import (
	"strings"
	"testing"
)

func TestParseGitDomain(t *testing.T) {
	t.Parallel()
	got, err := ParseGitDomain("")
	if err != nil || got != "" {
		t.Fatalf("empty domain = %q, %v", got, err)
	}
	got, err = ParseGitDomain("Local.")
	if err != nil {
		t.Fatal(err)
	}
	if got != "local" {
		t.Fatalf("domain = %q, want local", got)
	}
	got, err = ParseGitDomain("Lab.Example")
	if err != nil {
		t.Fatal(err)
	}
	if got != "lab.example" {
		t.Fatalf("domain = %q", got)
	}
	_, err = ParseGitDomain(".local")
	if err == nil || !strings.Contains(err.Error(), "Use local, not .local.") {
		t.Fatalf("leading dot = %v", err)
	}
	for _, raw := range []string{"not a domain", "example.com/foo", "-bad.com", "bad-.com", ".."} {
		if _, err := ParseGitDomain(raw); err == nil {
			t.Errorf("ParseGitDomain(%q) succeeded", raw)
		}
	}
}
