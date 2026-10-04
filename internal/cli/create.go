package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/domain"
	"github.com/snaphop/snaphop-agent-vm/internal/github"
	"github.com/snaphop/snaphop-agent-vm/internal/guestinit"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
	"github.com/snaphop/snaphop-agent-vm/internal/network"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// defaultWaitForSSH is how long create waits for a guest to accept SSH. It is
// the one wait an operator routinely cares about: it is the difference between
// "the VM is defined" and "the VM is usable".
const defaultWaitForSSH = 90 * time.Second

// userDataPerm is stricter than the rest of the state directory. The generated
// user-data holds no secrets, but the operator may have merged their own file
// into it and this tool does not get to decide that theirs holds none.
const userDataPerm = 0o600

func createCommand() *command {
	return &command{
		name:    "create",
		summary: "create and start a VM",
		usage:   "agent-vm create <name> [flags]",
		run:     runCreate,
	}
}

// repeatedFlag collects a flag given more than once, in the order given.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ", ") }

func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func runCreate(ctx context.Context, app *App, args []string) (err error) {
	flags := newFlagSet("create", "agent-vm create <name> [flags]", app.Stderr)
	distroRef := flags.String("distro", "", "base image to use, <distro>[:<tag>] (ubuntu defaults to 26.04, fedora to 44, arch to base-20260927.0.600689; ubuntu:24.04 and fedora:43 still select those releases, and arch:base still selects the rolling tag); append -slim, -nix, or -runner for that variant; built automatically if not cached")
	vcpus := flags.String("vcpus", "", "virtual CPUs")
	memory := flags.String("memory", "", "guest RAM, for example 512M or 4G")
	maxMemory := flags.String("max-memory", "", "ceiling the guest's RAM can be grown to at runtime, via virtio-mem; unset means a fixed-size guest")
	disk := flags.String("disk", "", "virtual root disk size (a thin overlay)")
	networkMode := flags.String("network", "", "network mode: nat or bridge")
	bridge := flags.String("bridge", "", "host bridge to attach to; required with --network bridge")
	cloudInit := flags.String("cloud-init", "", "extra cloud-init user-data merged into the generated user-data")
	opencodeConfig := flags.String("opencode-config", "", "opencode.json to install in the guest, replacing the one the base image ships")
	noStart := flags.Bool("no-start", false, "define the domain without starting it")
	waitForSSH := flags.Duration("wait-for-ssh", defaultWaitForSSH, "how long to wait for the guest to accept SSH; 0 disables waiting")
	githubSSHKey := flags.Bool("github-ssh-key", false, "add the SSH public key the guest generates for itself to your GitHub account, using gh")
	tailscaleAuthKeyFile := flags.String("tailscale-auth-key-file", "", "file containing a Tailscale auth key; the guest joins that network after it boots")
	tailscaleHostname := flags.String("tailscale-hostname", "", "hostname on the tailnet (default: the VM name)")
	tailscaleLoginServer := flags.String("tailscale-login-server", "", "https URL of a coordination server other than Tailscale's")
	tailscaleEphemeral := flags.Bool("tailscale-ephemeral", false, "remove the tailnet node when the VM stops")
	tailscaleSSH := flags.Bool("tailscale-ssh", false, "enable Tailscale SSH on the guest")
	var tailscaleTags repeatedFlag
	flags.Var(&tailscaleTags, "tailscale-advertise-tag", "tailnet `tag` to advertise, such as tag:ci; repeatable")
	hostAuthorizedKeys := flags.Bool("host-authorized-keys", false, "also authorize the keys in this host account's ~/.ssh/authorized_keys")
	var sshKeys repeatedFlag
	flags.Var(&sshKeys, "ssh-key", "SSH public `key` to authorize; repeatable (default: this account's ~/.ssh identities)")
	var virtInstallArgs repeatedFlag
	flags.Var(&virtInstallArgs, "virt-install-arg", "extra `argument` passed through to virt-install; repeatable")

	name, err := flags.parseNamed(args)
	if err != nil {
		return err
	}
	if err := config.ValidateVMName(name); err != nil {
		return err
	}
	if *noStart {
		return errNoStart(name)
	}
	// The key only exists inside the guest, so it can only be read once the
	// guest is up. Asking for both would be asking for a key that cannot be
	// fetched.
	if *githubSSHKey && *waitForSSH <= 0 {
		return exitf(ExitUsage,
			"--github-ssh-key needs --wait-for-ssh: the key is generated inside the guest on first boot\n"+
				"  and is read over SSH once it is reachable.")
	}

	cfg, err := app.ConfigWith(config.Overrides{
		Distro:    *distroRef,
		VCPUs:     *vcpus,
		Memory:    *memory,
		MaxMemory: *maxMemory,
		Disk:      *disk,
		Network:   *networkMode,
		Bridge:    *bridge,
		SSHKeys:   sshKeys,
	})
	if err != nil {
		return err
	}

	// Key material and operator-supplied files are read and validated before
	// anything on the host changes, so a typo in a path or a file cloud-init or
	// opencode would refuse is a usage error rather than a half-created VM or a
	// base image built for nothing.
	keyPaths := cfg.SSHKeys
	keys, err := guestinit.LoadPublicKeys(keyPaths)
	if err != nil {
		return err
	}
	if len(keyPaths) == 0 {
		// Nothing named a key, so fall back to the operator's own OpenSSH
		// identities, the same way `ssh` offers them without -i.
		keys, keyPaths = defaultSSHKeys()
	}
	if *hostAuthorizedKeys {
		paths, err := hostAuthorizedKeysPaths()
		if err != nil {
			return err
		}
		hostKeys, sources, err := guestinit.LoadAuthorizedKeys(paths)
		if err != nil {
			return err
		}
		// --ssh-key comes first: the keys the operator named for this VM are
		// the ones they will look for at the top of the guest's
		// authorized_keys. Duplicates between the two sources appear once.
		keys = mergeKeys(keys, hostKeys)
		keyPaths = append(append([]string{}, keyPaths...), sources...)
	}
	if len(keys) == 0 {
		return errNoSSHKeys()
	}
	extraUserData, err := readExtraUserData(*cloudInit)
	if err != nil {
		return err
	}
	opencodeJSON, err := readOpencodeConfig(*opencodeConfig)
	if err != nil {
		return err
	}
	userData := guestinit.Options{
		Hostname:          name,
		User:              cfg.GuestUser,
		SSHAuthorizedKeys: keys,
		ExtraUserData:     extraUserData,
		ExtraSource:       *cloudInit,
		OpencodeConfig:    opencodeJSON,
		OpencodeSource:    *opencodeConfig,
		AgentVMVersion:    Version,
	}
	// The same checks Generate makes later, run here so they precede the
	// tool checks, the lock, and any base image build — and so a dry run
	// reports a file the real run would refuse.
	if err := userData.Validate(); err != nil {
		return err
	}
	join, err := resolveTailscale(name, cfg.GuestUser, *tailscaleAuthKeyFile, *tailscaleHostname, *tailscaleLoginServer, *tailscaleEphemeral, *tailscaleSSH, tailscaleTags)
	if err != nil {
		return err
	}
	// The guest is joined over SSH, so there has to be a boot wait to join
	// through. Asking for both would create a VM and then have no way to log
	// it in.
	if join != nil && *waitForSSH <= 0 {
		return exitf(ExitUsage,
			"--tailscale-auth-key-file needs --wait-for-ssh: the guest is joined over SSH after it boots.")
	}

	if app.dryRun {
		return app.printCreatePlan(cfg, name, virtInstallArgs, *githubSSHKey, join)
	}

	// virt-make-fs writes the cloud-init seed, so its floor is checked here with
	// the others rather than discovered once the VM directory exists.
	tools := []hostexec.Tool{hostexec.VirtInstall, hostexec.Virsh, hostexec.QemuImg, hostexec.VirtMakeFS}
	if *githubSSHKey {
		tools = append(tools, hostexec.GH)
	}
	if err := app.requireTools(ctx, tools...); err != nil {
		return err
	}
	if *githubSSHKey {
		// Checked before anything is created, so an expired login costs
		// nothing more than the message.
		if err := github.New(app.runner).CheckAuth(ctx); err != nil {
			return err
		}
	}

	store, err := app.Store()
	if err != nil {
		return err
	}
	// The lock is taken before the existence check, so two concurrent creates
	// of the same name cannot both find it free.
	lock, err := store.LockVM(ctx, name, "create")
	if err != nil {
		return err
	}
	// A lock we cannot release is host state the operator needs to know about,
	// so it is reported rather than dropped (AGENTS.md §6).
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil && err == nil {
			err = releaseErr
		}
	}()

	exists, err := store.HasVM(name)
	if err != nil {
		return err
	}
	if exists {
		return &state.ExistsError{Kind: "VM", Name: name}
	}

	hypervisorURI, err := app.hypervisorURI()
	if err != nil {
		return err
	}
	manager := domain.New(app.runner, hypervisorURI)
	defined, err := manager.Exists(ctx, name)
	if err != nil {
		return err
	}
	if defined {
		// A libvirt domain with this name that this tool has no record of is a
		// conflict, not something to adopt or overwrite: it is not ours.
		return exitf(ExitConflict,
			"libvirt already has a domain named %q, but this state directory has no record of it.\n"+
				"  Choose another name, or remove that domain yourself if it is not in use.", name)
	}

	builder, err := app.builder()
	if err != nil {
		return err
	}
	// The VM directory exists before the base image is looked up, so that
	// `image rm` and `image build --force` see this create in progress and
	// refuse, rather than removing the image its overlay is about to use.
	vmDir := store.VMDir(name)
	if err := store.MkdirAll(vmDir); err != nil {
		return err
	}
	app.out.Progress("Using base image %s\n", cfg.Distro)
	manifest, err := builder.EnsureImage(ctx, cfg.Distro)
	if err != nil {
		if rmErr := store.Remove(vmDir); rmErr != nil {
			return &CleanupError{
				Operation: fmt.Sprintf("creating VM %s failed and cleaning up after it", name),
				Cause:     err,
				Remaining: []string{fmt.Sprintf("state directory %s: %v", vmDir, rmErr)},
			}
		}
		return err
	}

	return app.createVM(ctx, createRequest{
		cfg:             cfg,
		name:            name,
		store:           store,
		manager:         manager,
		manifest:        manifest,
		userData:        userData,
		keyPaths:        keyPaths,
		virtInstallArgs: virtInstallArgs,
		waitForSSH:      *waitForSSH,
		githubSSHKey:    *githubSSHKey,
		tailscale:       join,
	})
}

