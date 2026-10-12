package hostsetup

import (
	"regexp"
	"strings"
	"testing"
)

func TestVirtAAHelperPlainRule_RejectsPathsTheProfileCannotExpress(t *testing.T) {
	t.Parallel()
	_, err := VirtAAHelperPlainRule("/home/operator/.local/my vm")
	if err == nil || !strings.Contains(err.Error(), "my vm") {
		t.Fatalf("error = %v", err)
	}
	_, err = VirtAAHelperPlainRule("/home/operator/.local/caf\u00e9")
	if err == nil || !strings.Contains(err.Error(), "non-ASCII") {
		t.Fatalf("error = %v", err)
	}
	got, err := VirtAAHelperPlainRule("/home/foo-bar/.local/share/agent-vm")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/foo-bar/.local/share/agent-vm/** rk," {
		t.Fatalf("rule = %q", got)
	}
}

func TestNarrowShippedHiddenDenies_RemovesOnlyTheShippedLines(t *testing.T) {
	t.Parallel()
	updated, changed, err := narrowShippedHiddenDenies(shippedVirtAAProfile, "/home/wensington/.local/share/agent-vm")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("profile was already narrowed")
	}
	if strings.Contains(updated, "deny @{HOME}/.*/**") || strings.Contains(updated, "deny @{HOME}/.* mrwkl") {
		t.Fatalf("shipped deny still present:\n%s", updated)
	}
	if strings.Count(updated, virtAADenyBegin) != 1 || !strings.Contains(updated, virtAADenyEnd) {
		t.Fatalf("marker:\n%s", updated)
	}
	if !strings.Contains(updated, "audit deny @{HOME}/bin/** mrwkl,") || !strings.Contains(updated, "@{HOME}/** r,") {
		t.Fatalf("kept rules:\n%s", updated)
	}
	again, changed, err := narrowShippedHiddenDenies(updated, "/home/wensington/.local/share/agent-vm")
	if err != nil || changed || again != updated {
		t.Fatalf("second narrow changed=%v err=%v", changed, err)
	}
}

func TestNarrowShippedHiddenDenies_RemovesTheOneLineTheDetectorSaw(t *testing.T) {
	t.Parallel()
	updated, changed, err := narrowShippedHiddenDenies(shippedVirtAADeny, "/home/operator/.local/share/agent-vm")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if strings.Contains(updated, "deny @{HOME}/.*/**") || !strings.Contains(updated, virtAADenyBegin) {
		t.Fatalf("profile =\n%s", updated)
	}
}

func TestNarrowShippedHiddenDenies_RefusesADifferentWording(t *testing.T) {
	t.Parallel()
	const stateDir = "/home/operator/.local/share/agent-vm"
	_, _, err := narrowShippedHiddenDenies("  audit deny @{HOME}/.*/** mrwklx,\n", stateDir)
	if err == nil || !strings.Contains(err.Error(), stateDir) || !strings.Contains(err.Error(), "shipped three lines") {
		t.Fatal(err)
	}
}

func TestNarrowShippedHiddenDenies_ReportsAMarkerWithoutAnEnd(t *testing.T) {
	t.Parallel()
	_, _, err := narrowShippedHiddenDenies("  "+virtAADenyBegin+"\n  @{HOME}/** r,\n", "/home/operator/.local/share/agent-vm")
	if err == nil || !strings.Contains(err.Error(), virtAADenyEnd) {
		t.Fatal(err)
	}
}

func TestNarrowShippedHiddenDenies_ReplacesADenyRestoredUnderTheMarker(t *testing.T) {
	t.Parallel()
	profile := "  " + virtAADenyBegin + "\n  " + virtAADenyNote + "\n  " + virtAADenyEnd + "\n" + shippedVirtAAProfile
	updated, changed, err := narrowShippedHiddenDenies(profile, "/home/wensington/.local/share/agent-vm")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if strings.Count(updated, virtAADenyBegin) != 1 || strings.Contains(updated, "deny @{HOME}/.*/**") {
		t.Fatalf("profile =\n%s", updated)
	}
	if !strings.Contains(updated, "audit deny @{HOME}/bin/** mrwkl,") {
		t.Fatalf("bin deny was dropped:\n%s", updated)
	}
}

func TestComplementGlobs_MatchEveryOtherShortName(t *testing.T) {
	t.Parallel()
	// Length 3 is already tens of thousands of names; length 4 is not a unit
	// test. Longer components are checked through their neighbours below.
	for _, literal := range []string{"a", "ab"} {
		globs := complementGlobs(literal)
		if matchAny(globs, literal) {
			t.Fatalf("complement of %q matches itself: %q", literal, globs)
		}
		upper := len(literal) + 1
		if upper > 2 {
			upper = 2
		}
		for n := 1; n <= upper; n++ {
			eachName(n, func(name string) {
				if name == literal {
					return
				}
				if !matchAny(globs, name) {
					t.Fatalf("no complement of %q matches %q", literal, name)
				}
			})
		}
	}
}

