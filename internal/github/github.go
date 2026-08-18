// Package github adds and removes SSH public keys on the operator's GitHub
// account through the `gh` CLI.
//
// It runs on the host, never in the guest: the guest is untrusted and a GitHub
// token must never reach it (SECURITY.md). What travels the other way is a
// public key the guest generated for itself, so no private material is
// involved in either direction.
//
// `gh api` is used rather than `gh ssh-key add`/`delete` because the API
// subcommand is the machine-readable one: it returns the key's numeric id on
// creation, which is the handle `destroy` needs later, and it takes that id
// back without a confirmation prompt. Matching a key by its title in the
// human-readable `gh ssh-key list` table would be guesswork by comparison.
package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

// Client talks to GitHub through the operator's authenticated `gh`.
type Client struct {
	runner hostexec.Runner
}

// New returns a client that runs gh through runner.
func New(runner hostexec.Runner) *Client {
	return &Client{runner: runner}
}

// keyScope is the token scope POST /user/keys needs. A login without it gets a
// 404 from the API, which says nothing useful on its own.
const keyScope = "admin:public_key"

// CheckAuth confirms gh has credentials for GitHub, and the scope that lets it
// add a key. It is worth running before anything is created, so that an
// expired login or a token that cannot manage keys is a refusal up front
// rather than a failure after a VM exists.
func (c *Client) CheckAuth(ctx context.Context) error {
	res, err := c.runner.Run(ctx, hostexec.Command{
		Name:   hostexec.GH.Name,
		Args:   []string{"auth", "status", "--hostname", "github.com"},
		Effect: hostexec.Read,
	})
	if err != nil {
		return fmt.Errorf("gh is not logged in to github.com; run `gh auth login`: %w", err)
	}
	// gh prints the scopes for a login it holds a token for. A login it cannot
	// enumerate scopes for -- a GITHUB_TOKEN from the environment, or a future
	// format for this output -- is left alone: the API is still the authority,
	// and refusing on a line we failed to find would be worse than letting the
	// request be tried.
	scopes, found := tokenScopes(string(res.Stdout))
	if found && !slices.Contains(scopes, keyScope) {
		return fmt.Errorf("gh is logged in to github.com, but its token does not have the %q scope "+
			"that adding an SSH key needs; add it with `gh auth refresh -h github.com -s %s`",
			keyScope, keyScope)
	}
	return nil
}

// tokenScopes pulls the scopes out of `gh auth status` output
// (test/toolout/gh-auth-status.txt). The second result is false when the
// output has no scope line at all, which is not the same as an empty scope
// list.
func tokenScopes(status string) ([]string, bool) {
	const marker = "Token scopes:"
	for _, line := range strings.Split(status, "\n") {
		_, rest, ok := strings.Cut(line, marker)
		if !ok {
			continue
		}
		var scopes []string
		for _, field := range strings.Split(rest, ",") {
			if scope := strings.Trim(strings.TrimSpace(field), "'\""); scope != "" {
				scopes = append(scopes, scope)
			}
		}
		return scopes, true
	}
	return nil, false
}

// AddKey uploads publicKey as an authentication key and returns its id, which
// is what identifies the key for removal later.
func (c *Client) AddKey(ctx context.Context, title, publicKey string) (int64, error) {
	title = strings.TrimSpace(title)
	publicKey = strings.TrimSpace(publicKey)
	if title == "" {
		return 0, errors.New("a GitHub SSH key needs a title")
	}
	if publicKey == "" {
		return 0, errors.New("no public key to add to GitHub")
	}

	res, err := c.runner.Run(ctx, hostexec.Command{
		Name: hostexec.GH.Name,
		Args: []string{
			"api", "--method", "POST", "user/keys",
			"-f", "title=" + title,
			"-f", "key=" + publicKey,
			// gh does the field extraction, so nothing here parses JSON that
			// GitHub may extend later.
			"--jq", ".id",
		},
		Effect: hostexec.Mutate,
	})
	if err != nil {
		return 0, err
	}

	id, err := strconv.ParseInt(strings.TrimSpace(string(res.Stdout)), 10, 64)
	if err != nil {
		return 0, &hostexec.ParseError{
			Tool:   hostexec.GH.Name,
			What:   "the id of the SSH key it created",
			Output: string(res.Stdout),
		}
	}
	return id, nil
}

// DeleteKey removes the key with this id. A key that is already gone is not an
// error — the end state is the one that was asked for — and is reported by
// returning false so the caller can say so.
func (c *Client) DeleteKey(ctx context.Context, id int64) (removed bool, err error) {
	_, err = c.runner.Run(ctx, hostexec.Command{
		Name: hostexec.GH.Name,
		Args: []string{
			"api", "--method", "DELETE", "user/keys/" + strconv.FormatInt(id, 10),
			// The API returns an empty body; --silent keeps it off stdout.
			"--silent",
		},
		Effect: hostexec.Mutate,
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

// isNotFound reports whether gh failed because the key no longer exists. gh
// exits 1 for any HTTP error and names the status in its stderr, which is the
// only place the distinction appears.
func isNotFound(err error) bool {
	var toolErr *hostexec.ToolError
	if !errors.As(err, &toolErr) {
		return false
	}
	return strings.Contains(toolErr.Stderr, "HTTP 404")
}
