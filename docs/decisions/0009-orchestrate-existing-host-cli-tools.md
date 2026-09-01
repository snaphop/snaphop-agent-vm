# 9. Orchestrate existing host CLI tools instead of reimplementing them

Date: 2026-08-17

## Status

Accepted

Amends [ADR-0002](./0002-use-libvirt-and-qemu-kvm-for-the-vm-lifecycle.md) (which
mandated the libvirt Go bindings) and
[ADR-0008](./0008-go-single-binary-cli-with-no-daemon.md) (which assumed a cgo
build). The libvirt/QEMU and single-binary decisions stand; how we talk to them
changes.

Narrowed in one place by
[ADR-0011](./0011-build-the-cloud-init-seed-and-attach-it-as-a-virtio-disk.md):
`virt-install --cloud-init` could not be told which bus to attach the seed to,
so we build the seed filesystem with `virt-make-fs` and attach it ourselves. The
rest of this record stands.

## Context

The virtualization ecosystem already ships mature, well-documented, widely
deployed command-line tools for nearly every step this project needs. `virt-install`
generates correct libvirt domain XML for a given host — machine type, firmware,
virtio device models, console, guest agent channel, architecture differences —
and has absorbed a decade of hard-won knowledge about what actually boots.
`virsh` covers the entire domain and network lifecycle. `podman` pulls, builds,
and exports OCI images. libguestfs ships `virt-make-fs`, `virt-copy-out`,
`virt-sysprep`, and `virt-ls`. `qemu-img` creates overlays.

Our original plan was to bind libvirt directly and render domain XML from our own
template. That means:

- Owning a domain XML template per architecture, and re-deriving what
  `virt-install` already knows about machine types, firmware, and device models.
- A cgo build with libvirt development headers, complicating static builds and CI.
- Reimplementing behavior that already exists, badly at first, and diverging from
  what operators see documented everywhere else.

The value this project adds is not "another way to define a domain." It is the
*workflow*: cache a base image built from an OCI reference, create a
copy-on-write overlay, wire up cloud-init, and tear the whole thing down cleanly
and safely. That workflow is orchestration.

The trade-off against direct API bindings is real: subprocess orchestration means
depending on tool availability and versions, and parsing output that was written
for humans. That is manageable if we hold ourselves to using machine-readable
output modes and checking versions, and it is a smaller cost than maintaining a
parallel implementation of `virt-install`.

## Decision

**`agent-vm` is an orchestrator of existing host CLI tools.** We do not
reimplement functionality that a standard tool already provides, and we do not
bind libvirt directly.

| Task | Tool we invoke |
|---|---|
| Define and start a domain | `virt-install --import --boot kernel=…,initrd=…,kernel_args=…` |
| Inspect domains | `virsh list --all`, `virsh dominfo`, `virsh dumpxml`, `virsh domblklist` |
| Lifecycle | `virsh start`, `virsh shutdown`, `virsh destroy`, `virsh undefine` |
| Guest address | `virsh domifaddr --source agent` (fallback `--source lease`) |
| Serial console | `virsh console` (exec'd directly, not proxied) |
| NAT network | `virsh net-define/net-start/net-autostart/net-list/net-dhcp-leases` |
| Host bridge check | `ip -d -json link show type bridge` |
| Pull / build / flatten OCI images | `podman pull`, `podman build`, `podman create`, `podman export` (or `skopeo copy`) |
| Root filesystem → qcow2 | `virt-make-fs --type=ext4 --format=qcow2` |
| Locate and extract kernel/initrd | `virt-ls`, `virt-copy-out` |
| Generalize the base image | `virt-sysprep --operations machine-id,ssh-hostkeys,…` |
| Copy-on-write overlay | `qemu-img create -f qcow2 -b … -F qcow2` |
| Disk facts | `qemu-img info --output=json` |
| cloud-init seed | `virt-make-fs --type=vfat --label=cidata` ([ADR-0011](./0011-build-the-cloud-init-seed-and-attach-it-as-a-virtio-disk.md); originally `virt-install --cloud-init`) |
| Shell into a guest | `ssh` (exec'd, with the recorded key and address) |

Rules that follow from this decision:

1. **No reimplementation.** If a standard tool does the job, we call it. Writing
   our own domain XML template, our own NoCloud ISO builder, or our own OCI layer
   extraction requires an ADR explaining why the existing tool could not be used.
2. **Machine-readable output only.** Prefer `--output=json`, `--format json`,
   `-json`, `--xml`, and structured `virsh` subcommands (`domifaddr`, `dominfo`,
   `net-dhcp-leases`) over scraping human-formatted text. Where no structured
   output exists, parse the narrowest possible thing and cover it with a fixture
   captured from the real tool.
3. **Explicit argument vectors, never shell strings.** Every invocation is an
   argv, logged with its exit status. Untrusted values are validated and passed
   after `--` where a tool supports it.
4. **Version floors, checked by `doctor`.** libvirt 9.0+, QEMU 8.0+,
   `virt-install` 4.0+, libguestfs 1.50+, podman 4.0+ (or skopeo 1.11+). A missing
   or too-old tool is a host-readiness failure (exit `3`) that names the tool and
   the version needed.
5. **Record what ran.** `vm.json` records the `virt-install` version and argument
   vector used to define the domain; `manifest.json` records the tool versions
   that built the base image. When a VM behaves strangely two months later, the
   answer to "what created this?" is in the state directory.
6. **The generated XML is libvirt's, not ours.** After `virt-install` defines a
   domain, `virsh dumpxml` is captured to `vms/<name>/domain.xml` as a record. We
   do not hand-maintain it, and we do not treat its exact content as our contract —
   the *argument vector* is what we control and what golden tests pin.
7. **`--dry-run` prints commands.** Every operation can show the exact tool
   invocations it would perform, so an operator can run them by hand, and so a
   reviewer can see the effect of a change without a KVM host.

`virsh` remains the documented escape hatch for operators, and now it is also
literally what the tool itself uses — the same commands, in the same order.

## Consequences

Easier:

- Correct domain XML for the host architecture, firmware, and device models comes
  from `virt-install` instead of from a template we would maintain forever.
- No cgo and no libvirt headers: a pure-Go static binary, simpler CI, trivial
  cross-compilation.
- Much less code, and the code that remains is the part that is actually ours —
  the image cache, the overlay/rollback logic, and the safety checks.
- Operators can reproduce any operation by hand from `--dry-run` output or the
  logged argv, which makes debugging and bug reports concrete.
- Tool improvements and fixes upstream arrive for free.

Harder:

- More runtime dependencies to detect and version-check, which is why `doctor`
  exists and why exit `3` is a first-class outcome.
- Output parsing is a maintenance surface. Mitigated by preferring structured
  output, keeping parsers narrow, and testing against captured real-tool fixtures.
- Errors arrive as another program's stderr, so error handling must attach context
  (which tool, which argv, which exit status) or the user gets an unattributable
  failure.
- Per-VM startup cost includes process spawns; irrelevant next to boot time.
- Some behavior is only reachable through flags a tool may not expose. When that
  happens the answer is `virt-install --xml` edits or a `virsh define` of a
  modified XML — both still going through the existing tools — not a private
  implementation.
- Subprocess orchestration is harder to unit test than a library call, so every
  tool sits behind an interface with a fake at the process boundary, and argv is
  pinned by golden tests.