func TestComplementGlobs_CoverTheNeighboursOfARealComponent(t *testing.T) {
	t.Parallel()
	for _, literal := range []string{"wensington", "share", "agent-vm", "foo-bar", "local"} {
		globs := complementGlobs(literal)
		if matchAny(globs, literal) {
			t.Fatalf("complement of %q matches itself", literal)
		}
		for i := 0; i < len(literal); i++ {
			buf := []byte(literal)
			buf[i] ^= 1
			if buf[i] < 0x21 || buf[i] >= 0x7f || buf[i] == '/' {
				buf[i] = 'Z'
			}
			if string(buf) != literal && !matchAny(globs, string(buf)) {
				t.Fatalf("no complement of %q matches %q", literal, buf)
			}
		}
		if !matchAny(globs, literal+"x") || !matchAny(globs, literal+"-old") {
			t.Fatalf("longer names of %q stay allowed", literal)
		}
		if len(literal) > 1 && !matchAny(globs, literal[:len(literal)-1]) {
			t.Fatalf("proper prefix of %q stays allowed", literal)
		}
	}
}

func TestDotComplement_LeavesTheHiddenNameOut(t *testing.T) {
	t.Parallel()
	globs := dotComplement(".local")
	if matchAny(globs, ".local") {
		t.Fatal("dot complement matches .local")
	}
	for _, other := range []string{".", ".ssh", ".gnupg", ".localX", ".loca", ".locale", ".."} {
		if !matchAny(globs, other) {
			t.Errorf("dot complement does not match %q", other)
		}
	}
	if matchAny(globs, "local") || matchAny(globs, ".local/x") {
		t.Fatal("dot complement matched outside one component")
	}
	onlyDot := dotComplement(".")
	if matchAny(onlyDot, ".") || !matchAny(onlyDot, ".a") {
		t.Fatalf("complement of %q = %q", ".", onlyDot)
	}
}

