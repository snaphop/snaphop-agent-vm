package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/pihole"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// piholeRegistration is one requested DNS record. The password sits inside
// server and formats as a redacted marker. passwordFile is safe to print.
type piholeRegistration struct {
	server       pihole.Server
	name         string
	passwordFile string
}

// piholeInsecureOverride is "true" when the flag is set and empty otherwise.
// An empty override leaves a config-file or environment value in place; the
// flag can turn verification off and cannot turn it back on.
func piholeInsecureOverride(on bool) string {
	if !on {
		return ""
	}
	return "true"
}

// resolvePiHole turns create's Pi-hole flags into a registration, or nil when
// the operator did not ask for one. The password file is read here, before
// anything on the host changes. Other --pihole-* flags without --pihole are a
// usage error: a configured server is not registered on every create.
func resolvePiHole(vmName string, cfg *config.Config, enabled bool, urlFlag, passwordFlag, domainFlag string, insecureFlag bool) (*piholeRegistration, error) {
	if !enabled {
		if urlFlag != "" || passwordFlag != "" || domainFlag != "" || insecureFlag {
			return nil, exitf(ExitUsage,
				"--pihole-url, --pihole-password-file, --pihole-domain, and --pihole-insecure apply only with --pihole.\n"+
					"  Pass --pihole to register <name>.agent on that server.")
		}
		return nil, nil
	}
	if cfg.PiHole.URL == "" {
		return nil, &config.ValidationError{
			Field: "pi-hole url", Value: "",
			Err:    fmt.Errorf("is required with --pihole"),
			Remedy: "Pass --pihole-url, or set pihole.url in the config file or AGENT_VM_PIHOLE_URL.",
		}
	}
	if cfg.PiHole.PasswordFile == "" {
		return nil, &config.ValidationError{
			Field: "pi-hole password file", Value: "",
			Err:    fmt.Errorf("is required with --pihole"),
			Remedy: "Pass --pihole-password-file, or set pihole.password_file in the config file or AGENT_VM_PIHOLE_PASSWORD_FILE.",
		}
	}
	name, err := pihole.RecordName(vmName, cfg.PiHole.Domain)
	if err != nil {
		return nil, err
	}
	password, err := pihole.ReadPassword(cfg.PiHole.PasswordFile)
	if err != nil {
		return nil, err
	}
	return &piholeRegistration{
		server: pihole.Server{
			BaseURL:  cfg.PiHole.URL,
			Password: password,
			Insecure: cfg.PiHole.Insecure,
		},
		name:         name,
		passwordFile: cfg.PiHole.PasswordFile,
	}, nil
}

// registerPiHole publishes the guest's address after libvirt has reported it
// and before the guest update. A later update failure then leaves the name in
// place. The password is not written into vm.json.
func (a *App) registerPiHole(ctx context.Context, req createRequest, vm *state.VM, address string) error {
	reg := req.pihole
	if address == "" {
		return exitf(ExitTimeout,
			"%s did not report an address, so it was not registered on Pi-hole.\n"+
				"  The VM is still there: look at it with `agent-vm info %s`.", vm.Name, vm.Name)
	}
	ip, err := pihole.NormalizeAddress(address)
	if err != nil {
		return piholeKept(vm.Name, reg.name, err, pendingAfterPiHole(req))
	}
	a.out.Progress("Registering %s on Pi-hole as %s\n", vm.Name, reg.name)
	err = a.finishPiHole(reg.server.Ensure(ctx, reg.name, ip))
	if err != nil {
		return piholeKept(vm.Name, reg.name, err, pendingAfterPiHole(req))
	}
	vm.PiHole = &state.VMPiHole{
		URL:          reg.server.BaseURL,
		Name:         reg.name,
		Address:      ip,
		Insecure:     reg.server.Insecure,
		PasswordFile: reg.passwordFile,
	}
	if err := req.store.SaveVM(vm); err != nil {
		return fmt.Errorf("registered %s on Pi-hole as %s, but recording that failed: %w\n"+
			"  The VM is still there, and the name points at %s. Destroy will not remove the name until vm.json records it",
			vm.Name, reg.name, err, ip)
	}
	return nil
}

// pendingAfterPiHole names the create steps that have not run when Pi-hole
// registration fails. The update is one of them: registration runs first.
func pendingAfterPiHole(req createRequest) []string {
	var pending []string
	if req.update {
		pending = append(pending, "the guest update")
	}
	return append(pending, pendingAfterUpdate(req)...)
}

func piholeKept(name, fqdn string, err error, pending []string) error {
	if len(pending) == 0 {
		return fmt.Errorf("registering %s on Pi-hole as %s: %w\n  The VM %s is still there. Look at it with `agent-vm info %s`, or remove it with `agent-vm destroy %s`",
			name, fqdn, err, name, name, name)
	}
	return fmt.Errorf("registering %s on Pi-hole as %s: %w\n  The VM %s is still there. %s did not run. Look at it with `agent-vm info %s`, or remove it with `agent-vm destroy %s`",
		name, fqdn, err, name, englishList(pending), name, name)
}

// finishPiHole turns a logout failure after a successful DNS change into a
// warning. Failing the command would skip the vm.json record, and destroy
// would not know the name was registered. The session expires on its own.
func (a *App) finishPiHole(err error) error {
	var leak *pihole.SessionLeak
	if errors.As(err, &leak) {
		a.out.Warn("Warning: %s\n", leak.Error())
		return nil
	}
	return err
}

