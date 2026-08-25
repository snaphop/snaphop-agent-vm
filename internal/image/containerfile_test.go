package image

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/image/distro"
	"git.snaphop.xyz/snaphop/snaphop-agent-vm/templates"
)

// TestContainerfiles_ForceTheResolvConfSymlink guards the fix for guests that
// booted with working networking and no working DNS.
//
// podman bind-mounts /etc/resolv.conf for the duration of every RUN, so
// systemd-resolved's packaging never gets to install its symlink and the empty
// regular file from the OCI base is what lands in the image. systemd's vendor
// rule is an `L`, which will not replace an existing path, so the guest boots
// with an empty resolv.conf and every name lookup fails while the lease, the
// route and resolved itself all look healthy. Only the forcing form (`L+`)
// repairs it, and it has to be there for every family.
func TestContainerfiles_ForceTheResolvConfSymlink(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		// The rule must both point at resolved's stub and use the forcing
		// form; an `L` here would silently reproduce the original bug.
		if !strings.Contains(recipe, "L+! /etc/resolv.conf - - - - ../run/systemd/resolve/stub-resolv.conf") {
			t.Errorf("%s does not force /etc/resolv.conf to systemd-resolved's stub with an `L+` tmpfiles rule; guests built from it will have no DNS", d.Containerfile)
		}

		// Writing it anywhere but /etc/tmpfiles.d/systemd-resolve.conf leaves
		// the vendor `L` rule in place and in conflict with ours.
		if !strings.Contains(recipe, "/etc/tmpfiles.d/systemd-resolve.conf") {
			t.Errorf("%s does not write the rule to /etc/tmpfiles.d/systemd-resolve.conf, so it does not mask systemd's own `L` rule for the same path", d.Containerfile)
		}

		// The stub is only answered while resolved is running.
		if !strings.Contains(recipe, "systemd-resolved.service") {
			t.Errorf("%s points resolv.conf at systemd-resolved's stub without enabling systemd-resolved.service", d.Containerfile)
		}
	}
}

// TestUbuntuContainerfile_InstallsLsbRelease guards a guest whose apt sources
// all pointed at a suite named "UNAVAILABLE".
//
// cloud-init's apt module rewrites the Ubuntu sources on first boot and asks
// the lsb_release command for the codename to write. cloud-init only
// *recommends* the package that provides it, so --no-install-recommends leaves
// it out and cloud-init falls back to the literal string UNAVAILABLE — every
// repository then 404s. The recipe has to name it explicitly.
func TestUbuntuContainerfile_InstallsLsbRelease(t *testing.T) {
	contents, err := templates.FS.ReadFile("distro/" + distro.Ubuntu.Containerfile)
	if err != nil {
		t.Fatalf("reading %s: %v", distro.Ubuntu.Containerfile, err)
	}
	recipe := string(contents)

	// Only meaningful while the recipe suppresses recommends; if that ever
	// changes the dependency comes back on its own and this test should be
	// revisited rather than silently passing for the wrong reason.
	if !strings.Contains(recipe, "--no-install-recommends") {
		t.Skip("ubuntu.Containerfile no longer uses --no-install-recommends; lsb-release arrives as a recommended dependency again")
	}
	if !strings.Contains(recipe, "lsb-release") {
		t.Errorf("%s installs cloud-init with --no-install-recommends but does not install lsb-release; cloud-init will write \"UNAVAILABLE\" as the suite in every guest's apt sources", distro.Ubuntu.Containerfile)
	}
}

// TestContainerfiles_InstallCommonGuestTooling guards the guest contract
// documented under "Guest tooling" in docs/cli.md: an agent that lands in a VM
// finds the ordinary tools already there instead of having to install them,
// which is only true if every family's recipe installs them.
//
// The package names differ per family, so this checks the name each family
// actually uses; a family that grows a fourth spelling belongs in this table
// rather than in a loosened assertion.
func TestContainerfiles_InstallCommonGuestTooling(t *testing.T) {
	packages := map[string][]string{
		distro.Ubuntu.Containerfile: {
			"iputils-ping", "curl", "wget", "git", "build-essential",
			"python3", "jq", "docker.io", "docker-compose-v2",
		},
		distro.Fedora.Containerfile: {
			"iputils", "curl", "wget", "git", "gcc", "make",
			"python3", "jq", "moby-engine", "docker-compose",
		},
		distro.Arch.Containerfile: {
			"iputils", "curl", "wget", "git", "base-devel",
			"python", "jq", "docker", "docker-compose",
		},
	}

	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		wanted, ok := packages[d.Containerfile]
		if !ok {
			t.Fatalf("%s has no expected package list here; a new family must state the tooling it installs", d.Containerfile)
		}
		for _, pkg := range wanted {
			if !strings.Contains(recipe, pkg) {
				t.Errorf("%s does not install %q; guests built from it will be missing tooling docs/cli.md promises", d.Containerfile, pkg)
			}
		}

		// A Docker daemon that is installed but never started is the same
		// thing as no Docker at all from inside the guest.
		if !strings.Contains(recipe, "enable docker.service") {
			t.Errorf("%s installs Docker without enabling docker.service; the daemon will not be running when the VM becomes reachable", d.Containerfile)
		}

		// The login user only exists after cloud-init has run, so socket
		// access is granted by the unit the image ships rather than by the
		// generated user-data — which must keep working against base images
		// built before Docker was added.
		if !strings.Contains(recipe, "enable agent-vm-user-setup.service") {
			t.Errorf("%s does not enable agent-vm-user-setup.service; the login user will need sudo for every docker command and will have no SSH key of its own", d.Containerfile)
		}
		if !strings.Contains(recipe, "COPY user-setup.sh /usr/local/sbin/agent-vm-user-setup") {
			t.Errorf("%s does not install the first-boot account setup script the unit runs", d.Containerfile)
		}
		// The unit waits for cloud-final.service, which cloud-init orders
		// after multi-user.target. Hanging it off that target as well is an
		// ordering cycle, and systemd breaks a cycle by deleting the job: the
		// unit stays enabled and inactive for the life of the VM, silently.
		if !strings.Contains(recipe, "'WantedBy=cloud-final.service'") {
			t.Errorf("%s does not install the first-boot unit into cloud-final.service; wanted by multi-user.target it would be an ordering cycle and would never run", d.Containerfile)
		}
		if strings.Contains(recipe, "'WantedBy=multi-user.target'") {
			t.Errorf("%s installs a unit into multi-user.target; combined with After=cloud-final.service that job is deleted at boot", d.Containerfile)
		}
	}
}

