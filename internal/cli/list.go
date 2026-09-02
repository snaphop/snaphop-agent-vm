package cli

import (
	"context"
	"fmt"
	"sort"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/config"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/domain"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/state"
)

func listCommand() *command {
	return &command{
		name:    "list",
		summary: "list the VMs in this state directory",
		usage:   "agent-vm list",
		run:     runList,
	}
}

// vmStatus is a stored VM record plus what only libvirt can say: whether it is
// running and what address it has. The record is embedded rather than copied so
// that `list --output json` and `info --output json` carry the same fields as
// vm.json itself.
type vmStatus struct {
	*state.VM
	State string `json:"state"`
	// Address is the guest's current IPv4 address, absent when it has none —
	// the VM is stopped, or still booting.
	Address string `json:"address,omitempty"`
}

func runList(ctx context.Context, app *App, args []string) error {
	flags := newFlagSet("list", "agent-vm list", app.Stderr)
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return flags.usagef("unexpected argument %q", flags.Arg(0))
	}

	store, err := app.Store()
	if err != nil {
		return err
	}
	// Only VMs this state directory recorded are listed. A domain that exists
	// in libvirt but not here belongs to someone else, and showing it would
	// invite acting on it (SECURITY.md).
	vms, err := store.ListVMs()
	if err != nil {
		return err
	}

	statuses, err := app.statusesOf(ctx, vms)
	if err != nil {
		return err
	}

	if app.out.format == OutputJSON {
		return app.out.JSON(statuses)
	}
	if len(statuses) == 0 {
		app.out.Printf("No VMs. Create one with `agent-vm create <name>`.\n")
		return nil
	}

	rows := [][]string{{"NAME", "STATE", "DISTRO", "RESOURCES", "NETWORK", "ADDRESS", "CREATED"}}
	for _, status := range statuses {
		rows = append(rows, []string{
			status.Name,
			status.State,
			status.Distro,
			resourceSummary(status.VM),
			networkSummary(status.VM),
			addressOrDash(status.Address),
			status.CreatedAt.Local().Format("2006-01-02 15:04"),
		})
	}
	app.out.Table(rows)
	return nil
}

// statusesOf pairs each stored record with its live state, asking libvirt once
// per connection rather than once per VM.
func (a *App) statusesOf(ctx context.Context, vms []*state.VM) ([]vmStatus, error) {
	byURI := map[string][]*state.VM{}
	for _, vm := range vms {
		byURI[a.uriFor(vm)] = append(byURI[a.uriFor(vm)], vm)
	}

	statuses := make([]vmStatus, 0, len(vms))
	for uri, group := range byURI {
		manager := domain.New(a.runner, uri)
		names := make([]string, 0, len(group))
		for _, vm := range group {
			names = append(names, vm.Name)
		}

		states, err := manager.StatesOf(ctx, names)
		if err != nil {
			return nil, err
		}
		for _, vm := range group {
			status := vmStatus{VM: vm, State: string(states[vm.Name])}
			if states[vm.Name].IsRunning() {
				// A running guest may still have no address: it is booting, or
				// its network never came up. That is reported as no address,
				// not as a failure to list.
				address, err := manager.IPv4Address(ctx, vm.Name)
				if err != nil {
					a.logger.Debug("could not read guest address", "vm", vm.Name, "error", err)
				}
				status.Address = address
			}
			statuses = append(statuses, status)
		}
	}

	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses, nil
}

// uriFor returns the connection a VM was created on, spelled as virsh sees it.
// A VM is queried where it was defined, not where this invocation happens to
// point, so a record written against qemu:///session is not reported as missing
// from qemu:///system.
//
// The record holds the URI the operator gave, which for a remote hypervisor
// names the transport as well. virsh runs on the hypervisor, so the transport
// is dropped here (ADR-0010); keeping it would send virsh back over ssh to the
// machine it is already on.
func (a *App) uriFor(vm *state.VM) string {
	if vm.LibvirtURI != "" {
		conn, err := config.ParseConnection(vm.LibvirtURI)
		if err != nil {
			// The record was written by another version, or edited by hand.
			// It is passed through unchanged so virsh, not this function, is
			// the one to reject it.
			return vm.LibvirtURI
		}
		return conn.HypervisorURI()
	}
	uri, err := a.hypervisorURI()
	if err != nil {
		return ""
	}
	return uri
}

func resourceSummary(vm *state.VM) string {
	memory := vm.Resources.Memory.Human()
	if max := vm.Resources.MaxMemory; max > vm.Resources.Memory {
		// A range, because that is what the guest's RAM is once it has a
		// virtio-mem device: somewhere between the two, depending on how much
		// has been plugged in.
		memory += "-" + max.Human()
	}
	return fmt.Sprintf("%dv/%s/%s", vm.Resources.VCPUs, memory, vm.Resources.Disk.Human())
}

// memoryDetail names the ceiling a virtio-mem device can grow the guest to,
// so that `info` explains a guest whose RAM does not match what it was created
// with rather than leaving it looking wrong.
func memoryDetail(r state.VMResources) string {
	if r.MaxMemory > r.Memory {
		return fmt.Sprintf("%s, growable to %s", r.Memory.Human(), r.MaxMemory.Human())
	}
	return r.Memory.Human()
}

// describeResources is the long form, for a report with room to spell it out.
func describeResources(r state.VMResources) string {
	memory := r.Memory.Human() + " RAM"
	if r.MaxMemory > r.Memory {
		memory += fmt.Sprintf(" (growable to %s)", r.MaxMemory.Human())
	}
	return fmt.Sprintf("%d vCPU, %s, %s disk", r.VCPUs, memory, r.Disk.Human())
}

// networkSummary names the mode and, in bridged mode, the bridge — because
// "which LAN is this guest on?" is the question that matters there.
func networkSummary(vm *state.VM) string {
	if vm.Network.Bridge != "" {
		return string(vm.Network.Mode) + ":" + vm.Network.Bridge
	}
	return string(vm.Network.Mode)
}

func addressOrDash(address string) string {
	if address == "" {
		return "-"
	}
	return address
}
