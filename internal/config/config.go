// Package config resolves and validates configuration from four sources —
// built-in defaults, the configuration file, AGENT_VM_* environment variables,
// and command-line flags, in that precedence order — and fails closed before
// any host state changes.
//
// The configuration file keys and environment variable names are a public
// contract (docs/cli.md). Adding a key is fine; renaming or repurposing one is
// a contract change.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
)

// NetworkMode selects how a guest is attached to the network.
type NetworkMode string

const (
	// NetworkNAT is the default: the guest sits behind libvirt's NAT network
	// and is not reachable from the LAN.
	NetworkNAT NetworkMode = "nat"
	// NetworkBridge attaches the guest directly to a host bridge, placing an
	// untrusted guest on the operator's LAN. It is never selected implicitly
	// (SECURITY.md, "Networking Boundaries").
	NetworkBridge NetworkMode = "bridge"
)

// Documented defaults (docs/cli.md, ADR-0007). Changing one is a contract
// change.
const (
	DefaultVCPUs      = 2
	DefaultMemory     = 4 * GiB
	DefaultDisk       = 50 * GiB
	DefaultNetwork    = NetworkNAT
	DefaultLibvirtURI = "qemu:///system"
	DefaultNATNetwork = "agent-vm-nat"
	DefaultGuestUser  = "agent"
)

// Bounds on operator-supplied resource values. These are sanity limits, not
// capacity planning: the host, not this tool, decides what it can actually
// serve (docs/architecture.md, "Constraints And Risks").
const (
	MinVCPUs = 1
	MaxVCPUs = 255
	// MemoryFloor and MemoryCeiling bound every memory value the operator can
	// give, both Memory and MaxMemory. They are deliberately not named
	// Min/MaxMemory: MaxMemory is a per-VM ceiling an operator chooses, and a
	// constant one character away from it would be too easy to reach for by
	// mistake.
	MemoryFloor   = 256 * MiB
	MemoryCeiling = 1024 * GiB
	MinDisk       = 1 * GiB
	MaxDisk       = 8 * TiB
)

// VirtioMemBlock is the granularity virtio-mem plugs memory in. QEMU derives
// the device's block size from the host page size — 2 MiB wherever transparent
// huge pages are 2 MiB, which is every supported host with 4 KiB pages — and
// refuses a device whose size is not a multiple of it. Growth room is checked
// against this up front so the refusal is a usage error here rather than a
// QEMU error halfway through virt-install.
//
// A host with 64 KiB pages (some aarch64 kernels) uses a 512 MiB block and
// will still reject a size this accepts; that failure carries QEMU's own
// message, which names the block size it wanted.
const VirtioMemBlock = 2 * MiB

// Config is the resolved, validated configuration for one run.
type Config struct {
	// ConfigFile is the file that was read, or "" if none existed.
	ConfigFile string
	StateDir   string
	// StateDirIsDefault reports that nothing named a state directory, so
	// StateDir is the built-in default resolved against *this* machine's home
	// directory. That distinction only matters for a remote hypervisor, where
	// the state directory lives on the other machine and this machine's home
	// says nothing about where it is (ADR-0010).
	StateDirIsDefault bool
	LibvirtURI        string
	// ApplianceKernel is a directory on the hypervisor holding a
	// general-purpose kernel for libguestfs to build its appliance from, named
	// after that kernel's version and containing an "Image" and a "modules"
	// tree. It is empty on a host whose own kernel can boot QEMU's virt board,
	// which is every general-purpose distribution kernel; it is needed on a
	// host running a hardware-specific one, where the appliance would
	// otherwise boot to silence (docs/host-setup.md).
	ApplianceKernel string

	Distro distro.Ref
	VCPUs  int
	Memory Size
	// MaxMemory is the ceiling the guest's RAM may be grown to at runtime,
	// through a virtio-mem device sized to the difference from Memory. Zero —
	// the default — means no such device and a fixed-size guest.
	MaxMemory  Size
	Disk       Size
	Network    NetworkMode
	Bridge     string
	NATNetwork string

	GuestUser string
	// SSHKeys are paths to SSH *public* keys. Private key material never
	// enters this tool (SECURITY.md).
	SSHKeys []string
}

// Overrides carries values from one source as the operator wrote them. An
// empty string means "not set by this source", which is what lets a lower
// precedence source show through.
type Overrides struct {
	ConfigFile string
	StateDir   string
	LibvirtURI string

	ApplianceKernel string

	Distro     string
	VCPUs      string
	Memory     string
	MaxMemory  string
	Disk       string
	Network    string
	Bridge     string
	NATNetwork string

	GuestUser string
	SSHKeys   []string
}

// Environ is a lookup of environment variables, so tests do not mutate the
// process environment.
type Environ func(string) string

// SessionURI is the unprivileged libvirt connection. It supports NAT only;
// bridged networking needs qemu:///system.
const SessionURI = "qemu:///session"

