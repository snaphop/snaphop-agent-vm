package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// fileConfig mirrors ~/.config/agent-vm/config.toml. Its keys are a public
// contract (docs/cli.md). Scalars are decoded as their natural types and
// converted to the string form Overrides carries, so one parser handles a
// value however it arrived.
type fileConfig struct {
	StateDir        string `toml:"state_dir"`
	LibvirtURI      string `toml:"libvirt_uri"`
	ApplianceKernel string `toml:"appliance_kernel"`

	Defaults struct {
		Distro    string `toml:"distro"`
		VCPUs     int    `toml:"vcpus"`
		Memory    string `toml:"memory"`
		MaxMemory string `toml:"max_memory"`
		Disk      string `toml:"disk"`
		Network   string `toml:"network"`
	} `toml:"defaults"`

	Network struct {
		NAT struct {
			Name string `toml:"name"`
		} `toml:"nat"`
		Bridge struct {
			Interface string `toml:"interface"`
		} `toml:"bridge"`
	} `toml:"network"`

	Guest struct {
		User    string   `toml:"user"`
		SSHKeys []string `toml:"ssh_keys"`
	} `toml:"guest"`

	// GitHub names the organization create registers a -runner VM with.
	// The registration token is not a config value: it is fetched at create
	// time and lives only for that request (ADR-0015).
	GitHub struct {
		Org string `toml:"org"`
	} `toml:"github"`
}

// loadFile reads the configuration file at path. A missing file is not an
// error — the file is optional — but an unreadable or malformed one is, because
// silently ignoring it would run with settings the operator thinks they changed.
func loadFile(path string) (Overrides, bool, error) {
	if path == "" {
		return Overrides{}, false, nil
	}

	contents, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Overrides{}, false, nil
	case err != nil:
		return Overrides{}, false, &ValidationError{Field: "config file", Value: path, Err: err}
	}

	var file fileConfig
	meta, err := toml.Decode(string(contents), &file)
	if err != nil {
		return Overrides{}, false, &ValidationError{
			Field: "config file", Value: path, Err: err,
			Remedy: "See docs/cli.md for the supported keys.",
		}
	}
	// A key this build does not know is refused rather than skipped: a
	// misspelled `netwrok = "nat"` or `[defualts]` would otherwise leave the
	// operator running with a setting they believe they changed.
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		return Overrides{}, false, &ValidationError{
			Field: "config file", Value: path,
			Err:    fmt.Errorf("unknown keys: %s", strings.Join(keys, ", ")),
			Remedy: "Check their spelling against the keys docs/cli.md lists, and remove any this version of agent-vm does not support.",
		}
	}

	o := Overrides{
		StateDir:        file.StateDir,
		LibvirtURI:      file.LibvirtURI,
		ApplianceKernel: file.ApplianceKernel,
		Distro:          file.Defaults.Distro,
		Memory:          file.Defaults.Memory,
		MaxMemory:       file.Defaults.MaxMemory,
		Disk:            file.Defaults.Disk,
		Network:         file.Defaults.Network,
		Bridge:          file.Network.Bridge.Interface,
		NATNetwork:      file.Network.NAT.Name,
		GuestUser:       file.Guest.User,
		SSHKeys:         file.Guest.SSHKeys,
		GitHubOrg:       file.GitHub.Org,
	}
	// Whether vcpus was written is asked of the file, not inferred from the
	// value: `vcpus = 0` is a mistake to report, not an absent key.
	if meta.IsDefined("defaults", "vcpus") {
		o.VCPUs = strconv.Itoa(file.Defaults.VCPUs)
	}
	return o, true, nil
}
