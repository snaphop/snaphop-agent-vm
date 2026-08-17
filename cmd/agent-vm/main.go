// Command agent-vm creates and manages short-lived QEMU/KVM virtual machines
// for AI coding agents.
//
// This file is deliberately thin: it wires up signal handling and turns the
// CLI's result into an exit status. All behavior lives in internal/cli.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/cli"
)

func main() {
	// A cancelled context propagates to every running host tool, so Ctrl-C
	// stops a long virt-install or podman build instead of orphaning it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
