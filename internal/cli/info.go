package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/domain"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

func infoCommand() *command {
	return &command{
		name:    "info",
		summary: "print one VM's full record",
		usage:   "agent-vm info <name>",
		run:     runInfo,
	}
}

// vmDetail is one VM's stored record, its live state, and what its disk
// currently costs. The disk is read live because the number that matters —
// what the overlay actually occupies — changes as the guest writes.
type vmDetail struct {
	vmStatus
	Disk *diskDetail `json:"disk,omitempty"`
}

type diskDetail struct {
	VirtualSize config.Size `json:"virtualSize"`
	ActualSize  config.Size `json:"actualSize"`
	BackingFile string      `json:"backingFile,omitempty"`
}

func runInfo(ctx context.Context, app *App, args []string) error {
	flags := flag.NewFlagSet("info", flag.ContinueOnError)
	flags.SetOutput(app.Stderr)
	if err := flags.Parse(args); err != nil {
		return &ExitError{Code: ExitUsage, Err: err}
	}
	if flags.NArg() != 1 {
		return exitf(ExitUsage, "usage: agent-vm info <name>")
	}

	store, err := app.Store()
	if err != nil {
		return err
	}
	vm, err := store.LoadVM(flags.Arg(0))
	if err != nil {
		return err
	}

	statuses, err := app.statusesOf(ctx, []*state.VM{vm})
	if err != nil {
		return err
	}
	detail := vmDetail{vmStatus: statuses[0]}

	manager := domain.New(app.runner, app.uriFor(vm))
	if disk, err := manager.InspectDisk(ctx, vm.Paths.Overlay); err == nil {
		detail.Disk = &diskDetail{
			VirtualSize: disk.VirtualSize,
			ActualSize:  disk.ActualSize,
			BackingFile: disk.BackingFile,
		}
	} else {
		// A missing or unreadable overlay is worth knowing about but is not a
		// reason to refuse the rest of the record.
		app.logger.Debug("could not read the overlay", "vm", vm.Name, "error", err)
	}

	if app.out.format == OutputJSON {
		return app.out.JSON(detail)
	}

	rows := [][]string{
		{"name", vm.Name},
		{"state", detail.State},
	}
	if detail.Address != "" {
		rows = append(rows, []string{"address", detail.Address})
		rows = append(rows, []string{"ssh", fmt.Sprintf("agent-vm ssh %s", vm.Name)})
	}
	rows = append(rows,
		[]string{"created", vm.CreatedAt.Local().Format("2006-01-02 15:04:05")},
		[]string{"distro", vm.Distro},
		// The digest, not the tag, is what actually booted: a tag can be
		// rebuilt, a digest cannot.
		[]string{"base image", vm.BaseImage.SourceRef},
		[]string{"base digest", vm.BaseImage.SourceDigest},
		[]string{"base disk", vm.BaseImage.Path},
		[]string{"vcpus", fmt.Sprint(vm.Resources.VCPUs)},
		[]string{"memory", vm.Resources.Memory.Human()},
		[]string{"network", networkSummary(vm)},
		[]string{"mac", vm.Network.MAC},
		[]string{"guest user", vm.Guest.User},
		[]string{"ssh keys", strings.Join(vm.Guest.SSHKeyPaths, ", ")},
		[]string{"libvirt", vm.LibvirtURI},
	)
	if key := vm.Guest.GitHubKey; key != nil {
		rows = append(rows, []string{"github key", fmt.Sprintf("%s (id %d)", key.Title, key.ID)})
	}
	if detail.Disk != nil {
		rows = append(rows,
			[]string{"disk (virtual)", detail.Disk.VirtualSize.Human()},
			// The overlay's real cost, which is the number that fills a disk.
			[]string{"disk (on host)", detail.Disk.ActualSize.Human()},
		)
	}
	rows = append(rows,
		[]string{"overlay", vm.Paths.Overlay},
		[]string{"user-data", vm.Paths.UserData},
		[]string{"domain xml", vm.Paths.DomainXML},
		[]string{"console log", vm.Paths.ConsoleLog},
		[]string{"created by", "agent-vm " + vm.CreatedBy.AgentVMVersion},
		[]string{"virt-install", vm.CreatedBy.VirtInstallVersion},
	)
	app.out.Table(rows)

	// The argv is printed on its own line rather than squeezed into the table:
	// it is long, and its whole value is that it can be copied and rerun.
	if len(vm.CreatedBy.VirtInstallArgv) > 0 {
		app.out.Printf("\ncreated with:\n  %s\n",
			(hostexec.Command{
				Name: vm.CreatedBy.VirtInstallArgv[0],
				Args: vm.CreatedBy.VirtInstallArgv[1:],
			}).String())
	}
	return nil
}
