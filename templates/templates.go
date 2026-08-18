// Package templates embeds the files agent-vm ships inside its binary: the
// per-distro Containerfiles that define what a base image contains, the guest
// dotfiles they copy in, the cloud-init user-data template, and the libvirt NAT
// network XML.
//
// They are embedded rather than installed so that a single static binary is the
// whole tool, and kept as files rather than string literals so they stay
// readable and reviewable — the Containerfiles in particular are the readable
// form of every distro-specific decision in this project (ADR-0006).
package templates

import "embed"

// FS holds the embedded template tree.
//
//go:embed network/*.xml.tmpl distro/*.Containerfile distro/tmux.conf cloud-init/*.tmpl
var FS embed.FS
