package hostexec

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Version is a tool version reduced to major.minor.patch. Host tools version
// themselves in enough different ways that anything richer would be a parser
// per tool per release; this is the part we actually compare against a floor.
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion reads a bare "X", "X.Y" or "X.Y.Z" version, ignoring any
// trailing qualifier such as "1.50.1rc1" or "9.6p1".
func ParseVersion(s string) (Version, error) {
	m := bareVersionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, fmt.Errorf("not a version: %q", s)
	}
	v := Version{}
	v.Major, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		v.Minor, _ = strconv.Atoi(m[2])
	}
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
	}
	return v, nil
}

var bareVersionRe = regexp.MustCompile(`^(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// AtLeast reports whether v is min or newer.
func (v Version) AtLeast(min Version) bool {
	switch {
	case v.Major != min.Major:
		return v.Major > min.Major
	case v.Minor != min.Minor:
		return v.Minor > min.Minor
	default:
		return v.Patch >= min.Patch
	}
}

// IsZero reports whether the version is unset.
func (v Version) IsZero() bool { return v == Version{} }

// Tool describes a host tool this project delegates to: how to ask it for its
// version, how to read the answer, and the floor we require. The floors are a
// public contract — raising one can stop the tool working on a host where it
// worked yesterday (AGENTS.md §8).
type Tool struct {
	Name    string
	Package string
	Minimum Version
	// versionArgs asks the tool for its version.
	versionArgs []string
	// versionRe extracts the version from the tool's output; capture group 1 is
	// the version. Narrow by design: a tool that changes its output should fail
	// loudly rather than match something else.
	versionRe *regexp.Regexp
	// stderrVersion marks tools that print their version to stderr.
	stderrVersion bool
	// location is the machine this tool has to be installed on. Almost every
	// one belongs on the hypervisor; ssh and gh belong here, because they use
	// the operator's keys and logins (ADR-0010).
	location Location
}

// Location is the machine this tool runs on.
func (t Tool) Location() Location { return t.location }

// The tools agent-vm requires, with the minimum versions doctor enforces.
// Keep in sync with docs/cli.md (doctor's table) and docs/architecture.md.
var (
	Virsh = Tool{
		Name: "virsh", Package: "libvirt-clients", Minimum: Version{Major: 9},
		versionArgs: []string{"--version"},
		versionRe:   regexp.MustCompile(`^\s*(\d+\.\d+(?:\.\d+)?)\s*$`),
	}
	VirtInstall = Tool{
		Name: "virt-install", Package: "virtinst", Minimum: Version{Major: 4},
		versionArgs: []string{"--version"},
		versionRe:   regexp.MustCompile(`^\s*(\d+\.\d+(?:\.\d+)?)\s*$`),
	}
	QemuImg = Tool{
		Name: "qemu-img", Package: "qemu-utils", Minimum: Version{Major: 8},
		versionArgs: []string{"--version"},
		versionRe:   regexp.MustCompile(`qemu-img version (\d+\.\d+(?:\.\d+)?)`),
	}
	Podman = Tool{
		Name: "podman", Package: "podman", Minimum: Version{Major: 4},
		versionArgs: []string{"--version"},
		versionRe:   regexp.MustCompile(`podman version (\d+\.\d+(?:\.\d+)?)`),
	}
	VirtMakeFS  = libguestfsTool("virt-make-fs")
	VirtLs      = libguestfsTool("virt-ls")
	VirtCopyOut = libguestfsTool("virt-copy-out")
	VirtSysprep = libguestfsTool("virt-sysprep")
	IP          = Tool{
		Name: "ip", Package: "iproute2",
		versionArgs: []string{"-V"},
		versionRe:   regexp.MustCompile(`iproute2-(\d+\.\d+(?:\.\d+)?)`),
	}
	// GH is optional: it is required only for `create --github-ssh-key`,
	// `destroy --github-ssh-key`, and registering or removing a GitHub Actions
	// runner (`create --github-org` on a -runner image). It is not in
	// RequiredTools and doctor never fails a host for its absence.
	GH = Tool{
		Name: "gh", Package: "gh", Minimum: Version{Major: 2},
		versionArgs: []string{"--version"},
		versionRe:   regexp.MustCompile(`gh version (\d+\.\d+(?:\.\d+)?)`),
		location:    Client,
	}
	// SSH is needed here rather than on the hypervisor: it is what reaches
	// the hypervisor in the first place, and what reaches a guest through it.
	SSH = Tool{
		Name: "ssh", Package: "openssh-client",
		versionArgs:   []string{"-V"},
		versionRe:     regexp.MustCompile(`OpenSSH_(\d+\.\d+)`),
		stderrVersion: true,
		location:      Client,
	}
)

// VersionArgs are the arguments that ask this tool for its version. Exposed so
// that a report about an unreadable version can name the exact command to rerun.
func (t Tool) VersionArgs() []string { return append([]string(nil), t.versionArgs...) }

func libguestfsTool(name string) Tool {
	// Debian and Ubuntu split the tools. guestfs-tools provides virt-make-fs,
	// virt-ls, and virt-sysprep. virt-copy-out is a guestfish wrapper and
	// ships in the guestfish package. libguestfs-tools is a metapackage that
	// depends on guestfish; it does not contain the binary.
	pkg := "libguestfs-tools"
	if name == "virt-copy-out" {
		pkg = "guestfish"
	}
	return Tool{
		Name: name, Package: pkg, Minimum: Version{Major: 1, Minor: 50},
		versionArgs: []string{"--version"},
		versionRe:   regexp.MustCompile(regexp.QuoteMeta(name) + ` (\d+\.\d+(?:\.\d+)?)`),
	}
}

// LibguestfsTools are the tools that boot a libguestfs appliance to do their
// work, and so are the ones the appliance kernel environment applies to.
func LibguestfsTools() []Tool {
	return []Tool{VirtMakeFS, VirtLs, VirtCopyOut, VirtSysprep}
}

// IsLibguestfsTool reports whether name is one of them.
func IsLibguestfsTool(name string) bool {
	for _, t := range LibguestfsTools() {
		if t.Name == name {
			return true
		}
	}
	return false
}

// RequiredTools are checked by doctor and resolved once per run. Every one of
// them is a thing we deliberately do not implement ourselves (ADR-0009).
func RequiredTools() []Tool {
	return []Tool{Virsh, VirtInstall, QemuImg, Podman, VirtMakeFS, VirtLs, VirtCopyOut, VirtSysprep, IP, SSH}
}

// OptionalTools are tools only some flags need. doctor reports them so an
// operator can see whether those flags will work, but a host without them is
// still a working host.
func OptionalTools() []Tool {
	return []Tool{GH}
}

// Versions resolves and caches tool versions for the lifetime of one run, so a
// command that touches five tools does not probe any of them twice. Resolved
// versions are recorded in vm.json and manifest.json, which is what makes a
// "worked last month" regression traceable to a host tool upgrade.
type Versions struct {
	runner Runner

	mu    sync.Mutex
	cache map[string]versionResult
}

type versionResult struct {
	version Version
	err     error
}

// NewVersions returns a version resolver backed by runner.
func NewVersions(runner Runner) *Versions {
	return &Versions{runner: runner, cache: map[string]versionResult{}}
}

// Get returns tool's version, probing it at most once per run. A missing tool
// yields *NotFoundError and unreadable output yields *ParseError.
func (v *Versions) Get(ctx context.Context, tool Tool) (Version, error) {
	v.mu.Lock()
	if got, ok := v.cache[tool.Name]; ok {
		v.mu.Unlock()
		return got.version, got.err
	}
	v.mu.Unlock()

	version, err := v.probe(ctx, tool)

	v.mu.Lock()
	v.cache[tool.Name] = versionResult{version: version, err: err}
	v.mu.Unlock()
	return version, err
}

// Require returns tool's version, or *VersionError if it is below the floor.
func (v *Versions) Require(ctx context.Context, tool Tool) (Version, error) {
	got, err := v.Get(ctx, tool)
	if err != nil {
		return got, err
	}
	if !tool.Minimum.IsZero() && !got.AtLeast(tool.Minimum) {
		return got, &VersionError{Tool: tool.Name, Found: got, Minimum: tool.Minimum}
	}
	return got, nil
}

func (v *Versions) probe(ctx context.Context, tool Tool) (Version, error) {
	if _, err := v.runner.LookPath(tool.Name, tool.location); err != nil {
		var nf *NotFoundError
		if errors.As(err, &nf) {
			// LookPath does not know which package ships the tool; we do.
			return Version{}, &NotFoundError{Tool: nf.Tool, Package: tool.Package}
		}
		return Version{}, err
	}

	res, err := v.runner.Run(ctx, Command{
		Name:     tool.Name,
		Args:     tool.versionArgs,
		Effect:   Read,
		Location: tool.location,
	})
	// ssh -V exits non-zero on some builds while still printing its version, so
	// the output is parsed before the exit status is judged.
	out := ""
	if res != nil {
		out = string(res.Stdout)
		if tool.stderrVersion {
			out = string(res.Stderr)
		}
	}
	if parsed, perr := parseToolVersion(tool, out); perr == nil {
		return parsed, nil
	}
	if err != nil {
		return Version{}, err
	}
	return Version{}, &ParseError{Tool: tool.Name, What: "version", Output: out}
}

func parseToolVersion(tool Tool, out string) (Version, error) {
	m := tool.versionRe.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return Version{}, &ParseError{Tool: tool.Name, What: "version", Output: out}
	}
	return ParseVersion(m[1])
}
