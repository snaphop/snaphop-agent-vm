package tailscale

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canaryKey = "tskey-auth-test-CANARYKEY1234567890"

func TestReadAuthKey_AcceptsOneLineAndRedactsIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tskey")
	if err := os.WriteFile(path, []byte("  "+canaryKey+"\n"), 0o600); err != nil {
		t.Fatalf("writing the key file: %v", err)
	}

	key, err := ReadAuthKey(path)
	if err != nil {
		t.Fatalf("ReadAuthKey: %v", err)
	}
	got := fmt.Sprintf("%v %+v %#v", key, key, key)
	if strings.Contains(got, canaryKey) || strings.Contains(got, "CANARY") {
		t.Fatalf("formatting the key printed it: %s", got)
	}
	if !strings.Contains(got, "tailscale-auth-key") {
		t.Fatalf("formatting the key = %q, want the redacted marker", got)
	}

	stdinBytes, err := io.ReadAll(key.Reader())
	if err != nil {
		t.Fatalf("reading the key back: %v", err)
	}
	stdin := string(stdinBytes)
	if stdin != canaryKey {
		t.Fatalf("stdin = %q, want the trimmed key", stdin)
	}
}

func TestReadAuthKey_RejectsAFileThatIsNotOneKey(t *testing.T) {
	t.Parallel()
	for _, contents := range []string{
		"",
		"\n",
		"too-short",
		canaryKey + "\n" + canaryKey,
		"not a key " + canaryKey,
		"$(cat /etc/shadow)",
		strings.Repeat("a", 4097),
	} {
		path := filepath.Join(t.TempDir(), "tskey")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("writing the key file: %v", err)
		}
		_, err := ReadAuthKey(path)
		if err == nil {
			t.Fatalf("ReadAuthKey accepted %q", contents)
		}
		if strings.Contains(err.Error(), canaryKey) || strings.Contains(err.Error(), "shadow") {
			t.Fatalf("the error quotes the file: %v", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("the error does not name the file: %v", err)
		}
	}
}

func TestReadAuthKey_MissingFileNamesThePath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing")
	_, err := ReadAuthKey(path)
	if err == nil {
		t.Fatal("ReadAuthKey accepted a missing file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %v, want the path", err)
	}
}

func TestPrepare_AcceptsAJoinAndDropsDuplicateTags(t *testing.T) {
	t.Parallel()
	got, err := Prepare(Options{
		Hostname:      "agent-01",
		Operator:      "agent",
		LoginServer:   "https://headscale.example.com",
		AdvertiseTags: []string{"tag:ci", " tag:ci ", "tag:vm"},
		Ephemeral:     true,
		SSH:           true,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if strings.Join(got.AdvertiseTags, ",") != "tag:ci,tag:vm" {
		t.Fatalf("tags = %v, want tag:ci then tag:vm once each", got.AdvertiseTags)
	}
	args := strings.Join(UpCommand(got), " ")
	for _, want := range []string{
		"--hostname=agent-01",
		"--operator=agent",
		"--ephemeral",
		"--ssh",
		"--login-server=https://headscale.example.com",
		"--advertise-tags=tag:ci,tag:vm",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("up command missing %q: %s", want, args)
		}
	}
	if strings.Contains(args, canaryKey) || strings.Contains(args, "auth-key") {
		t.Errorf("up command carries key material: %s", args)
	}
}

func TestPrepare_RejectsAJoinThatWouldChangeTheCommand(t *testing.T) {
	t.Parallel()
	ok := Options{Hostname: "agent-01", Operator: "agent"}
	for _, opts := range []Options{
		{Hostname: "Agent_01", Operator: "agent"},
		{Hostname: "-agent", Operator: "agent"},
		{Hostname: "agent-01", Operator: "root;id"},
		{Hostname: "agent-01", Operator: "agent", LoginServer: "http://headscale.example.com"},
		{Hostname: "agent-01", Operator: "agent", LoginServer: "https://user:secret@headscale.example.com"},
		{Hostname: "agent-01", Operator: "agent", AdvertiseTags: []string{"ci"}},
		{Hostname: "agent-01", Operator: "agent", AdvertiseTags: []string{"tag:ci;rm"}},
	} {
		if _, err := Prepare(opts); err == nil {
			t.Errorf("Prepare accepted %+v", opts)
		}
	}
	if _, err := Prepare(ok); err != nil {
		t.Fatalf("Prepare rejected a plain join: %v", err)
	}
}

func TestUpCommand_DoesNotIncludeTheAuthKey(t *testing.T) {
	t.Parallel()
	opts, err := Prepare(Options{Hostname: "agent-01", Operator: "agent"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	for _, arg := range UpCommand(opts) {
		if strings.Contains(arg, "tskey-") || strings.Contains(arg, "file:") {
			t.Errorf("argument %q carries an auth key", arg)
		}
	}
	joined := strings.Join(append(append(CopyCommand(), ChmodCommand()...), InstallCommand()...), " ")
	if strings.Contains(joined, "up") {
		t.Errorf("install commands include up: %s", joined)
	}
}

func TestScript_InstallsWithoutReadingTheKeyAndRemovesItAfterUp(t *testing.T) {
	t.Parallel()
	script, err := Script()
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	text := string(script)
	for _, want := range []string{
		"set -eu",
		"https://tailscale.com/install.sh",
		"--auth-key=\"file:",
		"rm -f \"$keyfile\"",
		"sh \"$installer\" </dev/null",
		"command -v tailscale",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("join script missing %q", want)
		}
	}
	// The key file is written from stdin and deleted. It is never printed.
	for _, forbidden := range []string{
		"cat \"$keyfile\"",
		"echo \"$keyfile\"",
		"printf %s \"$keyfile\"",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("join script prints the auth key via %q", forbidden)
		}
	}
}

func TestIPv4FromOutput_TakesTheLastAddressAndIgnoresTheRest(t *testing.T) {
	t.Parallel()
	got := IPv4FromOutput([]byte("Success.\n100.64.0.1\nfd7a:115c:a1e0::1\n100.64.0.2\n"))
	if got != "100.64.0.2" {
		t.Fatalf("IPv4 = %q, want 100.64.0.2", got)
	}
	if got := IPv4FromOutput([]byte("no address\n")); got != "" {
		t.Fatalf("IPv4 = %q, want none", got)
	}
}