// TestContainerfiles_InstallTheGuestTmuxConfig guards the tmux configuration
// every guest is supposed to come up with.
//
// tmux is only useful to an agent if it behaves the same way in every VM, so
// the configuration ships in the base image rather than being pasted in by
// hand per VM. It has to reach two places: /etc/skel, which useradd copies
// into the login user cloud-init creates, and /root, whose home directory
// already exists by then and so never consults skel.
func TestContainerfiles_InstallTheGuestTmuxConfig(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		// The file has to be installed by the same family that installs tmux;
		// a configuration without the program is nothing.
		if !strings.Contains(recipe, "tmux") {
			t.Errorf("%s does not install tmux", d.Containerfile)
		}
		if !strings.Contains(recipe, "COPY tmux.conf /etc/skel/.tmux.conf") {
			t.Errorf("%s does not copy tmux.conf into /etc/skel; the login user cloud-init creates will have no tmux configuration", d.Containerfile)
		}
		if !strings.Contains(recipe, "/root/.tmux.conf") {
			t.Errorf("%s does not install tmux.conf for root; skel is only consulted when a home directory is created and /root already exists", d.Containerfile)
		}
	}
}

// TestContainerfiles_InstallTheTmuxSessionMenu guards the menu an interactive
// login lands on, and the guards that keep it out of everything else.
//
// The menu is only useful if it is unavoidable for a person and invisible to a
// script: a prompt reached by `ssh <vm> some-command`, or by an agent driving
// the VM, is a hang with no one there to answer it.
func TestContainerfiles_InstallTheTmuxSessionMenu(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, "COPY tmux-menu.sh /usr/local/bin/agent-vm-menu") {
			t.Errorf("%s does not install the tmux session menu where every account can run it", d.Containerfile)
		}
		// zz- so the menu starts after PATH and the mise shims are set: the shells
		// tmux starts inherit the environment this login shell ends up with.
		if !strings.Contains(recipe, "COPY tmux-menu-profile.sh /etc/profile.d/zz-agent-vm-tmux-menu.sh") {
			t.Errorf("%s does not start the menu from a profile script that sorts after the rest of /etc/profile.d", d.Containerfile)
		}
	}
}

// TestTmuxMenu_OnlyRunsWhereAPersonIsWatching guards the profile script's
// guards. Each one prevents a hang rather than a cosmetic problem.
func TestTmuxMenu_OnlyRunsWhereAPersonIsWatching(t *testing.T) {
	profile := readTemplate(t, "distro/tmux-menu-profile.sh")

	for _, guard := range []struct{ needle, why string }{
		{"case $- in", "does not check that the shell is interactive, so `ssh <vm> some-command` would stop at the menu"},
		{"[ -t 0 ] && [ -t 1 ] || return", "does not require a terminal on both ends, so a piped session would stop at the menu"},
		{`[ -z "${TMUX:-}" ] || return`, "does not check TMUX, so a shell inside tmux would offer to nest another session"},
		{`[ -z "${AGENT_VM_NO_MENU:-}" ] || return`, "has no opt-out; AGENT_VM_NO_MENU is the documented one"},
	} {
		if !strings.Contains(profile, guard.needle) {
			t.Errorf("the tmux menu profile script %s", guard.why)
		}
	}
}

// TestTmuxMenu_AlwaysLeavesAWayOut is the property that makes it safe to put a
// prompt in front of every login: a menu a person cannot leave has taken the
// VM away from them.
func TestTmuxMenu_AlwaysLeavesAWayOut(t *testing.T) {
	menu := readTemplate(t, "distro/tmux-menu.sh")

	if !strings.Contains(menu, "q | Q) break") {
		t.Error("the tmux menu has no quit option, so a login could not reach a plain shell")
	}
	// A failed read is end of input. Ignoring it spins the loop forever and
	// the session becomes unusable rather than dropping to a shell.
	if !strings.Contains(menu, `read -r -n1 -p "Choice: " choice || break`) {
		t.Error("the tmux menu does not break out of its loop at end of input, so ^D would spin it forever")
	}
	if !strings.Contains(menu, `read -r -p "Session name (empty for a generated one): " name || return`) {
		t.Error("the tmux menu's new-session prompt does not stop at end of input")
	}
	// uuidgen is packaged separately on Ubuntu and is not in these images.
	if strings.Contains(menu, "$(uuidgen") {
		t.Error("the tmux menu calls uuidgen, which Ubuntu does not install; read /proc/sys/kernel/random/uuid instead")
	}
}

// TestTmuxConfig_IsShippedInTheBuildContext ties the COPY above to the file the
// builder actually writes next to the Containerfile. A COPY of a file that is
// not in the build context fails the build minutes in, after the package
// installation has already been paid for.
func TestTmuxConfig_IsShippedInTheBuildContext(t *testing.T) {
	if !slices.Contains(buildContextFiles, "tmux.conf") {
		t.Fatalf("tmux.conf is not in buildContextFiles %v, so podman's build context will not contain it", buildContextFiles)
	}
	for _, name := range buildContextFiles {
		contents, err := templates.FS.ReadFile("distro/" + name)
		if err != nil {
			t.Fatalf("build context file %q is not embedded: %v", name, err)
		}
		if len(contents) == 0 {
			t.Errorf("embedded build context file %q is empty", name)
		}
	}
}

// miseAgents are the coding agents installed with mise, by the registry name
// each recipe asks for. agy and codex are absent because neither has a mise
// package: agy comes from its vendor's installer, and codex from OpenAI's own,
// which is the only thing that produces the standalone package
// `codex remote-control` requires.
var miseAgents = []string{"claude", "opencode", "pi"}

// miseShims are every command installed with mise that a guest has to be able
// to run without a login shell, in the order the recipes link them.
var miseShims = []string{
	"node", "npm", "npx",
	"go", "gofmt", "golangci-lint",
	"claude", "opencode", "pi", "wrangler", "playwright", "cf",
}

// shimLoopCommands returns the command names the recipe's `for command in ...;
// do` loop links into /usr/local/bin. The loop is written across several lines,
// so the names are read out of it rather than matched as one string.
func shimLoopCommands(recipe string) ([]string, bool) {
	const marker = "for command in "
	start := strings.Index(recipe, marker)
	if start < 0 {
		return nil, false
	}
	rest := recipe[start+len(marker):]
	end := strings.Index(rest, "; do")
	if end < 0 {
		return nil, false
	}
	list := strings.NewReplacer("\\", " ", "\n", " ").Replace(rest[:end])
	return strings.Fields(list), true
}

