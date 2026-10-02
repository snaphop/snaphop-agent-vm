package cli

import (
	"context"

	"github.com/snaphop/snaphop-agent-vm/internal/notices"
)

func licensesCommand() *command {
	return &command{
		name:    "licenses",
		summary: "print the license and third-party notices",
		usage:   "agent-vm licenses",
		run:     runLicenses,
	}
}

// runLicenses prints the project's MIT license and the third-party notices.
// Referencing the embedded texts here is what keeps the linker from dropping
// them, so the strings are present in the binary and not only on stdout.
func runLicenses(_ context.Context, app *App, args []string) error {
	flags := newFlagSet("licenses", "agent-vm licenses", app.Stderr)
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return flags.usagef("unexpected argument %q", flags.Arg(0))
	}

	if app.out.format == OutputJSON {
		return app.out.JSON(struct {
			License string `json:"license"`
			Notice  string `json:"notice"`
		}{
			License: notices.License,
			Notice:  notices.Notice,
		})
	}

	// License already ends in a newline, so the extra one is a blank line
	// between the two documents.
	app.out.Printf("%s\n%s", notices.License, notices.Notice)
	return nil
}
