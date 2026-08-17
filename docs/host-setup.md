# Host Setup

How to prepare a Linux host to run agent VMs, and how to diagnose one that
cannot. Everything here is host configuration — `agent-vm` never changes it for
you, because silently reconfiguring someone's networking or firewall is not a
thing a VM tool should do.

Verify a host at any point with:

```bash
agent-vm doctor
```

## 1. Hardware Virtualization

```bash
# Should print a non-zero count
grep -cE 'vmx|svm' /proc/cpuinfo

# Should exist and be usable
ls -l /dev/kvm
```

If `/dev/kvm` is missing, enable VT-x/AMD-V in firmware. Inside a nested
hypervisor, the outer host must expose nested virtualization; without KVM the
tool refuses to run rather than falling back to software emulation, which is too
slow to be useful.

## 2. Packages

Debian/Ubuntu:

```bash
sudo apt install qemu-system-x86 qemu-utils libvirt-daemon-system libvirt-clients \
                 virtinst podman libguestfs-tools
```

Fedora:

```bash
sudo dnf install qemu-kvm qemu-img libvirt libvirt-client virt-install podman \
                 guestfs-tools
```

Arch Linux:

```bash
sudo pacman -S qemu-full libvirt virt-install podman libguestfs
```

Minimum versions, all checked by `agent-vm doctor`:

| Tool | Minimum | Provides |
|---|---|---|
| libvirt / `virsh` | 9.0 | domain and network management |
| QEMU / `qemu-img` | 8.0 | guest execution, overlays |
| `virt-install` | 4.0 | defining domains, cloud-init seeds |
| libguestfs | 1.50 | `virt-make-fs`, `virt-ls`, `virt-copy-out`, `virt-sysprep` |
| `podman` (or `skopeo`) | 4.0 (1.11) | OCI pull, build, flatten |

`skopeo` works in place of `podman` for pulling, but `podman` is preferred because
the per-distro image recipes are `Containerfile`s. `cloud-image-utils` (Debian/
Ubuntu) or `cloud-utils` (Fedora) is optional — it supplies `cloud-localds`, used
only for the persistent-seed fallback path.

Because the tool orchestrates these programs rather than reimplementing them
([ADR-0009](./decisions/0009-orchestrate-existing-host-cli-tools.md)), a missing or
too-old tool is a hard failure with exit code `3`, naming the tool and the version
needed.

## 3. Service And Permissions

```bash
sudo systemctl enable --now libvirtd          # or virtqemud on modular setups
sudo usermod -aG libvirt,kvm "$USER"
```

Log out and back in (or `newgrp libvirt`) for group membership to take effect.
Confirm the connection works without `sudo`:

```bash
virsh -c qemu:///system list --all
```

`qemu:///system` is the default because bridged networking requires it. For
NAT-only, fully unprivileged use, `qemu:///session` works — set
`libvirt_uri = "qemu:///session"` in the config file, and note that host bridges
are unavailable in that mode.

## 4. Storage

State lives in `~/.local/share/agent-vm` by default: cached base images
under `images/`, per-VM overlays under `vms/`. Plan roughly 2–3 GiB per cached
base image, plus whatever the guests actually write.

Overlays are thin. A 50 GiB VM starts at a few megabytes, so `df` at creation
time tells you nothing about where you will be after an agent runs a build.
Check real consumption with:

```bash
du -sh ~/.local/share/agent-vm/vms/*
```

Put the state directory on a filesystem that handles sparse files well (ext4,
XFS). On Btrfs, disable copy-on-write for the directory to avoid stacking CoW on
CoW:

```bash
chattr +C ~/.local/share/agent-vm/vms
```

To relocate state, set `state_dir` in the config file or `AGENT_VM_STATE_DIR`.
A shared state directory means shared images *and* shared locks across users —
workable, but everyone needs write access to it.

## 5. NAT Networking (Default)

NAT mode uses a libvirt-managed network named `agent-vm-nat` on
`192.168.171.0/24`, with the host at `192.168.171.1` and DHCP handing out
`.2`–`.254`. `agent-vm` defines and starts it on first use; the guest reaches the
internet and the host, and nothing on the LAN reaches the guest. The subnet
differs from libvirt's own `default` network (`192.168.122.0/24`) so both can
exist on one host. The bridge device is left unnamed so libvirt allocates one
(`virbrN`).

