package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noEnv is an environment in which nothing is set but a home directory that
// holds nothing: no AGENT_VM_* variable and no XDG_* directory. Configuration
// resolution reads the environment only through an Environ, so a test using
// this cannot pick up the configuration file of whoever is running it.
func noEnv(key string) string {
	if key == "HOME" {
		return emptyHome
	}
	return ""
}

// emptyHome is an absolute home directory that does not exist, so the default
// configuration file under it is simply absent.
const emptyHome = "/nonexistent/agent-vm-test-home"

// envMap turns a map into an Environ, so tests never mutate the real
// environment.
func envMap(vars map[string]string) Environ {
	return func(key string) string { return vars[key] }
}

// writeConfig writes a config file into a temp dir and returns its path.
func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestLoad_DefaultsMatchTheDocumentedProfile(t *testing.T) {
	t.Parallel()
	// These are the defaults docs/cli.md and ADR-0007 promise. Changing one is
	// a contract change, so this test is meant to fail loudly.
	cfg, err := Load(noEnv, Overrides{ConfigFile: filepath.Join(t.TempDir(), "absent.toml")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.VCPUs != 2 {
		t.Errorf("VCPUs = %d, want 2", cfg.VCPUs)
	}
	if cfg.Memory.String() != "4G" {
		t.Errorf("Memory = %s, want 4G", cfg.Memory)
	}
	if cfg.Disk.String() != "50G" {
		t.Errorf("Disk = %s, want 50G", cfg.Disk)
	}
	// Swap stays off so `agent-vm create` keeps producing the machine ADR-0007
	// describes. A size here would write a swap file into every guest.
	if cfg.Swap != 0 {
		t.Errorf("Swap = %s, want it unset", cfg.Swap)
	}
	if cfg.Network != NetworkNAT {
		t.Errorf("Network = %s, want nat", cfg.Network)
	}
	if cfg.Distro.String() != "ubuntu:26.04" {
		t.Errorf("Distro = %s, want ubuntu:26.04", cfg.Distro)
	}
	if cfg.LibvirtURI != "qemu:///system" {
		t.Errorf("LibvirtURI = %s, want qemu:///system", cfg.LibvirtURI)
	}
	if cfg.GuestUser != "agent" {
		t.Errorf("GuestUser = %s, want agent", cfg.GuestUser)
	}
	if cfg.NATNetwork != "agent-vm-nat" {
		t.Errorf("NATNetwork = %s, want agent-vm-nat", cfg.NATNetwork)
	}
	if cfg.GitHubOrg != "" {
		t.Errorf("GitHubOrg = %q, want empty", cfg.GitHubOrg)
	}
	if cfg.GitDomain != DefaultGitDomain {
		t.Errorf("GitDomain = %q, want %s", cfg.GitDomain, DefaultGitDomain)
	}
}

func TestLoad_PrecedenceIsDefaultsThenFileThenEnvThenFlags(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
[defaults]
distro = "fedora"
vcpus  = 8
memory = "16G"
disk   = "100G"
`)

	cfg, err := Load(
		envMap(map[string]string{
			"HOME":            emptyHome,
			"AGENT_VM_VCPUS":  "4",
			"AGENT_VM_MEMORY": "8G",
		}),
		Overrides{ConfigFile: path, VCPUs: "16"},
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.VCPUs != 16 {
		t.Errorf("VCPUs = %d, want 16 (flag beats env and file)", cfg.VCPUs)
	}
	if cfg.Memory.String() != "8G" {
		t.Errorf("Memory = %s, want 8G (env beats file)", cfg.Memory)
	}
	if cfg.Disk.String() != "100G" {
		t.Errorf("Disk = %s, want 100G (file beats defaults)", cfg.Disk)
	}
	if cfg.Distro.String() != "fedora:44" {
		t.Errorf("Distro = %s, want fedora:44 (file beats defaults)", cfg.Distro)
	}
}

func TestLoad_MissingConfigFileIsNotAnError(t *testing.T) {
	t.Parallel()
	cfg, err := Load(noEnv, Overrides{ConfigFile: filepath.Join(t.TempDir(), "nope.toml")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ConfigFile != "" {
		t.Errorf("ConfigFile = %q, want empty when no file exists", cfg.ConfigFile)
	}
}

func TestLoad_MalformedConfigFileIsAValidationError(t *testing.T) {
	t.Parallel()
	// A malformed file must not be silently ignored: the operator believes
	// their settings are in effect.
	path := writeConfig(t, "this is not = = toml\n")

	_, err := Load(noEnv, Overrides{ConfigFile: path})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want *ValidationError", err)
	}
}

func TestLoad_RejectsUnsupportedDistro(t *testing.T) {
	t.Parallel()
	_, err := Load(noEnv, Overrides{Distro: "alpine"})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want *ValidationError", err)
	}
	if verr.Field != "distro" {
		t.Errorf("Field = %q, want %q", verr.Field, "distro")
	}
}

func TestLoad_MaxMemoryIsUnsetByDefault(t *testing.T) {
	t.Parallel()
	cfg, err := Load(noEnv, Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Zero is what keeps a VM's domain identical to what it was before growable
	// memory existed, so the default has to stay zero.
	if cfg.MaxMemory != 0 {
		t.Errorf("MaxMemory = %s, want it unset", cfg.MaxMemory)
	}
}

func TestLoad_SwapFollowsTheResourcePrecedence(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
[defaults]
swap = "2G"
`)

	fromFile, err := Load(noEnv, Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load file: %v", err)
	}
	if fromFile.Swap != 2*GiB {
		t.Errorf("Swap = %s, want 2G from the config file", fromFile.Swap)
	}

	fromEnv, err := Load(envMap(map[string]string{
		"HOME":            emptyHome,
		"AGENT_VM_SWAP":   "4G",
		"AGENT_VM_CONFIG": path,
	}), Overrides{})
	if err != nil {
		t.Fatalf("Load env: %v", err)
	}
	if fromEnv.Swap != 4*GiB {
		t.Errorf("Swap = %s, want 4G (env beats the file)", fromEnv.Swap)
	}

	fromFlag, err := Load(envMap(map[string]string{
		"HOME":          emptyHome,
		"AGENT_VM_SWAP": "4G",
	}), Overrides{Swap: "8G"})
	if err != nil {
		t.Fatalf("Load flag: %v", err)
	}
	if fromFlag.Swap != 8*GiB {
		t.Errorf("Swap = %s, want 8G (flag beats env)", fromFlag.Swap)
	}

	// Zero is a size, so a flag of 0 turns a configured swap file off for
	// this one VM.
	cleared, err := Load(envMap(map[string]string{
		"HOME":          emptyHome,
		"AGENT_VM_SWAP": "4G",
	}), Overrides{Swap: "0"})
	if err != nil {
		t.Fatalf("Load cleared: %v", err)
	}
	if cleared.Swap != 0 {
		t.Errorf("Swap = %s, want 0 to clear the environment", cleared.Swap)
	}
}

func TestLoad_AcceptsASwapThatLeavesTheRootFilesystemAGiB(t *testing.T) {
	t.Parallel()
	cfg, err := Load(noEnv, Overrides{Disk: "50G", Swap: "49G"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Swap != 49*GiB {
		t.Errorf("Swap = %s, want 49G", cfg.Swap)
	}
}

func TestLoad_AcceptsAMaxMemoryAboveTheBootMemory(t *testing.T) {
	t.Parallel()
	cfg, err := Load(noEnv, Overrides{Memory: "4G", MaxMemory: "16G"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxMemory != 16*GiB {
		t.Errorf("MaxMemory = %s, want 16G", cfg.MaxMemory)
	}
}

func TestLoad_RejectsResourcesOutsideBounds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		flags Overrides
		field string
	}{
		{"zero vcpus", Overrides{VCPUs: "0"}, "vcpus"},
		{"too many vcpus", Overrides{VCPUs: "9999"}, "vcpus"},
		{"vcpus not a number", Overrides{VCPUs: "many"}, "vcpus"},
		{"memory below floor", Overrides{Memory: "16M"}, "memory"},
		{"memory not a size", Overrides{Memory: "lots"}, "memory"},
		{"max memory below the boot memory", Overrides{Memory: "4G", MaxMemory: "2G"}, "max_memory"},
		{"max memory equal to the boot memory", Overrides{Memory: "4G", MaxMemory: "4G"}, "max_memory"},
		{"max memory above the ceiling", Overrides{MaxMemory: "2048G"}, "max_memory"},
		{"max memory not a size", Overrides{MaxMemory: "plenty"}, "max_memory"},
		{"growth room is not a whole virtio-mem block", Overrides{Memory: "4G", MaxMemory: "4097M"}, "max_memory"},
		{"disk below floor", Overrides{Disk: "100M"}, "disk"},
		{"unknown unit", Overrides{Disk: "50X"}, "disk"},
		{"swap not a size", Overrides{Swap: "plenty"}, "swap"},
		{"swap below a mebibyte", Overrides{Swap: "512K"}, "swap"},
		{"swap is not a whole number of mebibytes", Overrides{Swap: "1500K"}, "swap"},
		{"swap leaves the root filesystem less than 1G", Overrides{Disk: "50G", Swap: "50G"}, "swap"},
		{"swap on the smallest disk", Overrides{Disk: "1G", Swap: "1M"}, "swap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(noEnv, tt.flags)

			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("got %v, want *ValidationError", err)
			}
			if verr.Field != tt.field {
				t.Errorf("Field = %q, want %q", verr.Field, tt.field)
			}
		})
	}
}

func TestLoad_BridgeModeRequiresABridge(t *testing.T) {
	t.Parallel()
	// Refusing is the point: falling back to NAT would silently change the
	// guest's exposure, and inferring a bridge would do the opposite.
	_, err := Load(noEnv, Overrides{Network: "bridge"})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want *ValidationError", err)
	}
	if verr.Field != "bridge" {
		t.Errorf("Field = %q, want %q", verr.Field, "bridge")
	}
}