// piholeReachability is the sentence create prints under the new name. A NAT
// address is only reachable from this host. A bridged address is reachable by
// machines that use this Pi-hole.
func piholeReachability(vm *state.VM) string {
	if vm.Network.Mode == config.NetworkBridge {
		return fmt.Sprintf("Other machines that use this Pi-hole can open %s.", vm.PiHole.Name)
	}
	return fmt.Sprintf("%s points at a NAT address only this host can reach.", vm.PiHole.Name)
}

// removePiHole deletes the DNS name this VM's create registered. It runs
// before the domain is undefined, so a Pi-hole failure leaves a working VM
// whose record still names the entry. A name Pi-hole no longer has is success.
func (a *App) removePiHole(ctx context.Context, store *state.Store, vm *state.VM, urlOverride string) error {
	rec := vm.PiHole
	if rec == nil {
		return nil
	}
	server, path, err := a.piholeServer(rec, urlOverride)
	if err != nil {
		return err
	}
	if a.dryRun {
		a.out.Printf("# DELETE %s/api/config/dns/hosts/%s\n",
			strings.TrimRight(server.BaseURL, "/"), url.PathEscape(rec.Address+" "+rec.Name))
		a.out.Printf("# Pi-hole password file: %s\n", path)
		return nil
	}
	a.out.Progress("Removing %s from Pi-hole\n", rec.Name)
	if err := a.finishPiHole(server.Remove(ctx, rec.Name)); err != nil {
		return fmt.Errorf("removing %s from Pi-hole: %w\n"+
			"  The VM %s is still there. Fix the password file or the URL, then run destroy again.\n"+
			"  If the Pi-hole server is gone, remove the pihole object from %s and run destroy again",
			rec.Name, err, vm.Name, vm.Paths.Dir+"/"+state.VMRecordFile)
	}
	vm.PiHole = nil
	if err := store.SaveVM(vm); err != nil {
		return fmt.Errorf("removed %s from Pi-hole, but recording that failed: %w\n"+
			"  The VM is still there. Run destroy again; a name that is already gone is not an error", rec.Name, err)
	}
	return nil
}

// refreshPiHole points the recorded name at the address libvirt reports now.
// An address that is still unknown, or the same as the record, does not call
// Pi-hole, so start works without the password file until the address changes.
func (a *App) refreshPiHole(ctx context.Context, vm *state.VM, address string) error {
	rec := vm.PiHole
	if rec == nil || a.dryRun || strings.TrimSpace(address) == "" {
		return nil
	}
	next, err := pihole.NormalizeAddress(address)
	if err != nil {
		return nil
	}
	if current, err := pihole.NormalizeAddress(rec.Address); err == nil && current == next {
		return nil
	}
	server, _, err := a.piholeServer(rec, "")
	if err != nil {
		return piholeRefreshFailed(vm, rec, next, err)
	}
	a.out.Progress("Updating %s on Pi-hole to %s\n", rec.Name, next)
	if err := a.finishPiHole(server.Ensure(ctx, rec.Name, next)); err != nil {
		return piholeRefreshFailed(vm, rec, next, err)
	}
	rec.Address = next
	store, err := a.Store()
	if err != nil {
		return err
	}
	if err := store.SaveVM(vm); err != nil {
		return fmt.Errorf("updated %s on Pi-hole to %s, but recording that failed: %w\n"+
			"  %s is running, and the name points at %s", rec.Name, next, err, vm.Name, next)
	}
	return nil
}

func piholeRefreshFailed(vm *state.VM, rec *state.VMPiHole, next string, err error) error {
	return fmt.Errorf("updating %s on Pi-hole from %s to %s: %w\n"+
		"  %s is running, and %s still points at %s.\n"+
		"  Fix the Pi-hole password file, then `agent-vm stop %s` and `agent-vm start %s`, or remove the VM with `agent-vm destroy %s`",
		rec.Name, rec.Address, next, err, vm.Name, rec.Name, rec.Address, vm.Name, vm.Name, vm.Name)
}

// piholeServer is the Pi-hole a recorded VM is updated or removed on. The URL
// is the one stored in vm.json unless this invocation passed a URL, so a later
// config change cannot delete a name from a different server. The password
// file is the flag, environment, or config file when one of those set it, and
// the recorded path otherwise.
func (a *App) piholeServer(rec *state.VMPiHole, urlOverride string) (pihole.Server, string, error) {
	rawURL := rec.URL
	if urlOverride != "" {
		parsed, err := config.ParsePiHoleURL(urlOverride)
		if err != nil {
			return pihole.Server{}, "", err
		}
		rawURL = parsed
	}
	cfg, err := a.Config()
	if err != nil {
		return pihole.Server{}, "", err
	}
	path := cfg.PiHole.PasswordFile
	if path == "" {
		path = rec.PasswordFile
	}
	if path == "" {
		return pihole.Server{}, "", &config.ValidationError{
			Field: "pi-hole password file", Value: "",
			Err:    fmt.Errorf("is not recorded for %s and was not given", rec.Name),
			Remedy: "Pass --pihole-password-file, or set pihole.password_file in the config file.",
		}
	}
	password, err := pihole.ReadPassword(path)
	if err != nil {
		return pihole.Server{}, "", err
	}
	return pihole.Server{
		BaseURL:  rawURL,
		Password: password,
		Insecure: rec.Insecure || cfg.PiHole.Insecure,
	}, path, nil
}
