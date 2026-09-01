# Captured Tool Output

Every file here is **verbatim output from a real tool**, used as a fixture for
the parsers in this repository. Do not edit a file to make a parser pass — that
turns a fixture into fiction. Refresh one by re-running the command below and
recording the tool version in this table.

Naming: `<tool>-<flags with dashes collapsed>.txt`, e.g. `virsh--version.txt`
holds the output of `virsh --version`.

| Fixture | Command | Captured from | Captured on |
|---|---|---|---|
| `virsh--version.txt` | `virsh --version` | libvirt 12.6.0 (Arch Linux) | 2026-08-17 |
| `virt-install--version.txt` | `virt-install --version` | virt-install 5.1.0 (Arch Linux) | 2026-08-17 |
| `qemu-img--version.txt` | `qemu-img --version` | QEMU 11.1.0 (Arch Linux) | 2026-08-17 |
| `ip-V.txt` | `ip -V` | iproute2 7.1.0 (Arch Linux) | 2026-08-17 |
| `ssh-V.txt` | `ssh -V` (stderr) | OpenSSH 10.5p1 (Arch Linux) | 2026-08-17 |
| `gh--version.txt` | `gh --version` | gh 2.97.0 (mise) | 2026-08-18 |
| `gh-auth-status.txt` | `gh auth status --hostname github.com` | gh 2.97.0 (mise) | 2026-08-18 |
| `ip-json-link-show-type-bridge.txt` | `ip -json link show type bridge` | iproute2 7.1.0 (Arch Linux) | 2026-08-17 |
| `podman--version.txt` | `podman --version` | podman 6.1.0 (Arch Linux) | 2026-08-17 |
| `virt-make-fs--version.txt` | `virt-make-fs --version` | libguestfs 1.56.0 (Arch Linux) | 2026-08-17 |
| `virt-ls--version.txt` | `virt-ls --version` | libguestfs 1.56.0 (Arch Linux) | 2026-08-17 |
| `virt-copy-out--version.txt` | `virt-copy-out --version` | libguestfs 1.60.1 (Arch Linux) | 2026-08-17 |
| `virt-sysprep--version.txt` | `virt-sysprep --version` | libguestfs 1.56.0 (Arch Linux) | 2026-08-17 |
| `virsh-domifaddr.txt` | `virsh -c test:///default domifaddr test` | libvirt 12.6.0 (Arch Linux) | 2026-08-17 |
| `virsh-domifaddr-source-agent.txt` | `virsh -c qemu:///system domifaddr <vm> --source agent` | libvirt 12.6.0 (Arch Linux) | 2026-08-17 |
| `virsh-domiflist.txt` | `virsh -c test:///default domiflist test` | libvirt 12.6.0 (Arch Linux) | 2026-08-17 |
| `virsh-domblklist.txt` | `virsh -c test:///default domblklist test` | libvirt 12.6.0 (Arch Linux) | 2026-08-17 |
| `podman-image-inspect.json` | `podman image inspect --format json docker.io/library/busybox:latest` | podman 6.1.0 (Arch Linux) | 2026-08-17 |
| `virsh-domstate.txt` | `virsh -c test:///default domstate test` | libvirt 12.6.0 (Arch Linux) | 2026-08-17 |
| `virsh-list-all-name.txt` | `virsh -c test:///default list --all --name` | libvirt 12.6.0 (Arch Linux) | 2026-08-17 |
| `virsh-capabilities.txt` | `virsh -c test:///default capabilities` | libvirt 12.6.0 (Arch Linux) | 2026-08-31 |
| `virsh-net-dumpxml.xml` | `virsh -c qemu:///system net-dumpxml agent-vm-nat` | libvirt 12.6.0 (Arch Linux) | 2026-08-31 |
| `qemu-img-info-json-overlay.json` | `qemu-img info --output=json` on a fresh overlay | QEMU 11.1.0 (Arch Linux) | 2026-08-17 |
| `posix-acl-access-search-grant.bin` | `getxattr(dir, "system.posix_acl_access")` after `setfacl -m u:libvirt-qemu:x` | Linux 7.1.8, Btrfs, acl 2.3.2 (Arch Linux) | 2026-08-17 |

Most `virsh-*` captures come from libvirt's built-in `test:///default`
driver. The data in them is synthetic, but the formatting is produced by the
real `virsh` — which is the part a parser depends on — and capturing them this
way needs no VM on the host doing the capture.

`virsh-domifaddr-source-agent.txt` is the exception: it comes from a real
booted guest, because the test driver cannot produce it. It is what the QEMU
guest agent reports, which is every interface the guest can see — including
`lo` with 127.0.0.1, listed *first*, and the `-` continuation rows a second
protocol on one interface produces. The `test:///default` capture has neither,
which is how the address picker came to return the loopback address.

`podman-image-inspect.json` is a multi-architecture repository's inspect
output, which is what makes it worth having: alongside the image's own `Digest`
it carries a `RepoDigests` array holding a second, different digest — the
manifest list's. Pinning a build to that one would record provenance for an
artifact other than the one podman pulled, and a hand-written fixture with a
single digest in it could not catch the mistake.

The `qemu-img` capture is of an overlay created with
`qemu-img create -f qcow2 -F qcow2 -b base.qcow2 root.qcow2 50G`, so it shows
what matters about a VM's root disk: a 50 GiB virtual size, an actual size
under a megabyte, and a backing file pointing at the base image.

The POSIX ACL capture is binary rather than text: it is the raw
`system.posix_acl_access` extended attribute the kernel returns, not `getfacl`
output. `getfacl` rendered the same directory as `user::rwx`,
`user:libvirt-qemu:--x`, `group::---`, `mask::--x`, `other::---`, and uid 957 is
`libvirt-qemu` on the capture host. It exists because doctor's state-directory
access check decodes this attribute directly — that is how a `setfacl` grant is
recognised instead of being reported as a failure the operator already fixed.
Refresh it by re-running `setfacl` on a scratch directory and re-reading the
attribute, never by editing the bytes.

The bridge capture holds both `UP` and `DOWN` bridges, which is what makes it
useful: a bridge that exists but is down must be reported as not ready, not as
present.

## Missing captures

There is no `skopeo --version` capture, and none is needed yet: `internal/hostexec`
defines a `skopeo` tool but nothing invokes skopeo and `doctor` does not check
for it. Capture it — with a matching table row — if the skopeo path in
[ADR-0009](../../docs/decisions/0009-orchestrate-existing-host-cli-tools.md) is
ever implemented.

Every other fixture here is read by a test. Keep it that way: a capture nothing
asserts on is a file that goes stale without anything noticing.

There is also no capture of `virsh domifaddr` for a guest that has **no**
address yet — the state a VM is in for the first seconds of its boot, and the
one the boot wait polls on. The test driver's domain always has an address, and
producing the empty case needs a real VM mid-boot. Capture it during an
integration run on a KVM host.