func TestLoad_BridgeModeIsRefusedOnSessionURI(t *testing.T) {
	t.Parallel()
	_, err := Load(noEnv, Overrides{Network: "bridge", Bridge: "br0", LibvirtURI: SessionURI})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want *ValidationError", err)
	}
	if verr.Field != "network" {
		t.Errorf("Field = %q, want %q", verr.Field, "network")
	}
}

func TestLoad_BridgeModeIsNeverSelectedImplicitly(t *testing.T) {
	t.Parallel()
	// Configuring a bridge is not the same as asking for bridged networking.
	path := writeConfig(t, "[network.bridge]\ninterface = \"br0\"\n")

	cfg, err := Load(noEnv, Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Network != NetworkNAT {
		t.Errorf("Network = %s, want nat: a configured bridge must not select bridged mode", cfg.Network)
	}
}

func TestLoad_RejectsUnknownNetworkMode(t *testing.T) {
	t.Parallel()
	_, err := Load(noEnv, Overrides{Network: "host"})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want *ValidationError", err)
	}
}

// A test that resolves configuration must not read the config file of the
// account running it: the operator's own file sets a network mode, a bridge and
// resource sizes, so leaking it makes results depend on whose machine the suite
// runs on. Every environment and home-directory lookup therefore goes through
// the injected Environ rather than the process environment. Both lookups that
// locate the file are covered, because either one leaking is enough.
func TestLoad_IgnoresTheProcessEnvironment(t *testing.T) {
	// A configuration that would be impossible to miss if it were read.
	const contents = `[defaults]
network = "bridge"
vcpus = 64

[network.bridge]
interface = "br-leaked"
`
	tests := []struct {
		name string
		env  map[string]string
	}{
		// XDG_CONFIG_HOME unset, so the file is located through $HOME.
		{name: "via HOME", env: map[string]string{"XDG_CONFIG_HOME": ""}},
		{name: "via XDG_CONFIG_HOME"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			configHome := filepath.Join(home, ".config")
			dir := filepath.Join(configHome, "agent-vm")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("creating config dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(contents), 0o600); err != nil {
				t.Fatalf("writing config: %v", err)
			}

			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			// Confirm the fixture is where the process environment says it is,
			// so a passing test cannot mean "the file was never there".
			if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".config", "agent-vm", "config.toml")); err != nil {
				t.Fatalf("fixture not in place: %v", err)
			}

			cfg, err := Load(noEnv, Overrides{})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.ConfigFile != "" {
				t.Errorf("read %s from the process environment; Environ is the only source", cfg.ConfigFile)
			}
			if cfg.Network != NetworkNAT {
				t.Errorf("Network = %q, want %q — bridged mode is never implicit", cfg.Network, NetworkNAT)
			}
			if cfg.Bridge != "" {
				t.Errorf("Bridge = %q, want empty", cfg.Bridge)
			}
			if cfg.VCPUs != DefaultVCPUs {
				t.Errorf("VCPUs = %d, want the default %d", cfg.VCPUs, DefaultVCPUs)
			}
		})
	}
}

