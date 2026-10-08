package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

const registrationToken = "AABFregistrationCANARYTOKEN1234567890"

func TestRegistrationToken_ReturnsTheTokenAndRedactsIt(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh api --method POST orgs/acme/actions/runners/registration-token",
		hostexec.FakeResponse{Stdout: registrationToken + "\n"})

	token, err := New(fake).RegistrationToken(context.Background(), "acme")
	if err != nil {
		t.Fatalf("RegistrationToken: %v", err)
	}
	got, err := io.ReadAll(token.Reader())
	if err != nil {
		t.Fatalf("reading the token: %v", err)
	}
	if string(got) != registrationToken {
		t.Fatalf("stdin token = %q, want the token with no added newline", got)
	}
	rendered := fmt.Sprintf("%v %#v %+v %s", token, token, token, token.GoString())
	if strings.Contains(rendered, registrationToken) {
		t.Fatalf("formatting the token printed it: %s", rendered)
	}
	if !strings.Contains(rendered, "github-registration-token") {
		t.Fatalf("formatting did not redact: %s", rendered)
	}
	if !fake.Ran("gh api --method POST orgs/acme/actions/runners/registration-token --jq .token") {
		t.Errorf("gh was not asked for a registration token:\n%s", fake)
	}
}

func TestRegistrationToken_DoesNotEchoOutputThatIsNotAToken(t *testing.T) {
	t.Parallel()
	for _, stdout := range []string{"null\n", "\"quoted-token-value-12345\"\n", "short\n", registrationToken + " extra\n"} {
		t.Run(stdout, func(t *testing.T) {
			t.Parallel()
			fake := hostexec.NewFake()
			fake.RespondPrefix("gh api --method POST", hostexec.FakeResponse{Stdout: stdout})

			_, err := New(fake).RegistrationToken(context.Background(), "acme")
			if err == nil {
				t.Fatal("output that is not one token was accepted")
			}
			if strings.Contains(err.Error(), strings.TrimSpace(stdout)) {
				t.Errorf("the error echoes the rejected output:\n%v", err)
			}
		})
	}
}

func TestRegistrationToken_RedactsItselfFromAToolError(t *testing.T) {
	t.Parallel()
	token := RegistrationToken{raw: registrationToken}
	err := token.RedactError(&hostexec.ToolError{
		Tool:     "ssh",
		Argv:     []string{"ssh", "agent@192.0.2.1", "sudo", "-n", "configure"},
		ExitCode: 1,
		Stderr:   "config.sh: bad token " + registrationToken,
	})
	if strings.Contains(err.Error(), registrationToken) {
		t.Errorf("the tool error still contains the token:\n%v", err)
	}
	if !strings.Contains(err.Error(), "github-registration-token") {
		t.Errorf("the tool error was not redacted:\n%v", err)
	}
}

func TestRunnerID_ReadsTheSingleMatchingID(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh api --paginate orgs/acme/actions/runners", hostexec.FakeResponse{Stdout: "884422\n"})

	id, err := New(fake).RunnerID(context.Background(), "acme", "agent-01")
	if err != nil {
		t.Fatalf("RunnerID: %v", err)
	}
	if id != 884422 {
		t.Errorf("id = %d, want 884422", id)
	}
	if !fake.Ran(`gh api --paginate orgs/acme/actions/runners --jq .runners[] | select(.name == "agent-01") | .id`) {
		t.Errorf("the runner was not looked up by name:\n%s", fake)
	}
}

func TestRunnerID_ReportsNoneAndRefusesTwo(t *testing.T) {
	t.Parallel()
	none := hostexec.NewFake()
	none.RespondPrefix("gh api --paginate", hostexec.FakeResponse{Stdout: "\n"})
	if _, err := New(none).RunnerID(context.Background(), "acme", "agent-01"); !errors.As(err, new(*RunnerNotFoundError)) {
		t.Fatalf("err = %v, want *RunnerNotFoundError", err)
	}

	two := hostexec.NewFake()
	two.RespondPrefix("gh api --paginate", hostexec.FakeResponse{Stdout: "10\n11\n"})
	_, err := New(two).RunnerID(context.Background(), "acme", "agent-01")
	if err == nil || errors.As(err, new(*RunnerNotFoundError)) {
		t.Fatalf("err = %v, want a refusal to guess", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("the error does not say two runners matched: %v", err)
	}
}

func TestDeleteRunner_RemovesByIDAndTreats404AsDone(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	removed, err := New(fake).DeleteRunner(context.Background(), "acme", 884422)
	if err != nil || !removed {
		t.Fatalf("DeleteRunner = %v, %v; want removed", removed, err)
	}
	if !fake.Ran("gh api --method DELETE orgs/acme/actions/runners/884422 --silent") {
		t.Errorf("the runner was not deleted by id:\n%s", fake)
	}

	gone := hostexec.NewFake()
	gone.RespondPrefix("gh api --method DELETE", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "gh: Not Found (HTTP 404)\n",
	})
	removed, err = New(gone).DeleteRunner(context.Background(), "acme", 884422)
	if err != nil || removed {
		t.Fatalf("DeleteRunner of a missing runner = %v, %v; want not removed and no error", removed, err)
	}
}

func TestCheckRunnerAuth_AcceptsARunnerScopeOrNoScopeLine(t *testing.T) {
	t.Parallel()
	for _, scopes := range []string{"'admin:org'", "'manage_runners:org', 'repo'", ""} {
		t.Run(scopes, func(t *testing.T) {
			t.Parallel()
			fake := hostexec.NewFake()
			stdout := "github.com\n  ✓ Logged in to github.com account wensington (GITHUB_TOKEN)\n"
			if scopes != "" {
				stdout = authStatus(t, scopes)
			}
			fake.RespondPrefix("gh auth status", hostexec.FakeResponse{Stdout: stdout})
			if err := New(fake).CheckRunnerAuth(context.Background()); err != nil {
				t.Fatalf("CheckRunnerAuth: %v", err)
			}
		})
	}
}

func TestCheckRunnerAuth_RefusesATokenThatCannotManageRunners(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{Stdout: authStatus(t, "'read:org', 'repo'")})

	err := New(fake).CheckRunnerAuth(context.Background())
	if err == nil {
		t.Fatal("a token without a runner scope was accepted")
	}
	if !strings.Contains(err.Error(), "gh auth refresh -h github.com -s admin:org") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

func TestCheckRunnerAuth_NamesTheFixWhenGHIsNotLoggedIn(t *testing.T) {
	t.Parallel()
	fake := hostexec.NewFake()
	fake.RespondPrefix("gh auth status", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "You are not logged into any GitHub hosts.\n",
	})

	err := New(fake).CheckRunnerAuth(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("err = %v, want it to name `gh auth login`", err)
	}
}
