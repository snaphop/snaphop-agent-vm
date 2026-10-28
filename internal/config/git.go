package config

import (
	"fmt"
	"regexp"
	"strings"
)

// gitDomainLabel is one DNS label. The domain is lowercased before this runs.
var gitDomainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// DefaultGitDomain is the email domain create uses when the operator does
// not name one. The guest's git email is <vm-name>@local (ADR-0026).
const DefaultGitDomain = "local"

// ParseGitDomain returns a DNS domain with no leading or trailing dot, or ""
// when raw is empty. An empty result means the caller should use
// DefaultGitDomain. ".local" is rejected so the email is not "name@.local".
func ParseGitDomain(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, ".") {
		return "", &ValidationError{
			Field: "git domain", Value: raw,
			Err:    fmt.Errorf("must not start with a dot"),
			Remedy: "Use local, not .local. The email is <vm-name>@<domain>.",
		}
	}
	raw = strings.TrimSuffix(raw, ".")
	raw = strings.ToLower(raw)
	if raw == "" || strings.Contains(raw, "..") {
		return "", invalidGitDomain(raw)
	}
	for _, label := range strings.Split(raw, ".") {
		if !gitDomainLabel.MatchString(label) {
			return "", invalidGitDomain(raw)
		}
	}
	if len(raw) > 253 {
		return "", invalidGitDomain(raw)
	}
	return raw, nil
}

func invalidGitDomain(raw string) error {
	return &ValidationError{
		Field: "git domain", Value: raw,
		Err:    fmt.Errorf("must be a DNS domain such as local or example.com"),
		Remedy: "Use labels of letters, digits, and hyphens. The email is <vm-name>@<domain>.",
	}
}
