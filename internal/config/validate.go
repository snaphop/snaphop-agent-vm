package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ValidationError is bad input from the operator: a flag, an environment
// variable, or a configuration file value. The CLI reports it as a usage error
// (exit 2) and nothing on the host has changed when one is returned.
type ValidationError struct {
	Field string
	Value string
	Err   error
	// Remedy, when set, tells the operator what to do instead.
	Remedy string
}

func (e *ValidationError) Error() string {
	msg := fmt.Sprintf("invalid %s %q: %v", e.Field, e.Value, e.Err)
	if e.Remedy != "" {
		msg += "\n  " + e.Remedy
	}
	return msg
}

func (e *ValidationError) Unwrap() error { return e.Err }

// VMNamePattern is the contract for VM names (SECURITY.md, docs/cli.md). Names
// become libvirt domain names, hostnames, and path segments under the state
// directory, so they are validated against this pattern rather than escaped at
// each point of use.
var VMNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`)

// ValidateVMName checks a VM name against the documented pattern.
func ValidateVMName(name string) error {
	if VMNamePattern.MatchString(name) {
		return nil
	}
	return &ValidationError{
		Field:  "VM name",
		Value:  name,
		Err:    fmt.Errorf("names must match %s", VMNamePattern),
		Remedy: "Use 2 to 32 lowercase letters, digits, and hyphens, starting and ending with a letter or digit.",
	}
}

// validate checks the fully resolved configuration. It runs before any host
// state changes, so a rejected configuration leaves nothing behind.
// validateMaxMemory checks the virtio-mem ceiling. Zero means the feature is
// off, which is the default and always valid.
func (c *Config) validateMaxMemory() error {
	if c.MaxMemory == 0 {
		return nil
	}
	if c.MaxMemory < MemoryFloor || c.MaxMemory > MemoryCeiling {
		return &ValidationError{
			Field: "max_memory", Value: c.MaxMemory.String(),
			Err: fmt.Errorf("must be between %s and %s", MemoryFloor, MemoryCeiling),
		}
	}
	if c.MaxMemory <= c.Memory {
		return &ValidationError{
			Field: "max_memory", Value: c.MaxMemory.String(),
			Err:    fmt.Errorf("must be greater than memory (%s)", c.Memory),
			Remedy: "max_memory is the ceiling the guest can grow to; leave it unset for a fixed-size guest.",
		}
	}
	if growth := c.MaxMemory - c.Memory; growth%VirtioMemBlock != 0 {
		return &ValidationError{
			Field: "max_memory", Value: c.MaxMemory.String(),
			Err: fmt.Errorf("leaves %s of growth room, which is not a multiple of the %s virtio-mem block size",
				growth, VirtioMemBlock),
			Remedy: fmt.Sprintf("Pick a max_memory that exceeds memory (%s) by a multiple of %s.", c.Memory, VirtioMemBlock),
		}
	}
	return nil
}

func (c *Config) validate() error {
	// The one state directory that may still be empty here is a remote
	// hypervisor's default, which only that machine can name (ADR-0010).
	remoteDefault := c.StateDirIsDefault && c.RemoteHypervisor()
	if c.StateDir == "" && !remoteDefault {
		return &ValidationError{Field: "state_dir", Value: "", Err: fmt.Errorf("must not be empty")}
	}
	// The URI is parsed here rather than at the point of use, so an
	// unsupported transport is a usage error before anything on either host
	// changes.
	if _, err := c.Connection(); err != nil {
		return err
	}
	if c.VCPUs < MinVCPUs || c.VCPUs > MaxVCPUs {
		return &ValidationError{
			Field: "vcpus", Value: fmt.Sprint(c.VCPUs),
			Err: fmt.Errorf("must be between %d and %d", MinVCPUs, MaxVCPUs),
		}
	}
	if c.Memory < MemoryFloor || c.Memory > MemoryCeiling {
		return &ValidationError{
			Field: "memory", Value: c.Memory.String(),
			Err: fmt.Errorf("must be between %s and %s", MemoryFloor, MemoryCeiling),
		}
	}
	if err := c.validateMaxMemory(); err != nil {
		return err
	}
	if c.Disk < MinDisk || c.Disk > MaxDisk {
		return &ValidationError{
			Field: "disk", Value: c.Disk.String(),
			Err: fmt.Errorf("must be between %s and %s", MinDisk, MaxDisk),
		}
	}
	if c.GuestUser == "" {
		return &ValidationError{Field: "guest user", Value: "", Err: fmt.Errorf("must not be empty")}
	}
	if c.NATNetwork == "" {
		return &ValidationError{Field: "network.nat.name", Value: "", Err: fmt.Errorf("must not be empty")}
	}
	return c.validateNetwork()
}

func (c *Config) validateNetwork() error {
	if c.Network != NetworkBridge {
		return nil
	}
	// Bridged mode is opt-in and never inferred. Both of these are refusals,
	// not fallbacks to NAT: silently changing the network mode would change the
	// guest's exposure without the operator asking.
	if c.Bridge == "" {
		return &ValidationError{
			Field:  "bridge",
			Value:  "",
			Err:    fmt.Errorf("--network bridge requires a host bridge"),
			Remedy: "Pass --bridge <iface>, set AGENT_VM_BRIDGE, or set [network.bridge] interface in the config file.",
		}
	}
	if c.SessionMode() {
		return &ValidationError{
			Field:  "network",
			Value:  string(NetworkBridge),
			Err:    fmt.Errorf("bridged networking is not supported on %s", c.LibvirtURI),
			Remedy: "Use a system connection such as qemu:///system for bridged networking, or keep the default NAT mode.",
		}
	}
	return nil
}

// publicKeyPrefixes are the key types OpenSSH writes into a .pub file.
var publicKeyPrefixes = []string{"ssh-", "ecdsa-", "sk-ssh-", "sk-ecdsa-"}

// ValidateSSHPublicKey reads a key file and confirms it holds an SSH *public*
// key. Pointing --ssh-key at a private key would copy private material into a
// cloud-init seed, which SECURITY.md forbids outright, so this check is a
// refusal rather than a warning.
//
// The file's contents are never logged or included in the error.
func ValidateSSHPublicKey(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return &ValidationError{
			Field: "ssh key", Value: path, Err: err,
			Remedy: "Pass --ssh-key <path to a .pub file>, or set [guest] ssh_keys in the config file.",
		}
	}
	if info.IsDir() {
		return &ValidationError{Field: "ssh key", Value: path, Err: fmt.Errorf("is a directory")}
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		return &ValidationError{Field: "ssh key", Value: path, Err: err}
	}
	line := strings.TrimSpace(string(contents))

	if strings.Contains(line, "PRIVATE KEY") {
		return &ValidationError{
			Field: "ssh key", Value: path,
			Err:    fmt.Errorf("this is a private key"),
			Remedy: "Point --ssh-key at the matching public key (usually the same path with a .pub suffix). Private keys never enter a VM.",
		}
	}
	for _, prefix := range publicKeyPrefixes {
		if strings.HasPrefix(line, prefix) {
			return nil
		}
	}
	return &ValidationError{
		Field: "ssh key", Value: path,
		Err:    fmt.Errorf("does not look like an SSH public key"),
		Remedy: "An OpenSSH public key file starts with a key type such as ssh-ed25519 or ssh-rsa.",
	}
}
