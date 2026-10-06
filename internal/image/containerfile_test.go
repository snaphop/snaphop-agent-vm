package image

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
	"github.com/snaphop/snaphop-agent-vm/templates"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestTmuxMenu_GeneratesNamesWithoutAWordList pins the fallback rather than the
// word list: a generated name is what Enter at the menu gets you, so the menu
// has to produce one on an image where /usr/share/dict/words is missing or
// holds nothing matching the filter.
func TestTmuxMenu_GeneratesNamesWithoutAWordList(t *testing.T) {
	t.Parallel()
	menu := readTemplate(t, "distro/tmux-menu.sh")

	if !strings.Contains(menu, "/proc/sys/kernel/random/uuid") {
		t.Error("the tmux menu has no UUID fallback for a generated session name; an image with no word list would name every session the same")
	}
	if !strings.Contains(menu, `printf 'session-%s' "$(date +%H%M%S)"`) {
		t.Error("the tmux menu has no last-resort generated name, so a guest with neither a word list nor /proc would leave the name empty")
	}
}

// TestContainerfiles_InstallAWordList backs the menu's preferred path. The
// package name differs per family; without it every generated session name
// falls back to a hex suffix, which is what the two-word names replaced.
func TestContainerfiles_InstallAWordList(t *testing.T) {
	t.Parallel()
	wordList := map[string]string{
		distro.Ubuntu.Containerfile: "wamerican",
		distro.Fedora.Containerfile: "words",
		distro.Arch.Containerfile:   "words",
	}

	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		pkg, ok := wordList[d.Containerfile]
		if !ok {
			t.Fatalf("%s has no word-list package here; a new family must state the one it installs", d.Containerfile)
		}
		if !strings.Contains(readTemplate(t, "distro/"+d.Containerfile), "\n      "+pkg+" \\\n") {
			t.Errorf("%s does not install %q, so /usr/share/dict/words is absent and every generated tmux session name falls back to a hex suffix", d.Containerfile, pkg)
		}
	}
}

// TestTailscaleJoinScript_IsNotPartOfTheBaseImage keeps the join script out of
// the image build. It is sent to one guest over SSH. A base image is shared,
// and the auth key never is.
func TestTailscaleJoinScript_IsNotPartOfTheBaseImage(t *testing.T) {
	t.Parallel()
	if slices.Contains(buildContextFiles, "tailscale-join.sh") {
		t.Fatal("tailscale-join.sh is in the base image build context")
	}
}

