package hostexec

import (
	"fmt"
	"strings"
	"time"
)

// ToolError is a helper process exiting non-zero. It deliberately carries the
// tool, its argv, its exit status, and a bounded stderr excerpt: "operation
// failed" is not acceptable output from a tool that drives another program.
type ToolError struct {
	Tool     string
	Argv     []string
	ExitCode int
	Stderr   string
	Duration time.Duration
	// Host is the machine the tool ran on, empty when that is this one. An
	// operator rerunning the command by hand has to know which machine to run
	// it on, and "it works here" is the wrong conclusion to let them draw.
	Host string
}

func (e *ToolError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s exited %d", e.Tool, e.ExitCode)
	if e.Host != "" {
		fmt.Fprintf(&b, " on %s", e.Host)
	}
	fmt.Fprintf(&b, "\n  command: %s", quoteArgv(e.Argv))
	if e.Stderr != "" {
		fmt.Fprintf(&b, "\n  stderr: %s", indent(e.Stderr))
	}
	return b.String()
}

// NotFoundError is a tool missing from PATH. It is a host-readiness failure
// (exit 3), not a generic error, and names the command that installs it where
// we know one.
type NotFoundError struct {
	Tool string
	// Package names the host package providing the tool, when known.
	Package string
}

func (e *NotFoundError) Error() string {
	if e.Package != "" {
		return fmt.Sprintf("%s not found on PATH; install it (package %q) and run `agent-vm doctor`", e.Tool, e.Package)
	}
	return fmt.Sprintf("%s not found on PATH; install it and run `agent-vm doctor`", e.Tool)
}

// TimeoutError is a helper process exceeding its deadline.
type TimeoutError struct {
	Tool    string
	Argv    []string
	Timeout time.Duration
	// Host is the machine the tool ran on, empty when that is this one.
	Host string
}

func (e *TimeoutError) Error() string {
	where := ""
	if e.Host != "" {
		where = " on " + e.Host
	}
	return fmt.Sprintf("%s did not finish within %s%s\n  command: %s", e.Tool, e.Timeout, where, quoteArgv(e.Argv))
}

// VersionError is a tool present but older than this project's floor. Raising a
// floor can stop the tool working on a host where it worked yesterday, so the
// message states both versions rather than just failing.
type VersionError struct {
	Tool    string
	Found   Version
	Minimum Version
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("%s %s is too old: %s %s or newer is required", e.Tool, e.Found, e.Tool, e.Minimum)
}

// ParseError is a tool producing output we could not read. It is an error
// rather than an empty result on purpose: "no address found" and "the output
// format changed" must not be indistinguishable.
type ParseError struct {
	Tool   string
	What   string
	Output string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("could not parse %s from %s output: %s", e.What, e.Tool, indent(excerpt([]byte(e.Output))))
}

func quoteArgv(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func indent(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ")
}
