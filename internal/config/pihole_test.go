package config

import (
	"strings"
	"testing"
)

func TestParsePiHoleURL(t *testing.T) {
	t.Parallel()
	got, err := ParsePiHoleURL("https://pi.hole/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://pi.hole" {
		t.Fatalf("URL = %q", got)
	}
	got, err = ParsePiHoleURL("http://192.168.1.2:8080")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://192.168.1.2:8080" {
		t.Fatalf("URL = %q", got)
	}
	for _, raw := range []string{
		"",
		"pi.hole",
		"ftp://pi.hole",
		"https://user:secret@pi.hole",
		"https://pi.hole/admin",
		"https://pi.hole?x=1",
		"https://pi.hole/#top",
	} {
		if _, err := ParsePiHoleURL(raw); err == nil {
			t.Errorf("ParsePiHoleURL(%q) succeeded", raw)
		}
	}
}

func TestParsePiHoleDomain(t *testing.T) {
	t.Parallel()
	got, err := ParsePiHoleDomain("")
	if err != nil || got != "" {
		t.Fatalf("empty domain = %q, %v", got, err)
	}
	got, err = ParsePiHoleDomain("Agent.")
	if err != nil {
		t.Fatal(err)
	}
	if got != "agent" {
		t.Fatalf("domain = %q, want agent", got)
	}
	got, err = ParsePiHoleDomain("Lab.Internal")
	if err != nil {
		t.Fatal(err)
	}
	if got != "lab.internal" {
		t.Fatalf("domain = %q", got)
	}
	_, err = ParsePiHoleDomain(".agent")
	if err == nil || !strings.Contains(err.Error(), "Use agent, not .agent.") {
		t.Fatalf("leading dot = %v", err)
	}
	if _, err := ParsePiHoleDomain("not a domain"); err == nil {
		t.Fatal("a space was accepted")
	}
}

func TestParseBoolFlag(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"true", "YES", "on", "1"} {
		got, err := parseBoolFlag("pi-hole insecure", raw)
		if err != nil || !got {
			t.Errorf("parseBoolFlag(%q) = %v, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"false", "no", "off", "0"} {
		got, err := parseBoolFlag("pi-hole insecure", raw)
		if err != nil || got {
			t.Errorf("parseBoolFlag(%q) = %v, %v", raw, got, err)
		}
	}
	if _, err := parseBoolFlag("pi-hole insecure", "maybe"); err == nil {
		t.Fatal("parseBoolFlag accepted maybe")
	}
}