// TestContainerfiles_InstallTheCodingAgents guards the guest contract that a VM
// comes up with claude, codex, opencode, pi and agy already installed.
//
// None of the five is packaged by any distro, so nothing else in the image
// would pull them in by accident: if a recipe stops naming one, guests simply
// stop having it, and that only shows up when someone SSHes in.
func TestContainerfiles_InstallTheCodingAgents(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		// Node is no longer what the agents run on -- all five are native
		// binaries -- but wrangler and Playwright are still npm installs,
		// and a guest handed a JavaScript repository needs a runtime. It is
		// mise's node, like every other toolchain here, rather than a distro
		// package or a third-party repository.
		if !strings.Contains(recipe, "mise use --global --yes node@latest") {
			t.Errorf("%s does not install Node.js with mise; wrangler and Playwright cannot be installed without it", d.Containerfile)
		}
		if !strings.Contains(recipe, "mise use --global --yes "+strings.Join(miseAgents, " ")) {
			t.Errorf("%s does not install %v with mise; guests built from it will be missing an agent docs/cli.md promises", d.Containerfile, miseAgents)
		}
		// A mise install reaches a login shell through the shims, but
		// `ssh <vm> claude -p ...` runs no login shell, so each command also
		// needs a symlink to mise on the default PATH.
		linked, ok := shimLoopCommands(recipe)
		if !ok {
			t.Errorf("%s has no `for command in ...; do` loop linking mise shims into /usr/local/bin", d.Containerfile)
		}
		for _, command := range miseShims {
			if !slices.Contains(linked, command) {
				t.Errorf("%s does not link %q into /usr/local/bin; a non-interactive `ssh <vm> %s ...` would not find it", d.Containerfile, command, command)
			}
		}

		// codex is installed from OpenAI's installer rather than npm, and
		// where it lands matters twice over: /usr/local/bin so every
		// account finds the command, and a CODEX_HOME outside any one home
		// so every account shares the ~300 MiB standalone package that
		// codex remote-control starts its app-server from.
		if !strings.Contains(recipe, "chatgpt.com/codex/install.sh") {
			t.Errorf("%s does not install codex from OpenAI's installer; an npm-installed codex has no standalone package and `codex remote-control` refuses to run without one", d.Containerfile)
		}
		if strings.Contains(recipe, "@openai/codex") {
			t.Errorf("%s still installs @openai/codex from npm; that install shadows the installer's codex on PATH and cannot run remote control", d.Containerfile)
		}
		for _, setting := range []string{"CODEX_INSTALL_DIR=/usr/local/bin", "CODEX_HOME=/usr/local/lib/codex", "CODEX_NON_INTERACTIVE=1"} {
			if !strings.Contains(recipe, setting) {
				t.Errorf("%s does not set %s for the codex installer; it would install into the build user's home, prompt, or edit a shell profile", d.Containerfile, setting)
			}
		}
		if !strings.Contains(recipe, "test -x /usr/local/lib/codex/packages/standalone/current/codex") {
			t.Errorf("%s does not check that the codex installer produced a standalone package; remote control would fail in every VM built on the image", d.Containerfile)
		}

		// agy has no npm package. Installing it anywhere but a directory on
		// the default PATH leaves it invisible to every account but the one
		// that ran the build.
		if !strings.Contains(recipe, "antigravity.google/cli/install.sh") {
			t.Errorf("%s does not install the Antigravity CLI (agy)", d.Containerfile)
		}
		if !strings.Contains(recipe, "--dir /usr/local/bin") {
			t.Errorf("%s installs agy without pointing it at /usr/local/bin; it will land in the build user's home and no guest account will find it", d.Containerfile)
		}

		// 22.19 is the Node floor the image promises. Catching a distro
		// that drops below it during the build is the difference between a
		// failed build and a guest that cannot run what it was handed.
		if !strings.Contains(recipe, "this image requires >= 22.19") {
			t.Errorf("%s does not assert a minimum Node version; a distro that ships Node older than 22.19 would produce an image below the floor docs/cli.md promises", d.Containerfile)
		}
	}
}

// TestContainerfiles_ConfigureTheAgentsForUnattendedUse guards the reason the
// agents are in the image at all: they have to work without a human approving
// each tool call.
//
// The configuration has to reach both /etc/skel, which useradd copies into the
// login user cloud-init creates, and /root, whose home already exists in the
// image and so never consults skel.
func TestContainerfiles_ConfigureTheAgentsForUnattendedUse(t *testing.T) {
	configs := []struct{ file, dest string }{
		{"claude-settings.json", "/etc/skel/.claude/settings.json"},
		{"codex-config.toml", "/etc/skel/.codex/config.toml"},
		{"opencode.json", "/etc/skel/.config/opencode/opencode.json"},
	}

	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		for _, config := range configs {
			if !strings.Contains(recipe, "COPY "+config.file+" "+config.dest) {
				t.Errorf("%s does not copy %s to %s; the login user cloud-init creates will get that agent's default, which prompts for approval", d.Containerfile, config.file, config.dest)
			}
		}
		if !strings.Contains(recipe, `install -D -m 0644 "/etc/skel/${file}" "/root/${file}"`) {
			t.Errorf("%s does not install the agent configuration for root; skel is only consulted when a home directory is created and /root already exists", d.Containerfile)
		}

		// agy has no configuration file, so the alias is the only thing
		// standing between it and an approval prompt.
		if !strings.Contains(recipe, "COPY agent-aliases.sh /etc/profile.d/agent-vm-agents.sh") {
			t.Errorf("%s does not install the agy alias into /etc/profile.d; agy will prompt for approval in every interactive session", d.Containerfile)
		}
	}
}