func TestAAREMatch_CoversTheGlobsTheCarveEmits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"/home/wensington/.local/**", "/home/wensington/.local/share", true},
		{"/home/wensington/.local/**", "/home/wensington/.local", false},
		{"/home/wensington/.local/**", "/home/wensington/.local/", true},
		{"[^s]*", "share", false},
		{"[^s]*", "hare", true},
		{"[^s]*", "", false},
		{"share?*", "share", false},
		{"share?*", "shareX", true},
		{"s", "share", false},
		{"s", "s", true},
		{"foo[^-]*", "foo-bar", false},
		{"foo[^-]*", "fooXbar", true},
		{"/home/*/.*", "/home/other/.ssh", true},
		{"/home/*/.*", "/home/other/ssh", false},
		{"/home/*/.*", "/home/other/.ssh/id", false},
		{"/home/*/.*/**", "/home/other/.ssh/id", true},
		{"/home/*/.*/**", "/home/other/Documents/id", false},
		{"/home/foo[^-]*/.*", "/home/foo-bar/.ssh", false},
		{"/home/foo[^-]*/.*", "/home/foobar/.ssh", true},
	}
	for _, tc := range cases {
		if got := aareMatch(tc.pattern, tc.path); got != tc.want {
			t.Errorf("aareMatch(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestHiddenHomeExceptDenies_LeavesOnlyTheStateDirectory(t *testing.T) {
	t.Parallel()
	const state = "/home/wensington/.local/share/agent-vm"
	assertCarve(t, state,
		[]string{
			state,
			state + "/",
			state + "/locks/image.qcow2",
			state + "/a/b",
			"/home/wensington/Documents/file",
		},
		[]string{
			"/home/wensington/.ssh/id_rsa",
			"/home/wensington/.ssh/",
			"/home/wensington/.gnupg/private",
			"/home/wensington/.local",
			"/home/wensington/.local/",
			"/home/wensington/.local/share",
			"/home/wensington/.local/share/",
			"/home/wensington/.local/share/keyrings/x",
			"/home/wensington/.local/bin/tool",
			"/home/wensington/.config/agent/x",
			"/home/wensington/.local/share/agent-vm-old/x",
			"/home/wensington/.local/share/agent-vm.bak/x",
			"/home/other/.local/share/agent-vm/x",
			"/home/a/.ssh/id",
			"/root/.local/share/agent-vm/x",
			"/root/.ssh/id",
			"/home/wensington/.local/share/agent-v/x",
		},
	)
}

func TestHiddenHomeExceptDenies_CoversARootStateDirectory(t *testing.T) {
	t.Parallel()
	const state = "/root/.cache/agent-vm"
	assertCarve(t, state,
		[]string{state, state + "/", state + "/locks/image.qcow2", "/home/someone/Documents/file"},
		[]string{"/root/.ssh/id", "/root/.cache", "/root/.cache/", "/home/someone/.ssh/id", "/root/.cache/agent-vm-old/x"},
	)
}

func TestHiddenHomeExceptDenies_AllowsTheHiddenDirectoryWhenItIsTheStateDirectory(t *testing.T) {
	t.Parallel()
	const state = "/home/wensington/.local"
	assertCarve(t, state,
		[]string{state, state + "/", state + "/share/keyrings/x", state + "/share/agent-vm/base.qcow2"},
		[]string{"/home/wensington/.ssh/id_rsa", "/home/other/.local/x", "/root/.ssh/id"},
	)
}

func TestHiddenHomeExceptDenies_CoversShortNamesAndHyphenatedUsers(t *testing.T) {
	t.Parallel()
	assertCarve(t, "/home/a/.b/c",
		[]string{"/home/a/.b/c", "/home/a/.b/c/file", "/home/a/Documents/file"},
		[]string{"/home/a/.ssh/id", "/home/a/.b", "/home/a/.b/", "/home/b/.b/c", "/home/aa/.b/c"},
	)
	assertCarve(t, "/home/foo-bar/.local/share/agent-vm",
		[]string{"/home/foo-bar/.local/share/agent-vm/locks/x", "/home/foo-bar/Documents/file"},
		[]string{
			"/home/foo-bar/.ssh/id",
			"/home/foobar/.local/share/agent-vm/x",
			"/home/foo-baz/.local/x",
			"/home/foo-bar/.local/share/agent-vm-extra/x",
		},
	)
}

func assertCarve(t *testing.T, stateDir string, allowed, denied []string) {
	t.Helper()
	rules, err := hiddenHomeExceptDenies(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) == 0 || len(rules) >= 500 {
		t.Fatalf("%s produced %d rules", stateDir, len(rules))
	}
	line := regexp.MustCompile(`^audit deny .+ (mrwkl|rw),$`)
	for _, rule := range rules {
		if !line.MatchString(rule) || strings.Contains(rule, "priority=") || strings.Contains(rule, "{") {
			t.Fatalf("rule %q", rule)
		}
	}
	allow, err := VirtAAHelperPlainRule(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	allowPattern := strings.TrimSuffix(allow, " rk,")
	for _, path := range allowed {
		if deniedBy(rules, path) {
			t.Errorf("%s denies %s", stateDir, path)
		}
	}
	for _, path := range denied {
		if !deniedBy(rules, path) {
			t.Errorf("%s allows %s", stateDir, path)
		}
	}
	file := stateDir + "/locks/image.qcow2"
	if !aareMatch(allowPattern, file) || !aareMatch(allowPattern, stateDir+"/") {
		t.Errorf("plain allow %s does not cover files under the state directory", allow)
	}
	if aareMatch(allowPattern, stateDir) {
		t.Errorf("plain allow %s matches the directory inode; @{HOME}/** r is what grants that read", allow)
	}
}

func deniedBy(rules []string, path string) bool {
	for _, rule := range rules {
		pattern := strings.TrimPrefix(rule, "audit deny ")
		if i := strings.LastIndex(pattern, " "); i >= 0 {
			pattern = pattern[:i]
		}
		if aareMatch(pattern, path) {
			return true
		}
	}
	return false
}

func matchAny(globs []string, name string) bool {
	for _, glob := range globs {
		if aareMatch(glob, name) {
			return true
		}
	}
	return false
}

func eachName(length int, visit func(string)) {
	buf := make([]byte, length)
	var rec func(int)
	rec = func(n int) {
		if n == length {
			visit(string(buf))
			return
		}
		for c := byte(0x21); c < 0x7f; c++ {
			if c == '/' {
				continue
			}
			buf[n] = c
			rec(n + 1)
		}
	}
	rec(0)
}

// aareMatch is a test double for the AppArmor glob subset the carve emits.
// The kernel test is the authority when the two disagree.
func aareMatch(pattern, path string) bool {
	return matchAARE(pattern, path)
}

func matchAARE(pattern, path string) bool {
	if pattern == "" {
		return path == ""
	}
	// ** and * match an empty remainder, so they run before an empty path is
	// rejected. share?* matches shareX only because the last * matches nothing.
	if strings.HasPrefix(pattern, "**") {
		rest := pattern[2:]
		for i := 0; i <= len(path); i++ {
			if matchAARE(rest, path[i:]) {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(pattern, "*") {
		rest := pattern[1:]
		for i := 0; i <= len(path); i++ {
			if i > 0 && path[i-1] == '/' {
				break
			}
			if matchAARE(rest, path[i:]) {
				return true
			}
		}
		return false
	}
	if path == "" {
		return false
	}
	switch pattern[0] {
	case '?':
		if path[0] == '/' {
			return false
		}
		return matchAARE(pattern[1:], path[1:])
	case '[':
		end := strings.IndexByte(pattern, ']')
		if end < 1 {
			return false
		}
		if !classMatches(pattern[1:end], path[0]) {
			return false
		}
		return matchAARE(pattern[end+1:], path[1:])
	default:
		if pattern[0] != path[0] {
			return false
		}
		return matchAARE(pattern[1:], path[1:])
	}
}

func classMatches(class string, c byte) bool {
	if c == '/' || class == "" {
		return false
	}
	negate := false
	if class[0] == '^' {
		negate = true
		class = class[1:]
	}
	matched := false
	for i := 0; i < len(class); i++ {
		if class[i] == c {
			matched = true
			break
		}
	}
	if negate {
		return !matched
	}
	return matched
}
