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
		Swap      string `toml:"swap"`
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

	// Git names the domain in the guest's git email, <vm-name>@<domain>.
	// The name is the VM name and is not a config value (ADR-0026).
	Git struct {
		Domain string `toml:"domain"`
	} `toml:"git"`

	// PiHole names the DNS server create --pihole registers a guest on.
	// The password is a file path, never the password itself (ADR-0021).
	PiHole struct {
		URL          string `toml:"url"`
		PasswordFile string `toml:"password_file"`
		Domain       string `toml:"domain"`
		Insecure     bool   `toml:"insecure"`
	} `toml:"pihole"`

	// Tailscale names the auth key file and the other join settings. The
	// key itself is not a config value (ADR-0014). These settings do not
	// request a join; create --tailscale does.
	Tailscale struct {
		AuthKeyFile   string   `toml:"auth_key_file"`
		Hostname      string   `toml:"hostname"`
		LoginServer   string   `toml:"login_server"`
		AdvertiseTags []string `toml:"advertise_tags"`
		Ephemeral     bool     `toml:"ephemeral"`
		SSH           bool     `toml:"ssh"`
	} `toml:"tailscale"`
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
		StateDir:               file.StateDir,
		LibvirtURI:             file.LibvirtURI,
		ApplianceKernel:        file.ApplianceKernel,
		Distro:                 file.Defaults.Distro,
		Memory:                 file.Defaults.Memory,
		MaxMemory:              file.Defaults.MaxMemory,
		Disk:                   file.Defaults.Disk,
		Swap:                   file.Defaults.Swap,
		Network:                file.Defaults.Network,
		Bridge:                 file.Network.Bridge.Interface,
		NATNetwork:             file.Network.NAT.Name,
		GuestUser:              file.Guest.User,
		SSHKeys:                file.Guest.SSHKeys,
		GitHubOrg:              file.GitHub.Org,
		GitDomain:              file.Git.Domain,
		PiHoleURL:              file.PiHole.URL,
		PiHolePasswordFile:     file.PiHole.PasswordFile,
		PiHoleDomain:           file.PiHole.Domain,
		TailscaleAuthKeyFile:   file.Tailscale.AuthKeyFile,
		TailscaleHostname:      file.Tailscale.Hostname,
		TailscaleLoginServer:   file.Tailscale.LoginServer,
		TailscaleAdvertiseTags: file.Tailscale.AdvertiseTags,
	}
	// Whether vcpus was written is asked of the file, not inferred from the
	// value: `vcpus = 0` is a mistake to report, not an absent key.
	if meta.IsDefined("defaults", "vcpus") {
		o.VCPUs = strconv.Itoa(file.Defaults.VCPUs)
	}
	// An absent insecure key must not override a higher-precedence true: a
	// bool cannot tell "left out" from "set to false".
	if meta.IsDefined("pihole", "insecure") {
		o.PiHoleInsecure = strconv.FormatBool(file.PiHole.Insecure)
	}
	// An absent bool must not override a higher-precedence true.
	if meta.IsDefined("tailscale", "ephemeral") {
		o.TailscaleEphemeral = strconv.FormatBool(file.Tailscale.Ephemeral)
	}
	if meta.IsDefined("tailscale", "ssh") {
		o.TailscaleSSH = strconv.FormatBool(file.Tailscale.SSH)
	}
	return o, true, nil
}
