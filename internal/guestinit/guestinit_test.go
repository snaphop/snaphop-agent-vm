package guestinit

import (
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/golden"
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
	t.Parallel()
	got, err := Generate(options())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	golden.Assert(t, "user-data-default.yaml", got)
}

func TestGenerate_StartsWithTheCloudConfigHeader(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	opts := options()
	opts.SSHAuthorizedKeys = nil

	if _, err := Generate(opts); err == nil {
		t.Fatal("a VM with no authorized key would accept no logins; want an error")
	}
}

func TestGenerate_RejectsAnInvalidHostname(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.Hostname = "Agent VM"

	if _, err := Generate(opts); err == nil {
		t.Fatal("want an error for a hostname that is not a valid VM name")
	}
}

func TestGenerate_RejectsPrivateKeyMaterial(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// cloud-init parses user-data with PyYAML, which refuses a whole document
// holding a character outside its printable set. A key comment carrying one
// would leave the guest with no user and no key, so it is refused up front.
func TestGenerate_RejectsAKeyCommentCloudInitCannotParse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, comment string }{
		{"DEL", "agent\x7f@example"},
		{"C1 control", "agent\u0080@example"},
		{"last C1 control", "agent\u009f@example"},
		{"next line, a YAML line break", "agent\u0085@example"},
		{"line separator, a YAML line break", "agent\u2028@example"},
		{"paragraph separator, a YAML line break", "agent\u2029@example"},
		{"tab", "agent\t@example"},
		{"noncharacter U+FFFE", "agent\ufffe@example"},
		{"noncharacter U+FFFF", "agent\uffff@example"},
		{"invalid UTF-8", "agent\xff@example"},
		{"truncated UTF-8", "agent\xc3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := options()
			opts.SSHAuthorizedKeys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1TfEt0YKXQ+eZmJHCcTKQ0lMSzQFm/kQGHMvhE7Hqx " + tc.comment}

			_, err := Generate(opts)
			var validation *config.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("Generate error = %v, want a *config.ValidationError", err)
			}
			if !strings.Contains(err.Error(), "does not look like an OpenSSH public key line") {
				t.Errorf("the refusal should say what is wrong with the key: %v", err)
			}
		})
	}
}

func TestGenerate_AcceptsAKeyCommentInAnyPrintableScript(t *testing.T) {
	t.Parallel()
	for _, comment := range []string{
		"agent@example",
		"José Müller",
		"agent\u00a0at\u00a0example",
		"エージェント@例",
		"agent 🚀",
		"agent\ufffd",
	} {
		opts := options()
		key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1TfEt0YKXQ+eZmJHCcTKQ0lMSzQFm/kQGHMvhE7Hqx " + comment
		opts.SSHAuthorizedKeys = []string{key}

		got, err := Generate(opts)
		if err != nil {
			t.Errorf("Generate refused the comment %q: %v", comment, err)
			continue
		}
		if !strings.Contains(string(got), key) {
			t.Errorf("the key with comment %q is not in the user-data:\n%s", comment, got)
		}
	}
}

func TestLoadPublicKeys_RejectsAKeyWithADELInItsComment(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "id_ed25519.pub")
	line := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1TfEt0YKXQ+eZmJHCcTKQ0lMSzQFm/kQGHMvhE7Hqx agent\x7f@example\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}

	_, err := LoadPublicKeys([]string{path})
	if err == nil {
		t.Fatal("want a refusal: cloud-init cannot parse user-data holding a DEL")
	}
	if !strings.Contains(err.Error(), "line 1 is not an SSH public key") {
		t.Errorf("the refusal should name the line: %v", err)
	}
}

