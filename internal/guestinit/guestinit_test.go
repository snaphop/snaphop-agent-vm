package guestinit

import (
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/golden"
)

// A real ed25519 public key: golden files and parser tests are worth nothing if
// the input is a shape no tool ever produces.
const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1TfEt0YKXQ+eZmJHCcTKQ0lMSzQFm/kQGHMvhE7Hqx agent@example"

func options() Options {
	return Options{
		Hostname:          "agent-01",
		User:              "agent",
		SSHAuthorizedKeys: []string{testKey},
		AgentVMVersion:    "0.1.0-test",
	}
}

func TestGenerate_MatchesTheUserDataContract(t *testing.T) {
	got, err := Generate(options())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	golden.Assert(t, "user-data-default.yaml", got)
}

func TestGenerate_StartsWithTheCloudConfigHeader(t *testing.T) {
	got, err := Generate(options())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// cloud-init identifies plain user-data by its first line; anything before
	// it means the document is ignored entirely.
	if !strings.HasPrefix(string(got), "#cloud-config\n") {
		t.Errorf("user-data must begin with #cloud-config, got:\n%s", firstLine(string(got)))
	}
}

func TestGenerate_AuthorizesEveryKeyAndNoPassword(t *testing.T) {
	opts := options()
	second := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDS8kRJ other@example"
	opts.SSHAuthorizedKeys = append(opts.SSHAuthorizedKeys, second)

	got, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, want := range []string{testKey, second, "ssh_pwauth: false", "disable_root: true", "lock_passwd: true"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("user-data is missing %q:\n%s", want, got)
		}
	}
}

func TestGenerate_PutsTheLoginUserInTheGuestToolingGroups(t *testing.T) {
	got, err := Generate(options())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// The memberships have to be part of creating the account: a group added
	// afterwards does not apply to a session that has already started, and the
	// first SSH session can land before any boot-time unit has run.
	if !strings.Contains(string(got), "    groups:\n      - docker\n      - libvirt\n      - kvm\n") {
		t.Errorf("the login user is not created in the docker, libvirt, and kvm groups:\n%s", got)
	}
	// Declaring the groups is what lets a base image that has none of that
	// software — one built before the guest tooling landed — still create the
	// account. cloud-init creates groups before users, so a group named for an
	// account but missing from the image would leave the VM unreachable.
	if !strings.Contains(string(got), "\ngroups:\n  - docker\n  - libvirt\n  - kvm\n") {
		t.Errorf("the guest tooling groups are not declared, so a VM on an older base image would lose its login user:\n%s", got)
	}
}

func TestGenerate_RejectsAVMWithNoAuthorizedKey(t *testing.T) {
	opts := options()
	opts.SSHAuthorizedKeys = nil

	if _, err := Generate(opts); err == nil {
		t.Fatal("a VM with no authorized key would accept no logins; want an error")
	}
}

func TestGenerate_RejectsAnInvalidHostname(t *testing.T) {
	opts := options()
	opts.Hostname = "Agent VM"

	if _, err := Generate(opts); err == nil {
		t.Fatal("want an error for a hostname that is not a valid VM name")
	}
}

func TestGenerate_RejectsPrivateKeyMaterial(t *testing.T) {
	opts := options()
	opts.SSHAuthorizedKeys = []string{"-----BEGIN OPENSSH PRIVATE KEY-----"}

	_, err := Generate(opts)
	if err == nil {
		t.Fatal("want a refusal to put private key material in a seed")
	}
	if strings.Contains(err.Error(), "BEGIN OPENSSH PRIVATE KEY") {
		t.Errorf("the error echoed the key material: %v", err)
	}
}

func TestGenerate_QuotesAKeyCommentThatWouldChangeTheYAML(t *testing.T) {
	opts := options()
	opts.SSHAuthorizedKeys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 agent: #comment"}

	got, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(got), `- "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 agent: #comment"`) {
		t.Errorf("the key was not emitted as a quoted scalar:\n%s", got)
	}
}

