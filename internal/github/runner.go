package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

// runnerScopeAdmin and runnerScopeManage are the classic token scopes that
// can register and delete an organization's Actions runners. Either one is
// enough. A login with no scope line is left to the API, the same way key
// management treats a GITHUB_TOKEN.
const (
	runnerScopeAdmin  = "admin:org"
	runnerScopeManage = "manage_runners:org"
)

const registrationTokenRedacted = "github-registration-token"

// A registration token is a single visible ASCII value. Shorter output is
// `null` or an error fragment; longer output is not a token. The bounds keep
// a page of JSON, or a quoted string, from being handed to the guest.
const (
	minRegistrationTokenLength = 20
	maxRegistrationTokenLength = 512
)

// RegistrationToken is a short-lived Actions runner registration token.
// Formatting it yields a redacted marker, so a log, an error, or a %#v of a
// value that holds one cannot print the token.
type RegistrationToken struct {
	raw string
}

func (t RegistrationToken) String() string   { return registrationTokenRedacted }
func (t RegistrationToken) GoString() string { return registrationTokenRedacted }

// Reader is the token as the guest's configure command must receive it: on
// stdin, with no trailing newline. The wrapper's `registration=$(cat)` keeps
// a trailing newline off the value, and adding one here would still be a
// second copy of the secret in the byte stream.
func (t RegistrationToken) Reader() io.Reader {
	return bytes.NewReader([]byte(t.raw))
}

// Redact replaces the token in s. An empty token replaces nothing: replacing
// the empty string would rewrite every boundary.
func (t RegistrationToken) Redact(s string) string {
	if t.raw == "" {
		return s
	}
	return strings.ReplaceAll(s, t.raw, registrationTokenRedacted)
}

// RedactError removes the token from a tool failure's stderr. config.sh
// receives the token as an argument inside the guest and can echo it, and
// ToolError.Error includes that stderr. The raw token stays unexported.
func (t RegistrationToken) RedactError(err error) error {
	var toolErr *hostexec.ToolError
	if errors.As(err, &toolErr) {
		toolErr.Stderr = t.Redact(toolErr.Stderr)
	}
	return err
}

// OrgURL is the GitHub URL an organization runner is configured against.
func OrgURL(org string) string { return "https://github.com/" + org }

// RegistrationTokenArgs is the gh invocation that mints a registration token.
// The path has no leading slash, matching the other gh api calls in this
// package. The token is the command's stdout, never an argument.
func RegistrationTokenArgs(org string) []string {
	return []string{
		"api", "--method", "POST",
		"orgs/" + org + "/actions/runners/registration-token",
		"--jq", ".token",
	}
}

// RunnerIDArgs lists the organization's runners and prints the id of the one
// named name. The id comes from GitHub, not from the guest: the guest is
// untrusted, and a forged id could make destroy delete the wrong runner.
func RunnerIDArgs(org, name string) []string {
	return []string{
		"api", "--paginate",
		"orgs/" + org + "/actions/runners",
		"--jq", fmt.Sprintf(`.runners[] | select(.name == %q) | .id`, name),
	}
}

// DeleteRunnerArgs removes one runner by GitHub's id. --silent keeps the
// empty body off stdout.
func DeleteRunnerArgs(org string, id int64) []string {
	return []string{
		"api", "--method", "DELETE",
		"orgs/" + org + "/actions/runners/" + strconv.FormatInt(id, 10),
		"--silent",
	}
}

// CheckRunnerAuth confirms gh can manage an organization's runners. It runs
// before a VM is created and before a runner is deleted, so a missing scope
// is not mistaken for a runner that is already gone. A login with no scope
// line is accepted: fine-grained tokens and GITHUB_TOKEN omit it, and the
// API remains the authority.
func (c *Client) CheckRunnerAuth(ctx context.Context) error {
	status, err := c.ghAuthStatus(ctx)
	if err != nil {
		return fmt.Errorf("gh is not logged in to github.com; run `gh auth login`: %w", err)
	}
	scopes, found := tokenScopes(status)
	if !found {
		return nil
	}
	if slicesContainsRunnerScope(scopes) {
		return nil
	}
	return fmt.Errorf("gh is logged in to github.com, but its token does not have the %q or %q scope "+
		"that registering Actions runners needs; add one with `gh auth refresh -h github.com -s %s`",
		runnerScopeAdmin, runnerScopeManage, runnerScopeAdmin)
}