// jumpArgs renders the -J a guest connection carries when the hypervisor is
// another machine, for the plan lines that are written out rather than built
// as commands.
func jumpArgs(jump string) string {
	if jump == "" {
		return ""
	}
	return " -J " + jump
}

// errNoStart explains why --no-start cannot be honored. It is a usage error
// rather than a silently ignored flag: an operator who asked for a stopped VM
// must not be handed a running one.
func errNoStart(name string) error {
	return exitf(ExitUsage,
		"--no-start cannot be honored: virt-install always boots the guest it defines,\n"+
			"  and a VM stopped before cloud-init finished would never receive its SSH key,\n"+
			"  so it could not be reached afterwards.\n"+
			"  Create the VM and stop it: agent-vm create %s && agent-vm stop %s", name, name)
}

// readExtraUserData reads an operator-supplied cloud-init file. Its contents
// are never logged (SECURITY.md); only its path appears in messages.
func readExtraUserData(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, &config.ValidationError{Field: "cloud-init user-data", Value: path, Err: err}
	}
	if len(contents) == 0 {
		return nil, &config.ValidationError{
			Field: "cloud-init user-data", Value: path,
			Err: fmt.Errorf("the file is empty"),
		}
	}
	return contents, nil
}

// readOpencodeConfig reads an operator-supplied opencode.json. Like
// --cloud-init data it may hold credentials, so its contents are never logged
// (SECURITY.md); only its path appears in messages. Whether it is valid JSON is
// guestinit's check, so every caller building user-data gets it.
func readOpencodeConfig(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, &config.ValidationError{Field: "opencode config", Value: path, Err: err}
	}
	if len(contents) == 0 {
		return nil, &config.ValidationError{
			Field: "opencode config", Value: path,
			Err:    fmt.Errorf("the file is empty"),
			Remedy: "Drop --opencode-config to keep the copy the base image ships.",
		}
	}
	return contents, nil
}

