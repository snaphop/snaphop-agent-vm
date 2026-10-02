package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/notices"
)

func TestLicenses_PrintsTheLicenseAndThirdPartyNotices(t *testing.T) {
	code, stdout, stderr := run(t, "licenses")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0:\n%s", code, stderr)
	}
	if stdout != notices.License+"\n"+notices.Notice {
		t.Errorf("stdout is not the LICENSE file, a blank line, and the NOTICE file")
	}
	for _, want := range []string{
		"Copyright (c) 2026 SnapHop",
		"Copyright (c) 2013 TOML authors",
		"Copyright 2009 The Go Authors.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q", want)
		}
	}
}

func TestLicenses_JSONReportsTheLicenseAndNoticeSeparately(t *testing.T) {
	code, stdout, stderr := run(t, "--output", "json", "licenses")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0:\n%s", code, stderr)
	}
	var got struct {
		License string `json:"license"`
		Notice  string `json:"notice"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decoding licenses JSON: %v\n%s", err, stdout)
	}
	if got.License != notices.License {
		t.Error("license field is not the LICENSE file")
	}
	if got.Notice != notices.Notice {
		t.Error("notice field is not the NOTICE file")
	}
}

func TestLicenses_RejectsAnArgument(t *testing.T) {
	code, _, stderr := run(t, "licenses", "extra")

	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr, "unexpected argument") {
		t.Errorf("stderr does not explain the rejection:\n%s", stderr)
	}
}

func TestLicenses_IsListedInGlobalHelp(t *testing.T) {
	code, _, stderr := run(t, "--help")

	if code != ExitOK {
		t.Fatalf("exit code = %d, want 0:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "licenses") || !strings.Contains(stderr, "print the license and third-party notices") {
		t.Errorf("global help does not list licenses:\n%s", stderr)
	}
}