// Load resolves configuration from defaults, the configuration file, the
// environment, and flags, then validates the result. Every error it returns is
// a *ValidationError, which the CLI reports as a usage error (exit 2).
func Load(env Environ, flags Overrides) (*Config, error) {
	if env == nil {
		env = os.Getenv
	}

	cfg := defaults()
	fromEnv := environOverrides(env)

	// The config file's location is itself configurable, at flag-then-env
	// precedence, and is resolved before anything is read from the file.
	path, err := configFilePath(env, flags, fromEnv)
	if err != nil {
		return nil, err
	}
	fileOverrides, found, err := loadFile(path)
	if err != nil {
		return nil, err
	}
	if found {
		cfg.ConfigFile = path
	}

	for _, o := range []Overrides{fileOverrides, fromEnv, flags} {
		if err := apply(env, cfg, o); err != nil {
			return nil, err
		}
	}
	if err := cfg.resolveDefaultStateDir(env); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// defaults is the built-in profile. The state directory is filled in only
// after every source has been applied, by resolveDefaultStateDir, because
// locating the default can fail and must not when something else names one.
func defaults() *Config {
	return &Config{
		StateDirIsDefault: true,
		LibvirtURI:        DefaultLibvirtURI,
		Distro:            distro.Ref{Distro: distro.Default, Tag: distro.Default.DefaultTag},
		VCPUs:             DefaultVCPUs,
		Memory:            DefaultMemory,
		Disk:              DefaultDisk,
		Network:           DefaultNetwork,
		NATNetwork:        DefaultNATNetwork,
		GuestUser:         DefaultGuestUser,
	}
}

// resolveDefaultStateDir fills in the built-in state directory when no source
// named one. With no usable home directory there is no default to give, and
// that is reported here rather than as a relative path that would be resolved
// against whatever directory agent-vm happened to start in.
//
// A remote hypervisor is the exception: its default lives under that
// account's home, which the CLI asks the hypervisor for (ADR-0010), so this
// machine's home is not needed and its absence is not an error.
func (c *Config) resolveDefaultStateDir(env Environ) error {
	if !c.StateDirIsDefault {
		return nil
	}
	dir, err := userDataDir(env)
	if err != nil {
		if c.RemoteHypervisor() {
			return nil
		}
		return err
	}
	c.StateDir = filepath.Join(dir, "agent-vm")
	return nil
}

func configFilePath(env Environ, flags, fromEnv Overrides) (string, error) {
	path := fromEnv.ConfigFile
	if flags.ConfigFile != "" {
		path = flags.ConfigFile
	}
	if path == "" {
		dir, err := userConfigDir(env)
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, "agent-vm", "config.toml")
	}
	return expandPath(env, path)
}

// environOverrides reads the documented AGENT_VM_* variables. Unset variables
// stay empty so the configuration file still shows through.
func environOverrides(env Environ) Overrides {
	o := Overrides{
		ConfigFile: env("AGENT_VM_CONFIG"),
		StateDir:   env("AGENT_VM_STATE_DIR"),
		LibvirtURI: env("AGENT_VM_LIBVIRT_URI"),

		ApplianceKernel: env("AGENT_VM_APPLIANCE_KERNEL"),

		Distro:    env("AGENT_VM_DISTRO"),
		VCPUs:     env("AGENT_VM_VCPUS"),
		Memory:    env("AGENT_VM_MEMORY"),
		MaxMemory: env("AGENT_VM_MAX_MEMORY"),
		Disk:      env("AGENT_VM_DISK"),
		Network:   env("AGENT_VM_NETWORK"),
		Bridge:    env("AGENT_VM_BRIDGE"),
	}
	if key := env("AGENT_VM_SSH_KEY"); key != "" {
		o.SSHKeys = []string{key}
	}
	return o
}

