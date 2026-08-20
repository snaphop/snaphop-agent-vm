package config

import (
	"errors"
	"io/fs"
	"os"
	"strconv"

	"github.com/BurntSushi/toml"
)

// fileConfig mirrors ~/.config/agent-vm/config.toml. Its keys are a public
// contract (docs/cli.md). Scalars are decoded as their natural types and
// converted to the string form Overrides carries, so one parser handles a
// value however it arrived.
type fileConfig struct {
	StateDir   string `toml:"state_dir"`
	LibvirtURI string `toml:"libvirt_uri"`

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
	if _, err := toml.Decode(string(contents), &file); err != nil {
		return Overrides{}, false, &ValidationError{
			Field: "config file", Value: path, Err: err,
			Remedy: "See docs/cli.md for the supported keys.",
		}
	}

	o := Overrides{
		StateDir:   file.StateDir,
		LibvirtURI: file.LibvirtURI,
		Distro:     file.Defaults.Distro,
		Memory:     file.Defaults.Memory,
		MaxMemory:  file.Defaults.MaxMemory,
		Disk:       file.Defaults.Disk,
		Network:    file.Defaults.Network,
		Bridge:     file.Network.Bridge.Interface,
		NATNetwork: file.Network.NAT.Name,
		GuestUser:  file.Guest.User,
		SSHKeys:    file.Guest.SSHKeys,
	}
	if file.Defaults.VCPUs != 0 {
		o.VCPUs = strconv.Itoa(file.Defaults.VCPUs)
	}
	return o, true, nil
}
