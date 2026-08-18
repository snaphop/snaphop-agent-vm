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

### Nested Virtualization

Every VM is given the host CPU, so a guest can run VMs of its own — including
another `agent-vm` — provided the host's KVM module allows nesting:

```bash
# Should print Y (Intel) or 1 (AMD)
cat /sys/module/kvm_intel/parameters/nested 2>/dev/null || \
  cat /sys/module/kvm_amd/parameters/nested

# To enable it, for Intel; use kvm_amd on AMD
echo 'options kvm_intel nested=1' | sudo tee /etc/modprobe.d/kvm-nested.conf
sudo modprobe -r kvm_intel && sudo modprobe kvm_intel   # or reboot
```

Recent kernels enable this by default. Where it is off, VMs started inside a
guest still run — QEMU falls back to emulation — but they are slow enough to be
unusable for real work.

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
the per-distro image recipes are `Containerfile`s. No separate cloud-init tooling
is needed on the host: `virt-install --cloud-init` builds the NoCloud seed.

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
base image, plus whatever the guests actually write. (An Ubuntu 24.04 base image
measures about 1.4 GiB of qcow2 plus 35 MiB of kernel and initramfs.)

Building a base image also leaves images in **podman's** storage, which is
separate from the state directory and is not counted by anything `agent-vm`
reports: the pulled source image, the built `agent-vm/<distro>:<tag>` image, and
usually a dangling intermediate layer. That is deliberate — it makes a rebuild
much faster — but it is real disk. Reclaim it with the usual podman commands:

```bash
podman images                       # what is actually there
podman image prune                  # dangling layers
podman rmi agent-vm/ubuntu:24.04    # the built image; agent-vm rebuilds it on demand
```

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

### Letting The Hypervisor Reach The State Directory

Under `qemu:///system` the QEMU process does **not** run as you — it runs as
libvirt's own account (`libvirt-qemu` on Debian, Ubuntu, and Arch; `qemu` on
Fedora and RHEL; whatever `user` is set to in `/etc/libvirt/qemu.conf`). That
account has to search every directory between `/` and a VM's disk, and the
default state directory lives under `~/.local/share`, where a home directory is
commonly `0700` or `0710`. Being able to write the state directory yourself is
not the same thing and does not help.

The symptom is a `create` that gets all the way to defining the domain and then
fails:

```text
ERROR    Cannot access storage file '/home/you/.local/share/agent-vm/vms/<name>/root.qcow2'
         (as uid:957, gid:957): Permission denied
```

`agent-vm doctor` reports this up front as **state directory access**, naming the
shallowest directory that blocks the path. Grant search permission — and only
search permission, not read — to the account it names:

```bash
sudo setfacl -m u:libvirt-qemu:x /home/you
sudo setfacl -m u:libvirt-qemu:x /home/you/.local
agent-vm doctor            # confirm; repeat for any other directory it names
```

`x` without `r` lets the hypervisor traverse a directory without listing it, so
the rest of your home stays as private as it was. Reverse it with
`setfacl -x u:libvirt-qemu <dir>`. This needs a filesystem mounted with ACL
support, which is the default on ext4, XFS, and Btrfs.

The alternative is to keep the state directory out of your home entirely, which
avoids the problem instead of working around it:

```bash
sudo install -d -o "$USER" -g "$USER" -m 0755 /var/lib/agent-vm
```

then set `state_dir = "/var/lib/agent-vm"` in the config file. Prefer this on a
multi-user host, where poking a hole into one user's home is the wrong shape of
fix.

Do **not** reach for `chmod 0755 ~` — it opens your whole home directory to every
local account to fix a problem that two ACL entries solve. Under
`qemu:///session` none of this applies: QEMU runs as you, and `doctor` skips the
check.

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

### Host Firewalls And The `virbrN` Bridge

If the host runs a firewall of its own — `ufw` is the common case — NAT mode
needs one rule, and getting it wrong produces a uniquely misleading failure.

libvirt adds its own nftables rules accepting the guest's traffic, but that is
not the last word on a packet. Every nftables base chain registered on the
forward hook runs, and a drop in any of them is final. `ufw` registers such a
chain and ships with `DEFAULT_FORWARD_POLICY="DROP"` in `/etc/default/ufw`, so
it discards traffic libvirt has already accepted. You can confirm this is what
happened by looking at libvirt's own NAT counters — if the packets were dropped
at the forward hook they never reached masquerade, and the counters stay at
zero:

```bash
sudo nft list chain ip libvirt_network guest_nat
```

**The VM will look completely healthy.** It boots, `agent-vm ssh` works, and DNS
resolves — because the resolver is dnsmasq on the host bridge, and that traffic
is delivered to the host rather than forwarded. Only connections past the host
fail, and because the packets are dropped rather than rejected they fail by
hanging rather than by erroring: `apt update` sits at 0% until it times out.

`agent-vm doctor` reports this as the **host firewall forwarding** check.

Forwarding is only half of it. A guest also talks *to* the host — it asks the
host's dnsmasq for a DHCP lease and for every DNS answer — and ufw defaults to
`deny (incoming)`. Its `ufw-before-input` chain accepts DHCP *replies*
(`sport 67 → dport 68`, the host acting as a DHCP client) but nothing arriving
on port 67 or port 53, so on a host with no rules for the bridge a guest never
gets an address at all. NAT mode therefore needs three rules, all naming the
bridge:

```bash
# The bridge name is not fixed — look it up, do not assume virbr0.
virsh -c qemu:///system net-info agent-vm-nat | grep Bridge

sudo ufw allow in on virbr1 to any port 67 proto udp   # DHCP lease
sudo ufw allow in on virbr1 to any port 53             # DNS via dnsmasq
sudo ufw route allow in on virbr1                      # everything past the host
```

libvirt does **not** add these for you. It manages its own nftables table
(`libvirt_network`) and knows nothing about ufw, so anything in `ufw status` for
a `virbr` interface was put there by an operator and is yours to maintain.

With all three in place, `ufw status` lists six entries — each rule has an IPv6
twin, because ufw applies rules to both families when `IPV6=yes` in
`/etc/default/ufw`, which is the default:

```text
67/udp on virbr1           ALLOW       Anywhere
53 on virbr1               ALLOW       Anywhere
67/udp (v6) on virbr1      ALLOW       Anywhere (v6)
53 (v6) on virbr1          ALLOW       Anywhere (v6)
Anywhere                   ALLOW FWD   Anywhere on virbr1
Anywhere (v6)              ALLOW FWD   Anywhere (v6) on virbr1
```

`ufw status numbered` prints the same rules with an index and shows the input
ones as `ALLOW IN` rather than `ALLOW`; use it when deleting by number.

The IPv6 twins are harmless here — the NAT network is IPv4-only, so nothing
matches them — but they are worth recognising rather than deleting as clutter,
and they go stale with the bridge name exactly like their IPv4 counterparts.

Which rule is missing determines the symptom, and the two look nothing alike:
without the input rules the VM never gets an address and `create` times out
waiting for SSH; without the route rule it gets an address, boots, and hangs on
every outbound connection.

Two things about the route rule are worth knowing before you rely on it.

**The bridge name is allocated, not configured.** The network definition
deliberately leaves the bridge device unnamed so that libvirt picks one and two
networks cannot collide on it. Which `virbrN` you get depends on what else
exists on the host when the network is first started, so it is not stable
across hosts, and **it can change on the same host** if the network is
undefined and redefined, or if another `virbr` interface appears first. All six
rules above are pinned to the name, so they stop matching together — and the
guest regresses all the way back to having no address, not just to the hang. If
anything about a VM's networking breaks after the NAT network was recreated,
re-check the bridge name before anything else:

```bash
virsh -c qemu:///system net-info agent-vm-nat | grep Bridge
sudo ufw status | grep virbr        # compare against the rules you added
```

Rules naming a bridge that no longer exists are dead weight rather than a
hazard, but they are confusing to read later; delete them with
`sudo ufw delete allow in on <old-bridge> to any port 53` and the matching
`route` and port 67 forms, or by number from `ufw status numbered` (deleting
from the bottom up, since the numbers shift).

**The rule above also lets the guest reach your LAN.** `route allow in on
<bridge>` permits forwarding to every destination, not just the internet, which
widens the NAT boundary that [`SECURITY.md`](../SECURITY.md) relies on — and the
guest is untrusted by design. To keep the guest on the internet only, deny the
private ranges first; ufw evaluates rules in order, so these must be added
before the blanket allow:

