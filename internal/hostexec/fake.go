package hostexec

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
	// MatchFunc is consulted before the argv rules, for invocations that are
	// easier to describe as a predicate than as a literal argument vector —
	// the same tool asked about two different paths, for instance.
	MatchFunc func(c Command) (FakeResponse, bool)
	// Default answers any invocation no other rule matched.
	Default FakeResponse

	// Hypervisor is what HypervisorHost reports, for tests that exercise the
	// remote-hypervisor branches.
	Hypervisor string

	mu      sync.Mutex
	calls   []Command
	became  []Command
	started []Command
}

// FakeResponse is a canned outcome for one invocation.
type FakeResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
	// Do runs before the response is returned, for the tools whose real effect
	// a later step depends on — virt-make-fs producing a disk, virt-copy-out
	// producing a kernel. Without it a test could not exercise the steps that
	// read what an earlier tool wrote.
	Do func(c Command) error
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

// Run records the invocation and returns the matching canned response. Like
// the real runner, it refuses to run anything once ctx is done, so a caller
// that keeps using a canceled context fails here as it would on a host.
func (f *Fake) Run(ctx context.Context, c Command) (*Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("running %s: %w", c.Name, err)
	}
	if f.Missing[c.Name] {
		return nil, &NotFoundError{Tool: c.Name}
	}

	r := f.lookup(c)
	if r.Do != nil {
		if err := r.Do(c); err != nil {
			return nil, err
		}
	}
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

func (f *Fake) lookup(c Command) FakeResponse {
	if f.MatchFunc != nil {
		if r, ok := f.MatchFunc(c); ok {
			return r
		}
	}

	argv := strings.Join(c.Argv(), " ")
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

// Become records the invocation instead of replacing the test process, which
// is the one thing a test cannot let happen.
func (f *Fake) Become(c Command) error {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.became = append(f.became, c)
	f.mu.Unlock()

	if f.Missing[c.Name] {
		return &NotFoundError{Tool: c.Name}
	}
	r := f.lookup(c)
	return r.Err
}

// Start records the invocation and returns a process wired to the canned
// response: its stdout replays the response, and its stdin is discarded.
func (f *Fake) Start(_ context.Context, c Command) (*Process, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.started = append(f.started, c)
	f.mu.Unlock()

	if f.Missing[c.Name] {
		return nil, &NotFoundError{Tool: c.Name}
	}
	r := f.lookup(c)
	if r.Err != nil {
		return nil, r.Err
	}
	if r.ExitCode != 0 {
		return nil, &ToolError{Tool: c.Name, Argv: c.Argv(), ExitCode: r.ExitCode, Stderr: r.Stderr}
	}
	return &Process{
		Stdin:  nopWriteCloser{io.Discard},
		Stdout: bufio.NewReader(strings.NewReader(r.Stdout)),
	}, nil
}

// Started returns the commands this Fake was asked to start and not wait for.
func (f *Fake) Started() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Command(nil), f.started...)
}

// HypervisorHost is the machine a test says host tools run on. It is empty
// unless a test sets it.
func (f *Fake) HypervisorHost() string { return f.Hypervisor }

// Render is the command itself: a Fake stands in for the whole process
// boundary, transport included.
func (f *Fake) Render(c Command) string { return c.String() }

// Became returns the commands this process was asked to be replaced by.
func (f *Fake) Became() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Command(nil), f.became...)
}

// LookPath reports a tool as present unless it is listed in Missing.
func (f *Fake) LookPath(name string, _ Location) (string, error) {
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