func TestGenerate_MergesOperatorUserDataAsASeparateMIMEPart(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestValidate_RefusesWhatGenerateRefusesWithoutRendering(t *testing.T) {
	t.Parallel()
	headerless := options()
	headerless.ExtraUserData = []byte("packages:\n  - ripgrep\n")
	headerless.ExtraSource = "/home/operator/extra.yaml"
	notJSON := options()
	notJSON.OpencodeConfig = []byte("permission:\n  bash: allow\n")
	notJSON.OpencodeSource = "/home/operator/opencode.json"

	for name, opts := range map[string]Options{"user-data without a header": headerless, "opencode config that is not JSON": notJSON} {
		if err := opts.Validate(); err == nil {
			t.Errorf("%s: Validate accepted what Generate refuses", name)
		} else if !strings.Contains(err.Error(), "/home/operator/") {
			t.Errorf("%s: the refusal does not name the file: %v", name, err)
		}
	}
	if err := options().Validate(); err != nil {
		t.Errorf("Validate refused valid options: %v", err)
	}
}

func TestGenerate_IsDeterministic(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	dir := t.TempDir()
	path := writeFile(t, dir, "empty.pub", "\n\n")

	if _, err := LoadPublicKeys([]string{path}); err == nil {
		t.Fatal("want an error for a key file with no key in it")
	}
}

func TestLoadPublicKeys_ReportsAMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := LoadPublicKeys([]string{filepath.Join(t.TempDir(), "absent.pub")}); err == nil {
		t.Fatal("want an error naming the missing key file")
	}
}

func TestLoadAuthorizedKeys_ReadsEveryKeyAndSkipsComments(t *testing.T) {
	t.Parallel()
	other := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDS8kRJ other@example"
	path := writeFile(t, t.TempDir(), "authorized_keys",
		"# laptop\n"+testKey+"\n\n"+other+"\n"+testKey+"\n")

	keys, sources, err := LoadAuthorizedKeys([]string{path})
	if err != nil {
		t.Fatalf("LoadAuthorizedKeys: %v", err)
	}
	if !equal(keys, []string{testKey, other}) {
		t.Errorf("keys = %v, want both keys once, in file order", keys)
	}
	if !equal(sources, []string{path}) {
		t.Errorf("sources = %v, want the file that was read", sources)
	}
}

func TestLoadAuthorizedKeys_MergesEveryFileAndSkipsTheAbsentOnes(t *testing.T) {
	t.Parallel()
	other := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDS8kRJ other@example"
	dir := t.TempDir()
	first := writeFile(t, dir, "authorized_keys", testKey+"\n")
	second := writeFile(t, dir, "second", other+"\n"+testKey+"\n")
	absent := filepath.Join(dir, "absent", "authorized_keys")

	keys, sources, err := LoadAuthorizedKeys([]string{first, absent, second})
	if err != nil {
		t.Fatalf("LoadAuthorizedKeys: %v", err)
	}
	if !equal(keys, []string{testKey, other}) {
		t.Errorf("keys = %v, want both keys once, in the order the files were read", keys)
	}
	// A file that is not there is not a source, and not an error either.
	if !equal(sources, []string{first, second}) {
		t.Errorf("sources = %v, want only the files that existed", sources)
	}
}

func TestLoadAuthorizedKeys_RefusesEntriesCarryingOptions(t *testing.T) {
	t.Parallel()
	// Honoring the restriction in the guest and dropping it are both decisions
	// this tool does not get to make for the operator, so it refuses instead.
	path := writeFile(t, t.TempDir(), "authorized_keys",
		`command="/usr/bin/true",restrict `+testKey+"\n")

	_, _, err := LoadAuthorizedKeys([]string{path})
	if err == nil {
		t.Fatal("want a refusal for an authorized_keys entry with options")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("the error does not name the offending line: %v", err)
	}
}