func TestLoad_ExpandsTildeInPaths(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	cfg, err := Load(envMap(map[string]string{"HOME": home}), Overrides{StateDir: "~/agent-vm-state"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home, "agent-vm-state"); cfg.StateDir != want {
		t.Errorf("StateDir = %s, want %s", cfg.StateDir, want)
	}
}

func TestLoad_EnvNamesMatchTheDocumentedContract(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envMap(map[string]string{
		"HOME":                 emptyHome,
		"AGENT_VM_STATE_DIR":   "/srv/agent-vm",
		"AGENT_VM_LIBVIRT_URI": SessionURI,
		"AGENT_VM_DISTRO":      "arch",
		"AGENT_VM_VCPUS":       "6",
		"AGENT_VM_MEMORY":      "2G",
		"AGENT_VM_DISK":        "20G",
		"AGENT_VM_SWAP":        "8G",
		"AGENT_VM_NETWORK":     "nat",
		"AGENT_VM_GITHUB_ORG":  "acme",
	}), Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.StateDir != "/srv/agent-vm" {
		t.Errorf("StateDir = %s", cfg.StateDir)
	}
	if cfg.LibvirtURI != SessionURI {
		t.Errorf("LibvirtURI = %s", cfg.LibvirtURI)
	}
	if cfg.Distro.String() != "arch:base-20260927.0.600689" {
		t.Errorf("Distro = %s, want arch:base-20260927.0.600689", cfg.Distro)
	}
	if cfg.VCPUs != 6 || cfg.Memory.String() != "2G" || cfg.Disk.String() != "20G" || cfg.Swap.String() != "8G" {
		t.Errorf("resources = %d vcpu / %s / %s / swap %s", cfg.VCPUs, cfg.Memory, cfg.Disk, cfg.Swap)
	}
	if cfg.GitHubOrg != "acme" {
		t.Errorf("GitHubOrg = %q, want acme", cfg.GitHubOrg)
	}
}

// The appliance kernel is a host property rather than a per-VM one, so it has
// no flag: the configuration file and the environment are the whole surface.
func TestLoad_ApplianceKernelFromTheConfigFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`appliance_kernel = "/opt/kernels/6.12.4-arch1-1"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(noEnv, Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ApplianceKernel != "/opt/kernels/6.12.4-arch1-1" {
		t.Errorf("ApplianceKernel = %q, want the configured directory", cfg.ApplianceKernel)
	}
}

func TestLoad_ApplianceKernelFromTheEnvironmentExpandsHome(t *testing.T) {
	t.Parallel()
	env := func(name string) string {
		switch name {
		case "HOME":
			return "/home/operator"
		case "AGENT_VM_APPLIANCE_KERNEL":
			return "~/.local/share/agent-vm/appliance-kernel/6.12.4-arch1-1"
		}
		return ""
	}

	cfg, err := Load(env, Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := "/home/operator/.local/share/agent-vm/appliance-kernel/6.12.4-arch1-1"
	if cfg.ApplianceKernel != want {
		t.Errorf("ApplianceKernel = %q, want %q", cfg.ApplianceKernel, want)
	}
}

// A host whose own kernel boots the appliance configures nothing, which is
// every general-purpose distribution kernel.
func TestLoad_ApplianceKernelIsUnsetByDefault(t *testing.T) {
	t.Parallel()
	cfg, err := Load(noEnv, Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ApplianceKernel != "" {
		t.Errorf("ApplianceKernel = %q, want it empty by default", cfg.ApplianceKernel)
	}
}

func TestLoad_RejectsUnknownConfigFileKeys(t *testing.T) {
	t.Parallel()
	// A misspelled key, and a misspelled table, would otherwise leave the
	// operator on settings they believe they changed.
	path := writeConfig(t, `
libvirt_url = "qemu:///session"

[defaults]
vcpus = 4
netwrok = "bridge"

[defualts]
memory = "8G"
`)

	_, err := Load(noEnv, Overrides{ConfigFile: path})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Load = %v, want *ValidationError", err)
	}
	for _, want := range []string{path, "libvirt_url", "defaults.netwrok", "defualts.memory"} {
		if !strings.Contains(verr.Error(), want) {
			t.Errorf("error does not name %q:\n%v", want, verr)
		}
	}
}

func TestLoad_GitHubOrgFromFileEnvAndFlag(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[github]\norg = \"from-file\"\n")

	fromFile, err := Load(noEnv, Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load file: %v", err)
	}
	if fromFile.GitHubOrg != "from-file" {
		t.Errorf("GitHubOrg = %q, want from-file", fromFile.GitHubOrg)
	}

	fromEnv, err := Load(envMap(map[string]string{
		"HOME":                emptyHome,
		"AGENT_VM_GITHUB_ORG": "from-env",
	}), Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load env: %v", err)
	}
	if fromEnv.GitHubOrg != "from-env" {
		t.Errorf("GitHubOrg = %q, want from-env (env beats file)", fromEnv.GitHubOrg)
	}

	fromFlag, err := Load(envMap(map[string]string{
		"HOME":                emptyHome,
		"AGENT_VM_GITHUB_ORG": "from-env",
	}), Overrides{ConfigFile: path, GitHubOrg: "from-flag"})
	if err != nil {
		t.Fatalf("Load flag: %v", err)
	}
	if fromFlag.GitHubOrg != "from-flag" {
		t.Errorf("GitHubOrg = %q, want from-flag (flag beats env)", fromFlag.GitHubOrg)
	}
}

func TestLoad_GitDomainFromFileEnvAndFlag(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[git]\ndomain = \"From-File.Example.\"\n")

	fromFile, err := Load(noEnv, Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load file: %v", err)
	}
	if fromFile.GitDomain != "from-file.example" {
		t.Errorf("GitDomain = %q, want from-file.example", fromFile.GitDomain)
	}

	fromEnv, err := Load(envMap(map[string]string{
		"HOME":                emptyHome,
		"AGENT_VM_GIT_DOMAIN": "from-env.example",
	}), Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load env: %v", err)
	}
	if fromEnv.GitDomain != "from-env.example" {
		t.Errorf("GitDomain = %q, want from-env.example (env beats file)", fromEnv.GitDomain)
	}

	fromFlag, err := Load(envMap(map[string]string{
		"HOME":                emptyHome,
		"AGENT_VM_GIT_DOMAIN": "from-env.example",
	}), Overrides{ConfigFile: path, GitDomain: "from-flag.example"})
	if err != nil {
		t.Fatalf("Load flag: %v", err)
	}
	if fromFlag.GitDomain != "from-flag.example" {
		t.Errorf("GitDomain = %q, want from-flag.example (flag beats env)", fromFlag.GitDomain)
	}
}

func TestLoad_RejectsAnInvalidGitDomain(t *testing.T) {
	t.Parallel()
	for _, domain := range []string{".local", "not a domain", "-bad.com"} {
		t.Run(domain, func(t *testing.T) {
			t.Parallel()
			_, err := Load(noEnv, Overrides{GitDomain: domain})
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Field != "git domain" {
				t.Fatalf("Load(%q) = %v, want a git domain *ValidationError", domain, err)
			}
		})
	}
}

func TestLoad_RejectsAnInvalidGitHubOrg(t *testing.T) {
	t.Parallel()
	for _, org := range []string{"foo/bar", "-acme", "acme-", "this-name-is-longer-than-thirty-nine-characters"} {
		t.Run(org, func(t *testing.T) {
			t.Parallel()
			_, err := Load(noEnv, Overrides{GitHubOrg: org})
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Field != "github org" {
				t.Fatalf("Load(%q) = %v, want a github org *ValidationError", org, err)
			}
		})
	}
}

func TestLoad_PiHoleFromFileEnvAndFlag(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
[pihole]
url = "https://from-file.example/"
password_file = "~/pihole-password"
domain = "Lab."
insecure = true
`)
	fromFile, err := Load(noEnv, Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load file: %v", err)
	}
	if fromFile.PiHole.URL != "https://from-file.example" {
		t.Errorf("URL = %q", fromFile.PiHole.URL)
	}
	if fromFile.PiHole.PasswordFile != "/nonexistent/agent-vm-test-home/pihole-password" {
		t.Errorf("PasswordFile = %q", fromFile.PiHole.PasswordFile)
	}
	if fromFile.PiHole.Domain != "lab" {
		t.Errorf("Domain = %q, want lab", fromFile.PiHole.Domain)
	}
	if !fromFile.PiHole.Insecure {
		t.Error("Insecure = false, want true from the file")
	}

	fromEnv, err := Load(envMap(map[string]string{
		"HOME":                          emptyHome,
		"AGENT_VM_PIHOLE_URL":           "http://from-env.example",
		"AGENT_VM_PIHOLE_DOMAIN":        "env.agent",
		"AGENT_VM_PIHOLE_INSECURE":      "false",
		"AGENT_VM_PIHOLE_PASSWORD_FILE": "/tmp/from-env",
	}), Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load env: %v", err)
	}
	if fromEnv.PiHole.URL != "http://from-env.example" || fromEnv.PiHole.Domain != "env.agent" || fromEnv.PiHole.Insecure {
		t.Errorf("env did not beat the file: %+v", fromEnv.PiHole)
	}
	if fromEnv.PiHole.PasswordFile != "/tmp/from-env" {
		t.Errorf("PasswordFile = %q", fromEnv.PiHole.PasswordFile)
	}

	fromFlag, err := Load(envMap(map[string]string{
		"HOME":                     emptyHome,
		"AGENT_VM_PIHOLE_URL":      "http://from-env.example",
		"AGENT_VM_PIHOLE_INSECURE": "false",
	}), Overrides{
		ConfigFile:     path,
		PiHoleURL:      "https://from-flag.example",
		PiHoleInsecure: "true",
	})
	if err != nil {
		t.Fatalf("Load flag: %v", err)
	}
	if fromFlag.PiHole.URL != "https://from-flag.example" || !fromFlag.PiHole.Insecure {
		t.Errorf("flag did not beat env: %+v", fromFlag.PiHole)
	}
}

