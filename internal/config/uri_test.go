package config

import "testing"

func TestParseConnection_Local(t *testing.T) {
	t.Parallel()
	for _, uri := range []string{"qemu:///system", "qemu:///session"} {
		conn, err := ParseConnection(uri)
		if err != nil {
			t.Fatalf("ParseConnection(%q) = %v", uri, err)
		}
		if conn.Remote {
			t.Errorf("ParseConnection(%q).Remote = true, want false", uri)
		}
		if got := conn.HypervisorURI(); got != uri {
			t.Errorf("HypervisorURI() = %q, want %q", got, uri)
		}
	}
}

func TestParseConnection_SSHTransport(t *testing.T) {
	t.Parallel()
	conn, err := ParseConnection("qemu+ssh://kvm@hv.example.com/system")
	if err != nil {
		t.Fatalf("ParseConnection: %v", err)
	}
	if !conn.Remote {
		t.Fatal("Remote = false, want true")
	}
	if conn.SSHDestination != "kvm@hv.example.com" {
		t.Errorf("SSHDestination = %q, want kvm@hv.example.com", conn.SSHDestination)
	}
	if conn.SSHPort != 0 {
		t.Errorf("SSHPort = %d, want 0", conn.SSHPort)
	}
	// virsh and virt-install run on the hypervisor, so the transport is gone
	// by the time they see the URI.
	if got := conn.HypervisorURI(); got != "qemu:///system" {
		t.Errorf("HypervisorURI() = %q, want qemu:///system", got)
	}
}

func TestParseConnection_SSHParameters(t *testing.T) {
	t.Parallel()
	conn, err := ParseConnection("qemu+ssh://hv:2222/system?keyfile=/home/me/.ssh/hv_ed25519&no_verify=1")
	if err != nil {
		t.Fatalf("ParseConnection: %v", err)
	}
	if conn.SSHPort != 2222 {
		t.Errorf("SSHPort = %d, want 2222", conn.SSHPort)
	}
	if conn.IdentityFile != "/home/me/.ssh/hv_ed25519" {
		t.Errorf("IdentityFile = %q", conn.IdentityFile)
	}
	if !conn.NoVerify {
		t.Error("NoVerify = false, want true")
	}
}

// Every transport but ssh reaches libvirt without giving agent-vm a shell on
// the hypervisor, which is what it needs to build images and create disks
// there. Those are refused up front rather than failing halfway through a
// create (ADR-0010).
func TestParseConnection_RejectsTransportsWithoutAShell(t *testing.T) {
	t.Parallel()
	for _, uri := range []string{
		"qemu+tls://hv/system",
		"qemu+tcp://hv/system",
		"qemu+libssh2://hv/system",
		"qemu://hv/system",
	} {
		if _, err := ParseConnection(uri); err == nil {
			t.Errorf("ParseConnection(%q) = nil error, want a refusal", uri)
		}
	}
}

func TestParseConnection_RejectsSSHWithoutAHost(t *testing.T) {
	t.Parallel()
	if _, err := ParseConnection("qemu+ssh:///system"); err == nil {
		t.Error("ParseConnection = nil error, want a refusal")
	}
}

// A password in the URI cannot be handed to ssh, and ignoring it would leave
// the operator wondering why they are prompted.
func TestParseConnection_RejectsPassword(t *testing.T) {
	t.Parallel()
	if _, err := ParseConnection("qemu+ssh://me:secret@hv/system"); err == nil {
		t.Error("ParseConnection = nil error, want a refusal")
	}
}

func TestSessionMode(t *testing.T) {
	t.Parallel()
	for uri, want := range map[string]bool{
		"qemu:///system":               false,
		"qemu:///session":              true,
		"qemu+ssh://hv/session":        true,
		"qemu+ssh://hv/system":         false,
		"qemu+ssh://kvm@hv.lan/system": false,
	} {
		cfg := &Config{LibvirtURI: uri}
		if got := cfg.SessionMode(); got != want {
			t.Errorf("SessionMode(%q) = %v, want %v", uri, got, want)
		}
	}
}

// An unsupported transport must be rejected by configuration validation, which
// is what makes it a usage error before anything on either host changes.
func TestLoad_RejectsUnsupportedTransport(t *testing.T) {
	t.Parallel()
	if _, err := Load(noEnv, Overrides{StateDir: "/srv/agent-vm", LibvirtURI: "qemu+tls://hv/system"}); err == nil {
		t.Fatal("Load = nil error, want a validation failure")
	}
}

func TestLoad_RecordsWhetherTheStateDirectoryIsTheDefault(t *testing.T) {
	t.Parallel()
	env := func(key string) string {
		if key == "HOME" {
			return "/home/me"
		}
		return ""
	}

	cfg, err := Load(env, Overrides{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.StateDirIsDefault {
		t.Error("StateDirIsDefault = false for an unconfigured state directory, want true")
	}

	cfg, err = Load(env, Overrides{StateDir: "/srv/agent-vm"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StateDirIsDefault {
		t.Error("StateDirIsDefault = true for --state-dir, want false")
	}
}