// TestAgentConfigs_SelectTheMostPermissiveMode checks the settings themselves
// rather than that a file exists. A config file shipped with a default or
// misspelled value is worse than none: it looks configured and still blocks.
func TestAgentConfigs_SelectTheMostPermissiveMode(t *testing.T) {
	claude := struct {
		Permissions struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
		RemoteControlAtStartup bool `json:"remoteControlAtStartup"`
	}{}
	readJSON(t, "distro/claude-settings.json", &claude)
	if claude.Permissions.DefaultMode != "bypassPermissions" {
		t.Errorf("claude-settings.json sets permissions.defaultMode to %q, want \"bypassPermissions\"; any other mode stops to ask", claude.Permissions.DefaultMode)
	}
	// The settings-file equivalent of `claude --remote-control`. claude treats
	// it as security-sensitive and ignores it from project or local settings,
	// so this per-account file is the only place that can turn it on.
	if !claude.RemoteControlAtStartup {
		t.Error("claude-settings.json does not set remoteControlAtStartup; claude sessions in a VM would start without Remote Control")
	}

	opencode := struct {
		Permission map[string]string `json:"permission"`
	}{}
	readJSON(t, "distro/opencode.json", &opencode)
	for _, action := range []string{"edit", "bash", "webfetch"} {
		if got := opencode.Permission[action]; got != "allow" {
			t.Errorf("opencode.json sets permission.%s to %q, want \"allow\"; opencode will ask before every %s", action, got, action)
		}
	}

	// Codex takes TOML, and pulling in a parser for two keys is not worth a
	// dependency (AGENTS.md §11).
	codex := readTemplate(t, "distro/codex-config.toml")
	for _, setting := range []string{`approval_policy = "never"`, `sandbox_mode = "danger-full-access"`} {
		if !strings.Contains(codex, setting) {
			t.Errorf("codex-config.toml does not set %s; codex will either ask for approval or sandbox itself inside a VM that is already the sandbox", setting)
		}
	}

	// agy's only lever is the flag.
	aliases := readTemplate(t, "distro/agent-aliases.sh")
	if !strings.Contains(aliases, "--dangerously-skip-permissions") {
		t.Errorf("agent-aliases.sh does not pass --dangerously-skip-permissions to agy, which is the only way it runs unattended")
	}
}

// TestAgentConfigs_CarryNoCredentials is the SECURITY.md guard on this whole
// change. A base image is shared by every VM built on it and cached
// indefinitely, so an API key that reached one of these files would be handed
// to every guest and every operator who copied the cache. Credentials belong
// in per-VM cloud-init, never here.
func TestAgentConfigs_CarryNoCredentials(t *testing.T) {
	secretish := []string{"api_key", "apikey", "api-key", "token", "secret", "password", "sk-", "bearer"}

	for _, name := range buildContextFiles {
		contents := strings.ToLower(readTemplate(t, "distro/"+name))
		for _, needle := range secretish {
			if strings.Contains(contents, needle) {
				t.Errorf("build context file %q contains %q; a base image is shared by every VM built on it and must carry no credentials (SECURITY.md)", name, needle)
			}
		}
	}
}

// TestAgentConfigs_AreShippedInTheBuildContext ties the COPYs above to the
// files the builder actually writes next to the Containerfile. A COPY of a
// file missing from the build context fails the build minutes in, after the
// package installation has already been paid for.
func TestAgentConfigs_AreShippedInTheBuildContext(t *testing.T) {
	for _, name := range []string{"claude-settings.json", "codex-config.toml", "opencode.json", "agent-aliases.sh", "codex-remote-control.sh"} {
		if !slices.Contains(buildContextFiles, name) {
			t.Errorf("%s is not in buildContextFiles %v, so podman's build context will not contain it", name, buildContextFiles)
		}
	}
}

// TestContainerfiles_InstallTheGoAndRustToolchains guards the toolchains that
// do not come from the family's packages.
//
// Every family packages some Go, and the versions are years apart -- an image
// whose Go is older than the `go` directive of the repository an agent is
// given cannot build it at all -- so Go and golangci-lint come from mise and
// Rust from rustup on all three, which is what keeps the images on one
// release. Each has to be reachable without a profile script: the
// `ssh <vm> go build` an agent runs is not a login shell.
func TestContainerfiles_InstallTheGoAndRustToolchains(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, "mise use --global --yes go@latest golangci-lint@latest") {
			t.Errorf("%s does not install Go and golangci-lint with mise; a family's packages would put a different release in each image", d.Containerfile)
		}
		// Rust stays on rustup rather than mise: mise's rust is rustup
		// underneath and re-reads RUSTUP_HOME and CARGO_HOME from the
		// environment of whoever runs cargo, so a shared installation makes it
		// re-run rustup-init as each account, which cannot write there. Per
		// account it works, at roughly 1.5 GiB of toolchain per account.
		if strings.Contains(recipe, "rust@latest") {
			t.Errorf("%s installs Rust with mise; every account would carry its own 1.5 GiB toolchain, or fail on a shared one", d.Containerfile)
		}
		if !strings.Contains(recipe, "https://sh.rustup.rs") {
			t.Errorf("%s does not install Rust through rustup", d.Containerfile)
		}
		if !strings.Contains(recipe, "export RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo") {
			t.Errorf("%s does not install Rust into a shared /usr/local/rustup; every account would download its own toolchain at first use", d.Containerfile)
		}
		// A root-owned toolchain nobody else can read is the same thing as no
		// shared toolchain at all.
		if !strings.Contains(recipe, "chmod -R a+rX /usr/local/rustup /usr/local/cargo") {
			t.Errorf("%s does not make the shared Rust installation readable; every account but root would be unable to run cargo", d.Containerfile)
		}
		// The rustup proxies resolve their toolchain through RUSTUP_HOME, so a
		// shared installation is only usable if every account sees that
		// variable -- including a non-interactive command, which reads
		// /etc/environment through PAM and no profile script at all.
		if !strings.Contains(recipe, "printf 'RUSTUP_HOME=/usr/local/rustup\\n' >> /etc/environment") {
			t.Errorf("%s does not put RUSTUP_HOME in /etc/environment; cargo would look for a toolchain in an empty ~/.rustup", d.Containerfile)
		}
		// CARGO_HOME is left unset on purpose: cargo then defaults to the
		// account's own ~/.cargo, so `cargo install` does not need root and
		// accounts do not share an install root.
		if strings.Contains(recipe, "CARGO_HOME=/usr/local/cargo\\n' >> /etc/environment") {
			t.Errorf("%s exports CARGO_HOME image-wide; `cargo install` would then write into a root-owned directory", d.Containerfile)
		}
		if !strings.Contains(recipe, "COPY toolchains.sh /etc/profile.d/agent-vm-toolchains.sh") {
			t.Errorf("%s does not install the toolchain PATH script; binaries from `go install` and `cargo install` would not be on the path", d.Containerfile)
		}

		// A toolchain that unpacked but cannot run looks exactly like a
		// working one until someone types the command inside a VM.
		for _, check := range []string{
			"go version", "gofmt", "golangci-lint --version",
			"rustc --version", "cargo --version", "rustup --version",
			"cargo fmt --version", "cargo clippy --version",
		} {
			if !strings.Contains(recipe, check) {
				t.Errorf("%s does not run %q at build time", d.Containerfile, check)
			}
		}
		// cargo is exercised the way a guest reaches it. The shared
		// RUSTUP_HOME is the part that can be wrong while every file is in
		// place, and only sourcing /etc/environment proves it is not.
		if !strings.Contains(recipe, "set -a; . /etc/environment; set +a;") {
			t.Errorf("%s does not smoke-test cargo through /etc/environment, which is the only thing that proves the shared RUSTUP_HOME reaches a guest", d.Containerfile)
		}
	}
}

