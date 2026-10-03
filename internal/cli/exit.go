package cli

import (
	"errors"
	"fmt"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/domain"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/network"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// Exit codes are a public contract (docs/cli.md): scripts and agent
// supervisors branch on them, so their meanings may not be repurposed.
const (
	ExitOK           = 0 // Success.
	ExitFailure      = 1 // Generic failure.
	ExitUsage        = 2 // Bad flag, argument, name, or size.
	ExitHostNotReady = 3 // Host not ready: no KVM, no libvirt, missing or too-old tool.
	ExitNotFound     = 4 // Unknown VM or base image.
	ExitConflict     = 5 // Already exists, already in progress, wrong state.
	ExitTimeout      = 6 // Guest did not boot, become reachable, or shut down in time.
	ExitCleanup      = 7 // Operation finished but host state was left behind.
)

// ExitError forces a specific exit code for an error whose type does not imply
// one.
type ExitError struct {
	Code int
	Err  error
	// Usage is the rendered usage listing of the command that raised this
	// error — its documented invocation and its own flags — printed with the
	// error so a rejected flag is answered by the flags the command does take.
	// It is empty for a usage error raised outside a subcommand's flag
	// parsing, which is answered by a pointer to `agent-vm --help` instead.
	Usage string
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// usageTextFor returns the usage listing an error carries, or "" when it
// carries none.
func usageTextFor(err error) string {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Usage
	}
	return ""
}

// exitf builds an ExitError with a formatted message.
func exitf(code int, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

// CleanupError is the one case where the primary operation succeeded but the
// host was left in a state that needs attention. It exits 7 and lists exactly
// what remains, because reporting success with host state left behind is
// forbidden (SECURITY.md).
type CleanupError struct {
	Operation string
	Cause     error
	Remaining []string
}

func (e *CleanupError) Error() string {
	msg := fmt.Sprintf("%s left host state behind: %v", e.Operation, e.Cause)
	for _, item := range e.Remaining {
		msg += "\n  still present: " + item
	}
	msg += "\n  Remove these by hand once you have looked at them."
	return msg
}

func (e *CleanupError) Unwrap() error { return e.Cause }

// exitCodeFor maps an error to its documented exit code. Classification lives
// here, in one place, so a new error type gets a code by declaring what kind of
// failure it is rather than by threading a number through every call site.
func exitCodeFor(err error) int {
	if err == nil {
		return ExitOK
	}

	// A CleanupError wraps the failure that started the cleanup, and that
	// cause's own code must not hide what matters more: host state was left
	// behind.
	var cleanup *CleanupError
	if errors.As(err, &cleanup) {
		return ExitCleanup
	}

	var explicit *ExitError
	if errors.As(err, &explicit) {
		return explicit.Code
	}

	var (
		validation *config.ValidationError
		notFound   *state.NotFoundError
		exists     *state.ExistsError
		busy       *state.BusyError
		schema     *state.SchemaError
		contained  *state.ContainmentError
		record     *state.RecordError
		missing    *hostexec.NotFoundError
		tooOld     *hostexec.VersionError
		timeout    *hostexec.TimeoutError
		waitedOut  *domain.TimeoutError
		bridge     *network.BridgeError
		netMode    *network.ModeError
	)
	switch {
	case errors.As(err, &validation), errors.As(err, &schema):
		return ExitUsage
	case errors.As(err, &missing), errors.As(err, &tooOld), errors.As(err, &bridge):
		return ExitHostNotReady
	case errors.As(err, &notFound):
		return ExitNotFound
	case errors.As(err, &exists), errors.As(err, &busy), errors.As(err, &netMode):
		return ExitConflict
	case errors.As(err, &timeout), errors.As(err, &waitedOut):
		return ExitTimeout
	case errors.As(err, &contained), errors.As(err, &record):
		// A path escaping the state directory, or a record naming paths that
		// are not its own, is a bug, corruption, or an attack, never a routine
		// failure; it gets the generic code and a loud message.
		return ExitFailure
	default:
		return ExitFailure
	}
}
