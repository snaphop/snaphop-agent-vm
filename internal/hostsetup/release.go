package hostsetup

import (
	"fmt"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
)

// Release is one supported host operating system.
type Release struct {
	// Family is ubuntu, fedora, or arch.
	Family string
	// Version is the os-release VERSION_ID for Ubuntu and Fedora. It is empty
	// for Arch: that family's guest tags name container images of the rolling
	// release, and a host reports a build stamp that is not one of those tags.
	Version string
	// Name is the text an operator should see, usually PRETTY_NAME.
	Name string
}

// UnsupportedOSError is a host whose os-release is not one of the supported
// guest releases. A derivative that only sets ID_LIKE is unsupported: Debian
// is not Ubuntu, and an Arch derivative is not Arch.
type UnsupportedOSError struct {
	ID        string
	VersionID string
	Name      string
}

func (e *UnsupportedOSError) Error() string {
	host := e.Name
	if host == "" {
		host = e.ID
		if e.VersionID != "" {
			host += " " + e.VersionID
		}
	}
	if host == "" {
		host = "an unrecognized operating system"
	}
	return fmt.Sprintf("this host is %s. agent-vm setup supports the same releases as the guest images: %s",
		host, SupportedSummary())
}

// UnsupportedArchError is a machine that is neither x86_64 nor aarch64.
type UnsupportedArchError struct {
	Arch string
}

func (e *UnsupportedArchError) Error() string {
	return fmt.Sprintf("this host is %s. agent-vm setup supports x86_64 and aarch64", e.Arch)
}

// SupportedSummary names the releases setup accepts, in the order the guest
// families list their supported tags.
func SupportedSummary() string {
	parts := make([]string, 0, 2)
	for _, name := range []string{distro.Ubuntu.Name, distro.Fedora.Name} {
		d, ok := distro.Lookup(name)
		if !ok {
			continue
		}
		parts = append(parts, title(name)+" "+joinAnd(d.SupportedTags))
	}
	return strings.Join(parts, ", ") + ", and Arch Linux"
}

// ParseOSRelease reads the KEY=VALUE lines of an os-release file.
func ParseOSRelease(text string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
	}
	return fields
}

// MatchRelease accepts a parsed os-release when it is a supported guest
// release. Ubuntu and Fedora must name a supported VERSION_ID. Arch is the
// rolling release: ID=arch is enough, and VERSION_ID is a build stamp rather
// than a guest tag.
func MatchRelease(fields map[string]string) (Release, error) {
	id := fields["ID"]
	version := fields["VERSION_ID"]
	name := fields["PRETTY_NAME"]
	switch id {
	case distro.Ubuntu.Name, distro.Fedora.Name:
		if !supportedVersion(id, version) {
			return Release{}, &UnsupportedOSError{ID: id, VersionID: version, Name: name}
		}
		if name == "" {
			name = title(id) + " " + version
		}
		return Release{Family: id, Version: version, Name: name}, nil
	case distro.Arch.Name:
		if name == "" {
			name = "Arch Linux"
		}
		return Release{Family: id, Name: name}, nil
	default:
		return Release{}, &UnsupportedOSError{ID: id, VersionID: version, Name: name}
	}
}

func supportedVersion(family, version string) bool {
	d, ok := distro.Lookup(family)
	if !ok {
		return false
	}
	for _, tag := range d.SupportedTags {
		if tag == version {
			return true
		}
	}
	return false
}

func unquote(value string) string {
	if len(value) >= 2 {
		if value[0] == '"' && value[len(value)-1] == '"' {
			return value[1 : len(value)-1]
		}
		if value[0] == '\'' && value[len(value)-1] == '\'' {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func title(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}