// apply layers one source over the config. Values are parsed here, so a bad
// value is attributed to the source that set it.
func apply(env Environ, cfg *Config, o Overrides) error {
	if o.StateDir != "" {
		path, err := expandPath(env, o.StateDir)
		if err != nil {
			return err
		}
		cfg.StateDir = path
		cfg.StateDirIsDefault = false
	}
	if o.LibvirtURI != "" {
		cfg.LibvirtURI = o.LibvirtURI
	}
	if o.ApplianceKernel != "" {
		// "~" is expanded against this machine's home even when the directory
		// lives on a remote hypervisor, for the same reason the state
		// directory is: nothing here can know the remote account's home, so an
		// operator naming a remote path writes it out in full.
		path, err := expandPath(env, o.ApplianceKernel)
		if err != nil {
			return err
		}
		cfg.ApplianceKernel = path
	}
	if o.Distro != "" {
		ref, err := distro.ParseRef(o.Distro)
		if err != nil {
			return &ValidationError{Field: "distro", Value: o.Distro, Err: err}
		}
		cfg.Distro = ref
	}
	if o.VCPUs != "" {
		n, err := strconv.Atoi(strings.TrimSpace(o.VCPUs))
		if err != nil {
			return &ValidationError{Field: "vcpus", Value: o.VCPUs, Err: fmt.Errorf("expected a whole number")}
		}
		cfg.VCPUs = n
	}
	if o.Memory != "" {
		size, err := ParseSize(o.Memory)
		if err != nil {
			return &ValidationError{Field: "memory", Value: o.Memory, Err: err}
		}
		cfg.Memory = size
	}
	if o.MaxMemory != "" {
		size, err := ParseSize(o.MaxMemory)
		if err != nil {
			return &ValidationError{Field: "max_memory", Value: o.MaxMemory, Err: err}
		}
		cfg.MaxMemory = size
	}
	if o.Disk != "" {
		size, err := ParseSize(o.Disk)
		if err != nil {
			return &ValidationError{Field: "disk", Value: o.Disk, Err: err}
		}
		cfg.Disk = size
	}
	if o.Network != "" {
		mode := NetworkMode(strings.ToLower(strings.TrimSpace(o.Network)))
		if mode != NetworkNAT && mode != NetworkBridge {
			return &ValidationError{Field: "network", Value: o.Network, Err: fmt.Errorf("expected %q or %q", NetworkNAT, NetworkBridge)}
		}
		cfg.Network = mode
	}
	if o.Bridge != "" {
		cfg.Bridge = o.Bridge
	}
	if o.NATNetwork != "" {
		cfg.NATNetwork = o.NATNetwork
	}
	if o.GuestUser != "" {
		cfg.GuestUser = o.GuestUser
	}
	if len(o.SSHKeys) > 0 {
		keys := make([]string, 0, len(o.SSHKeys))
		for _, k := range o.SSHKeys {
			path, err := expandPath(env, k)
			if err != nil {
				return err
			}
			keys = append(keys, path)
		}
		cfg.SSHKeys = keys
	}
	return nil
}

// BridgeMode reports whether this configuration puts guests on the LAN.
func (c *Config) BridgeMode() bool { return c.Network == NetworkBridge }

// DefaultStateDirSuffix is the state directory's location under a home
// directory. It is named separately from the default itself so that the same
// layout can be built against a remote account's home.
const DefaultStateDirSuffix = ".local/share/agent-vm"

// ImagesDir and VMsDir are the two top-level areas of the state directory.
func (c *Config) ImagesDir() string { return filepath.Join(c.StateDir, "images") }

// VMsDir is where per-VM state directories live.
func (c *Config) VMsDir() string { return filepath.Join(c.StateDir, "vms") }

// The XDG directories and the home directory are read through the injected
// Environ rather than from the process environment, so that a test resolving
// configuration cannot pick up the config file of whoever is running it.
//
// The XDG Base Directory specification says a relative XDG_*_HOME is invalid
// and must be ignored, so it falls back to the home directory exactly as an
// unset one does.
func userDataDir(env Environ) (string, error) {
	return xdgDir(env, "XDG_DATA_HOME", filepath.Join(".local", "share"), "the default state directory",
		"name the state directory with --state-dir, AGENT_VM_STATE_DIR, or the state_dir config key")
}

func userConfigDir(env Environ) (string, error) {
	return xdgDir(env, "XDG_CONFIG_HOME", ".config", "the default configuration file",
		"name the configuration file with --config or AGENT_VM_CONFIG")
}

func xdgDir(env Environ, variable, underHome, what, alternative string) (string, error) {
	if dir := env(variable); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := homeDir(env)
	if err != nil {
		return "", &ValidationError{
			Field: "HOME", Value: env("HOME"),
			Err:    fmt.Errorf("cannot locate %s: %w, and %s is not set to an absolute path", what, err, variable),
			Remedy: fmt.Sprintf("Set HOME or %s to an absolute path, or %s.", variable, alternative),
		}
	}
	return filepath.Join(home, underHome), nil
}

// homeDir is os.UserHomeDir against the injected environment, which on Linux
// reads exactly this variable. A relative HOME is refused alongside an unset
// one: either would turn every path derived from it into one relative to
// whatever directory agent-vm was started in.
func homeDir(env Environ) (string, error) {
	home := env("HOME")
	switch {
	case home == "":
		return "", errors.New("$HOME is not set")
	case !filepath.IsAbs(home):
		return "", errors.New("$HOME is not an absolute path")
	}
	return home, nil
}

// expandPath resolves a leading "~" and makes the path absolute. It does not
// resolve symlinks — containment checks against the state directory do that,
// at the point of use (internal/state).
func expandPath(env Environ, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := homeDir(env)
		if err != nil {
			return "", &ValidationError{Field: "path", Value: path, Err: fmt.Errorf("cannot resolve ~: %w", err)}
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", &ValidationError{Field: "path", Value: path, Err: err}
	}
	return abs, nil
}
