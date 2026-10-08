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
	if cfg.VCPUs != 6 || cfg.Memory.String() != "2G" || cfg.Disk.String() != "20G" {
		t.Errorf("resources = %d vcpu / %s / %s", cfg.VCPUs, cfg.Memory, cfg.Disk)
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
