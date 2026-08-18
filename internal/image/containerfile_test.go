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
// contract: gh, tea, Playwright with a headless Chromium, and SDKMAN.
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

		if !strings.Contains(recipe, "npm install -g playwright") {
			t.Errorf("%s does not install Playwright", d.Containerfile)
		}
		if !strings.Contains(recipe, "playwright install") || !strings.Contains(recipe, "chromium") {
			t.Errorf("%s does not install a Chromium for Playwright to drive", d.Containerfile)
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

		if !strings.Contains(recipe, "get.sdkman.io") {
			t.Errorf("%s does not install SDKMAN", d.Containerfile)
		}
		if !strings.Contains(recipe, "SDKMAN_DIR=/etc/skel/.sdkman") {
			t.Errorf("%s does not install SDKMAN into /etc/skel; accounts cloud-init creates would have none, and a shared copy would have every user writing to one directory", d.Containerfile)
		}
		if !strings.Contains(recipe, "COPY sdkman.sh /etc/profile.d/agent-vm-sdkman.sh") {
			t.Errorf("%s does not install the SDKMAN shell init; `sdk` is a shell function and does not exist until it is sourced", d.Containerfile)
		}

		// The JVM toolchain has to be installed into the skel copy, before it
		// is cloned to /root: a JDK downloaded per account at first use would
		// be a download a network-isolated guest cannot make.
		if !strings.Contains(recipe, `sdk install java "$java_id"`) {
			t.Errorf("%s does not install a JDK with SDKMAN; a guest would come up with `sdk` and no Java", d.Containerfile)
		}
		if !strings.Contains(recipe, "-tem") {
			t.Errorf("%s does not select a Temurin JDK", d.Containerfile)
		}
		if !strings.Contains(recipe, "sdk install maven") {
			t.Errorf("%s does not install Maven with SDKMAN", d.Containerfile)
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
			"dnsmasq-base", "guestfs-tools", "podman", "golang-go",
		},
		distro.Fedora.Containerfile: {
			"qemu-kvm", "libvirt", "libvirt-client", "virt-install",
			"dnsmasq", "guestfs-tools", "podman", "golang",
		},
		distro.Arch.Containerfile: {
			"qemu-base", "libvirt", "virt-install",
			"dnsmasq", "guestfs-tools", "podman", "go",
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

		// golangci-lint is not packaged by Ubuntu at all, and where it is
		// packaged the version differs per family.
		if !strings.Contains(recipe, "golangci-lint/HEAD/install.sh") {
			t.Errorf("%s does not install golangci-lint from its own installer", d.Containerfile)
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
		for _, check := range []string{"gh --version", "tea --version", "playwright --version"} {
			if !strings.Contains(recipe, check) {
				t.Errorf("%s does not run %q at build time", d.Containerfile, check)
			}
		}
		if !strings.Contains(recipe, "SDKMAN is missing from /etc/skel") {
			t.Errorf("%s does not check that SDKMAN landed in /etc/skel, which is where accounts cloud-init creates get it from", d.Containerfile)
		}
		// sdk is a shell function, so the file existing says nothing about
		// whether it is actually defined in a login shell.
		if !strings.Contains(recipe, `bash -lc 'type sdk'`) {
			t.Errorf("%s does not check that `sdk` is defined in a login shell; the profile script could be missing and the file check would still pass", d.Containerfile)
		}
		for _, check := range []string{`bash -lc 'java -version'`, `bash -lc 'mvn -version'`} {
			if !strings.Contains(recipe, check) {
				t.Errorf("%s does not run %s at build time; a JDK or Maven that did not install would only surface inside a VM", d.Containerfile, check)
			}
		}
		for _, check := range []string{
			"virsh --version", "virt-install --version", "qemu-img --version",
			"virt-make-fs --version", "podman --version", "dnsmasq --version",
			"go version", "golangci-lint --version",
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