// TestTmuxConfig_IsShippedInTheBuildContext ties the COPY above to the file the
// builder actually writes next to the Containerfile. A COPY of a file that is
// not in the build context fails the build minutes in, after the package
// installation has already been paid for.
func TestTmuxConfig_IsShippedInTheBuildContext(t *testing.T) {
	t.Parallel()
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

// miseAgents are the coding agents installed with mise's registry, by the name
// each recipe asks for. codex is absent because it has no mise package: it
// comes from OpenAI's installer, which is the only thing that produces the
// standalone package `codex remote-control` requires. grok is absent here
// because it is an npm package, installed with the other npm tools.
var miseAgents = []string{"claude", "opencode", "pi", "agy"}

// miseShims are every command installed with mise that a guest has to be able
// to run without a login shell, in the order the recipes link them.
var miseShims = []string{
	"node", "npm", "npx",
	"go", "gofmt", "golangci-lint",
	"claude", "opencode", "pi", "agy", "herdr", "wrangler", "playwright", "cf", "grok",
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
// comes up with claude, codex, opencode, pi, agy and grok already installed.
//
// None of the five is packaged by any distro, so nothing else in the image
// would pull them in by accident: if a recipe stops naming one, guests simply
// stop having it, and that only shows up when someone SSHes in.
func TestContainerfiles_InstallTheCodingAgents(t *testing.T) {
	t.Parallel()
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

		// The vendor installer writes a root-owned binary into /usr/local/bin.
		// That shadows the mise shim and is a file a non-root account cannot
		// replace, which is why agent-vm update used to leave agy alone.
		if strings.Contains(recipe, "antigravity.google/cli/install.sh") {
			t.Errorf("%s still installs agy from the vendor script; it belongs to mise with the other registry agents", d.Containerfile)
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
	t.Parallel()
	configs := []struct{ file, dest string }{
		{"claude-settings.json", "/etc/skel/.claude/settings.json"},
		{"codex-config.toml", "/etc/skel/.codex/config.toml"},
		{"opencode.json", "/etc/skel/.config/opencode/opencode.json"},
		{"grok-config.toml", "/etc/skel/.grok/config.toml"},
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
	t.Parallel()
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

	// permission_mode is only read from the user config, not from a project
	// file, which is why this copy lives in the account's home. auto_update
	// has to stay off: grok's updater would replace the binary mise owns.
	grok := readTemplate(t, "distro/grok-config.toml")
	for _, setting := range []string{`permission_mode = "always-approve"`, "auto_update = false"} {
		if !strings.Contains(grok, setting) {
			t.Errorf("grok-config.toml does not set %s; grok will ask before every tool call, or replace the mise-managed binary on its own", setting)
		}
	}
}

// TestAgentConfigs_CarryNoCredentials is the SECURITY.md guard on this whole
// change. A base image is shared by every VM built on it and cached
// indefinitely, so an API key that reached one of these files would be handed
// to every guest and every operator who copied the cache. Credentials belong
// in per-VM cloud-init, never here.
func TestAgentConfigs_CarryNoCredentials(t *testing.T) {
	t.Parallel()
	secretish := []string{"api_key", "apikey", "api-key", "token", "secret", "password", "sk-", "bearer"}

	for _, name := range buildContextFiles {
		contents := strings.ToLower(readTemplate(t, "distro/"+name))
		for _, needle := range secretish {
			// github-runner-configure.sh is the operator's registration
			// command, so its flag is the word "token". It still has to pass
			// every other needle here, and
			// TestGitHubRunnerConfigure_CarriesNoCredentialMaterial rejects
			// token-shaped values in that file.
			if name == "github-runner-configure.sh" && needle == "token" {
				continue
			}
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
	t.Parallel()
	for _, name := range []string{"claude-settings.json", "codex-config.toml", "opencode.json", "grok-config.toml", "agent-aliases.sh", "codex-remote-control.sh"} {
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
	t.Parallel()
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
// First boot then writes into that slack -- cloud-init's own logs, the account
// it creates, and everything an agent does afterwards -- runs out of space, and
// takes cloud-final and sshd down with it: a VM that boots and never accepts
// SSH. Ubuntu's cloud-init pulls growpart in as a dependency; Fedora's and
// Arch's do not.
func TestContainerfiles_CanGrowTheRootFilesystem(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, `for agent in claude codex opencode pi agy grok; do`) {
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

		// Playwright, wrangler, cf and grok are published only to npm, so they
		// are the one place mise's npm backend is used.
		if !strings.Contains(recipe, "mise use --global --yes npm:wrangler npm:playwright npm:cf npm:@xai-official/grok") {
			t.Errorf("%s does not install wrangler, Playwright, cf and grok with mise", d.Containerfile)
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
		// The toolchain is installed into one store every account shares: a JDK
		// downloaded per account at first use would be a download a
		// network-isolated guest cannot make, and a copy per account cost every
		// VM about nine seconds of first boot and 1.7 GiB of overlay writes in
		// useradd.
		if !strings.Contains(recipe, "MISE_DATA_DIR=/usr/local/lib/mise") {
			t.Errorf("%s does not install the mise toolchain into the shared store; a per-account copy costs every first boot about nine seconds and 1.7 GiB of overlay writes", d.Containerfile)
		}
		// java@temurin, not java@latest: mise names a distribution by prefix,
		// and an unprefixed version gets an Oracle build of OpenJDK instead of
		// the Temurin one docs/cli.md promises.
		if !strings.Contains(recipe, "mise use --global --yes java@temurin maven@latest") {
			t.Errorf("%s does not install a Temurin JDK and Maven with mise; a guest would come up with `mise` and no Java, or with the wrong JDK", d.Containerfile)
		}
		// Every account reaches the store through a symlink at the path mise
		// looks in by default. /etc/skel is what useradd copies into a new
		// home, and useradd copies a symlink as a symlink; root needs its own
		// because it is created before /etc/skel holds anything and never
		// consults skel.
		for _, link := range []string{
			"ln -sfn /usr/local/lib/mise /etc/skel/.local/share/mise",
			"ln -sfn /usr/local/lib/mise /root/.local/share/mise",
		} {
			if !strings.Contains(recipe, link) {
				t.Errorf("%s does not run %q, so that account would have no toolchain at all", d.Containerfile, link)
			}
		}
		// Sharing a store is only usable if every account may install into it.
		// The sticky bit is what keeps one account from removing another's
		// tool, the way /tmp and /opt/ms-playwright work.
		if !strings.Contains(recipe, "find /usr/local/lib/mise -type d -exec chmod 1777 {} +") {
			t.Errorf("%s does not make the shared mise store writable; `mise use -g` would fail for every account but root", d.Containerfile)
		}
		// The store must not be copied per account by any route: that is the
		// cost this design exists to remove.
		if strings.Contains(recipe, "cp -a /etc/skel/.local/share/mise") {
			t.Errorf("%s still copies the mise store per account, which is the nine seconds of first boot the shared store removes", d.Containerfile)
		}
		// The shims are executables, not a shell function, but they still have
		// to be put on PATH for a login shell to find java and mvn.
		if !strings.Contains(recipe, "COPY mise.sh /etc/profile.d/agent-vm-mise.sh") {
			t.Errorf("%s does not install the mise shell init; the shims would be on no account's PATH", d.Containerfile)
		}
	}
}

// TestContainerfiles_DoNotHoldBackMiseReleases covers mise's default of
// skipping a release for 24 hours after it is published. The image sets
// MISE_MINIMUM_RELEASE_AGE=0s so a build and a later mise use in the guest
// take the newest release. A bare 0 is not a duration: mise 2026.10.2 rejects
// it while choosing a self-update, and the bootstrap installer rejects it too.
func TestContainerfiles_DoNotHoldBackMiseReleases(t *testing.T) {
	t.Parallel()
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		for _, file := range []string{d.Containerfile, d.NixContainerfile} {
			contents, err := templates.FS.ReadFile("distro/" + file)
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			recipe := string(contents)
			if !strings.Contains(recipe, "printf 'MISE_MINIMUM_RELEASE_AGE=0s\\n' >> /etc/environment") {
				t.Errorf("%s does not set MISE_MINIMUM_RELEASE_AGE=0s in /etc/environment; a later mise command in the guest skips releases from the last 24 hours, and a bare 0 makes mise self-update refuse to start", file)
			}
			if strings.Contains(recipe, "MISE_MINIMUM_RELEASE_AGE=0\\n") ||
				strings.Contains(recipe, "MISE_MINIMUM_RELEASE_AGE=0 ") ||
				strings.Contains(recipe, "MISE_MINIMUM_RELEASE_AGE=0;") {
				t.Errorf("%s still sets MISE_MINIMUM_RELEASE_AGE=0; mise 2026.10.2 rejects that duration", file)
			}
			if !strings.Contains(recipe, "MISE_INSTALL_PATH=/usr/local/bin/mise MISE_MINIMUM_RELEASE_AGE=0s") {
				t.Errorf("%s does not set MISE_MINIMUM_RELEASE_AGE=0s on the mise installer; that script rejects a bare 0 and otherwise installs a binary at least 24 hours old", file)
			}
			// Comments mention `mise use` while explaining the contract, so only
			// a command line counts. The age has to be on that same RUN: a
			// build step does not read /etc/environment.
			var run strings.Builder
			uses := 0
			for _, line := range strings.Split(recipe, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "RUN") {
					run.Reset()
				}
				if strings.HasPrefix(trimmed, "#") {
					continue
				}
				run.WriteString(line)
				run.WriteByte('\n')
				if !strings.Contains(trimmed, "mise use") {
					continue
				}
				uses++
				body := run.String()
				if !strings.Contains(body, "MISE_MINIMUM_RELEASE_AGE=0s ") &&
					!strings.Contains(body, "MISE_MINIMUM_RELEASE_AGE=0s;") &&
					!strings.Contains(body, "MISE_MINIMUM_RELEASE_AGE=0s\n") {
					t.Errorf("%s runs mise use without MISE_MINIMUM_RELEASE_AGE=0s, so the build installs a release at least 24 hours old:\n%s", file, body)
				}
			}
			if uses == 0 {
				t.Errorf("%s installs nothing with mise", file)
			}
		}
	}
}

// TestUserSetupScript_DoesEveryJobItIsThereFor guards the first-boot script
// that finishes every interactive account.
//
// None of these jobs can be done at build time — the accounts do not exist yet
// — and none can be done from the seed: cloud-init only knows about the login
// user agent-vm asks for, and a private key must never be written into a seed
// (SECURITY.md).
func TestUserSetupScript_DoesEveryJobItIsThereFor(t *testing.T) {
	t.Parallel()
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

	// The mise store is shared, and an account reaches it through a symlink at
	// the path mise looks in by default. An account created from /etc/skel
	// already has that link; this is the backstop for one that was not, such
	// as an account the distro baked into its own image.
	if !strings.Contains(script, `ln -sfn "${mise_store}" "${home}/.local/share/mise"`) {
		t.Error("the first-boot script does not link an account with no skel copy to the shared mise store; that account would have no toolchain at all")
	}
	// A VM created from a base image that predates the shared store has a real
	// directory there, holding whatever that account installed. Replacing it
	// with a link would throw that away.
	if !strings.Contains(script, `[ ! -e "${home}/.local/share/mise" ]`) {
		t.Error("the first-boot script does not leave an existing mise directory alone; on an older base image it would discard the account's own toolchain")
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
	t.Parallel()
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

// TestHerdr_RunsAsADaemonForEveryAccount guards the terminal workspace server
// a guest is supposed to come up with.
//
// Herdr is what keeps an agent's panes alive across disconnections, so it is
// installed from mise's registry like the agents and started at every boot --
// the accounts do not exist when the image is built, and a server dies with
// the VM. Each account's server is an instance of a template unit so systemd
// supervises it rather than this project's shell script.
func TestHerdr_RunsAsADaemonForEveryAccount(t *testing.T) {
	t.Parallel()
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, "mise use --global --yes herdr") {
			t.Errorf("%s does not install herdr with mise; a guest built from it has no terminal workspace server to attach to", d.Containerfile)
		}
		if !strings.Contains(recipe, "COPY herdr-server.sh /usr/local/sbin/agent-vm-herdr-server") {
			t.Errorf("%s does not install the herdr boot script", d.Containerfile)
		}
		// The template unit is what systemd supervises, one instance per
		// account. User=%i is what gives each account its own HOME, and so
		// its own configuration and socket under $HOME/.config/herdr.
		for _, line := range []string{
			"'User=%i' \\",
			// A five-second RestartSec fits only two starts into the
			// default ten-second window, so a server that cannot run at
			// all would be restarted forever without tripping the limit.
			"'StartLimitIntervalSec=60' \\",
			"'ExecStart=/usr/local/bin/herdr server' \\",
			"> /usr/lib/systemd/system/agent-vm-herdr@.service",
		} {
			if !strings.Contains(recipe, line) {
				t.Errorf("%s does not write %s into the herdr template unit; each account's server would not be its own", d.Containerfile, line)
			}
		}
		if !strings.Contains(recipe, "enable agent-vm-herdr.service") {
			t.Errorf("%s does not enable agent-vm-herdr.service; no server would ever start on a VM", d.Containerfile)
		}
		// Wanted by cloud-final.service, not multi-user.target, for the same
		// reason as the units above it: a unit ordered after cloud-final and
		// wanted by that target forms a cycle systemd breaks by dropping our
		// job, leaving the unit enabled, inactive, and silent.
		if !strings.Contains(recipe, "'WantedBy=cloud-final.service' \\\n      > /usr/lib/systemd/system/agent-vm-herdr.service") {
			t.Errorf("%s does not attach the herdr unit to cloud-final.service; ordering it after cloud-final under multi-user.target forms a cycle and the job is silently dropped", d.Containerfile)
		}
	}

	script := readTemplate(t, "distro/herdr-server.sh")

	// One instance per account, started through systemd: a server spawned by
	// the script itself would be a child of a oneshot unit with no journal of
	// its own and nothing to restart it.
	if !strings.Contains(script, `systemctl start "agent-vm-herdr@${name}.service"`) {
		t.Error("the boot script does not start a template-unit instance per account; the servers would not be supervised")
	}
	// root has a home in the image and may well be the account an agent runs
	// as, so it gets a server like every interactive account does.
	if !strings.Contains(script, "start_for_account root /root") {
		t.Error("the boot script does not start a server for root")
	}
	// The image installs herdr into the shared mise store, which every account
	// reaches through the ~/.local/share/mise symlink -- but an account that
	// has neither that link nor a store has no herdr, and /usr/local/bin/herdr
	// is a mise shim that exits at once for such an account.
	if !strings.Contains(script, `[ ! -x "${home}/.local/share/mise/shims/herdr" ]`) {
		t.Error("the boot script starts a server for an account with no mise-installed herdr; the shim exits at once and the unit is restarted")
	}
	// The uid range does not separate people from system accounts on its own:
	// Ubuntu's libvirt-qemu is uid 64055, and an instance started for it
	// crash-loops for the life of the VM because Restart= brings it back.
	if !strings.Contains(script, "*/nologin | */false") {
		t.Error("the boot script starts a server for accounts with no login shell; Ubuntu's libvirt-qemu (uid 64055) would get one and restart forever")
	}
	// An account whose server will not start is a reason to look in the
	// journal, not to mark the boot degraded.
	if strings.Contains(script, "exit 1") {
		t.Error("the boot script can exit non-zero; one account's failed server would boot the whole VM degraded")
	}
}

// TestContainerfiles_InstallTheVirtualizationStack guards the tooling that
// lets a guest run VMs of its own.
//
// It is the same set of host tools agent-vm itself drives, so a guest that has
// them can run agent-vm — and the guest half of nested virtualization is
// worthless without them. Names differ per family; the commands do not.
func TestContainerfiles_InstallTheVirtualizationStack(t *testing.T) {
	t.Parallel()
	packages := map[string][]string{
		// qemu-kvm is a virtual package on Ubuntu 26.04, provided by both the
		// release's emulator and the hardware-enablement build. apt will not
		// choose. The recipe names the concrete package for the architecture
		// being built; each one provides qemu-kvm.
		distro.Ubuntu.Containerfile: {
			"qemu-system-x86", "qemu-system-arm", "libvirt-daemon-system", "libvirt-clients", "virtinst",
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

	// ubuntu-nix keeps libvirt on apt and copies this install.
	nix := readTemplate(t, "distro/ubuntu-nix.Containerfile")
	for _, pkg := range packages[distro.Ubuntu.Containerfile] {
		if !strings.Contains(nix, pkg) {
			t.Errorf("ubuntu-nix.Containerfile does not install %q, so a nix guest could not run a VM of its own", pkg)
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
	t.Parallel()
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
		// Root fills the directory during the build, and every install
		// afterwards is run by an account that is not root: Playwright's
		// __dirlock and .links are writes into it, so a root-owned 0755
		// directory turns `playwright install` into EACCES for everyone.
		if !strings.Contains(recipe, "install -d -m 1777 /opt/ms-playwright /opt/ms-playwright/.links") {
			t.Errorf("%s leaves the shared browser directory writable only by root; `playwright install` fails with EACCES on __dirlock for every other account", d.Containerfile)
		}
	}
}

// TestContainerfiles_SmokeTestTheDevTooling is the guard on the failure this
// tooling is most prone to. A browser missing one shared library installs
// perfectly and dies the instant it is launched, so only launching it during
// the build says anything about whether a guest can drive a page.
func TestContainerfiles_SmokeTestTheDevTooling(t *testing.T) {
	t.Parallel()
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
		if !strings.Contains(recipe, "the mise shims are missing from the shared store") {
			t.Errorf("%s does not check that the mise shims landed in the shared store, which is where every account gets them from", d.Containerfile)
		}
		// A build that regressed to a per-account copy would still pass every
		// check above, and would only show up as a slow first boot.
		if !strings.Contains(recipe, "[ -L /etc/skel/.local/share/mise ]") {
			t.Errorf("%s does not check that /etc/skel points at the shared store; a copy there would silently cost every VM nine seconds of first boot", d.Containerfile)
		}
		// The shims existing in the store says nothing about whether the
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
	t.Parallel()
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
	t.Parallel()
	// The package that carries the interpreters, per family. Fedora splits
	// them per target architecture. Arch ships one package for every target
	// and keeps the binfmt_misc rules in a second one. Ubuntu 26.04 publishes
	// qemu-user-static as a virtual package with two providers, so apt will
	// not install that name; qemu-user-binfmt is the concrete package and its
	// rules carry the F flag. On 24.04 and earlier, qemu-user-binfmt's rules
	// omit the F flag and qemu-user-static is the package that has it. The
	// recipe selects on the release's major version, and both names have to
	// stay in the file.
	packages := map[string][]string{
		distro.Ubuntu.Containerfile: {"qemu-user-binfmt", "qemu-user-static", `[ "$major" -ge 26 ]`},
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

	// ubuntu-nix keeps Docker on apt and copies this install, rather than
	// sharing the full recipe's text. The same release split has to be there,
	// or an Ubuntu 26.04 nix build fails in apt and a 24.04 one registers
	// rules that docker build --platform cannot use.
	nixContents, err := templates.FS.ReadFile("distro/ubuntu-nix.Containerfile")
	if err != nil {
		t.Fatalf("reading ubuntu-nix.Containerfile: %v", err)
	}
	nix := string(nixContents)
	for _, needle := range packages[distro.Ubuntu.Containerfile] {
		if !strings.Contains(nix, needle) {
			t.Errorf("ubuntu-nix.Containerfile does not contain %q; it would not install a user-mode QEMU whose binfmt rules carry the F flag on both Ubuntu 26.04 and 24.04", needle)
		}
	}
}

// TestContainerfiles_LeaveMisesLockDirectoryWritableByEveryAccount guards the
// one thing that makes the npm packages above installable by anyone but root.
//
// mise's npm backend takes a lock under /tmp/fslock while it installs, and that
// directory belongs to whichever account created it, at mode 0755. Every mise
// install in these recipes runs as root, so a committed copy would leave the
// agent account unable to install any npm-backed tool -- and would break the
// unelevated half of `agent-vm update` -- with "failed to acquire project
// lock: Permission denied". The build removes its own copy, and a tmpfiles.d
// rule recreates the directory at boot with /tmp's own permissions.
func TestContainerfiles_LeaveMisesLockDirectoryWritableByEveryAccount(t *testing.T) {
	t.Parallel()
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

		if !strings.Contains(recipe, "rm -rf /tmp/mise-cache /tmp/fslock ") {
			t.Errorf("%s commits root's /tmp/fslock to the image; the agent account cannot install an npm-backed tool in a guest built from it", d.Containerfile)
		}
		if !strings.Contains(recipe, "d /tmp/fslock 1777 root root -") {
			t.Errorf("%s installs no tmpfiles.d rule for /tmp/fslock; whichever account installs first locks the others out", d.Containerfile)
		}
	}
}

// TestContainerfiles_SynchronizeTheGuestClock guards the fix for guests that
// came up weeks in the past.
//
// On aarch64 the guest has no real-time clock it can read: QEMU's virt machine
// provides a PL031, but the kernel flavours these images ship do not carry the
// driver — Ubuntu keeps rtc-pl031 in linux-modules-extra, which
// linux-image-virtual does not pull in — so /dev/rtc0 never appears and nothing
// sets the clock from hardware. systemd then falls back to its own build date,
// and the guest starts weeks behind its host with nothing to correct it: apt
// rejects repository metadata as "not valid yet", TLS handshakes fail against
// certificates that have not started yet, and build tools write timestamps from
// the wrong month. An NTP client is the only thing standing between a guest and
// that, so it has to be installed *and* enabled in every family.
//
// It has to be chrony specifically. systemd-timesyncd was observed wedging on
// this exact path — started before resolved could answer, it never acquired a
// server and never retried — and Fedora ships its unit disabled.
func TestContainerfiles_SynchronizeTheGuestClock(t *testing.T) {
	t.Parallel()
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

		if !strings.Contains(recipe, "chrony") {
			t.Errorf("%s does not install chrony; guests built from it have no way to learn the time", d.Containerfile)
		}

		// Installed and not enabled is the same as absent. The families spell
		// the daemon differently, so the recipe enables whichever unit it
		// finds; what must be true everywhere is that it looks for both and
		// for the wait unit that makes time-sync.target mean something.
		for _, unit := range []string{"chrony.service", "chronyd.service", "chrony-wait.service"} {
			if !strings.Contains(recipe, unit) {
				t.Errorf("%s never enables %s; guests built from it boot with systemd's build date as the time and keep it", d.Containerfile, unit)
			}
		}

		// sshd behind time-sync.target is what makes create's readiness wait —
		// which is "does SSH answer" and nothing else — also mean "is the clock
		// right". Without it a VM is handed over some five seconds into its
		// boot, two seconds before chrony steps the clock, and whatever the
		// agent runs first meets a clock weeks out.
		if !strings.Contains(recipe, "After=time-sync.target") {
			t.Errorf("%s does not order sshd after time-sync.target; create will hand over VMs whose clock has not been stepped yet", d.Containerfile)
		}

		// The ordering must not be able to outlast create's own budget: chrony-wait
		// ships TimeoutStartSec=180 and retries forever, so a guest that cannot
		// reach an NTP server would hold sshd past the point create gives up,
		// turning a wrong clock into a failed create.
		if !strings.Contains(recipe, "TimeoutStartSec=30") {
			t.Errorf("%s leaves chrony-wait's 180s timeout in place while ordering sshd behind it; a guest with no route to an NTP server would not become reachable before create stops waiting", d.Containerfile)
		}

		// That 30s bound only means anything if chrony can synchronize inside
		// it. chronyd resolves its pool once at startup, and started before a
		// name can be resolved it backs off and selects a source around 33
		// seconds in — past the bound, which then releases sshd on a timeout
		// instead of on a correct clock. Ordering it behind network-online.target
		// moved that to 4.9s on Fedora and Arch.
		if !strings.Contains(recipe, "After=network-online.target") {
			t.Errorf("%s does not order chronyd after network-online.target; its first pool lookup fails, the retry backs off past chrony-wait's 30s bound, and sshd is released on a timeout rather than a synchronized clock", d.Containerfile)
		}

		// One clock, one client. Arch enables systemd-timesyncd by default, and
		// running it alongside chrony made chronyd report "System clock
		// interference detected (another NTP client?)".
		if !strings.Contains(recipe, "mask systemd-timesyncd.service") {
			t.Errorf("%s does not mask systemd-timesyncd; on a family that enables it by default two NTP clients step the same clock", d.Containerfile)
		}
	}
}

// slimRecipes returns each family's full and slim recipe, so a test can state
// what the two must share and what only the full one may carry.
func slimRecipes(t *testing.T) map[string][2]string {
	t.Helper()
	return variantRecipes(t, distro.Slim)
}

func nixRecipes(t *testing.T) map[string][2]string {
	t.Helper()
	return variantRecipes(t, distro.Nix)
}

// variantRecipes pairs every family's full recipe with the named variant's,
// keyed by the variant recipe's filename. Both halves are returned because the
// contract tests below check the same needle in each: a needle the full recipe
// stopped carrying is a stale test, not a passing one.
func variantRecipes(t *testing.T, variant distro.Variant) map[string][2]string {
	t.Helper()

	recipes := map[string][2]string{}
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		ref := distro.Ref{Distro: d, Tag: d.DefaultTag, Variant: variant}
		if ref.Containerfile() == "" {
			t.Fatalf("%s has no %q recipe; every family has every variant", d.Name, variant)
		}
		full, err := templates.FS.ReadFile("distro/" + d.Containerfile)
		if err != nil {
			t.Fatalf("reading %s: %v", d.Containerfile, err)
		}
		other, err := templates.FS.ReadFile("distro/" + ref.Containerfile())
		if err != nil {
			t.Fatalf("reading %s: %v", ref.Containerfile(), err)
		}
		recipes[ref.Containerfile()] = [2]string{string(full), string(other)}
	}
	return recipes
}

// bootAndCloudInitContract is everything docs/cli.md calls the guest contract —
// the guest boots, gets an address, resolves names, learns the time, reads the
// NoCloud seed and no other datasource, and answers SSH. It comes from a
// handful of blocks the full recipe carries, and every variant must keep all of
// them, so the list is shared by the variant tests below.
var bootAndCloudInitContract = []struct{ needle, why string }{
	{"L+! /etc/resolv.conf", "guests built from it would have no DNS"},
	{"/etc/tmpfiles.d/systemd-resolve.conf", "systemd's own `L` rule would win and resolv.conf would stay empty"},
	{"systemd-resolved.service", "nothing would answer the stub resolver"},
	{"systemd-networkd.service", "the guest would never get an address"},
	{"datasource_list: [ NoCloud, None ]", "the guest could probe a metadata service on the network"},
	{"cloud-init.target", "the guest would never receive its SSH key"},
	{"chrony-wait.service", "a guest could be handed over with a clock weeks out"},
	{"After=time-sync.target", "SSH would answer before the clock was correct"},
	{"qemu-guest-agent.service", "the host could not query the guest"},
	{"10-agent-vm-start-without-virt-detection.conf", "Ubuntu 26.04 would skip the guest agent on a guest presented as hardware"},
	{"/dev/vda1 / ext4 defaults 0 1", "systemd's fstab generator would disagree with the kernel about the root filesystem"},
	{"net.ipv4.ping_group_range", "an unprivileged account could not run ping"},
}

// TestSlimContainerfiles_KeepTheBootAndCloudInitContract is what makes a slim
// image a base image rather than a container that happens to have a kernel.
//
// Everything docs/cli.md calls the guest contract — the guest boots, gets an
// address, resolves names, learns the time, reads the NoCloud seed and no
// other datasource, and answers SSH — comes from a handful of blocks that the
// full recipe carries. The slim recipe drops the agent tooling around them and
// must keep every one of these, so each is required in both files: a change
// that removes one from the full recipe fails here too, rather than leaving
// this list quietly checking something nothing produces any more.
func TestSlimContainerfiles_KeepTheBootAndCloudInitContract(t *testing.T) {
	t.Parallel()
	for slimName, pair := range slimRecipes(t) {
		full, slim := pair[0], pair[1]
		for _, want := range bootAndCloudInitContract {
			if !strings.Contains(full, want.needle) {
				t.Errorf("the full recipe no longer contains %q; this list describes what a slim image must keep from it, so update both", want.needle)
			}
			if !strings.Contains(slim, want.needle) {
				t.Errorf("%s does not contain %q: %s", slimName, want.needle, want.why)
			}
		}

		// The kernel and the init system are the two things a container image
		// lacks and a VM cannot boot without, whatever else is left out.
		for _, unit := range []string{"cloud-init", "openssh", "sudo", "chrony"} {
			if !strings.Contains(slim, unit) {
				t.Errorf("%s does not install %s; a guest built from it could not be reached or configured", slimName, unit)
			}
		}
	}
}

// TestSlimContainerfiles_LeaveOutTheAgentTooling is the point of the variant:
// everything the full recipe installs for a coding agent is most of the build
// time and most of the image, and a slim guest pays for none of it.
func TestSlimContainerfiles_LeaveOutTheAgentTooling(t *testing.T) {
	t.Parallel()
	// Named as they appear in an instruction, lower-cased for comparison.
	excluded := []string{"mise", "rustup", "cargo", "golangci", "playwright", "chromium", "docker", "libvirt", "codex", "herdr", "npm"}

	for slimName, pair := range slimRecipes(t) {
		full, slim := pair[0], pair[1]
		for _, tool := range excluded {
			if !strings.Contains(strings.ToLower(full), tool) {
				t.Errorf("the full recipe no longer mentions %q; this list describes what slim leaves out, so update both", tool)
			}
		}
		for _, line := range strings.Split(slim, "\n") {
			// Comments are where the slim recipe explains what it leaves out.
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, tool := range excluded {
				if strings.Contains(strings.ToLower(line), tool) {
					t.Errorf("%s carries agent tooling (%s): %s", slimName, tool, strings.TrimSpace(line))
				}
			}
		}

		// The build context files are the agent's configuration and helper
		// scripts; a slim image copies none of them in.
		if strings.Contains(slim, "COPY ") {
			t.Errorf("%s COPYs a file from the build context; a slim image ships no agent configuration", slimName)
		}
	}
}

// TestSlimContainerfiles_InstallTheCommonTooling keeps the promise the name
// makes: a slim guest is a working Linux machine, not a stripped one.
func TestSlimContainerfiles_InstallTheCommonTooling(t *testing.T) {
	t.Parallel()
	packages := map[string][]string{
		distro.Ubuntu.SlimContainerfile: {"iputils-ping", "curl", "wget", "git", "build-essential", "python3", "jq", "vim", "tmux", "rsync"},
		distro.Fedora.SlimContainerfile: {"iputils", "curl", "wget", "git", "gcc", "make", "python3", "jq", "vim", "tmux", "rsync"},
		distro.Arch.SlimContainerfile:   {"iputils", "curl", "wget", "git", "base-devel", "python", "jq", "vim", "tmux", "rsync"},
	}

	for slimName, pair := range slimRecipes(t) {
		wanted, ok := packages[slimName]
		if !ok {
			t.Fatalf("%s has no expected package list here; a new family's slim recipe must state the tooling it installs", slimName)
		}
		for _, pkg := range wanted {
			if !strings.Contains(pair[1], pkg) {
				t.Errorf("%s does not install %q; docs/cli.md promises a slim guest the same common tooling", slimName, pkg)
			}
		}
	}
}

// TestNixContainerfiles_KeepTheBootAndCloudInitContract is what makes a nix
// image a base image rather than a container that happens to have a kernel.
//
// The nix variant changes where the guest *tooling* comes from and nothing
// else, so every promise a VM built on a full image makes has to hold here
// too. The list is shared with the slim test on purpose: a block removed from
// the full recipe fails in both places rather than leaving either quietly
// checking something nothing produces any more.
func TestNixContainerfiles_KeepTheBootAndCloudInitContract(t *testing.T) {
	t.Parallel()
	for nixName, pair := range nixRecipes(t) {
		full, nix := pair[0], pair[1]
		for _, want := range bootAndCloudInitContract {
			if !strings.Contains(full, want.needle) {
				t.Errorf("the full recipe no longer contains %q; this list describes what every variant must keep from it, so update both", want.needle)
			}
			if !strings.Contains(nix, want.needle) {
				t.Errorf("%s does not contain %q: %s", nixName, want.needle, want.why)
			}
		}

		// The kernel and the init system are the two things a container image
		// lacks and a VM cannot boot without, whatever else is left out.
		for _, unit := range []string{"cloud-init", "openssh", "sudo", "chrony"} {
			if !strings.Contains(nix, unit) {
				t.Errorf("%s does not install %s; a guest built from it could not be reached or configured", nixName, unit)
			}
		}
	}
}

// TestNixContainerfiles_InstallTheToolingFromNix is the point of the variant:
// the tool set comes from the one shared expression, and the guest ends up with
// a working multi-user nix rather than a root-owned store nobody else can add
// to.
func TestNixContainerfiles_InstallTheToolingFromNix(t *testing.T) {
	t.Parallel()
	required := []struct{ needle, why string }{
		{"https://nixos.org/nix/install", "there would be no nix in the image to install anything with"},
		{"--no-daemon", "a multi-user install cannot complete in a build with no running systemd"},
		{"COPY agent-tools.nix /etc/agent-vm/agent-tools.nix", "the expression that defines the tool set would not be in the image"},
		{"nix-build --no-out-link /etc/agent-vm/agent-tools.nix -A env", "nothing would build the tool set"},
		{"nix-env --profile /nix/var/nix/profiles/default --set", "the tools would not be in a profile every account shares"},
		{"build-users-group = nixbld", "nix-daemon would have no unprivileged accounts to build under"},
		{"nix-daemon.socket", "an unprivileged account in the guest could not install anything"},
		{"COPY nix.sh /etc/profile.d/agent-vm-nix.sh", "a login shell would not find the tools"},
		{"PATH=/nix/var/nix/profiles/default/bin:", "`ssh <vm> go build`, which reads no profile, would not find the tools"},
		{"-A browsers", "Playwright would have no browser and `chromium` would not run"},
	}

	for nixName, pair := range nixRecipes(t) {
		nix := pair[1]
		for _, want := range required {
			if !strings.Contains(nix, want.needle) {
				t.Errorf("%s does not contain %q: %s", nixName, want.needle, want.why)
			}
		}
	}
}

// TestNixContainerfiles_LeaveTheToolingToTheNixFile guards the boundary the
// variant exists to draw. mise survives in a nix image for the tools nixpkgs
// does not package: pi, herdr and agy from its registry, and grok from its
// npm backend. herdr is not optional, because the image starts a server for
// every account at boot. Anything else installed through mise would give the
// image two sources for the same tool and no way to say which one a guest is
// running, which is the ambiguity the nix variant removes.
func TestNixContainerfiles_LeaveTheToolingToTheNixFile(t *testing.T) {
	t.Parallel()
	allowed := []string{"pi", "herdr", "agy", "npm:@xai-official/grok"}

	for nixName, pair := range nixRecipes(t) {
		full, nix := pair[0], pair[1]

		// Rust is the one toolchain the full recipe installs outside a package
		// manager; in a nix image it comes from the profile like the rest.
		for _, tool := range []string{"rustup-init", "sh.rustup.rs"} {
			if !strings.Contains(full, tool) {
				t.Errorf("the full recipe no longer mentions %q; this list describes what the nix variant replaces, so update both", tool)
			}
			if strings.Contains(nix, tool) {
				t.Errorf("%s installs a toolchain outside agent-tools.nix (%s)", nixName, tool)
			}
		}

		installs := 0
		for _, line := range strings.Split(nix, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || !strings.Contains(line, "mise use") {
				continue
			}
			installs++
			_, args, found := strings.Cut(line, "--yes")
			if !found {
				t.Errorf("%s runs mise without --yes, which would block an unattended build: %s", nixName, line)
				continue
			}
			// Everything after --yes is the tool list, up to the shell
			// separator that ends the command.
			got := strings.Fields(strings.TrimRight(strings.TrimSpace(args), "; \\"))
			if !slices.Equal(got, allowed) {
				t.Errorf("%s installs %v through mise, want exactly %v: everything else belongs in agent-tools.nix", nixName, got, allowed)
			}
		}
		if installs == 0 {
			t.Errorf("%s installs nothing through mise; pi and herdr come from nowhere else, and a guest without herdr has a failed unit at every boot", nixName)
		}

		// The shims are the other half of it: a symlink to the mise binary for
		// any other command would put a mise-resolved tool on PATH ahead of
		// the profile's.
		if want := "for command in pi herdr agy grok; do"; !strings.Contains(nix, want) {
			t.Errorf("%s does not shim exactly the mise-installed commands (%q missing)", nixName, want)
		}
		if got := strings.Count(nix, "ln -sf /usr/local/bin/mise"); got != 1 {
			t.Errorf("%s links commands to the mise binary in %d places, want exactly 1: the shim loop is the whole list", nixName, got)
		}
		if strings.Contains(nix, "antigravity.google/cli/install.sh") {
			t.Errorf("%s still installs agy from the vendor script; it is a mise registry tool in a nix image too", nixName)
		}
	}
}

// TestNixContainerfiles_KeepTheServicesAGuestStillRuns covers the two places a
// nix guest is not allowed to differ from a full one.
//
// Docker and libvirt are the deliberate exception to installing from nix: both
// are system daemons with kernel-side state and distro-owned units, and a nix
// profile can supply the binaries but not a running, socket-activated service,
// so they stay on the distro's package manager (ADR-0012).
//
// The herdr servers are the reason mise survives in this variant at all. The
// unit is a template instantiated per account, so an image that shipped it
// without installing herdr would come up with a failed unit at every boot of
// every guest — which is exactly what dropping mise from these recipes would
// have caused.
func TestNixContainerfiles_KeepTheServicesAGuestStillRuns(t *testing.T) {
	t.Parallel()
	for nixName, pair := range nixRecipes(t) {
		nix := pair[1]
		for _, unit := range []string{"docker.service", "libvirtd.service"} {
			if !strings.Contains(nix, unit) {
				t.Errorf("%s does not enable %s; a nix guest would lose a capability the full image promises", nixName, unit)
			}
		}
		for _, want := range []struct{ needle, why string }{
			{"COPY herdr-server.sh /usr/local/sbin/agent-vm-herdr-server", "nothing would start a herdr server for each account"},
			{"'ExecStart=/usr/local/bin/herdr server' \\", "the unit would not start the server the shim in /usr/local/bin points at"},
		} {
			if !strings.Contains(nix, want.needle) {
				t.Errorf("%s does not contain %q: %s", nixName, want.needle, want.why)
			}
		}
	}
}

// TestAgentToolsNix_IsShippedInTheBuildContext catches the failure where the
// recipes COPY a file the builder never writes: podman fails late, after the
// pull and most of the build, with a message about a missing context file.
func TestAgentToolsNix_IsShippedInTheBuildContext(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"agent-tools.nix", "nix.sh"} {
		if !slices.Contains(buildContextFiles, name) {
			t.Errorf("%s is not in buildContextFiles; every nix build would fail on the COPY", name)
		}
		if _, err := templates.FS.ReadFile("distro/" + name); err != nil {
			t.Errorf("reading embedded distro/%s: %v", name, err)
		}
	}
}

// TestAgentToolsNix_PinsItsNixpkgsInOnePlace keeps the pin findable and
// movable. Two builds of the same image name and tag should install the same
// versions, and the only thing that decides that is which nixpkgs is fetched.
func TestAgentToolsNix_PinsItsNixpkgsInOnePlace(t *testing.T) {
	t.Parallel()
	contents, err := templates.FS.ReadFile("distro/agent-tools.nix")
	if err != nil {
		t.Fatalf("reading distro/agent-tools.nix: %v", err)
	}
	nix := string(contents)

	for _, want := range []struct{ needle, why string }{
		{"nixpkgsRef =", "there would be nothing for scripts/pin-nixpkgs.sh to rewrite"},
		{"nixpkgsSha256 =", "a pinned revision could not be verified"},
		{"scripts/pin-nixpkgs.sh", "nothing would tell a reader how to pin it"},
		{"archive/${nixpkgsRef}.tar.gz", "the fetch would not follow the binding above it"},
	} {
		if !strings.Contains(nix, want.needle) {
			t.Errorf("agent-tools.nix does not contain %q: %s", want.needle, want.why)
		}
	}

	// A second fetchTarball would be a second, unpinned source of packages.
	if got := strings.Count(nix, "fetchTarball"); got != 1 {
		t.Errorf("agent-tools.nix calls fetchTarball %d times, want exactly 1: every package must come from the one pinned tree", got)
	}
}

// TestAgentToolsNix_CarriesNoCredentials is the same check the agent
// configuration files get. A base image is shared by every VM built on it, so
// nothing per-VM and nothing secret may be in a file it ships (SECURITY.md).
func TestAgentToolsNix_CarriesNoCredentials(t *testing.T) {
	t.Parallel()
	contents, err := templates.FS.ReadFile("distro/agent-tools.nix")
	if err != nil {
		t.Fatalf("reading distro/agent-tools.nix: %v", err)
	}
	lowered := strings.ToLower(string(contents))

	for _, secret := range []string{"api_key", "apikey", "token", "password", "secret", "begin openssh private key", "begin rsa private key"} {
		if strings.Contains(lowered, secret) {
			t.Errorf("agent-tools.nix mentions %q; a base image is shared by every VM built on it", secret)
		}
	}
}

// TestRunnerContainerfiles_AreTheSlimRecipePlusTheRunner is the whole variant:
// the bootable guest is the slim recipe, byte for byte, and the runner section
// appended to it is one section shared by every family.
func TestRunnerContainerfiles_AreTheSlimRecipePlusTheRunner(t *testing.T) {
	t.Parallel()
	// The instruction line, not a comment that happens to name it. The runner
	// header explains the copy and must not shift where the comparison starts.
	const marker = "\nARG BASE_IMAGE\n"
	var trailer string
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		slim := readTemplate(t, "distro/"+d.SlimContainerfile)
		runner := readTemplate(t, "distro/"+d.RunnerContainerfile)
		slimAt := strings.Index(slim, marker)
		runnerAt := strings.Index(runner, marker)
		if slimAt < 0 || runnerAt < 0 {
			t.Fatalf("%s or %s has no ARG BASE_IMAGE instruction", d.SlimContainerfile, d.RunnerContainerfile)
		}
		body := slim[slimAt+1:]
		fromBody := runner[runnerAt+1:]
		if !strings.HasPrefix(fromBody, body) {
			t.Errorf("%s diverges from %s at ARG BASE_IMAGE; the runner recipe is the slim recipe plus one section", d.RunnerContainerfile, d.SlimContainerfile)
			continue
		}
		rest := fromBody[len(body):]
		if !strings.HasPrefix(rest, "\n# agent-vm-runner-section\n") {
			t.Errorf("%s runner section = %q, want it to start with the agent-vm-runner-section marker", d.RunnerContainerfile, rest[:min(80, len(rest))])
		}
		if trailer == "" {
			trailer = rest
		} else if rest != trailer {
			t.Errorf("%s runner section differs from the other families; the section is shared", d.RunnerContainerfile)
		}
		for _, want := range []string{
			"COPY github-runner.sh /tmp/agent-vm-github-runner-install.sh",
			"COPY github-runner-configure.sh /usr/local/sbin/agent-vm-github-runner",
			"/usr/sbin/agent-vm-github-runner",
			"/tmp/agent-vm-github-runner-install.sh",
		} {
			if !strings.Contains(rest, want) {
				t.Errorf("%s runner section does not contain %q", d.RunnerContainerfile, want)
			}
		}
	}
	if trailer == "" {
		t.Fatal("no runner recipe was read")
	}
}

// TestRunnerContainerfiles_KeepTheBootAndCloudInitContract is what makes a
// runner image a base image: the slim body it copies still carries every block
// the guest contract depends on.
func TestRunnerContainerfiles_KeepTheBootAndCloudInitContract(t *testing.T) {
	t.Parallel()
	for runnerName, pair := range variantRecipes(t, distro.Runner) {
		full, runner := pair[0], pair[1]
		for _, want := range bootAndCloudInitContract {
			if !strings.Contains(full, want.needle) {
				t.Errorf("the full recipe no longer contains %q; this list describes what a runner image must keep, so update both", want.needle)
			}
			if !strings.Contains(runner, want.needle) {
				t.Errorf("%s does not contain %q: %s", runnerName, want.needle, want.why)
			}
		}
		for _, unit := range []string{"cloud-init", "openssh", "sudo", "chrony"} {
			if !strings.Contains(runner, unit) {
				t.Errorf("%s does not install %s; a guest built from it could not be reached or configured", runnerName, unit)
			}
		}
	}
}

// TestRunnerContainerfiles_LeaveOutTheAgentTooling keeps the variant on the
// slim promise. The two runner scripts are the only files it copies in.
func TestRunnerContainerfiles_LeaveOutTheAgentTooling(t *testing.T) {
	t.Parallel()
	excluded := []string{"mise", "rustup", "cargo", "golangci", "playwright", "chromium", "docker", "libvirt", "codex", "herdr", "npm"}
	allowedCopy := map[string]bool{
		"COPY github-runner.sh /tmp/agent-vm-github-runner-install.sh":           true,
		"COPY github-runner-configure.sh /usr/local/sbin/agent-vm-github-runner": true,
	}

	for runnerName, pair := range variantRecipes(t, distro.Runner) {
		full, runner := pair[0], pair[1]
		for _, tool := range excluded {
			if !strings.Contains(strings.ToLower(full), tool) {
				t.Errorf("the full recipe no longer mentions %q; this list describes what a runner image leaves out, so update both", tool)
			}
		}
		for _, line := range strings.Split(runner, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			for _, tool := range excluded {
				if strings.Contains(strings.ToLower(trimmed), tool) {
					t.Errorf("%s carries agent tooling (%s): %s", runnerName, tool, trimmed)
				}
			}
			if strings.HasPrefix(trimmed, "COPY ") && !allowedCopy[trimmed] {
				t.Errorf("%s COPYs %q; a runner image copies only the runner scripts", runnerName, trimmed)
			}
		}
	}
}