// TestContainerfiles_CanGrowTheRootFilesystem guards the one thing that makes
// the VM's disk size real inside the guest.
//
// The base filesystem is built to the size of the image plus a little slack, so
// a guest that cannot grow its root partition has that slack and nothing more.
// First boot then copies /etc/skel -- the JDK, Node, the agents, and the Rust
// toolchain -- into a new account, runs out of space, and takes cloud-final and
// sshd down with it: a VM that boots and never accepts SSH. Ubuntu's cloud-init
// pulls growpart in as a dependency; Fedora's and Arch's do not.
func TestContainerfiles_CanGrowTheRootFilesystem(t *testing.T) {
	growpart := map[string]string{
		"fedora": "cloud-utils-growpart",
		"arch":   "cloud-guest-utils",
		"ubuntu": "cloud-init",
	}
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		pkg, ok := growpart[name]
		if !ok {
			t.Fatalf("no growpart package recorded for the %s image; a guest that cannot grow its root filesystem fills it on first boot", name)
		}
		if !strings.Contains(recipe, pkg) {
			t.Errorf("%s does not install %s, so cloud-init cannot grow the root partition and first boot fills the filesystem", d.Containerfile, pkg)
		}
	}
}

// TestToolchainProfile_LeavesTheToolchainsOutOfPath is the other half of that
// contract. The profile script exists for what a user installs later; putting
// the toolchains themselves on the path through it would make them invisible
// to every non-interactive command.
func TestToolchainProfile_LeavesTheToolchainsOutOfPath(t *testing.T) {
	script := readTemplate(t, "distro/toolchains.sh")

	for _, dir := range []string{"$HOME/go/bin", "$HOME/.cargo/bin"} {
		if !strings.Contains(script, dir) {
			t.Errorf("toolchains.sh does not put %s on the path, so binaries a user installs would not be found", dir)
		}
	}
	if strings.Contains(script, "/usr/local/go/bin") || strings.Contains(script, "/usr/local/cargo/bin") {
		t.Error("toolchains.sh puts a toolchain directory on the path; the toolchains are linked into /usr/local/bin instead, so they work in a non-interactive command too")
	}
	if !slices.Contains(buildContextFiles, "toolchains.sh") {
		t.Errorf("toolchains.sh is not in buildContextFiles %v, so podman's build context will not contain it", buildContextFiles)
	}
}

func readTemplate(t *testing.T, path string) string {
	t.Helper()
	contents, err := templates.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(contents)
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(readTemplate(t, path)), into); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
}

// TestContainerfiles_SmokeTestTheAgents guards against the failure mode that
// this whole feature is most exposed to: an agent that installs cleanly and
// cannot run.
//
// An archive for the wrong architecture, or one whose entry point cannot find
// what it needs, unpacks cleanly and fails at first use — inside a VM, long
// after the image was built and cached. The recipes run every agent once, so a
// future packaging change fails the build instead of shipping a guest whose
// agents do not work.
func TestContainerfiles_SmokeTestTheAgents(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, `for agent in claude codex opencode pi agy; do`) {
			t.Errorf("%s does not run each agent once at build time; an agent that installs but cannot start would ship undetected", d.Containerfile)
		}
		if !strings.Contains(recipe, "installed but cannot run") {
			t.Errorf("%s does not fail the build when an agent cannot start", d.Containerfile)
		}
	}
}

// TestContainerfiles_PinTheDatasourceSoItActuallyWins guards a guest that
// reached for a metadata service on the network despite the image pinning
// NoCloud.
//
// cloud-init reads /etc/cloud/cloud.cfg.d in sorted order and the last file to
// set a key wins. The pin used to be written to 90-agent-vm-datasource.cfg,
// which sorts before Ubuntu's own 90_dpkg.cfg ('-' is 0x2D, '_' is 0x5F), and
// that file lists Ec2 along with every other network datasource. The pin was
// therefore dead on Ubuntu: the first boot looked fine because the seed is
// attached and NoCloud matches at once, and every later boot hung for four
// minutes probing 169.254.169.254 before sshd started.
//
// The prefix has to sort after 90_dpkg.cfg, and the recipe has to verify that
// at build time, because whether it sorts last depends on files this project
// does not control.
func TestContainerfiles_PinTheDatasourceSoItActuallyWins(t *testing.T) {
	const pinFile = "/etc/cloud/cloud.cfg.d/99-agent-vm-datasource.cfg"

	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, "> "+pinFile) {
			t.Errorf("%s does not write the datasource pin to %s", d.Containerfile, pinFile)
		}

		// The specific name that caused the bug. Matched as a redirect
		// target, so that the recipe can still name it while explaining
		// what went wrong.
		if strings.Contains(recipe, "> /etc/cloud/cloud.cfg.d/90-agent-vm-datasource.cfg") {
			t.Errorf("%s writes the datasource pin to 90-agent-vm-datasource.cfg, which sorts before Ubuntu's 90_dpkg.cfg and is silently overridden by it; guests would probe a metadata service on every boot after the first", d.Containerfile)
		}

		// A prefix alone is not a guarantee: it only holds as long as no
		// distro adds a later-sorting file. The build has to check.
		if !strings.Contains(recipe, "grep -l '^datasource_list:' /etc/cloud/cloud.cfg.d/*.cfg") {
			t.Errorf("%s does not verify at build time that its datasource pin is the last one cloud-init reads; a distro adding a later-sorting datasource_list would silently take the guarantee away again", d.Containerfile)
		}
	}
}

// TestDatasourcePinFilename_SortsAfterTheDistroFilesThatSetIt is the ordering
// rule itself, stated once. It is what the prefix in the recipes has to
// satisfy, and it fails on exactly the comparison that was originally got
// wrong.
func TestDatasourcePinFilename_SortsAfterTheDistroFilesThatSetIt(t *testing.T) {
	const ours = "99-agent-vm-datasource.cfg"

	// Files shipped by the supported families that set datasource_list.
	for _, theirs := range []string{"90_dpkg.cfg", "05_logging.cfg"} {
		if ours <= theirs {
			t.Errorf("%s does not sort after %s, so cloud-init would read theirs last and ignore our pin", ours, theirs)
		}
	}

	// The name that shipped the bug, kept here so the comparison that failed
	// is the one under test.
	if old := "90-agent-vm-datasource.cfg"; old > "90_dpkg.cfg" {
		t.Errorf("expected %s to sort before 90_dpkg.cfg, which is why the pin was overridden; if this no longer holds the bug's explanation is wrong", old)
	}
}