func TestLoadAuthorizedKeys_ReportsWhenNoFileHoldsAKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := writeFile(t, dir, "authorized_keys", "# no keys here\n")

	for _, paths := range [][]string{
		{filepath.Join(dir, "absent", "authorized_keys")},
		{empty, filepath.Join(dir, "absent", "authorized_keys")},
	} {
		_, _, err := LoadAuthorizedKeys(paths)
		if err == nil {
			t.Fatalf("LoadAuthorizedKeys(%v) = nil, want an error: no key was found", paths)
		}
		if !strings.Contains(err.Error(), "--host-authorized-keys") {
			t.Errorf("the error does not say which flag asked for the keys: %v", err)
		}
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

// The opencode.json a real operator would supply: the shape the base image
// ships, with one permission tightened.
const testOpencodeConfig = `{
  "$schema": "https://opencode.ai/config.json",
  "permission": {
    "edit": "allow",
    "bash": "ask",
    "webfetch": "allow"
  }
}
`

func TestGenerate_InstallsTheOperatorsOpencodeConfig(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.OpencodeConfig = []byte(testOpencodeConfig)
	opts.OpencodeSource = "/home/operator/opencode.json"

	got, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	golden.Assert(t, "user-data-opencode.yaml", got)

	// /etc/skel is what the account cloud-init creates inherits, and /root has
	// its own copy because a home directory that already exists never consults
	// skel. Missing either leaves one of the two accounts on the image's file.
	for _, path := range []string{
		"path: /etc/skel/.config/opencode/opencode.json",
		"path: /root/.config/opencode/opencode.json",
	} {
		if !strings.Contains(string(got), path) {
			t.Errorf("user-data does not write %s:\n%s", path, got)
		}
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(testOpencodeConfig))
	if !strings.Contains(string(got), "content: "+encoded) {
		t.Errorf("the config is not carried base64-encoded:\n%s", got)
	}
	// Emitting JSON into YAML unencoded is what this guards against: the braces
	// and quotes would be read as YAML rather than as the document's contents.
	if strings.Contains(string(got), `"$schema"`) {
		t.Errorf("the config was emitted as raw JSON inside the YAML:\n%s", got)
	}
}

func TestGenerate_OmitsWriteFilesWithoutAnOpencodeConfig(t *testing.T) {
	t.Parallel()
	got, err := Generate(options())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(got), "write_files") {
		t.Errorf("user-data writes files nobody asked for:\n%s", got)
	}
}

func TestGenerate_RejectsAnOpencodeConfigThatIsNotJSON(t *testing.T) {
	t.Parallel()
	opts := options()
	// A YAML file named opencode.json is the mistake worth catching: opencode
	// would refuse to start, minutes after create reported success.
	opts.OpencodeConfig = []byte("permission:\n  bash: allow\n")
	opts.OpencodeSource = "/home/operator/opencode.json"

	_, err := Generate(opts)
	if err == nil {
		t.Fatal("want a refusal to install an opencode.json that is not JSON")
	}
	if strings.Contains(err.Error(), "permission") {
		t.Errorf("the error echoed the file's contents: %v", err)
	}
	if !strings.Contains(err.Error(), "/home/operator/opencode.json") {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// TestGenerateMetaData_MatchesTheMetaDataContract pins the second file on the
// seed. It is a public contract for the same reason user-data is: it is what
// cloud-init reads to decide the guest is a new instance.
func TestGenerateMetaData_MatchesTheMetaDataContract(t *testing.T) {
	t.Parallel()
	got, err := GenerateMetaData(options())
	if err != nil {
		t.Fatalf("GenerateMetaData: %v", err)
	}
	golden.Assert(t, "meta-data.yaml", got)
}

// The instance id must be the VM's own name: cloud-init reruns its per-instance
// modules when the id changes, and two VMs sharing one would each believe they
// had already been configured as the other.
func TestGenerateMetaData_NamesTheInstanceAfterTheVM(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.Hostname = "agent-42"

	got, err := GenerateMetaData(opts)
	if err != nil {
		t.Fatalf("GenerateMetaData: %v", err)
	}
	for _, want := range []string{`instance-id: "agent-42"`, `local-hostname: "agent-42"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("meta-data does not contain %q:\n%s", want, got)
		}
	}
}

// meta-data is written before the keys are known to be usable, so it must not
// borrow user-data's validation: a VM name is all it needs.
func TestGenerateMetaData_NeedsNoSSHKeys(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.SSHAuthorizedKeys = nil

	if _, err := GenerateMetaData(opts); err != nil {
		t.Errorf("GenerateMetaData without keys: %v", err)
	}
}

func TestGenerateMetaData_RejectsAnInvalidVMName(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.Hostname = "../etc/passwd"

	if _, err := GenerateMetaData(opts); err == nil {
		t.Error("GenerateMetaData accepted a VM name that is not one")
	}
}

func TestGenerate_RejectsOperatorUserDataContainingTheMIMEBoundary(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.ExtraUserData = []byte("#cloud-config\nwrite_files:\n  - content: |\n--agent-vm-cloud-init\n      path: /etc/motd\n")
	opts.ExtraSource = "extra.yaml"

	for name, run := range map[string]func() error{
		"Generate": func() error { _, err := Generate(opts); return err },
		"Validate": opts.Validate,
	} {
		var validation *config.ValidationError
		if err := run(); !errors.As(err, &validation) {
			t.Errorf("%s = %v, want a ValidationError: the line would split the document", name, err)
		}
	}
}
