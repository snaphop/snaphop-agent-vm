package hostexec

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Fake is a Runner for tests. It is the substitution point at the process
// boundary that AGENTS.md §7 requires: tests replace virt-install, virsh,
// qemu-img, podman and the libguestfs tools here, and never mock inside our own
// packages.
//
// Responses are keyed by the whole argument vector joined with spaces, so a
// test states exactly which invocation it is answering. Prefix matching is
// available for callers that only care about the subcommand.
type Fake struct {
	// Responses maps a full argv ("virsh --version") to its outcome.
	Responses map[string]FakeResponse
	// PrefixResponses matches an argv prefix; the longest match wins.
	PrefixResponses map[string]FakeResponse
	// Missing lists tools that are not installed.
	Missing map[string]bool
	// Default answers any invocation no other rule matched.
	Default FakeResponse

	mu    sync.Mutex
	calls []Command
}

// FakeResponse is a canned outcome for one invocation.
type FakeResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// NewFake returns an empty Fake that succeeds silently by default.
func NewFake() *Fake {
	return &Fake{
		Responses:       map[string]FakeResponse{},
		PrefixResponses: map[string]FakeResponse{},
		Missing:         map[string]bool{},
	}
}

// Respond registers a response for an exact argv.
func (f *Fake) Respond(argv string, r FakeResponse) *Fake {
	f.Responses[argv] = r
	return f
}

// RespondPrefix registers a response for any argv starting with prefix.
func (f *Fake) RespondPrefix(prefix string, r FakeResponse) *Fake {
	f.PrefixResponses[prefix] = r
	return f
}

// Run records the invocation and returns the matching canned response.
func (f *Fake) Run(_ context.Context, c Command) (*Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()

	if f.Missing[c.Name] {
		return nil, &NotFoundError{Tool: c.Name}
	}

	r := f.lookup(strings.Join(c.Argv(), " "))
	res := &Result{
		Stdout:   []byte(r.Stdout),
		Stderr:   []byte(r.Stderr),
		ExitCode: r.ExitCode,
	}
	switch {
	case r.Err != nil:
		return res, r.Err
	case r.ExitCode != 0:
		return res, &ToolError{Tool: c.Name, Argv: c.Argv(), ExitCode: r.ExitCode, Stderr: r.Stderr}
	}
	return res, nil
}

func (f *Fake) lookup(argv string) FakeResponse {
	if r, ok := f.Responses[argv]; ok {
		return r
	}
	best := ""
	var bestResp FakeResponse
	for prefix, r := range f.PrefixResponses {
		if strings.HasPrefix(argv, prefix) && len(prefix) > len(best) {
			best, bestResp = prefix, r
		}
	}
	if best != "" {
		return bestResp
	}
	return f.Default
}

// LookPath reports a tool as present unless it is listed in Missing.
func (f *Fake) LookPath(name string) (string, error) {
	if f.Missing[name] {
		return "", &NotFoundError{Tool: name}
	}
	return "/usr/bin/" + name, nil
}

// Calls returns every invocation, in order.
func (f *Fake) Calls() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Command(nil), f.calls...)
}

// Argvs returns every invocation as a joined argv string, for assertions.
func (f *Fake) Argvs() []string {
	out := []string{}
	for _, c := range f.Calls() {
		out = append(out, strings.Join(c.Argv(), " "))
	}
	return out
}

// Ran reports whether an invocation with exactly this argv happened.
func (f *Fake) Ran(argv string) bool {
	for _, got := range f.Argvs() {
		if got == argv {
			return true
		}
	}
	return false
}

// String renders the recorded calls, for test failure messages.
func (f *Fake) String() string {
	return fmt.Sprintf("recorded calls:\n  %s", strings.Join(f.Argvs(), "\n  "))
}