// defaultSSHKeyFiles are the OpenSSH identities `create` authorizes when no key
// was named by --ssh-key, by the config file, or by AGENT_VM_SSH_KEY: the
// public halves of the private keys `ssh` itself would offer, in the same
// order. Every one that exists is authorized rather than only the first,
// because the operator may reach the guest from any host holding any of them.
//
// Names only — the public key sits beside the private one with a .pub suffix.
var defaultSSHKeyFiles = []string{
	"id_ed25519",
	"id_ed25519_sk",
	"id_ecdsa",
	"id_ecdsa_sk",
	"id_dsa",
	"id_rsa",
	"id_xmss",
}

// defaultSSHKeys returns the key lines of the default identity files that
// exist on this host, and the files they came from. A file that is missing or
// that does not hold a public key is skipped rather than reported: these paths
// were guessed, not asked for, so one stray file in ~/.ssh must not block a
// create. An operator with no usable identity at all gets errNoSSHKeys.
func defaultSSHKeys() (keys, paths []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil
	}
	keys, paths = []string{}, []string{}
	for _, name := range defaultSSHKeyFiles {
		path := filepath.Join(home, ".ssh", name+".pub")
		found, err := guestinit.LoadPublicKeys([]string{path})
		if err != nil {
			continue
		}
		keys = mergeKeys(keys, found)
		paths = append(paths, path)
	}
	return keys, paths
}

