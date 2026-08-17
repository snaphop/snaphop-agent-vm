package config

import (
	"errors"
	"fmt"
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
		Distro  string `toml:"distro"`
		VCPUs   int    `toml:"vcpus"`
		Memory  string `toml:"memory"`
		Disk    string `toml:"disk"`
		Network string `toml:"network"`
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

// WriteExample writes a commented starter configuration file. It never
// overwrites an existing file.
func WriteExample(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	return os.WriteFile(path, []byte(exampleConfig), 0o644)
}

const exampleConfig = `# agent-vm configuration. Every value is optional; the
# defaults in docs/cli.md apply when a key is absent.

# state_dir   = "~/.local/share/agent-vm"
# libvirt_uri = "qemu:///system"

[defaults]
# distro  = "ubuntu"
# vcpus   = 2
# memory  = "4G"
# disk    = "50G"
# network = "nat"

[network.nat]
# name = "agent-vm-nat"

[network.bridge]
# Bridged networking puts the guest on your LAN. It is never the default and
# always needs an explicit --network bridge.
# interface = "br0"

[guest]
# user     = "agent"
# ssh_keys = ["~/.ssh/id_ed25519.pub"]   # public keys only
`
