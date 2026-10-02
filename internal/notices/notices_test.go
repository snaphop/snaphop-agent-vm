package notices

import (
	"os"
	"strings"
	"testing"
)

// The root files are what a release attaches. The copies embedded here are what
// the binary contains. They have to stay the same text.
func TestEmbeddedTextsMatchRepoRoot(t *testing.T) {
	t.Parallel()
	for _, name := range []struct {
		file     string
		embedded string
	}{
		{"LICENSE", License},
		{"NOTICE", Notice},
	} {
		root, err := os.ReadFile("../../" + name.file)
		if err != nil {
			t.Fatalf("reading %s: %v", name.file, err)
		}
		if string(root) != name.embedded {
			t.Errorf("embedded %s differs from the copy at the repository root", name.file)
		}
	}
}

// The phrases below are the parts of each upstream license that the license
// itself says must accompany a copy: the copyright line and the permission
// terms.
func TestNoticeCarriesTheRequiredPermissionNotices(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"Copyright (c) 2013 TOML authors",
		"Permission is hereby granted, free of charge",
		"The above copyright notice and this permission notice shall be included in",
		"Copyright 2009 The Go Authors.",
		"Redistributions in binary form must reproduce the above",
		"Additional IP Rights Grant (Patents)",
	} {
		if !strings.Contains(Notice, want) {
			t.Errorf("NOTICE is missing %q", want)
		}
	}
	if !strings.Contains(License, "Copyright (c) 2026 SnapHop") {
		t.Error("LICENSE is missing the SnapHop copyright")
	}
	if !strings.Contains(License, "Permission is hereby granted, free of charge") {
		t.Error("LICENSE is missing the MIT permission notice")
	}
}

// A module added to go.mod is linked into the binary, so its name has to appear
// in NOTICE, which is the prompt to paste that module's license text in too.
func TestNoticeNamesEveryModuleRequirement(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	mods := requiredModules(string(data))
	if len(mods) == 0 {
		t.Fatal("parsed no requirements from go.mod")
	}
	for _, mod := range mods {
		if !strings.Contains(Notice, mod) {
			t.Errorf("NOTICE does not name module %s", mod)
		}
	}
}

func TestRequiredModulesParsesSingleAndBlockForms(t *testing.T) {
	t.Parallel()
	got := requiredModules(`
module example.com/mod

go 1.22

require github.com/BurntSushi/toml v1.6.0

require (
	github.com/example/one v1.2.3
	github.com/example/two v0.1.0 // indirect
)
`)
	want := []string{
		"github.com/BurntSushi/toml",
		"github.com/example/one",
		"github.com/example/two",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("requiredModules = %v, want %v", got, want)
	}
}

// requiredModules returns the module paths in a go.mod require directive, in
// either the single-line form or the parenthesized block. A comment after a
// version is ignored.
func requiredModules(gomod string) []string {
	var mods []string
	inBlock := false
	for _, line := range strings.Split(gomod, "\n") {
		line, _, _ = strings.Cut(line, "//")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "require (" {
			inBlock = true
			continue
		}
		if inBlock && line == ")" {
			inBlock = false
			continue
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 3 && fields[0] == "require" && strings.HasPrefix(fields[2], "v"):
			mods = append(mods, fields[1])
		case inBlock && len(fields) >= 2 && strings.HasPrefix(fields[1], "v"):
			mods = append(mods, fields[0])
		}
	}
	return mods
}
