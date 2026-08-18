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
		if !strings.Contains(recipe, "enable agent-vm-docker-group.service") {
			t.Errorf("%s does not enable agent-vm-docker-group.service; the login user will need sudo for every docker command", d.Containerfile)
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

// agentNPMPackages are the coding agents installed from npm. agy is absent
// because it has no npm package and arrives through its vendor's installer.
var agentNPMPackages = []string{
	"@anthropic-ai/claude-code",
	"@openai/codex",
	"opencode-ai",
	"@earendil-works/pi-coding-agent",
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

		// Four of the five are npm packages, so a Node runtime is not
		// optional; without it the npm step fails the build outright.
		if !strings.Contains(recipe, "nodejs") {
			t.Errorf("%s does not install Node.js; the npm-published agents cannot be installed without it", d.Containerfile)
		}
		for _, pkg := range agentNPMPackages {
			if !strings.Contains(recipe, pkg) {
				t.Errorf("%s does not install %q; guests built from it will be missing an agent docs/cli.md promises", d.Containerfile, pkg)
			}
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

		// pi and claude refuse to start on Node older than 22.19. Catching
		// that during the build is the difference between a failed build and
		// a guest whose agents silently do not run.
		if !strings.Contains(recipe, "the agent CLIs require >= 22.19") {
			t.Errorf("%s does not assert a minimum Node version; a distro that ships Node older than 22.19 would produce an image whose agents cannot start", d.Containerfile)
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
	}{}
	readJSON(t, "distro/claude-settings.json", &claude)
	if claude.Permissions.DefaultMode != "bypassPermissions" {
		t.Errorf("claude-settings.json sets permissions.defaultMode to %q, want \"bypassPermissions\"; any other mode stops to ask", claude.Permissions.DefaultMode)
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
	for _, name := range []string{"claude-settings.json", "codex-config.toml", "opencode.json", "agent-aliases.sh"} {
		if !slices.Contains(buildContextFiles, name) {
			t.Errorf("%s is not in buildContextFiles %v, so podman's build context will not contain it", name, buildContextFiles)
		}
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
// claude and opencode both download a native binary in an npm postinstall
// script. npm 12 does not run those scripts, so the install succeeds and the
// command fails at first use — inside a VM, long after the image was built and
// cached. The recipes hold npm to the 11 line, and run every agent once so
// that a future npm or Node change fails the build instead of shipping a guest
// whose agents do not work.
func TestContainerfiles_SmokeTestTheAgents(t *testing.T) {
	for _, name := range distro.Names() {
		d, ok := distro.Lookup(name)
		if !ok {
			t.Fatalf("distro.Names() returned %q, which distro.Lookup does not know", name)
		}
		recipe := readTemplate(t, "distro/"+d.Containerfile)

		if !strings.Contains(recipe, "npm install -g npm@11") {
			t.Errorf("%s does not hold npm to the 11 line; npm 12 skips the postinstall scripts claude and opencode use to fetch their native binaries, and both install cleanly then fail at first run", d.Containerfile)
		}
		if !strings.Contains(recipe, `for agent in claude codex opencode pi agy; do`) {
			t.Errorf("%s does not run each agent once at build time; an agent that installs but cannot start would ship undetected", d.Containerfile)
		}
		if !strings.Contains(recipe, "installed but cannot run") {
			t.Errorf("%s does not fail the build when an agent cannot start", d.Containerfile)
		}
	}
}