// TestContainerfiles_InstallTheDevTooling guards the second half of the guest
// contract: gh, tea, Playwright with a headless Chromium, and the JVM
// toolchain mise installs.
//
// Two of these have a trap in them that a looser assertion would walk into.
// Ubuntu's `tea` package is an unrelated text editor, so a recipe that
// installs "tea" from apt gives the guest the wrong program under the right
// name; tea therefore comes from Gitea's release server everywhere. And
// Ubuntu's chromium package is a snap stub, useless in a VM, so the only
// Chromium in the image is the one Playwright pins.
func TestContainerfiles_InstallTheDevTooling(t *testing.T) {
	ghPackage := map[string]string{
		distro.Ubuntu.Containerfile: "gh",
		distro.Fedora.Containerfile: "gh",
		distro.Arch.Containerfile:   "github-cli",
	}

	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if pkg := ghPackage[d.Containerfile]; !strings.Contains(recipe, pkg) {
			t.Errorf("%s does not install the GitHub CLI (%s)", d.Containerfile, pkg)
		}

		// tea has to come from Gitea, not from a package manager.
		if !strings.Contains(recipe, "dl.gitea.com/tea") {
			t.Errorf("%s does not install tea from Gitea's release server; only Arch packages it, and on Ubuntu the name belongs to an unrelated text editor", d.Containerfile)
		}

		// Playwright, wrangler and cf are published only to npm, so they are
		// the one place mise's npm backend is still used.
		if !strings.Contains(recipe, "mise use --global --yes npm:wrangler npm:playwright npm:cf") {
			t.Errorf("%s does not install wrangler, Playwright and cf with mise", d.Containerfile)
		}
		if !strings.Contains(recipe, "playwright install") || !strings.Contains(recipe, "chromium") {
			t.Errorf("%s does not install a Chromium for Playwright to drive", d.Containerfile)
		}
		// Anonymous metrics are on by default. A disposable VM an agent drives
		// is not a machine whose operator opted in, and the setting has to
		// reach a non-interactive command, which reads /etc/environment
		// through PAM and no profile script.
		if !strings.Contains(recipe, "printf 'WRANGLER_SEND_METRICS=false\\n' >> /etc/environment") {
			t.Errorf("%s does not turn off wrangler's usage metrics in /etc/environment", d.Containerfile)
		}

		if !strings.Contains(recipe, "COPY chromium.sh /usr/local/bin/chromium") {
			t.Errorf("%s does not expose Chromium as `chromium`; the browser would only be reachable through Playwright", d.Containerfile)
		}

		// A distro chromium alongside Playwright's would be hundreds of
		// megabytes Playwright never uses -- and on Ubuntu it is a snap stub.
		for _, pkg := range []string{"chromium-browser", "chromium-headless"} {
			if strings.Contains(recipe, pkg) {
				t.Errorf("%s installs the distro package %q as well as Playwright's Chromium; there should be exactly one browser in the image", d.Containerfile, pkg)
			}
		}

		// mise goes to /usr/local/bin rather than the installer's default of
		// ~/.local/bin, so that every account has it on the default PATH.
		if !strings.Contains(recipe, "MISE_INSTALL_PATH=/usr/local/bin/mise") {
			t.Errorf("%s does not install mise where every account has it on PATH", d.Containerfile)
		}
		// The JVM toolchain has to be installed into the skel copy, before it
		// is cloned to /root: a JDK downloaded per account at first use would
		// be a download a network-isolated guest cannot make. It goes to
		// /etc/skel rather than one shared directory because installing a tool
		// writes into mise's data directory.
		if !strings.Contains(recipe, "MISE_DATA_DIR=/etc/skel/.local/share/mise") {
			t.Errorf("%s does not install the mise toolchain into /etc/skel; accounts cloud-init creates would have none, and a shared copy would have every user writing to one directory", d.Containerfile)
		}
		// java@temurin, not java@latest: mise names a distribution by prefix,
		// and an unprefixed version gets an Oracle build of OpenJDK instead of
		// the Temurin one docs/cli.md promises.
		if !strings.Contains(recipe, "mise use --global --yes java@temurin maven@latest") {
			t.Errorf("%s does not install a Temurin JDK and Maven with mise; a guest would come up with `mise` and no Java, or with the wrong JDK", d.Containerfile)
		}
		if !strings.Contains(recipe, "cp -a /etc/skel/.local/share/mise /root/.local/share/mise") {
			t.Errorf("%s does not copy the toolchain to /root, which is created before /etc/skel holds it and never inherits from it", d.Containerfile)
		}
		// The shims are executables, not a shell function, but they still have
		// to be put on PATH for a login shell to find java and mvn.
		if !strings.Contains(recipe, "COPY mise.sh /etc/profile.d/agent-vm-mise.sh") {
			t.Errorf("%s does not install the mise shell init; the shims would be on no account's PATH", d.Containerfile)
		}
	}
}

// TestUserSetupScript_DoesBothJobsItIsThereFor guards the first-boot script
// that finishes every interactive account.
//
// Neither job can be done at build time — the accounts do not exist yet — and
// neither can be done from the seed: cloud-init only knows about the login
// user agent-vm asks for, and a private key must never be written into a seed
// (SECURITY.md).
func TestUserSetupScript_DoesBothJobsItIsThereFor(t *testing.T) {
	script := readTemplate(t, "distro/user-setup.sh")

	for _, group := range []string{"docker", "libvirt", "kvm"} {
		if !strings.Contains(script, group) {
			t.Errorf("the first-boot script does not add accounts to the %q group", group)
		}
	}
	// A group the image does not have must be skipped, not created: base
	// images built before that software was installed have none of them.
	if !strings.Contains(script, `getent group "${group}" >/dev/null 2>&1 || continue`) {
		t.Error("the first-boot script does not skip groups the image lacks; it would fail on an older base image")
	}

	if !strings.Contains(script, "ssh-keygen") || !strings.Contains(script, "id_ed25519") {
		t.Error("the first-boot script does not generate an SSH key pair for each account")
	}
	// An operator may have supplied their own key through --cloud-init, and a
	// re-run must not replace a key the account has already published.
	if !strings.Contains(script, `if [ -e "${home}/.ssh/id_ed25519" ]; then`) {
		t.Error("the first-boot script does not leave an existing key alone, so a re-run could replace a published key")
	}
	// A key readable by other accounts on the VM is not a key.
	if !strings.Contains(script, `chmod 0600 "${home}/.ssh/id_ed25519"`) {
		t.Error("the first-boot script does not restrict the private key's mode")
	}
	// Arch installs no hostname binary at all, so the comment naming the
	// account has to come from coreutils.
	if strings.Contains(script, "$(hostname)") {
		t.Error("the first-boot script calls hostname, which Arch does not install; use uname -n")
	}
}