func slicesContainsRunnerScope(scopes []string) bool {
	for _, scope := range scopes {
		if scope == runnerScopeAdmin || scope == runnerScopeManage {
			return true
		}
	}
	return false
}

// RegistrationToken mints a registration token for org. The value is not
// logged: hostexec records argv, and the token is stdout, which is returned
// only inside RegistrationToken.
func (c *Client) RegistrationToken(ctx context.Context, org string) (RegistrationToken, error) {
	res, err := c.runner.Run(ctx, hostexec.Command{
		Name:     hostexec.GH.Name,
		Args:     RegistrationTokenArgs(org),
		Effect:   hostexec.Mutate,
		Location: hostexec.Client,
	})
	if err != nil {
		return RegistrationToken{}, err
	}
	token := strings.TrimSpace(string(res.Stdout))
	if !validRegistrationToken(token) {
		// The output is not echoed. A rejected value may be a real token that
		// failed the shape check, and ParseError.Error prints Output.
		return RegistrationToken{}, &hostexec.ParseError{
			Tool:   hostexec.GH.Name,
			What:   "a registration token",
			Output: "gh did not print one registration token",
		}
	}
	return RegistrationToken{raw: token}, nil
}

func validRegistrationToken(token string) bool {
	if len(token) < minRegistrationTokenLength || len(token) > maxRegistrationTokenLength {
		return false
	}
	for _, r := range token {
		// Quotes are rejected as well as space and controls. `gh --jq` prints
		// a raw token; a quoted string is JSON that was not extracted, and
		// sending it would register nothing.
		if r < 0x21 || r > 0x7e || r == '"' || r == '\'' {
			return false
		}
	}
	return true
}

// RunnerNotFoundError is an organization with no runner of this name. Destroy
// treats it as already done when the recorded id is missing. More than one
// match is a different error: deleting either would be a guess.
type RunnerNotFoundError struct {
	Org  string
	Name string
}

func (e *RunnerNotFoundError) Error() string {
	return fmt.Sprintf("GitHub has no Actions runner named %q in %s", e.Name, e.Org)
}

// RunnerID returns the id of the single runner named name in org.
func (c *Client) RunnerID(ctx context.Context, org, name string) (int64, error) {
	res, err := c.runner.Run(ctx, hostexec.Command{
		Name:     hostexec.GH.Name,
		Args:     RunnerIDArgs(org, name),
		Effect:   hostexec.Read,
		Location: hostexec.Client,
	})
	if err != nil {
		return 0, err
	}
	return parseRunnerID(string(res.Stdout), org, name)
}

func parseRunnerID(output, org, name string) (int64, error) {
	var ids []int64
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, err := strconv.ParseInt(line, 10, 64)
		if err != nil || id <= 0 {
			return 0, &hostexec.ParseError{
				Tool:   hostexec.GH.Name,
				What:   fmt.Sprintf("the id of the Actions runner %s in %s", name, org),
				Output: output,
			}
		}
		ids = append(ids, id)
	}
	switch len(ids) {
	case 0:
		return 0, &RunnerNotFoundError{Org: org, Name: name}
	case 1:
		return ids[0], nil
	default:
		return 0, fmt.Errorf("GitHub has %d Actions runners named %q in %s; refusing to guess which one to remove",
			len(ids), name, org)
	}
}

// DeleteRunner removes the runner with this id. A runner that is already gone
// is not an error — the end state is the one that was asked for — and is
// reported by returning false so the caller can say so.
func (c *Client) DeleteRunner(ctx context.Context, org string, id int64) (removed bool, err error) {
	if id <= 0 {
		return false, errors.New("a GitHub Actions runner id is required")
	}
	_, err = c.runner.Run(ctx, hostexec.Command{
		Name:     hostexec.GH.Name,
		Args:     DeleteRunnerArgs(org, id),
		Effect:   hostexec.Mutate,
		Location: hostexec.Client,
	})
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, err
	}
}