// errNoSSHKeys reports that no key could be found at all. A VM with no
// authorized key accepts no logins, so this is refused before anything on the
// host changes rather than surfacing as an unreachable guest minutes later.
func errNoSSHKeys() error {
	return &config.ValidationError{
		Field: "ssh keys", Value: "",
		Err: fmt.Errorf("no SSH public key was given and this account has none of the default identities"),
		Remedy: "Pass --ssh-key <path to a .pub file>, set [guest] ssh_keys in the config file, or create a key with `ssh-keygen -t ed25519`.\n" +
			"  The defaults looked for are ~/.ssh/" + strings.Join(defaultSSHKeyFiles, ".pub, ~/.ssh/") + ".pub.",
	}
}

// hostAuthorizedKeysFiles are the files --host-authorized-keys reads, relative
// to the host account's home directory: the keys that already log in to this
// host. Both are read because a host may keep its keys in either — sshd is
// routinely configured with an AuthorizedKeysFile pointing at the second — and
// a file that is not there is skipped. They are fixed locations rather than
// flag values, because a key file somewhere else is what --ssh-key is for.
var hostAuthorizedKeysFiles = [][]string{
	{".ssh", "authorized_keys"},
	{".ssh", "authorized-keys", "authorized_keys"},
}

func hostAuthorizedKeysPaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, &config.ValidationError{
			Field: "host authorized_keys", Value: "~/.ssh/authorized_keys",
			Err:    fmt.Errorf("cannot resolve the home directory: %w", err),
			Remedy: "Pass the keys with --ssh-key instead.",
		}
	}
	paths := make([]string, 0, len(hostAuthorizedKeysFiles))
	for _, file := range hostAuthorizedKeysFiles {
		paths = append(paths, filepath.Join(append([]string{home}, file...)...))
	}
	return paths, nil
}

// mergeKeys concatenates key sets, keeping the first occurrence of each key.
// The same key reaching a guest twice would be two identical lines in its
// authorized_keys, which is confusing rather than harmful.
func mergeKeys(sets ...[]string) []string {
	merged := []string{}
	seen := map[string]bool{}
	for _, set := range sets {
		for _, key := range set {
			if !seen[key] {
				seen[key] = true
				merged = append(merged, key)
			}
		}
	}
	return merged
}

// requireTools checks the tools a command depends on, and their minimum
// versions, before it changes anything. Failing here yields exit 3 with the
// tool named, rather than a confusing failure halfway through.
func (a *App) requireTools(ctx context.Context, tools ...hostexec.Tool) error {
	for _, tool := range tools {
		if _, err := a.versions.Require(ctx, tool); err != nil {
			return err
		}
	}
	return nil
}

// createRequest is one validated create, ready to run.
type createRequest struct {
	cfg      *config.Config
	name     string
	store    *state.Store
	manager  *domain.Manager
	manifest *state.Manifest
	// userData is what the guest's cloud-init user-data is generated from,
	// already validated.
	userData guestinit.Options
	// keyPaths are the files the authorized keys came from, recorded in
	// vm.json so a VM's authorized keys can be traced back to their source.
	keyPaths        []string
	virtInstallArgs []string
	waitForSSH      time.Duration
	githubSSHKey    bool
	tailscale       *tailscaleJoin
}

// createVM performs the steps that change host state. Every one of them is
// registered with the rollback before the next begins, so a failure anywhere
// through "define and start" leaves no domain, no overlay, and no state
// directory behind (docs/cli.md).
func (a *App) createVM(ctx context.Context, req createRequest) error {
	rollback := &createRollback{store: req.store, manager: req.manager, name: req.name}

	vm, err := a.buildVM(ctx, req, rollback)
	if err != nil {
		return rollback.undo(ctx, err)
	}

	// From here the VM exists and is recorded. A wait that runs out is reported
	// as a timeout and the VM is deliberately left in place, because "it booted
	// slowly" and "it failed to boot" need the same evidence: its console log.
	address, err := a.waitForGuest(ctx, req, vm)
	if err != nil {
		return err
	}

	// Tailscale and the GitHub key are added after the VM is usable and
	// recorded. A failure here is reported without undoing the VM: the VM is
	// not the thing that failed. The join goes first so a later GitHub
	// failure does not leave a guest that was asked to join a tailnet off it.
	if req.tailscale != nil {
		if err := a.joinTailscale(ctx, req, vm, address); err != nil {
			return err
		}
	}
	if req.githubSSHKey {
		if err := a.addGitHubKey(ctx, req, vm, address); err != nil {
			return err
		}
	}

	return a.reportCreated(req, vm, address)
}

