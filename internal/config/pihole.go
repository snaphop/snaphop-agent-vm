package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// piholeLabel is one DNS label. The domain is lowercased before this runs.
var piholeLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// DefaultPiHoleDomain is the DNS domain create uses when the operator does
// not name one. The record is <vm-name>.agent. The value has no leading dot.
const DefaultPiHoleDomain = "agent"

// ParsePiHoleURL returns the base URL of a Pi-hole, without a trailing slash.
// It must be http or https, with a host, and with no user, query, fragment,
// or path. The password is not part of the URL.
func ParsePiHoleURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	invalid := err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != ""
	if invalid || (parsed.Path != "" && parsed.Path != "/") {
		return "", &ValidationError{
			Field: "pi-hole url", Value: raw,
			Err:    fmt.Errorf("must be an http or https URL with a host, no user info, no query, and no path"),
			Remedy: "Pass a base URL such as https://pi.hole or http://192.168.1.2.",
		}
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

// ParsePiHoleDomain returns a DNS domain with no leading or trailing dot, or
// "" when raw is empty. An empty result means the caller should use
// DefaultPiHoleDomain. ".agent" is rejected so the record is not "..agent".
func ParsePiHoleDomain(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.HasPrefix(raw, ".") {
		return "", &ValidationError{
			Field: "pi-hole domain", Value: raw,
			Err:    fmt.Errorf("must not start with a dot"),
			Remedy: "Use agent, not .agent.",
		}
	}
	raw = strings.TrimSuffix(raw, ".")
	raw = strings.ToLower(raw)
	if raw == "" || strings.Contains(raw, "..") {
		return "", invalidPiHoleDomain(raw)
	}
	for _, label := range strings.Split(raw, ".") {
		if !piholeLabel.MatchString(label) {
			return "", invalidPiHoleDomain(raw)
		}
	}
	if len(raw) > 253 {
		return "", invalidPiHoleDomain(raw)
	}
	return raw, nil
}

func invalidPiHoleDomain(raw string) error {
	return &ValidationError{
		Field: "pi-hole domain", Value: raw,
		Err:    fmt.Errorf("must be a DNS domain such as agent or lab.internal"),
		Remedy: "Use labels of letters, digits, and hyphens. Do not start the domain with a dot.",
	}
}

// parseBoolFlag accepts the spellings a flag, an environment variable, and a
// TOML bool (rendered as true or false) can arrive as.
func parseBoolFlag(field, raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, &ValidationError{
			Field: field, Value: raw,
			Err:    fmt.Errorf("expected true or false"),
			Remedy: "Use true, false, yes, no, on, off, 1, or 0.",
		}
	}
}
