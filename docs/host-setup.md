# Host Setup

How to prepare a Linux host to run agent VMs, and how to diagnose one that
cannot. Install packages and configure host permissions, bridges, and firewall
rules yourself. `agent-vm` manages its own storage, domains, and libvirt NAT
network; it does not reconfigure your existing host bridges or firewall rules.

Verify a host at any point with:

```bash
agent-vm doctor
```

## 1. Hardware Virtualization

```bash
# On x86_64, should print a non-zero count
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

Where nesting is off, `agent-vm` inside a guest fails its KVM readiness check.
QEMU can be configured separately to use software emulation, but `agent-vm`
does not fall back to it.

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

Optional, and only for `--github-ssh-key` on `create` and `destroy`: the GitHub
CLI, `gh` (`apt install gh`, `dnf install gh`, `pacman -S github-cli`), logged in
with `gh auth login`. Every other command works without it.

Minimum versions, all checked by `agent-vm doctor`:

| Tool | Minimum | Provides |
|---|---|---|
| libvirt / `virsh` | 9.0 | domain and network management |
| QEMU / `qemu-img` | 8.0 | guest execution, overlays |
| `virt-install` | 4.0 | defining and starting domains |
| libguestfs | 1.50 | `virt-make-fs` (base images and the cloud-init seed), `virt-ls`, `virt-copy-out`, `virt-sysprep` |
| `podman` | 4.0 | OCI pull, build, flatten |
| `ip` (iproute2) | any | host bridge validation |
| `ssh` (openssh-client) | any | reaching a guest, and a remote hypervisor |
| `gh` (optional) | 2.0 | adding and removing a VM's SSH key on GitHub |

`podman` is required and has no substitute today: the per-distro image recipes
are `Containerfile`s, and `podman build` is what runs them. `skopeo` appears as
a possible alternative in
[ADR-0009](./decisions/0009-orchestrate-existing-host-cli-tools.md) but nothing
invokes it, and `doctor` does not look for it. No separate cloud-init tooling is
needed on the host either: the NoCloud seed is written by `virt-make-fs`, which
libguestfs already provides for building base images
([ADR-0011](./decisions/0011-build-the-cloud-init-seed-and-attach-it-as-a-virtio-disk.md)).

Because the tool orchestrates these programs rather than reimplementing them
([ADR-0009](./decisions/0009-orchestrate-existing-host-cli-tools.md)), a missing or
too-old tool is a hard failure with exit code `3`, naming the tool and the version
needed.

## 3. Service And Permissions

```bash
sudo systemctl enable --now libvirtd          # or virtqemud on modular setups
sudo usermod -aG libvirt,kvm "$USER"
```

Log out and back in for both group memberships to take effect.
Confirm the connection works without `sudo`:

```bash
virsh -c qemu:///system list --all
```

`qemu:///system` is the standard setup for both network modes. The CLI accepts
`qemu:///session` with NAT mode only, but still requires a managed libvirt NAT
network. Ordinary unprivileged sessions lack that network out of the box; the
tool does not fall back to QEMU user-mode networking. See
[libvirt's connection FAQ](https://wiki.libvirt.org/FAQ.html#what-is-the-difference-between-qemu-system-and-qemu-session-which-one-should-i-use).

To drive this host from another machine, see
[§10](#10-driving-this-host-from-another-machine).

## 4. Storage

State lives in `~/.local/share/agent-vm` by default: cached base images
under `images/`, per-VM overlays under `vms/`. Full images include language
toolchains, browsers, and agent tooling and need substantially more space than
a minimal distro image. Allow additional space for build intermediates and
whatever guests write; measure cached artifacts with `agent-vm image list` and
`du -sh ~/.local/share/agent-vm/images`. Slim images omit the agent tooling.

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
Use one operating-system account per state directory. Giving other users write
access to its top-level directory is insufficient for shared operation: newly
created subdirectories and lock files are not group-writable by default.

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
internet, the host, and the LAN; unsolicited inbound connections from the LAN
are blocked. The subnet
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
needs rules for both guest services and forwarding. Missing forwarding rules
produce a misleading failure.

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

Forwarding is only half of it, and `agent-vm doctor` reports the other half as
the **host firewall guest services** check. A guest also talks *to* the host —
it asks the host's dnsmasq for a DHCP lease and for every DNS answer — and ufw
defaults to `deny (incoming)`. Its `ufw-before-input` chain accepts DHCP *replies*
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

**Standard NAT permits guest-initiated connections to your LAN.** The blanket
route allow restores that standard behavior; NAT blocks unsolicited inbound
connections, not outbound access to private networks. See
[libvirt's network documentation](https://libvirt.org/formatnetwork.html).
To block forwarding to the three RFC 1918 private IPv4 ranges, deny them before
the blanket allow; ufw evaluates rules in order:

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

These rules are not complete guest isolation: they do not restrict traffic
addressed to the host itself, or destinations outside those ranges. They also
do not reliably isolate guests on the same bridge; applying IP firewall rules
to bridged packets depends on the host's bridge netfilter configuration. See the
[Linux bridge documentation](https://cdn.kernel.org/doc/html/latest/networking/bridge.html#netfilter).
Use a separately reviewed host firewall policy if workloads require stronger
isolation.

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

### Turn Off The Spanning Tree Forward Delay

A bridge running STP puts every newly added port through the listening and
learning states before it forwards anything, for the forward delay each time. A
guest's tap device is a new port on every boot, so with the 15-second default
nothing the guest sends leaves the host for 30 seconds: DHCP cannot complete,
`systemd-networkd-wait-online` blocks, cloud-init's network stage waits behind
it, and sshd is the last thing to start. **This adds about 30 seconds to every
`agent-vm create` on that bridge**, and no guest-side setting can shorten it —
the frames genuinely do not pass. `agent-vm doctor` and `agent-vm create` both
warn when the configured bridge is in this state.

Check it, and turn it off:

```bash
ip -d link show br0
sudo ip link set br0 type bridge stp_state 0
```

STP exists to break forwarding loops. Disable it only when the topology cannot
form a loop. If STP is required, retain it and configure convergence through
your bridge manager; setting `forward_delay 0` while kernel STP is enabled is
not a supported shortcut. The Linux bridge documents a 2–30 second range for
that timer. See the [Linux bridge documentation](https://cdn.kernel.org/doc/html/latest/networking/bridge.html).

The runtime setting is lost on reboot. Make it persistent in
whatever manages the bridge — with NetworkManager:

```bash
nmcli connection modify br0 bridge.stp no
nmcli connection up br0
```

`agent-vm` never changes this for you: host network configuration is yours.

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

## 8. Hosts Whose Kernel Cannot Boot The libguestfs Appliance

Skip this section unless `agent-vm doctor` tells you to. Almost every host is
fine, and the ones that are not are fine after one directory and one setting.

libguestfs does all of its work — building the base image, writing the
cloud-init seed — inside a small VM it boots for the purpose, called the
*appliance*, and `supermin` builds that appliance around **the host's own
kernel**. That works with any general-purpose distribution kernel. It does not
necessarily work with a kernel built for one machine rather than for machines in
general: an Apple Silicon (Asahi) kernel, for instance, is configured for that
hardware alone and has neither the PL011 serial port nor the generic PCIe host
bridge that QEMU's `virt` board provides. The appliance then boots with no
console and no disks, and libguestfs reports only:

```
libguestfs: error: appliance closed the connection unexpectedly.
```

`doctor` names this before you hit it, reporting the missing kernel options:

```
 FAIL  libguestfs appliance   the host kernel is missing CONFIG_SERIAL_AMBA_PL011 … and CONFIG_PCI_HOST_GENERIC …
```

The fix is to give libguestfs a general-purpose kernel to build the appliance
from. **That kernel is never booted by the host** — only by QEMU, inside the
appliance — so it does not have to support this machine's hardware at all, and
installing it as a *bootable* kernel is neither required nor recommended.

Unpack any general-purpose kernel package for this architecture into a directory
**named after the kernel's version**, holding the kernel image as `Image` and a
module tree as `modules`, and index the modules with `depmod`. On Arch ARM, using
the stock `linux-aarch64` package:

```bash
# Fetch only the kernel package and unpack it without installing it.
appliance_work=$(mktemp -d)
curl -fL -o "$appliance_work/kernel.pkg.tar" "$(pacman -Sddp linux-aarch64)"
mkdir "$appliance_work/unpack"
tar -C "$appliance_work/unpack" -xf "$appliance_work/kernel.pkg.tar"

# Use the version of the downloaded package's module tree.
version=$(basename "$appliance_work"/unpack/usr/lib/modules/*)
dir=~/.local/share/agent-vm/appliance-kernel/$version
mkdir -p "$dir"
cp "$appliance_work/unpack/boot/Image" "$dir/Image"
cp -aT "$appliance_work/unpack/usr/lib/modules/$version" "$dir/modules"

# depmod wants a tree rooted at <basedir>/lib/modules/<version>.
mkdir -p "$appliance_work/depmod/lib/modules"
ln -s "$dir/modules" "$appliance_work/depmod/lib/modules/$version"
depmod -b "$appliance_work/depmod" "$version"
printf 'appliance_kernel = "%s"\n' "$dir"
```

Copy the printed `appliance_kernel` line into
`~/.config/agent-vm/config.toml`, or set `AGENT_VM_APPLIANCE_KERNEL` to the same
path. The directory name supplies the kernel version, so that one setting is
the whole configuration. Run
`agent-vm doctor` again; the appliance check turns green and names the directory
in use. The kernel and modules cost roughly 250 MiB and are read-only
thereafter.

**Clear the appliance cache after changing this.** `supermin` caches the built
appliance under `/var/tmp/.guestfs-$UID/` and decides whether to rebuild it from
the age of its *inputs* — which do not include this setting. A cache built
before you set `appliance_kernel` is therefore reused, and libguestfs goes on
failing exactly as it did:

```bash
rm -rf /var/tmp/.guestfs-$(id -u)
```

With a remote hypervisor the path names a directory on **that** machine, and the
kernel has to match its architecture, not this one's. Use its full absolute path:
`~` in an explicit configuration value expands to the client's home directory.

## 9. Verifying End To End

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

## 10. Driving This Host From Another Machine

`agent-vm` can drive a hypervisor over ssh:

```bash
agent-vm --libvirt-uri qemu+ssh://kvm@hypervisor.lan/system doctor
```

Set it permanently in the client's config file instead of typing it each time:

```toml
libvirt_uri = "qemu+ssh://kvm@hypervisor.lan/system"
```

**Everything in this document applies to the hypervisor, not to the client.**
The packages in §2, the group membership in §3, the storage in §4, the NAT
network and firewall rules in §5, and the bridge in §6 are all that host's;
`agent-vm` builds base images, creates disks, and defines domains there
([ADR-0010](./decisions/0010-drive-a-remote-hypervisor-by-running-host-tools-over-ssh.md)).

### On the hypervisor

Everything in §§1–7, plus `flock` (util-linux), `find` (findutils), and
coreutils — which any Linux host running libvirt already has. `ssh` and `gh` are
**not** needed there; they are needed on the client.

The account named in the URI is the one that matters: it needs the `kvm` and
`libvirt` group membership from §3, and its home directory is where the state
directory goes by default. `agent-vm doctor` run from the client reports both.

### On the client

Only `ssh` (and `gh`, if you use `--github-ssh-key`). No KVM, no libvirt, no
podman, no libguestfs.

Key-based login must work without a prompt, because `agent-vm` captures ssh's
output and a prompt would look like a hang:

```bash
ssh kvm@hypervisor.lan true    # must succeed silently
```

Use an SSH agent or a default identity. If the key is somewhere else, name it in
the URI: `qemu+ssh://kvm@hypervisor.lan/system?keyfile=/home/me/.ssh/hv_ed25519`.
A non-standard port goes in the URI too:
`qemu+ssh://kvm@hypervisor.lan:2222/system`.

### Where things live

| | Location |
|---|---|
| Base images, overlays, `vm.json`, locks | The hypervisor, under `~/.local/share/agent-vm` of the account in the URI, or the `--state-dir` you name — which is a path **on that host** |
| Guests | The hypervisor's networks. `agent-vm ssh` reaches them with `ssh -J kvm@hypervisor.lan` |
| Your GitHub login | The client. No GitHub credential is sent to the hypervisor |

### What `doctor` cannot tell you from here

The hypervisor's firewall (§5) and whether its QEMU account can reach its state
directory (§4) both need that machine's configuration files and passwd database.
Those two checks report `skip` from the client. If guests boot but their outbound
connections hang, or a `create` fails with a permission error on the overlay, run
`agent-vm doctor` on the hypervisor itself.

### Verifying

```bash
export AGENT_VM_LIBVIRT_URI=qemu+ssh://kvm@hypervisor.lan/system
agent-vm doctor
agent-vm create smoke-test --dry-run   # prints the ssh invocations it would run
agent-vm create smoke-test
agent-vm ssh smoke-test -- uname -a
agent-vm destroy smoke-test --yes
```

## Troubleshooting

| Symptom | Likely cause | Where to look |
|---|---|---|
| `exit 3`, cannot connect to libvirt | Service down, or user not in `libvirt` | `systemctl status libvirtd`, `id` |
| `exit 3`, tool missing or too old | Host package missing or below the floor | `agent-vm doctor`, the package table above |
| A tool failed and you want to reproduce it | — | The error names the tool, argv, exit status, and the machine it ran on; rerun it there, or use `--dry-run` |
| `cannot reach the hypervisor host … over ssh` | Key-based login is not working non-interactively | `ssh <destination> true` must succeed silently; see [§10](#10-driving-this-host-from-another-machine) |
| `the tls transport is not supported` | A remote URI that gives no shell on the hypervisor | Use `qemu+ssh://`; see [§10](#10-driving-this-host-from-another-machine) |
| `exit 3`, `/dev/kvm` unusable | Virtualization disabled, or user not in `kvm` | firmware settings, `ls -l /dev/kvm` |
| `Cannot access storage file ... Permission denied` on create | The hypervisor's account cannot search a directory above the state directory | `agent-vm doctor` (state directory access), then `setfacl -m u:<qemu user>:x` on the directory it names |
| `image build` fails in libguestfs | Broken appliance, or no `/dev/kvm` for the appliance | `libguestfs-test-tool` |
| `the appliance closed the connection unexpectedly`, with no console output at all | The host kernel cannot boot QEMU's `virt` board, or a cached appliance was built before `appliance_kernel` was set | `agent-vm doctor` (libguestfs appliance), then [§8](#8-hosts-whose-kernel-cannot-boot-the-libguestfs-appliance) |
| `image build` fails pulling | Registry unreachable, proxy, or rate limit | `podman pull <ref>` by hand; the error names the registry |
| `exit 6`, guest never reachable | Boot failure or cloud-init failure | `vms/<name>/console.log`, `agent-vm console <name>` |
| VM starts, no address | DHCP or NIC problem — including a host firewall dropping the guest's DHCP request to the host (ufw defaults to `deny (incoming)` and does not allow port 67 on the bridge) | `agent-vm doctor` (host firewall guest services), `virsh net-dhcp-leases agent-vm-nat`, console log, `sudo ufw status \| grep virbr` |
| VM boots, SSH and DNS work, but outbound connections hang (`apt update` at 0%) | A host firewall is dropping forwarded traffic — commonly `ufw` with `DEFAULT_FORWARD_POLICY="DROP"` | `agent-vm doctor` (host firewall forwarding), then [Host Firewalls And The `virbrN` Bridge](#host-firewalls-and-the-virbrn-bridge) |
| Bridged VM takes ~30 s longer than a NAT one to accept SSH | The host bridge runs STP with a non-zero forward delay, so the guest's tap port does not forward while DHCP is trying | `agent-vm doctor` (host bridge), `ip -d link show <bridge>`, then [Turn Off The Spanning Tree Forward Delay](#turn-off-the-spanning-tree-forward-delay) |
| The same hang, on a host where the ufw rule used to work | libvirt allocated a different `virbrN` and the rule no longer matches | `virsh net-info agent-vm-nat \| grep Bridge`, compare with `sudo ufw status` |
| Bridged VM has no address | Bridge down, or no DHCP on that VLAN | `ip -br link`, LAN DHCP server |
| Disk full mid-task | Thin overlays grew | `du -sh` on the state directory |
| `exit 7` after a failure | Cleanup was incomplete | The error names exactly what is left; `virsh list --all` |

Because everything is ordinary libvirt, standard tooling works for
investigation — `virsh list --all`, `virsh dumpxml <name>`,
`virsh domblklist <name>`, `journalctl -u libvirtd`. Prefer read-only `virsh`
commands: destroying or undefining a domain behind the tool's back leaves its
state directory orphaned.