```bash
sudo ufw route deny in on virbr1 to 192.168.0.0/16
sudo ufw route deny in on virbr1 to 10.0.0.0/8
sudo ufw route deny in on virbr1 to 172.16.0.0/12
sudo ufw route allow in on virbr1
```

Return traffic needs no rule of its own: ufw accepts established and related
connections before these are consulted.

If you already added the blanket allow, appending the denies will not help —
they would land after it and never be reached. Insert them ahead of it instead,
and confirm the resulting order:

```bash
sudo ufw route insert 1 deny in on virbr1 to 192.168.0.0/16
sudo ufw route insert 1 deny in on virbr1 to 10.0.0.0/8
sudo ufw route insert 1 deny in on virbr1 to 172.16.0.0/12
sudo ufw status numbered | grep -i virbr
```

Note that the NAT subnet is itself inside `192.168.0.0/16`, so these denies also
stop VMs on the network from reaching each other. That is usually what you want
for disposable agent VMs — and it is stricter than the NAT network's own
default, which allows guest-to-guest traffic on the same bridge. Drop the
`192.168.0.0/16` line, or narrow it to your LAN's prefix, if VMs need to talk.

`agent-vm` never adds, removes, or edits host firewall rules. Any rule here is
yours to add and yours to maintain — including after a bridge is renamed.

Other firewalls fail the same way for the same reason. `firewalld` puts the
bridge in a zone (`firewall-cmd --get-zone-of-interface=virbr1`) and libvirt
usually assigns its own networks to the `libvirt` zone automatically; a host
with a custom nftables ruleset needs its forward chain to accept the bridge.
`doctor` only knows how to read `ufw`'s configuration, so on those hosts the
check passing means "no `ufw` problem", not "no firewall problem".

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

If a VM fails to start with a permission error on its disk, check ordinary Unix
permissions **first**. The common cause is that the hypervisor's account cannot
search its way to the state directory, which has nothing to do with SELinux or
AppArmor — see [Letting the hypervisor reach the state
directory](#letting-the-hypervisor-reach-the-state-directory), and run
`agent-vm doctor`, which tests exactly that. Relabelling will not fix a missing
`x` bit.

If `doctor` passes and a VM still cannot open its disk, then look for a denial:

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
| `Cannot access storage file ... Permission denied` on create | The hypervisor's account cannot search a directory above the state directory | `agent-vm doctor` (state directory access), then `setfacl -m u:<qemu user>:x` on the directory it names |
| `image build` fails in libguestfs | Broken appliance, or no `/dev/kvm` for the appliance | `libguestfs-test-tool` |
| `image build` fails pulling | Registry unreachable, proxy, or rate limit | `skopeo inspect docker://<ref>` |
| `exit 6`, guest never reachable | Boot failure or cloud-init failure | `vms/<name>/console.log`, `agent-vm console <name>` |
| VM starts, no address | DHCP or NIC problem — including a host firewall dropping the guest's DHCP request to the host (ufw defaults to `deny (incoming)` and does not allow port 67 on the bridge) | `virsh net-dhcp-leases agent-vm-nat`, console log, `sudo ufw status \| grep virbr` |
| VM boots, SSH and DNS work, but outbound connections hang (`apt update` at 0%) | A host firewall is dropping forwarded traffic — commonly `ufw` with `DEFAULT_FORWARD_POLICY="DROP"` | `agent-vm doctor` (host firewall forwarding), then [Host Firewalls And The `virbrN` Bridge](#host-firewalls-and-the-virbrn-bridge) |
| The same hang, on a host where the ufw rule used to work | libvirt allocated a different `virbrN` and the rule no longer matches | `virsh net-info agent-vm-nat \| grep Bridge`, compare with `sudo ufw status` |
| Bridged VM has no address | Bridge down, or no DHCP on that VLAN | `ip -br link`, LAN DHCP server |
| Disk full mid-task | Thin overlays grew | `du -sh` on the state directory |
| `exit 7` after a failure | Cleanup was incomplete | The error names exactly what is left; `virsh list --all` |

Because everything is ordinary libvirt, standard tooling works for
investigation — `virsh list --all`, `virsh dumpxml <name>`,
`virsh domblklist <name>`, `journalctl -u libvirtd`. Prefer read-only `virsh`
commands: destroying or undefining a domain behind the tool's back leaves its
state directory orphaned.