```bash
virsh -c qemu:///system net-list --all
virsh -c qemu:///system net-dhcp-leases agent-vm-nat
```

If the network fails to start, the usual causes are a subnet conflict with an
existing libvirt network or another `virbr` interface, and a missing
`dnsmasq`/`nftables` dependency. Both appear in `journalctl -u libvirtd`.

## 6. Bridged Networking (Opt-In)

Bridged mode puts the VM directly on your LAN with its own DHCP address. This
removes the NAT boundary — anything on the LAN can reach the guest — so it is
never the default and requires `--network bridge`.

The bridge must already exist; `agent-vm` validates it and refuses to create,
modify, or delete host network interfaces. With NetworkManager:

```bash
nmcli connection add type bridge con-name br0 ifname br0
nmcli connection add type ethernet con-name br0-port ifname enp3s0 master br0
nmcli connection modify br0 ipv4.method auto
nmcli connection up br0
```

Adding your only Ethernet interface to a bridge will briefly drop the host's
network. Do this from a console or a second interface, not over the SSH session
you need to keep.

Verify, then create a VM:

```bash
ip -br link show type bridge
agent-vm create agent-04 --network bridge --bridge br0
```

Wireless interfaces cannot be bridged in the usual way; use NAT on laptops.

Set a default bridge in the config file to avoid repeating the flag:

```toml
[network.bridge]
interface = "br0"
```

## 7. SELinux And AppArmor

Leave them enabled. Both confine the QEMU process, which is precisely the barrier
you want between an untrusted guest and the host, and disabling them to fix a
permission error trades a real boundary for a shortcut.

If a VM fails to start with a permission error on its disk, the state directory
is usually mislabeled or outside a path the confinement policy allows:

```bash
# SELinux: check for denials
sudo ausearch -m avc -ts recent

# AppArmor: check for denials
sudo dmesg | grep -i apparmor
```

The fix is to label or allow the state directory, not to turn confinement off.

## 8. Verifying End To End

```bash
agent-vm doctor
agent-vm image build ubuntu
agent-vm create smoke-test --dry-run   # review the commands first
agent-vm create smoke-test
agent-vm ssh smoke-test -- uname -a
agent-vm destroy smoke-test --yes
```

The first `image build` needs registry access and takes a few minutes. Subsequent
`create` calls use the cache and should complete in seconds.

## Troubleshooting

| Symptom | Likely cause | Where to look |
|---|---|---|
| `exit 3`, cannot connect to libvirt | Service down, or user not in `libvirt` | `systemctl status libvirtd`, `id` |
| `exit 3`, tool missing or too old | Host package missing or below the floor | `agent-vm doctor`, the package table above |
| A tool failed and you want to reproduce it | — | The error names the tool, argv, and exit status; rerun it by hand, or use `--dry-run` |
| `exit 3`, `/dev/kvm` unusable | Virtualization disabled, or user not in `kvm` | firmware settings, `ls -l /dev/kvm` |
| `image build` fails in libguestfs | Broken appliance, or no `/dev/kvm` for the appliance | `libguestfs-test-tool` |
| `image build` fails pulling | Registry unreachable, proxy, or rate limit | `skopeo inspect docker://<ref>` |
| `exit 6`, guest never reachable | Boot failure or cloud-init failure | `vms/<name>/console.log`, `agent-vm console <name>` |
| VM starts, no address | DHCP or NIC problem | `virsh net-dhcp-leases agent-vm-nat`, console log |
| Bridged VM has no address | Bridge down, or no DHCP on that VLAN | `ip -br link`, LAN DHCP server |
| Disk full mid-task | Thin overlays grew | `du -sh` on the state directory |
| `exit 7` after a failure | Cleanup was incomplete | The error names exactly what is left; `virsh list --all` |

Because everything is ordinary libvirt, standard tooling works for
investigation — `virsh list --all`, `virsh dumpxml <name>`,
`virsh domblklist <name>`, `journalctl -u libvirtd`. Prefer read-only `virsh`
commands: destroying or undefining a domain behind the tool's back leaves its
state directory orphaned.
