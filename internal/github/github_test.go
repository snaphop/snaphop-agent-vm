package github

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// A real ed25519 public key: the shape of what a guest generates for itself.
const publicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1TfEt0YKXQ+eZmJHCcTKQ0lMSzQFm/kQGHMvhE7Hqx agent@build-01"

func TestAddKey_ReturnsTheIDGitHubAssigned(t *testing.T) {
	fake := hostexec.NewFake()
	// gh --jq extracts the field, so the whole answer is the id.
	fake.RespondPrefix("gh api --method POST user/keys", hostexec.FakeResponse{Stdout: "119548016\n"})

	id, err := New(fake).AddKey(context.Background(), "agent-vm build-01 on workstation", publicKey)
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if id != 119548016 {
		t.Errorf("id = %d, want 119548016", id)
	}

	argv := strings.Join(fake.Argvs(), "\n")
	for _, want := range []string{
		"-f title=agent-vm build-01 on workstation",
		"-f key=" + publicKey,
		"--jq .id",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("gh was not asked with %q:\n%s", want, argv)
		}
	}
}

func TestAddKey_ReportsOutputItCannotRead(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh api", hostexec.FakeResponse{Stdout: "null\n"})

	_, err := New(fake).AddKey(context.Background(), "agent-vm build-01", publicKey)
	var parse *hostexec.ParseError
	if !errors.As(err, &parse) {
		t.Fatalf("err = %v, want a *hostexec.ParseError", err)
	}
}

func TestAddKey_RefusesAnEmptyKey(t *testing.T) {
	fake := hostexec.NewFake()
	if _, err := New(fake).AddKey(context.Background(), "agent-vm build-01", "  \n"); err == nil {
		t.Fatal("an empty key was accepted")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("gh was run for an empty key:\n%s", fake)
	}
}

func TestDeleteKey_RemovesTheKeyByID(t *testing.T) {
	fake := hostexec.NewFake()

	removed, err := New(fake).DeleteKey(context.Background(), 119548016)
	if err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if !removed {
		t.Error("removed = false, want true")
	}
	if !fake.Ran("gh api --method DELETE user/keys/119548016 --silent") {
		t.Errorf("the key was not deleted by id:\n%s", fake)
	}
}

// A key an operator already removed on github.com is the end state that was
// asked for, so destroy must not fail on it.
func TestDeleteKey_TreatsAKeyThatIsAlreadyGoneAsDone(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh api --method DELETE", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: Not Found (HTTP 404)\n",
	})

	removed, err := New(fake).DeleteKey(context.Background(), 119548016)
	if err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if removed {
		t.Error("removed = true, want false for a key that was already gone")
	}
}

func TestDeleteKey_ReportsAnyOtherFailure(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh api --method DELETE", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: Bad credentials (HTTP 401)\n",
	})

	if _, err := New(fake).DeleteKey(context.Background(), 119548016); err == nil {
		t.Fatal("an authentication failure was reported as success")
	}
}

func TestCheckAuth_NamesTheFixWhenGHIsNotLoggedIn(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "You are not logged into any GitHub hosts.\n",
	})

	err := New(fake).CheckAuth(context.Background())
	if err == nil {
		t.Fatal("a logged-out gh was accepted")
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

// The captured output of a login without the key scope: the scope line is the
// only difference from the fixture, which is a real login of that kind.
func authStatus(t *testing.T, scopes string) string {
	t.Helper()
	captured, err := os.ReadFile(filepath.Join("..", "..", "test", "toolout", "gh-auth-status.txt"))
	if err != nil {
		t.Fatalf("reading the gh auth status fixture: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(captured), "\n"), "\n")
	for i, line := range lines {
		if strings.Contains(line, "Token scopes:") {
			lines[i] = "  - Token scopes: " + scopes
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestCheckAuth_RefusesATokenThatCannotAddKeys(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{
		Stdout: authStatus(t, "'read:org', 'repo'"),
	})

	err := New(fake).CheckAuth(context.Background())
	if err == nil {
		t.Fatal("a token without the key scope was accepted; the failure would come after the VM exists")
	}
	if !strings.Contains(err.Error(), "gh auth refresh -h github.com -s admin:public_key") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

func TestCheckAuth_AcceptsATokenWithTheKeyScope(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{
		Stdout: authStatus(t, "'admin:public_key', 'read:org', 'repo'"),
	})

	if err := New(fake).CheckAuth(context.Background()); err != nil {
		t.Fatalf("CheckAuth: %v", err)
	}
}

// A login gh reports no scopes for -- a GITHUB_TOKEN from the environment --
// is not refused: the API is the authority, and a missing line is not evidence
// of a missing scope.
func TestCheckAuth_AcceptsALoginWithNoScopeLine(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{
		Stdout: "github.com\n  ✓ Logged in to github.com account wensington (GITHUB_TOKEN)\n",
	})

	if err := New(fake).CheckAuth(context.Background()); err != nil {
		t.Fatalf("CheckAuth: %v", err)
	}
}
