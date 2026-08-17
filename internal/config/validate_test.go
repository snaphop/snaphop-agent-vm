package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateVMName(t *testing.T) {
	valid := []string{"agent-01", "vm", "a0", "agent-vm-test-run-1", strings.Repeat("a", 32)}
	for _, name := range valid {
		if err := ValidateVMName(name); err != nil {
			t.Errorf("ValidateVMName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []struct {
		name, why string
	}{
		{"", "empty"},
		{"a", "single character cannot start and end the pattern"},
		{"Agent-01", "uppercase"},
		{"-agent", "leading hyphen"},
		{"agent-", "trailing hyphen"},
		{"agent_01", "underscore"},
		{"agent.01", "dot"},
		{"../escape", "path traversal"},
		{"agent 01", "space"},
		{"agent/01", "path separator"},
		{"agent;rm -rf /", "shell metacharacters"},
		{strings.Repeat("a", 33), "too long"},
	}
	for _, tt := range invalid {
		if err := ValidateVMName(tt.name); err == nil {
			t.Errorf("ValidateVMName(%q) = nil, want an error (%s)", tt.name, tt.why)
		}
	}
}

func writeKey(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing key: %v", err)
	}
	return path
}

func TestValidateSSHPublicKey_AcceptsOpenSSHPublicKeys(t *testing.T) {
	keys := []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyMaterialHere operator@host\n",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQExample operator@host",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIExample operator@host",
	}
	for _, contents := range keys {
		path := writeKey(t, "id.pub", contents)
		if err := ValidateSSHPublicKey(path); err != nil {
			t.Errorf("ValidateSSHPublicKey(%q) = %v, want nil", strings.SplitN(contents, " ", 2)[0], err)
		}
	}
}

func TestValidateSSHPublicKey_RefusesAPrivateKey(t *testing.T) {
	// Accepting this would copy private key material into a cloud-init seed,
	// which SECURITY.md forbids outright.
	path := writeKey(t, "id_ed25519",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n")

	err := ValidateSSHPublicKey(path)
	if err == nil {
		t.Fatal("ValidateSSHPublicKey accepted a private key")
	}
	if !strings.Contains(err.Error(), "private key") {
		t.Errorf("error %q does not tell the operator what is wrong", err)
	}
}

func TestValidateSSHPublicKey_ErrorDoesNotLeakKeyContents(t *testing.T) {
	const material = "b3BlbnNzaC1rZXktdjEAAAAAsecretmaterial"
	path := writeKey(t, "id_ed25519", "-----BEGIN OPENSSH PRIVATE KEY-----\n"+material+"\n")

	err := ValidateSSHPublicKey(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), material) {
		t.Errorf("error message leaked key file contents: %q", err)
	}
}

func TestValidateSSHPublicKey_RejectsMissingAndUnrecognizedFiles(t *testing.T) {
	if err := ValidateSSHPublicKey(filepath.Join(t.TempDir(), "absent.pub")); err == nil {
		t.Error("ValidateSSHPublicKey accepted a missing file")
	}
	if err := ValidateSSHPublicKey(writeKey(t, "notes.txt", "just some text\n")); err == nil {
		t.Error("ValidateSSHPublicKey accepted a file that is not a key")
	}
	if err := ValidateSSHPublicKey(t.TempDir()); err == nil {
		t.Error("ValidateSSHPublicKey accepted a directory")
	}
}