func TestLoad_PiHoleInsecureStaysWhenTheFlagDoesNotSetIt(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[pihole]\ninsecure = true\n")
	cfg, err := Load(noEnv, Overrides{ConfigFile: path, PiHoleInsecure: ""})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PiHole.Insecure {
		t.Fatal("an absent flag cleared insecure = true")
	}
}

func TestLoad_TailscaleFromFileEnvAndFlag(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
[tailscale]
auth_key_file = "~/tskey"
hostname = "from-file"
login_server = "https://from-file.example"
advertise_tags = ["tag:file"]
ephemeral = true
ssh = false
`)
	fromFile, err := Load(noEnv, Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load file: %v", err)
	}
	if fromFile.Tailscale.AuthKeyFile != "/nonexistent/agent-vm-test-home/tskey" {
		t.Errorf("AuthKeyFile = %q", fromFile.Tailscale.AuthKeyFile)
	}
	if fromFile.Tailscale.Hostname != "from-file" || fromFile.Tailscale.LoginServer != "https://from-file.example" {
		t.Errorf("file settings = %+v", fromFile.Tailscale)
	}
	if len(fromFile.Tailscale.AdvertiseTags) != 1 || fromFile.Tailscale.AdvertiseTags[0] != "tag:file" {
		t.Errorf("tags = %v", fromFile.Tailscale.AdvertiseTags)
	}
	if !fromFile.Tailscale.Ephemeral || fromFile.Tailscale.SSH {
		t.Errorf("ephemeral=%v ssh=%v", fromFile.Tailscale.Ephemeral, fromFile.Tailscale.SSH)
	}

	fromEnv, err := Load(envMap(map[string]string{
		"HOME":                              emptyHome,
		"AGENT_VM_TAILSCALE_AUTH_KEY_FILE":  "/tmp/from-env",
		"AGENT_VM_TAILSCALE_HOSTNAME":       "from-env",
		"AGENT_VM_TAILSCALE_EPHEMERAL":      "false",
		"AGENT_VM_TAILSCALE_SSH":            "true",
		"AGENT_VM_TAILSCALE_ADVERTISE_TAGS": "tag:env, tag:env2",
	}), Overrides{ConfigFile: path})
	if err != nil {
		t.Fatalf("Load env: %v", err)
	}
	if fromEnv.Tailscale.AuthKeyFile != "/tmp/from-env" || fromEnv.Tailscale.Hostname != "from-env" || fromEnv.Tailscale.Ephemeral || !fromEnv.Tailscale.SSH {
		t.Errorf("env did not beat the file: %+v", fromEnv.Tailscale)
	}
	if fromEnv.Tailscale.LoginServer != "https://from-file.example" {
		t.Errorf("login server = %q, want the file value", fromEnv.Tailscale.LoginServer)
	}
	if len(fromEnv.Tailscale.AdvertiseTags) != 2 || fromEnv.Tailscale.AdvertiseTags[0] != "tag:env" || fromEnv.Tailscale.AdvertiseTags[1] != "tag:env2" {
		t.Errorf("tags = %v", fromEnv.Tailscale.AdvertiseTags)
	}

	fromFlag, err := Load(envMap(map[string]string{
		"HOME":                        emptyHome,
		"AGENT_VM_TAILSCALE_HOSTNAME": "from-env",
		"AGENT_VM_TAILSCALE_SSH":      "false",
	}), Overrides{
		ConfigFile:         path,
		TailscaleHostname:  "from-flag",
		TailscaleEphemeral: "true",
	})
	if err != nil {
		t.Fatalf("Load flag: %v", err)
	}
	if fromFlag.Tailscale.Hostname != "from-flag" || !fromFlag.Tailscale.Ephemeral || fromFlag.Tailscale.SSH {
		t.Errorf("flag did not beat env: %+v", fromFlag.Tailscale)
	}
}

func TestLoad_TailscaleEphemeralStaysWhenTheFlagDoesNotSetIt(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[tailscale]\nephemeral = true\nssh = true\n")
	cfg, err := Load(noEnv, Overrides{ConfigFile: path, TailscaleEphemeral: "", TailscaleSSH: ""})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Tailscale.Ephemeral || !cfg.Tailscale.SSH {
		t.Fatalf("an absent flag cleared the file: %+v", cfg.Tailscale)
	}
}

func TestDefaultTailscaleAuthKeyPath(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	got, err := DefaultTailscaleAuthKeyPath(envMap(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "agent-vm", "tailscale-auth-key"); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	xdg := t.TempDir()
	got, err = DefaultTailscaleAuthKeyPath(envMap(map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": xdg,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, "agent-vm", "tailscale-auth-key"); got != want {
		t.Fatalf("XDG path = %q, want %q", got, want)
	}

	got, err = DefaultTailscaleAuthKeyPath(envMap(map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": "relative/config",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "agent-vm", "tailscale-auth-key"); got != want {
		t.Fatalf("relative XDG path = %q, want %q", got, want)
	}
}

func TestLoad_RejectsAnUnknownTailscaleConfigKey(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[tailscale]\nauth_key = \"tskey-auth-test-NOTACONFIGVALUE\"\n")
	_, err := Load(noEnv, Overrides{ConfigFile: path})
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.Error(), "tailscale.auth_key") {
		t.Fatalf("Load = %v, want an unknown-key error", err)
	}
	if strings.Contains(verr.Error(), "tskey-auth-test-NOTACONFIGVALUE") {
		t.Fatalf("the error includes the value:\n%v", verr)
	}
}

func TestLoad_RejectsAnUnknownPiHoleConfigKey(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[pihole]\npassword = \"secret\"\n")
	_, err := Load(noEnv, Overrides{ConfigFile: path})
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.Error(), "pihole.password") {
		t.Fatalf("Load = %v, want an unknown-key error", err)
	}
}

func TestLoad_RejectsAnUnknownGitHubConfigKey(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "[github]\nnope = \"acme\"\n")
	_, err := Load(noEnv, Overrides{ConfigFile: path})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Load = %v, want *ValidationError", err)
	}
	if !strings.Contains(verr.Error(), "github.nope") {
		t.Errorf("error does not name the unknown key:\n%v", verr)
	}
}

func TestLoad_RejectsAnExplicitZeroOrNegativeVCPUsInTheConfigFile(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"0", "-2"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			path := writeConfig(t, "[defaults]\nvcpus = "+value+"\n")

			_, fileErr := Load(noEnv, Overrides{ConfigFile: path})
			_, flagErr := Load(noEnv, Overrides{ConfigFile: filepath.Join(t.TempDir(), "absent.toml"), VCPUs: value})

			var verr *ValidationError
			if !errors.As(fileErr, &verr) || verr.Field != "vcpus" {
				t.Fatalf("Load with vcpus = %s in the file = %v, want a vcpus *ValidationError", value, fileErr)
			}
			if flagErr == nil || fileErr.Error() != flagErr.Error() {
				t.Errorf("file error %q differs from the flag's %q", fileErr, flagErr)
			}
		})
	}
}

func TestLoad_DefaultPathsIgnoreARelativeXDGDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "agent-vm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[defaults]\nvcpus = 6\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(envMap(map[string]string{
		"HOME":            home,
		"XDG_DATA_HOME":   "relative/data",
		"XDG_CONFIG_HOME": "relative/config",
	}), Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home, ".local", "share", "agent-vm"); cfg.StateDir != want {
		t.Errorf("StateDir = %s, want %s", cfg.StateDir, want)
	}
	if cfg.VCPUs != 6 {
		t.Errorf("VCPUs = %d, want 6 from %s", cfg.VCPUs, filepath.Join(dir, "config.toml"))
	}
}

func TestLoad_DefaultPathsHonorAnAbsoluteXDGDirectory(t *testing.T) {
	t.Parallel()
	data := t.TempDir()

	cfg, err := Load(envMap(map[string]string{"XDG_DATA_HOME": data, "XDG_CONFIG_HOME": t.TempDir()}), Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(data, "agent-vm"); cfg.StateDir != want {
		t.Errorf("StateDir = %s, want %s", cfg.StateDir, want)
	}
}

func TestLoad_WithoutAUsableHomeTheDefaultsAreAnError(t *testing.T) {
	t.Parallel()
	absent := filepath.Join(t.TempDir(), "absent.toml")
	tests := []struct {
		name  string
		env   map[string]string
		flags Overrides
		want  string
	}{
		{"no HOME, default state directory", map[string]string{}, Overrides{ConfigFile: absent}, "state directory"},
		{"relative HOME, default state directory", map[string]string{"HOME": "home/me"}, Overrides{ConfigFile: absent}, "state directory"},
		{"relative XDG_DATA_HOME and no HOME", map[string]string{"XDG_DATA_HOME": "data"}, Overrides{ConfigFile: absent}, "state directory"},
		{"no HOME, default config file", map[string]string{}, Overrides{StateDir: "/srv/agent-vm"}, "configuration file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(envMap(tt.env), tt.flags)

			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Load = %+v, %v; want *ValidationError", cfg, err)
			}
			if !strings.Contains(verr.Error(), tt.want) {
				t.Errorf("error does not say which default it could not locate (%s):\n%v", tt.want, verr)
			}
		})
	}
}

func TestLoad_WithoutAHomeExplicitPathsStillWork(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envMap(map[string]string{}), Overrides{
		StateDir: "/srv/agent-vm", ConfigFile: filepath.Join(t.TempDir(), "absent.toml"),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StateDir != "/srv/agent-vm" {
		t.Errorf("StateDir = %s, want /srv/agent-vm", cfg.StateDir)
	}
}

func TestLoad_ARemoteHypervisorNeedsNoLocalHomeForItsDefaultStateDir(t *testing.T) {
	t.Parallel()
	// The default is under the remote account's home, which the CLI asks the
	// hypervisor for; this machine's home has nothing to do with it.
	cfg, err := Load(envMap(map[string]string{}), Overrides{
		ConfigFile: filepath.Join(t.TempDir(), "absent.toml"),
		LibvirtURI: "qemu+ssh://kvm@hypervisor.lan/system",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.StateDirIsDefault {
		t.Error("StateDirIsDefault = false, want true")
	}
}

func TestLoad_TildeWithARelativeHomeIsAnError(t *testing.T) {
	t.Parallel()
	_, err := Load(envMap(map[string]string{"HOME": "home/me"}), Overrides{
		StateDir: "~/agent-vm", ConfigFile: filepath.Join(t.TempDir(), "absent.toml"),
	})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Load = %v, want *ValidationError", err)
	}
}