func TestGenerate_MergesOperatorUserDataAsASeparateMIMEPart(t *testing.T) {
	opts := options()
	opts.ExtraUserData = []byte("#cloud-config\npackages:\n  - ripgrep\n")
	opts.ExtraSource = "/home/operator/extra.yaml"

	got, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	golden.Assert(t, "user-data-extra.mime", got)

	types, bodies := parseParts(t, got)
	if want := []string{"text/cloud-config", "text/cloud-config"}; !equal(types, want) {
		t.Errorf("part content types = %v, want %v", types, want)
	}
	// Ours is applied last, and says so, which is what keeps operator data from
	// replacing the authorized keys.
	if !strings.Contains(bodies[1], "merge_how:") {
		t.Errorf("the generated part must declare its merge rules:\n%s", bodies[1])
	}
	if !strings.Contains(bodies[0], "ripgrep") {
		t.Errorf("the operator's user-data is not the first part:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[1], testKey) {
		t.Errorf("the generated part must still authorize the key:\n%s", bodies[1])
	}
}

func TestGenerate_ClassifiesAShellScriptUserData(t *testing.T) {
	opts := options()
	opts.ExtraUserData = []byte("#!/bin/sh\necho hello\n")

	got, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	types, _ := parseParts(t, got)
	if types[0] != "text/x-shellscript" {
		t.Errorf("first part content type = %q, want text/x-shellscript", types[0])
	}
}

func TestGenerate_RejectsUserDataCloudInitWouldIgnore(t *testing.T) {
	opts := options()
	opts.ExtraUserData = []byte("packages:\n  - ripgrep\n")
	opts.ExtraSource = "/home/operator/extra.yaml"

	_, err := Generate(opts)
	if err == nil {
		t.Fatal("want a refusal: cloud-init ignores user-data with no recognized header")
	}
	if !strings.Contains(err.Error(), "#cloud-config") {
		t.Errorf("the error should name the headers cloud-init accepts: %v", err)
	}
	if strings.Contains(err.Error(), "ripgrep") {
		t.Errorf("operator user-data must never be echoed: %v", err)
	}
}

func TestGenerate_IsDeterministic(t *testing.T) {
	opts := options()
	opts.ExtraUserData = []byte("#cloud-config\npackages:\n  - ripgrep\n")

	first, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	second, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if string(first) != string(second) {
		t.Error("the same configuration produced different user-data twice")
	}
}

func TestLoadPublicKeys_ReadsKeysAndDropsDuplicates(t *testing.T) {
	dir := t.TempDir()
	first := writeFile(t, dir, "id_ed25519.pub", testKey+"\n")
	second := writeFile(t, dir, "copy.pub", testKey+"\n# the same key, given twice\n")

	keys, err := LoadPublicKeys([]string{first, second})
	if err != nil {
		t.Fatalf("LoadPublicKeys: %v", err)
	}
	if !equal(keys, []string{testKey}) {
		t.Errorf("keys = %v, want the one key once", keys)
	}
}

func TestLoadPublicKeys_RefusesAPrivateKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "id_ed25519",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n")

	_, err := LoadPublicKeys([]string{path})
	if err == nil {
		t.Fatal("want a refusal to read a private key")
	}
	if strings.Contains(err.Error(), "b3BlbnNzaC1rZXktdjEAAAAA") {
		t.Errorf("the error echoed private key material: %v", err)
	}
}

func TestLoadPublicKeys_ReportsAnEmptyKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "empty.pub", "\n\n")

	if _, err := LoadPublicKeys([]string{path}); err == nil {
		t.Fatal("want an error for a key file with no key in it")
	}
}

func TestLoadPublicKeys_ReportsAMissingFile(t *testing.T) {
	if _, err := LoadPublicKeys([]string{filepath.Join(t.TempDir(), "absent.pub")}); err == nil {
		t.Fatal("want an error naming the missing key file")
	}
}

func TestLoadAuthorizedKeys_ReadsEveryKeyAndSkipsComments(t *testing.T) {
	other := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDS8kRJ other@example"
	path := writeFile(t, t.TempDir(), "authorized_keys",
		"# laptop\n"+testKey+"\n\n"+other+"\n"+testKey+"\n")

	keys, err := LoadAuthorizedKeys(path)
	if err != nil {
		t.Fatalf("LoadAuthorizedKeys: %v", err)
	}
	if !equal(keys, []string{testKey, other}) {
		t.Errorf("keys = %v, want both keys once, in file order", keys)
	}
}

func TestLoadAuthorizedKeys_RefusesEntriesCarryingOptions(t *testing.T) {
	// Honoring the restriction in the guest and dropping it are both decisions
	// this tool does not get to make for the operator, so it refuses instead.
	path := writeFile(t, t.TempDir(), "authorized_keys",
		`command="/usr/bin/true",restrict `+testKey+"\n")

	_, err := LoadAuthorizedKeys(path)
	if err == nil {
		t.Fatal("want a refusal for an authorized_keys entry with options")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("the error does not name the offending line: %v", err)
	}
}

func TestLoadAuthorizedKeys_ReportsAMissingFile(t *testing.T) {
	_, err := LoadAuthorizedKeys(filepath.Join(t.TempDir(), ".ssh", "authorized_keys"))
	if err == nil {
		t.Fatal("want an error naming the missing authorized_keys file")
	}
	if !strings.Contains(err.Error(), "--host-authorized-keys") {
		t.Errorf("the error does not say which flag asked for the file: %v", err)
	}
}

func writeFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// parseParts reads the generated document back with the standard MIME reader,
// which is the same thing cloud-init does with it.
func parseParts(t *testing.T, doc []byte) (contentTypes, bodies []string) {
	t.Helper()

	header, body, ok := strings.Cut(string(doc), "\r\n\r\n")
	if !ok {
		t.Fatalf("no MIME header block in:\n%s", doc)
	}
	_, params, err := mime.ParseMediaType(strings.TrimPrefix(
		strings.SplitN(header, "\r\n", 2)[1], "Content-Type: "))
	if err != nil {
		t.Fatalf("parsing the Content-Type header: %v", err)
	}

	reader := multipart.NewReader(strings.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parsing a part's Content-Type: %v", err)
		}
		content, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("reading a part: %v", err)
		}
		contentTypes = append(contentTypes, mediaType)
		bodies = append(bodies, string(content))
	}
	if len(contentTypes) != 2 {
		t.Fatalf("got %d parts, want 2:\n%s", len(contentTypes), doc)
	}
	return contentTypes, bodies
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
