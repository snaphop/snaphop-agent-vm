package hostsetup

import (
	"fmt"
	"strings"
)

// hiddenHomeExceptDenies is the shipped virt-aa-helper deny of @{HOME}/.*/**
// with one state directory left out. AppArmor 4.0 has no priority= qualifier,
// and on that parser a later allow cannot override a deny, so the deny itself
// has to stop covering the directory. @{HOME} is /home/*/ and /root/, which is
// what the shipped profile and tunables/home describe.
//
// Every other path the shipped deny covers stays denied. Paths it does not
// cover, such as /home/<user>/Documents, stay allowed.
func hiddenHomeExceptDenies(stateDir string) ([]string, error) {
	cleaned, err := cleanAppArmorPath(stateDir)
	if err != nil {
		return nil, err
	}
	if !StateDirUnderHiddenHome(cleaned) {
		return nil, fmt.Errorf("state directory %q is not covered by virt-aa-helper's hidden-home deny", stateDir)
	}
	parts := strings.Split(cleaned, "/")
	var rules []string
	if parts[1] == "home" {
		rules = append(rules, denyRootHidden()...)
		rules = append(rules, deniesForHome(parts[2], parts[3], parts[4:])...)
	} else {
		rules = append(rules, denyAllHomeUsers()...)
		rules = append(rules, deniesForRoot(parts[2], parts[3:])...)
	}
	return rules, nil
}

func denyAllHomeUsers() []string {
	return []string{
		"audit deny /home/*/.* mrwkl,",
		"audit deny /home/*/.*/ rw,",
		"audit deny /home/*/.*/** mrwkl,",
	}
}

func denyRootHidden() []string {
	return []string{
		"audit deny /root/.* mrwkl,",
		"audit deny /root/.*/ rw,",
		"audit deny /root/.*/** mrwkl,",
	}
}

// deniesForHome carves /home/<user>/<hidden>/... . Other users keep the
// shipped deny. This user's other hidden directories keep it too. The hidden
// directory named here stays denied when the state directory is below it;
// only the state directory itself is left out.
func deniesForHome(user, hidden string, rest []string) []string {
	var rules []string
	for _, glob := range complementGlobs(user) {
		rules = append(rules, denyHiddenAt("/home/"+glob)...)
	}
	rules = append(rules, deniesUnderHidden("/home/"+user, hidden, rest)...)
	return rules
}

func deniesForRoot(hidden string, rest []string) []string {
	return deniesUnderHidden("/root", hidden, rest)
}

// deniesUnderHidden carves base/<hidden>/rest, where base is /home/<user> or
// /root. hidden matches the shipped .* component.
func deniesUnderHidden(base, hidden string, rest []string) []string {
	var rules []string
	for _, glob := range dotComplement(hidden) {
		rules = append(rules, denyTree(base+"/"+glob)...)
	}
	hiddenPath := base + "/" + hidden
	if len(rest) == 0 {
		// The state directory is the hidden directory. Everything under it
		// is the exception, and the directory itself is too.
		return rules
	}
	rules = append(rules, denyNode(hiddenPath)...)
	rules = append(rules, denyUnderExcept(hiddenPath, rest)...)
	return rules
}

// denyUnderExcept denies everything under prefix except prefix/rest[0]/rest[1]/...
func denyUnderExcept(prefix string, rest []string) []string {
	if len(rest) == 0 {
		return nil
	}
	var rules []string
	for _, glob := range complementGlobs(rest[0]) {
		rules = append(rules, denyTree(prefix+"/"+glob)...)
	}
	if len(rest) == 1 {
		return rules
	}
	next := prefix + "/" + rest[0]
	rules = append(rules, denyNode(next)...)
	rules = append(rules, denyUnderExcept(next, rest[1:])...)
	return rules
}

// denyHiddenAt is the shipped trio for one home directory: the hidden
// component, that component as a directory, and everything under it.
func denyHiddenAt(home string) []string {
	return []string{
		"audit deny " + home + "/.* mrwkl,",
		"audit deny " + home + "/.*/ rw,",
		"audit deny " + home + "/.*/** mrwkl,",
	}
}

// denyNode denies the path itself and the directory form, not its children.
func denyNode(path string) []string {
	return []string{
		"audit deny " + path + " mrwkl,",
		"audit deny " + path + "/ rw,",
	}
}

// denyTree denies the path, the directory form, and everything under it.
func denyTree(path string) []string {
	return []string{
		"audit deny " + path + " mrwkl,",
		"audit deny " + path + "/ rw,",
		"audit deny " + path + "/** mrwkl,",
	}
}

// complementGlobs matches every name except literal. The names are a single
// path component: no slash, and no AppArmor glob character. AARE has no
// complement operator, so the set is built from "differs at this character",
// "longer than literal", and the proper prefixes.
func complementGlobs(literal string) []string {
	globs := make([]string, 0, len(literal)*2+1)
	for i := 0; i < len(literal); i++ {
		globs = append(globs, literal[:i]+classNot(literal[i])+"*")
	}
	globs = append(globs, literal+"?*")
	for i := 1; i < len(literal); i++ {
		globs = append(globs, literal[:i])
	}
	return globs
}

// dotComplement matches every hidden component (the shipped .*) except literal.
// literal includes its leading dot.
func dotComplement(literal string) []string {
	suffix := strings.TrimPrefix(literal, ".")
	globs := complementGlobs(suffix)
	out := make([]string, 0, len(globs)+1)
	for _, glob := range globs {
		out = append(out, "."+glob)
	}
	// complementGlobs does not produce the empty string, so a component that
	// is only "." would otherwise stay allowed. The shipped .* matches it.
	// When the exception itself is that component, it must not be denied.
	if literal != "." {
		out = append(out, ".")
	}
	return out
}

// classNot matches one character other than c. A hyphen sits at the end of
// the class so it is literal rather than a range.
func classNot(c byte) string {
	if c == '-' {
		return "[^-]"
	}
	return "[^" + string(c) + "]"
}