// buildVM runs the steps that must be undone on failure.
func (a *App) buildVM(ctx context.Context, req createRequest, rollback *createRollback) (*state.VM, error) {
	cfg, store, name := req.cfg, req.store, req.name

	vm := store.NewVM(name)
	vm.LibvirtURI = cfg.LibvirtURI
	vm.Distro = req.manifest.Ref()
	vm.BaseImage = state.BaseImageRef{
		Distro:       req.manifest.Distro,
		Tag:          req.manifest.Tag,
		SourceRef:    req.manifest.SourceRef,
		SourceDigest: req.manifest.SourceDigest,
		Path:         store.BaseDiskPath(req.manifest.Distro, req.manifest.Tag),
	}
	vm.Resources = state.VMResources{VCPUs: cfg.VCPUs, Memory: cfg.Memory, MaxMemory: cfg.MaxMemory, Disk: cfg.Disk}
	// Only the attachment the mode actually uses is recorded. A bridged VM is
	// never on the NAT network and a NAT VM is never on the configured bridge,
	// and recording the unused one — both are configured whether or not they
	// apply — would make the record claim an attachment the guest does not
	// have, which is the opposite of the auditability the field exists for.
	vm.Network = state.VMNetwork{Mode: cfg.Network}
	if cfg.BridgeMode() {
		vm.Network.Bridge = cfg.Bridge
	} else {
		vm.Network.Name = cfg.NATNetwork
	}
	vm.Guest = state.VMGuest{User: cfg.GuestUser, SSHKeyPaths: req.keyPaths}
	vm.CreatedBy.AgentVMVersion = Version

	if err := store.MkdirAll(vm.Paths.Dir); err != nil {
		return nil, err
	}
	rollback.directory = vm.Paths.Dir

	userData, err := guestinit.Generate(req.userData)
	if err != nil {
		return nil, err
	}
	metaData, err := guestinit.GenerateMetaData(guestinit.Options{
		Hostname:       name,
		User:           cfg.GuestUser,
		AgentVMVersion: Version,
	})
	if err != nil {
		return nil, err
	}

	// The seed directory holds exactly what cloud-init reads, because
	// virt-make-fs copies all of it onto the disk the guest mounts.
	if err := store.MkdirAll(vm.Paths.SeedDir); err != nil {
		return nil, err
	}
	if err := store.WriteFile(vm.Paths.UserData, userData, userDataPerm); err != nil {
		return nil, err
	}
	if err := store.WriteFile(vm.Paths.MetaData, metaData, userDataPerm); err != nil {
		return nil, err
	}
	// The seed image is created empty and with user-data's mode before
	// virt-make-fs fills it in: it ends up holding everything user-data holds,
	// including anything the operator merged in with --cloud-init, and a disk
	// image written at the ambient umask would be world-readable. virt-make-fs
	// writes into the existing file and leaves its mode alone.
	if err := store.WriteFile(vm.Paths.SeedImage, nil, userDataPerm); err != nil {
		return nil, err
	}
	if err := req.manager.CreateSeed(ctx, vm.Paths.SeedDir, vm.Paths.SeedImage); err != nil {
		return nil, err
	}

	a.out.Progress("Creating root disk overlay (%s)\n", cfg.Disk.Human())
	if err := req.manager.CreateOverlay(ctx, vm.BaseImage.Path, vm.Paths.Overlay, cfg.Disk); err != nil {
		return nil, err
	}

	if err := a.ensureNetwork(ctx, cfg); err != nil {
		return nil, err
	}

	// The hypervisor's architecture decides a couple of the virt-install
	// arguments, and it is not necessarily this machine's: a qemu+ssh:// URI
	// defines the domain on another host (ADR-0010).
	arch, err := req.manager.HostArch(ctx)
	if err != nil {
		return nil, err
	}

	a.out.Progress("Defining and starting domain %s\n", name)
	rollback.domainAttempted = true
	argv, err := req.manager.Create(ctx, domain.CreateOptions{
		Name:           name,
		LibvirtURI:     req.manager.LibvirtURI(),
		Arch:           arch,
		VCPUs:          cfg.VCPUs,
		Memory:         cfg.Memory,
		MaxMemory:      cfg.MaxMemory,
		OverlayPath:    vm.Paths.Overlay,
		KernelPath:     store.KernelPath(req.manifest.Distro, req.manifest.Tag),
		InitrdPath:     store.InitrdPath(req.manifest.Distro, req.manifest.Tag),
		KernelCmdline:  req.manifest.KernelCmdline,
		SeedImagePath:  vm.Paths.SeedImage,
		Network:        cfg.Network,
		NATNetwork:     cfg.NATNetwork,
		Bridge:         cfg.Bridge,
		ConsoleLogPath: vm.Paths.ConsoleLog,
		ExtraArgs:      req.virtInstallArgs,
	})
	if err != nil {
		// The argv is returned even on failure, and is worth keeping in the
		// rollback's report: it is the command the operator can rerun by hand.
		return nil, err
	}
	rollback.domainDefined = true

	vm.CreatedBy.VirtInstallArgv = argv
	if version, err := a.versions.Get(ctx, hostexec.VirtInstall); err == nil {
		vm.CreatedBy.VirtInstallVersion = version.String()
	}

	mac, err := req.manager.MAC(ctx, name)
	if err != nil {
		return nil, err
	}
	vm.Network.MAC = mac

	// domain.xml is a record of what libvirt actually defined, captured once
	// here. It is never read back in.
	xml, err := req.manager.DumpXML(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := store.WriteFile(vm.Paths.DomainXML, xml, 0o644); err != nil {
		return nil, err
	}

	// vm.json is written before the boot wait, so that a VM left in place by a
	// timeout is still a VM `agent-vm destroy` knows how to remove.
	if err := store.SaveVM(vm); err != nil {
		return nil, err
	}
	return vm, nil
}

// ensureNetwork makes the guest's network usable, or refuses. In bridged mode
// the bridge is validated and never created: host networking belongs to the
// operator (SECURITY.md).
func (a *App) ensureNetwork(ctx context.Context, cfg *config.Config) error {
	if cfg.BridgeMode() {
		a.out.Progress("Checking host bridge %s\n", cfg.Bridge)
		warning, err := network.ValidateBridge(ctx, a.runner, cfg.Bridge)
		if err != nil {
			return err
		}
		// The VM is still created: a bridge that runs the spanning tree
		// protocol works, it just costs this boot half a minute. Saying so
		// here is what keeps the operator from reading that wait as a slow
		// image or a broken guest.
		if warning != nil {
			a.out.Warn("Warning: %s\n  %s\n", warning.Detail(), warning.Remedy())
		}
		return nil
	}

	store, err := a.Store()
	if err != nil {
		return err
	}
	a.out.Progress("Checking NAT network %s\n", cfg.NATNetwork)
	uri, err := a.hypervisorURI()
	if err != nil {
		return err
	}
	return network.EnsureNAT(ctx, a.runner, uri, cfg.NATNetwork, store)
}

// waitForGuest waits for the guest to get an address and accept SSH. Both waits
// share the --wait-for-ssh budget, because from the operator's side they are one
// question: is this VM usable yet?
func (a *App) waitForGuest(ctx context.Context, req createRequest, vm *state.VM) (string, error) {
	if req.waitForSSH <= 0 {
		return "", nil
	}

	deadline := time.Now().Add(req.waitForSSH)
	a.out.Progress("Waiting for the guest to boot (up to %s)\n", req.waitForSSH)

	address, err := req.manager.WaitForAddress(ctx, req.name, guestNIC(vm), req.waitForSSH)
	if err != nil {
		return "", err
	}

	remaining := time.Until(deadline)
	if remaining <= 0 {
		// The address arrived exactly as the budget ran out. One probe is still
		// worth trying rather than reporting a timeout that was never tested.
		remaining = time.Second
	}
	a.out.Progress("Waiting for SSH on %s\n", address)
	err = req.manager.WaitForSSH(ctx, req.name, domain.SSHOptions{
		User:    vm.Guest.User,
		Address: address,
		// The probe offers the same key `agent-vm ssh` will, and reaches the
		// guest the same way, so that "create says it is ready" and "ssh
		// works" cannot disagree.
		IdentityFile: privateKeyFor(vm),
		Jump:         a.sshJump(),
	}, remaining)
	if err != nil {
		return address, err
	}
	return address, nil
}

func (a *App) reportCreated(req createRequest, vm *state.VM, address string) error {
	if a.out.format == OutputJSON {
		return a.out.JSON(vm)
	}

	a.out.Printf("Created %s\n", vm.Name)
	rows := [][]string{
		{"  distro", vm.Distro},
		{"  resources", describeResources(vm.Resources)},
		{"  network", string(vm.Network.Mode)},
	}
	if address != "" {
		rows = append(rows, []string{"  address", address})
	}
	if key := vm.Guest.GitHubKey; key != nil {
		rows = append(rows, []string{"  github key", fmt.Sprintf("%s (id %d)", key.Title, key.ID)})
	}
	if detail := tailscaleDetail(vm.Tailscale); detail != "" {
		rows = append(rows, []string{"  tailscale", detail})
	}
	rows = append(rows, []string{"  state dir", vm.Paths.Dir})
	a.out.Table(rows)

	if address != "" {
		a.out.Printf("\nConnect with: agent-vm ssh %s\n", vm.Name)
		return nil
	}
	// Without a wait there is no address to print, and telling the operator to
	// ssh to a guest that may not have booted would be a guess.
	a.out.Printf("\nNot waited for: check with `agent-vm info %s`.\n", vm.Name)
	return nil
}

// createRollback undoes the parts of a create that already happened. It runs in
// the reverse order they were done, and reports what it could not remove rather
// than hiding it — an operation that leaves host state behind must say so
// (exit 7).
type createRollback struct {
	store   *state.Store
	manager *domain.Manager
	name    string

	// domainAttempted is set before virt-install runs and domainDefined once it
	// succeeds. virt-install defines the domain before it boots it, so a run
	// that failed or was killed in between can still have left one behind.
	domainAttempted bool
	domainDefined   bool
	directory       string
}

// rollbackTimeout bounds the whole rollback once it no longer follows the
// caller's context.
const rollbackTimeout = 2 * time.Minute

func (r *createRollback) undo(ctx context.Context, cause error) error {
	// A create interrupted by Ctrl-C or SIGTERM still has to clean up after
	// itself, and with the caller's context canceled every virsh call would
	// fail without running.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	remaining := []string{}

	switch {
	case r.domainAttempted && !r.domainDefined:
		remaining = append(remaining, r.undoUnconfirmedDomain(ctx)...)
	case r.domainDefined:
		// The domain is running: virt-install started it. Powering it off
		// first is what makes undefine succeed on a live domain.
		if err := r.manager.ForceOff(ctx, r.name); err != nil {
			remaining = append(remaining, fmt.Sprintf("domain %s could not be powered off: %v", r.name, err))
		}
		if err := r.manager.Undefine(ctx, r.name); err != nil {
			remaining = append(remaining, fmt.Sprintf("libvirt domain %s: %v", r.name, err))
		}
	}
	if r.directory != "" {
		// Store.Remove refuses any path that does not resolve inside the state
		// directory, which is what keeps a rollback from deleting anything else.
		if err := r.store.Remove(r.directory); err != nil {
			remaining = append(remaining, fmt.Sprintf("state directory %s: %v", r.directory, err))
		}
	}

	if len(remaining) == 0 {
		return cause
	}
	return &CleanupError{
		Operation: fmt.Sprintf("creating VM %s failed and cleaning up after it", r.name),
		Cause:     cause,
		Remaining: remaining,
	}
}

// undoUnconfirmedDomain removes a domain a failed virt-install may have left.
// The name was checked free under the VM lock before virt-install ran, so a
// domain by this name now is this create's own and is safe to remove.
func (r *createRollback) undoUnconfirmedDomain(ctx context.Context) []string {
	current, err := r.manager.State(ctx, r.name)
	if err != nil {
		return []string{fmt.Sprintf("libvirt domain %s may have been defined, and its state could not be read: %v", r.name, err)}
	}
	if current == domain.StateMissing {
		return nil
	}
	var remaining []string
	if current != domain.StateShutOff {
		if err := r.manager.ForceOff(ctx, r.name); err != nil {
			remaining = append(remaining, fmt.Sprintf("domain %s could not be powered off: %v", r.name, err))
		}
	}
	if err := r.manager.Undefine(ctx, r.name); err != nil {
		remaining = append(remaining, fmt.Sprintf("libvirt domain %s: %v", r.name, err))
	}
	return remaining
}

// printCreatePlan prints what a create would run and write. Like the image
// build plan, it creates nothing: values that only exist once the VM has been
// created are left out rather than invented.
func (a *App) printCreatePlan(cfg *config.Config, name string, extraArgs []string, githubSSHKey bool, join *tailscaleJoin) error {
	// The store, not the configured path, because a remote hypervisor's
	// default state directory sits under *its* home directory.
	store, err := a.Store()
	if err != nil {
		return err
	}
	layout := store.Layout
	vmDir := layout.VMDir(name)
	ref := cfg.Distro

	uri, err := a.hypervisorURI()
	if err != nil {
		return err
	}

	// Every line below is rendered through the runner rather than printed
	// directly, so that a plan for a remote hypervisor shows the ssh
	// invocations that would really run (ADR-0010).
	plan := func(name string, args ...string) {
		a.out.Printf("%s\n", a.runner.Render(hostexec.Command{Name: name, Args: args}))
	}

	if cfg.BridgeMode() {
		plan("ip", "-d", "-json", "link", "show", "type", "bridge")
	} else {
		plan(hostexec.Virsh.Name, "--connect", uri, "net-list", "--all", "--name")
	}

	plan(hostexec.VirtMakeFS.Name, "--type=vfat", "--label=cidata", "--size=8M", "--format=raw",
		vmDir+"/"+state.SeedDirectory, vmDir+"/"+state.SeedImageFile)

	plan(hostexec.QemuImg.Name, "create", "-f", "qcow2", "-F", "qcow2",
		"-b", layout.BaseDiskPath(ref.ImageName(), ref.Tag),
		vmDir+"/"+state.OverlayFile, strconv.FormatInt(int64(cfg.Disk), 10))

	plan(hostexec.Virsh.Name, "--connect", uri, "capabilities")

	args, err := domain.VirtInstallArgs(domain.CreateOptions{
		Name:       name,
		LibvirtURI: uri,
		// A plan runs nothing, so it cannot ask libvirt what a create asks it:
		// this machine's architecture stands in. The two differ only when the
		// hypervisor is another host, and then only in the --features argument.
		Arch:        runtime.GOARCH,
		VCPUs:       cfg.VCPUs,
		Memory:      cfg.Memory,
		MaxMemory:   cfg.MaxMemory,
		OverlayPath: vmDir + "/" + state.OverlayFile,
		KernelPath:  layout.KernelPath(ref.ImageName(), ref.Tag),
		InitrdPath:  layout.InitrdPath(ref.ImageName(), ref.Tag),
		// A plan has no manifest to read — the image may not be built yet — so
		// it uses the command line every base image is built with.
		KernelCmdline:  distro.KernelCmdline,
		SeedImagePath:  vmDir + "/" + state.SeedImageFile,
		Network:        cfg.Network,
		NATNetwork:     cfg.NATNetwork,
		Bridge:         cfg.Bridge,
		ConsoleLogPath: vmDir + "/" + state.ConsoleLog,
		ExtraArgs:      extraArgs,
	})
	if err != nil {
		return err
	}
	plan(hostexec.VirtInstall.Name, args...)
	plan(hostexec.Virsh.Name, "--connect", uri, "domifaddr", name, "--source", "agent")
	plan(hostexec.Virsh.Name, "--connect", uri, "domiflist", name)
	plan(hostexec.Virsh.Name, "--connect", uri, "dumpxml", name)
	if githubSSHKey {
		// gh and the connection to the guest run here rather than on the
		// hypervisor, so they are rendered as this machine would run them.
		a.out.Printf("gh auth status --hostname github.com\n")
		a.out.Printf("ssh%s %s@<guest address> cat %s\n", jumpArgs(a.sshJump()), cfg.GuestUser, guestPublicKeyPath)
		a.out.Printf("gh api --method POST user/keys -f title=%q -f key=<the guest's public key> --jq .id\n",
			githubKeyTitle(name))
	}
	if join != nil {
		a.printTailscalePlan(cfg.GuestUser, join)
	}

	for _, note := range []string{
		fmt.Sprintf("create the state directory %s", vmDir),
		fmt.Sprintf("write the generated cloud-init user-data and meta-data to %s/%s/", vmDir, state.SeedDirectory),
		fmt.Sprintf("capture the domain XML to %s/%s and the VM record to %s/%s",
			vmDir, state.DomainXMLFile, vmDir, state.VMRecordFile),
		fmt.Sprintf("build the base image %s first if it is not cached", ref),
	} {
		a.out.Printf("# %s\n", note)
	}
	return nil
}
