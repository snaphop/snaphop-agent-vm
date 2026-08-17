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
| `ip-json-link-show-type-bridge.txt` | `ip -json link show type bridge` | iproute2 7.1.0 (Arch Linux) | 2026-08-17 |
| `podman--version.txt` | `podman --version` | podman 6.1.0 (Arch Linux) | 2026-08-17 |
| `virt-make-fs--version.txt` | `virt-make-fs --version` | libguestfs 1.56.0 (Arch Linux) | 2026-08-17 |
| `virt-ls--version.txt` | `virt-ls --version` | libguestfs 1.56.0 (Arch Linux) | 2026-08-17 |
| `virt-copy-out--version.txt` | `virt-copy-out --version` | libguestfs 1.60.1 (Arch Linux) | 2026-08-17 |
| `virt-sysprep--version.txt` | `virt-sysprep --version` | libguestfs 1.56.0 (Arch Linux) | 2026-08-17 |

The bridge capture holds both `UP` and `DOWN` bridges, which is what makes it
useful: a bridge that exists but is down must be reported as not ready, not as
present.

## Missing captures

No fixture exists yet for `skopeo --version`; skopeo was not installed on the
host where these were captured, so that parser is not covered by a real
capture. Add it — and a matching table row — when working on a host that has it.