// TestCodexRemoteControl_StartsAtEveryBootWithoutBreakingIt guards the unit
// and script that bring Codex's remote-control daemon up on a running VM.
//
// The daemon cannot be started at build time and it is not a first-boot job
// either: it dies with the VM, so it is started again on every boot. It also
// needs credentials that only arrive per-VM (SECURITY.md), so on a VM where
// nobody has run `codex login` this must report the failure and leave the boot
// alone — a VM unreachable because a daemon nobody asked for could not
// authenticate would be far worse than one without remote control.
func TestCodexRemoteControl_StartsAtEveryBootWithoutBreakingIt(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, "COPY codex-remote-control.sh /usr/local/sbin/agent-vm-codex-remote-control") {
			t.Errorf("%s does not install the codex remote-control script", d.Containerfile)
		}
		if !strings.Contains(recipe, "enable agent-vm-codex-remote-control.service") {
			t.Errorf("%s does not enable agent-vm-codex-remote-control.service; the daemon would never start on a VM", d.Containerfile)
		}
		// Wanted by cloud-final.service, not multi-user.target: a unit
		// ordered after cloud-final and wanted by that target forms a cycle
		// systemd breaks by dropping our job, leaving the unit enabled,
		// inactive, and silent.
		if !strings.Contains(recipe, "'WantedBy=cloud-final.service' \\\n      > /usr/lib/systemd/system/agent-vm-codex-remote-control.service") {
			t.Errorf("%s does not attach the codex remote-control unit to cloud-final.service; ordering it after cloud-final under multi-user.target forms a cycle and the job is silently dropped", d.Containerfile)
		}
	}

	script := readTemplate(t, "distro/codex-remote-control.sh")

	if !strings.Contains(script, "codex remote-control start") {
		t.Error("the boot script does not start codex remote control")
	}
	// The daemon reads the account's own credentials, and runuser without -l
	// keeps root's environment: without an explicit HOME every account's
	// daemon would authenticate as root.
	if !strings.Contains(script, `runuser -u "${name}" -- env HOME="${home}"`) {
		t.Error("the boot script does not run codex as the account with that account's HOME; every daemon would use root's credentials")
	}
	// codex remote-control starts its app-server from a fixed path under the
	// account's CODEX_HOME and refuses to run when it is absent. One shared
	// package is ~300 MiB, so each account gets a symlink rather than a copy.
	if !strings.Contains(script, "/usr/local/lib/codex/packages/standalone/current") {
		t.Error("the boot script does not link accounts to the shared standalone package; codex remote-control refuses to start without one")
	}
	if !strings.Contains(script, `if [ -e "${link}" ] || [ -L "${link}" ]; then`) {
		t.Error("the boot script does not leave an existing ~/.codex/packages/standalone/current alone; a re-run could replace an operator's own install")
	}
	// A missing login is the normal state of a fresh VM. Failing the unit for
	// it would mark the boot degraded for something nobody asked for.
	if !strings.Contains(script, "codex login") {
		t.Error("the boot script does not tell the operator how to make remote control work; a failure with no next step is not an error message")
	}
	if strings.Contains(script, "exit 1") {
		t.Error("the boot script can exit non-zero; a VM with no codex credentials would boot degraded")
	}
}

// TestContainerfiles_InstallTheVirtualizationStack guards the tooling that
// lets a guest run VMs of its own.
//
// It is the same set of host tools agent-vm itself drives, so a guest that has
// them can run agent-vm — and the guest half of nested virtualization is
// worthless without them. Names differ per family; the commands do not.
func TestContainerfiles_InstallTheVirtualizationStack(t *testing.T) {
	packages := map[string][]string{
		distro.Ubuntu.Containerfile: {
			"qemu-kvm", "libvirt-daemon-system", "libvirt-clients", "virtinst",
			"dnsmasq-base", "guestfs-tools", "podman",
		},
		distro.Fedora.Containerfile: {
			"qemu-kvm", "libvirt", "libvirt-client", "virt-install",
			"dnsmasq", "guestfs-tools", "podman",
		},
		distro.Arch.Containerfile: {
			"qemu-base", "libvirt", "virt-install",
			"dnsmasq", "guestfs-tools", "podman",
		},
	}

	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		wanted, ok := packages[d.Containerfile]
		if !ok {
			t.Fatalf("%s has no expected package list here; a new family must state the virtualization stack it installs", d.Containerfile)
		}
		for _, pkg := range wanted {
			if !strings.Contains(recipe, pkg) {
				t.Errorf("%s does not install %q, so a guest built from it could not run a VM of its own", d.Containerfile, pkg)
			}
		}

		// Ubuntu's full dnsmasq package additionally enables a system-wide
		// resolver on port 53, which fights with the instance libvirt starts
		// per network. libvirt only needs the binary.
		if d.Containerfile == distro.Ubuntu.Containerfile && strings.Contains(recipe, "\n      dnsmasq \\\n") {
			t.Errorf("%s installs the full dnsmasq package; libvirt needs dnsmasq-base, and the daemon package would contend with libvirt's own instances", d.Containerfile)
		}

		// The guest half of nested virtualization. The host half is
		// virt-install's --cpu host-passthrough, in internal/domain.
		if !strings.Contains(recipe, "options kvm_intel nested=1") || !strings.Contains(recipe, "options kvm_amd nested=1") {
			t.Errorf("%s does not enable nested virtualization in the guest, so a VM inside this VM could not nest further", d.Containerfile)
		}

		// A libvirt that is installed but never started is the same thing as
		// no libvirt at all from inside the guest.
		if !strings.Contains(recipe, "libvirtd.service") || !strings.Contains(recipe, "virtqemud.service") {
			t.Errorf("%s does not enable a libvirt daemon; a nested `agent-vm create` would fail to connect", d.Containerfile)
		}
	}
}

// TestContainerfiles_ShareTheBrowsersThroughTheEnvironment guards the setting
// that decides whether a non-interactive command can drive a browser at all.
//
// Playwright looks for its browsers under ~/.cache unless
// PLAYWRIGHT_BROWSERS_PATH says otherwise. The image installs them once into
// /opt/ms-playwright, so every account has to see that variable -- including
// the `ssh <vm> node script.js` an agent actually runs, which reads
// /etc/environment through PAM but never sources a profile script.
func TestContainerfiles_ShareTheBrowsersThroughTheEnvironment(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, "PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright") {
			t.Errorf("%s does not point Playwright at the shared browser directory", d.Containerfile)
		}
		if !strings.Contains(recipe, ">> /etc/environment") {
			t.Errorf("%s does not put PLAYWRIGHT_BROWSERS_PATH in /etc/environment; a profile script would leave non-interactive commands looking in an empty ~/.cache", d.Containerfile)
		}
	}
}

