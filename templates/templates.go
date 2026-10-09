// Package templates embeds the files agent-vm ships inside its binary: the
// per-distro Containerfiles that define what a base image contains, the guest
// dotfiles they copy in, the cloud-init user-data template, the libvirt NAT
// network XML, and the Tailscale join script, which is sent to one guest over
// SSH and is not part of a base image.
//
// They are embedded rather than installed so that a single static binary is the
// whole tool, and kept as files rather than string literals so they stay
// readable and reviewable — the Containerfiles in particular are the readable
// form of every distro-specific decision in this project (ADR-0006).
package templates

import "embed"

// FS holds the embedded template tree.
//
//go:embed network/*.xml.tmpl distro/*.Containerfile distro/tmux.conf
//go:embed distro/claude-settings.json distro/codex-config.toml distro/opencode.json distro/grok-config.toml
//go:embed distro/agent-aliases.sh distro/chromium.sh distro/mise.sh
//go:embed distro/toolchains.sh distro/nix.sh distro/agent-tools.nix
//go:embed distro/user-setup.sh distro/tmux-menu.sh distro/tmux-menu-profile.sh
//go:embed distro/codex-remote-control.sh distro/herdr-server.sh
//go:embed distro/github-runner.sh distro/github-runner-configure.sh distro/runner-docker.sh
//go:embed distro/tailscale-join.sh
//go:embed cloud-init/*.tmpl
var FS embed.FS
