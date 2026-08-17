package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// noEnv is an environment in which no AGENT_VM_* variable is set.
func noEnv(string) string { return "" }

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
	if cfg.Distro.String() != "ubuntu:24.04" {
		t.Errorf("Distro = %s, want ubuntu:24.04", cfg.Distro)
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
}

func TestLoad_PrecedenceIsDefaultsThenFileThenEnvThenFlags(t *testing.T) {
	path := writeConfig(t, `
[defaults]
distro = "fedora"
vcpus  = 8
memory = "16G"
disk   = "100G"
`)

	cfg, err := Load(
		envMap(map[string]string{
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
	if cfg.Distro.String() != "fedora:42" {
		t.Errorf("Distro = %s, want fedora:42 (file beats defaults)", cfg.Distro)
	}
}

func TestLoad_MissingConfigFileIsNotAnError(t *testing.T) {
	cfg, err := Load(noEnv, Overrides{ConfigFile: filepath.Join(t.TempDir(), "nope.toml")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ConfigFile != "" {
		t.Errorf("ConfigFile = %q, want empty when no file exists", cfg.ConfigFile)
	}
}

func TestLoad_MalformedConfigFileIsAValidationError(t *testing.T) {
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
	_, err := Load(noEnv, Overrides{Distro: "alpine"})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want *ValidationError", err)
	}
	if verr.Field != "distro" {
		t.Errorf("Field = %q, want %q", verr.Field, "distro")
	}
}

func TestLoad_RejectsResourcesOutsideBounds(t *testing.T) {
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
	_, err := Load(noEnv, Overrides{Network: "host"})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("got %v, want *ValidationError", err)
	}
}

func TestLoad_ExpandsTildeInPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}

	cfg, err := Load(noEnv, Overrides{StateDir: "~/agent-vm-state"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home, "agent-vm-state"); cfg.StateDir != want {
		t.Errorf("StateDir = %s, want %s", cfg.StateDir, want)
	}
}

func TestLoad_EnvNamesMatchTheDocumentedContract(t *testing.T) {
	cfg, err := Load(envMap(map[string]string{
		"AGENT_VM_STATE_DIR":   "/srv/agent-vm",
		"AGENT_VM_LIBVIRT_URI": SessionURI,
		"AGENT_VM_DISTRO":      "arch",
		"AGENT_VM_VCPUS":       "6",
		"AGENT_VM_MEMORY":      "2G",
		"AGENT_VM_DISK":        "20G",
		"AGENT_VM_NETWORK":     "nat",
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
	if cfg.Distro.String() != "arch:base" {
		t.Errorf("Distro = %s, want arch:base", cfg.Distro)
	}
	if cfg.VCPUs != 6 || cfg.Memory.String() != "2G" || cfg.Disk.String() != "20G" {
		t.Errorf("resources = %d vcpu / %s / %s", cfg.VCPUs, cfg.Memory, cfg.Disk)
	}
}