// TestContainerfiles_SmokeTestTheDevTooling is the guard on the failure this
// tooling is most prone to. A browser missing one shared library installs
// perfectly and dies the instant it is launched, so only launching it during
// the build says anything about whether a guest can drive a page.
func TestContainerfiles_SmokeTestTheDevTooling(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, "chromium --headless=new --no-sandbox --disable-gpu --dump-dom about:blank") {
			t.Errorf("%s does not launch Chromium during the build; a missing shared library would only surface inside a VM", d.Containerfile)
		}
		for _, check := range []string{"gh --version", "tea --version", "wrangler --version", "cf --version", "playwright --version", "mise --version"} {
			if !strings.Contains(recipe, check) {
				t.Errorf("%s does not run %q at build time", d.Containerfile, check)
			}
		}
		if !strings.Contains(recipe, "the mise shims are missing from /etc/skel") {
			t.Errorf("%s does not check that the mise shims landed in /etc/skel, which is where accounts cloud-init creates get them from", d.Containerfile)
		}
		// The shims existing in /etc/skel says nothing about whether the
		// profile script puts them on PATH, which is what makes `java`
		// resolvable at all.
		if !strings.Contains(recipe, `bash -lc 'command -v java'`) {
			t.Errorf("%s does not check that java resolves in a login shell; the profile script could be missing and the shim check would still pass", d.Containerfile)
		}
		for _, check := range []string{`bash -lc 'java -version'`, `bash -lc 'mvn -version'`} {
			if !strings.Contains(recipe, check) {
				t.Errorf("%s does not run %s at build time; a JDK or Maven that did not install would only surface inside a VM", d.Containerfile, check)
			}
		}
		for _, check := range []string{
			"virsh --version", "virt-install --version", "qemu-img --version",
			"virt-make-fs --version", "podman --version", "dnsmasq --version",
		} {
			if !strings.Contains(recipe, check) {
				t.Errorf("%s does not run %q at build time", d.Containerfile, check)
			}
		}
		// The binary is named for the architecture, so an image can be missing
		// it while every other check passes.
		if !strings.Contains(recipe, `command -v "qemu-system-$(uname -m)"`) {
			t.Errorf("%s does not check that a qemu-system binary for this architecture is present", d.Containerfile)
		}
	}
}

// TestContainerfiles_AllowUnprivilegedPing guards the fix for a guest whose
// networking worked and whose `ping` did not.
//
// The root filesystem reaches the disk as a `podman export` tar unpacked by
// virt-make-fs, and the security.capability xattr that distro packaging puts
// on /usr/bin/ping does not survive that. Ubuntu and Fedora leave
// net.ipv4.ping_group_range at the kernel's empty `1 0` default because they
// expect that capability to be there, so iputils falls back to a raw socket
// and the agent user gets "missing cap_net_raw+p capability" — which reads as
// a broken network on a guest that has a lease, a route and working TCP.
func TestContainerfiles_AllowUnprivilegedPing(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		// The whole range: cloud-init creates the agent user with a gid this
		// image cannot predict, so a narrower one would work by luck.
		if !strings.Contains(recipe, "net.ipv4.ping_group_range = 0 2147483647") {
			t.Errorf("%s does not widen net.ipv4.ping_group_range; a non-root user in guests built from it cannot ping, because the cap_net_raw xattr on ping does not survive the export-and-virt-make-fs pipeline", d.Containerfile)
		}

		// Anything sysctl.d reads would do, but only a file under /etc wins
		// over a vendor default that sets the same key.
		if !strings.Contains(recipe, "/etc/sysctl.d/") {
			t.Errorf("%s sets ping_group_range somewhere other than /etc/sysctl.d, where a vendor default could override it", d.Containerfile)
		}
	}
}

// TestContainerfiles_InstallCrossArchitectureBinfmt guards the guest's ability
// to run `docker build --platform linux/arm64` without any setup inside the VM.
//
// The registration cannot be done at runtime with `docker run --privileged
// tonistiigi/binfmt`: that needs a registry round trip on a VM that may have no
// route out, and the rules it installs live only until the next reboot. Every
// family therefore installs its own qemu-user-static packages at build time,
// and their /usr/lib/binfmt.d rules are re-registered by systemd-binfmt.service
// on every boot.
func TestContainerfiles_InstallCrossArchitectureBinfmt(t *testing.T) {
	// The package that carries the interpreters, per family. Fedora splits
	// them per target architecture; Ubuntu and Arch ship one package for
	// every target, and Arch keeps the binfmt_misc rules in a second one.
	packages := map[string][]string{
		distro.Ubuntu.Containerfile: {"qemu-user-static"},
		distro.Fedora.Containerfile: {"qemu-user-static-aarch64", "qemu-user-static-x86"},
		distro.Arch.Containerfile:   {"qemu-user-static", "qemu-user-static-binfmt"},
	}

	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}

		contents, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		recipe := string(contents)

		wanted, known := packages[d.Containerfile]
		if !known {
			t.Fatalf("%s has no expected package list here; a new family must state the qemu-user-static packages it installs", d.Containerfile)
		}
		for _, pkg := range wanted {
			if !strings.Contains(recipe, pkg) {
				t.Errorf("%s does not install %s; `docker build --platform linux/arm64` in a guest built from it would fail with exec format error", d.Containerfile, pkg)
			}
		}

		// Installing the packages is not enough on its own. The rule has to
		// carry the F (fix-binary) flag, or the interpreter is not reachable
		// from inside a build container, and systemd-binfmt has to be wanted by
		// sysinit.target, or nothing registers the rules at boot. Both are
		// properties of the distro's packaging rather than of anything written
		// here, so each Containerfile asserts them at build time instead of
		// shipping an image whose cross-builds are quietly broken.
		if !strings.Contains(recipe, `grep -q ':[A-Za-z]*F[A-Za-z]*$'`) {
			t.Errorf("%s does not check that the installed binfmt_misc rule carries the F (fix-binary) flag", d.Containerfile)
		}
		if !strings.Contains(recipe, "sysinit.target.wants/systemd-binfmt.service") {
			t.Errorf("%s does not check that systemd-binfmt.service is wanted by sysinit.target; the rules would never be registered at boot", d.Containerfile)
		}
	}
}
