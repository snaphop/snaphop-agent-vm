# 20. Publish SSH with mDNS on a bridged guest

Date: 2026-10-18

## Status

Accepted

Extends [ADR-0005](./0005-support-nat-and-bridged-networking.md). Bridge remains
an explicit network mode. This decision adds what a bridged guest announces
on that LAN. It does not add a network mode.

## Context

Bridge exists so other machines can connect to the guest. The guest receives
an address from the LAN's DHCP server. `agent-vm ssh` learns that address
from the QEMU guest agent, because this tool can ask libvirt. Another machine
on the LAN cannot. The person who wants to open the VM has to find the lease
first.

mDNS is how a machine on a LAN publishes its own name. `ssh <user>@<name>.local`
and a DNS-SD record for `_ssh._tcp` are the two things a person looks for.

NAT must stay unpublished. A NAT guest is not a host on the LAN, and its name
would be announced only on the host-local NAT network. The seed for a NAT
guest is also a public contract that existing VMs were created with. Adding a
publication there would change that contract for a mode that was chosen so
the guest would not be found.

The base images already install and enable systemd-resolved and
systemd-networkd. resolved publishes mDNS and DNS-SD when two switches are
on: `MulticastDNS=yes` in `resolved.conf`, and per-link mDNS on the guest's
NIC. Per-link mDNS defaults to off. DNS-SD services are files in
`/etc/systemd/dnssd/`. resolved reads those files when it starts, and it
expands `%H` from the hostname at that moment.

Avahi would publish the same records and would also need a new package in
every image. It binds UDP 5353, which systemd-resolved already owns, so the
images would have to be rebuilt before any guest could advertise. That
rebuild is the cost this decision avoids.

cloud-init's local stage renders the fallback DHCP network before
systemd-networkd starts, and it runs no user-data modules. `bootcmd` runs in
the network stage, after the network is up and before SSH is offered, on
every boot. A `.network` file written at that point cannot beat the file
cloud-init already installed, and a file sorted ahead of it would hide
cloud-init's DHCP configuration. `networkctl reconfigure` also needs a device
name and can renew the lease. Per-link mDNS is therefore set with
`resolvectl mdns <iface> yes` each boot. That setting is not stored by
networkd, which is why the command has to run every boot.

Restarting resolved while chrony is making its first DNS lookup trips the
image's retry backoff, and sshd waits on the clock. The publication files
are written only when their contents differ from what is already on disk, and
resolved is restarted only then. The second boot finds the files in place,
starts resolved with them, and only flips the per-link switch.

The VM name is already a single DNS label: lowercase letters, digits, and
hyphens, 2 to 32 characters. It is a valid mDNS name. `bootcmd` runs before
cloud-init's `set_hostname` module, so the script sets `/etc/hostname` and
the live hostname before any restart. `%H` is then the VM name.

`agent-vm ssh` must keep using the address libvirt reports. The guest is
untrusted, and so is every other machine on a bridged LAN. Connecting to
`<name>.local` would let any of them answer for that name. A hypervisor
reached over `qemu+ssh://` and a client that does not resolve mDNS would
also lose the ability to open the guest.

## Decision

A VM created with `--network bridge` publishes its hostname and SSH with
mDNS. A NAT VM does not. There is no flag, config key, or environment
variable for this. Choosing bridge is already the explicit decision to put
the guest on the LAN.

The publication is cloud-init `bootcmd` in the generated user-data. When the
operator also passes `--user-data`, that `bootcmd` is a following cloud-config
part merged with `list(append)`. The settings part replaces lists it names,
which would drop an operator `bootcmd`. The publication part keeps theirs
and runs it first. The script:

- Write `/etc/hostname` and set the live hostname to the VM name. A missing
  `hostname` command, which the Arch images do not ship, and a failed
  `hostnamectl` are ignored.
- Write `/etc/systemd/resolved.conf.d/10-agent-vm-mdns.conf` with
  `MulticastDNS=yes`, and `/etc/systemd/dnssd/ssh.dnssd` advertising
  `_ssh._tcp` on port 22 under `%H`.
- Restart `systemd-resolved` only when one of those files changed and
  resolved is already running.
- Turn on per-link mDNS for every interface whose name starts with `en` or
  `eth`. Those are the predictable and classic names of the virtio NIC this
  tool attaches. Other names, including `docker0`, are left alone. If none
  match, the script fails and cloud-init records it. SSH through the guest
  agent still works.

No package is installed. No Containerfile changes. No base image is rebuilt.
A cached image already runs resolved, so the next `create` of a bridged VM
publishes. A VM that already exists keeps the seed it was created with until
it is created again.

`agent-vm ssh`, `create`'s own SSH session, and the address recorded for the
VM continue to use `virsh domifaddr`. `--dry-run` of a bridged create says
that the guest will publish `<name>.local`.

## Consequences

Easier:

- Another machine on the LAN can open the guest with
  `ssh <user>@<name>.local` without looking up the DHCP lease.
- Cached base images keep working. Nothing new is installed on the host, and
  the host's firewall, routes, and resolver are unchanged.
- NAT user-data gains no publication. The comment at the top of every seed
  now says that a bridged guest publishes and a NAT guest does not.

Harder:

- The first boot restarts resolved when the publication files are new. That
  can overlap chrony's first lookup. Later boots do not restart it.
- Per-link mDNS begins when `bootcmd` runs. That is before SSH is offered, and
  it is not stored in a networkd file, so a guest whose bootcmd did not run
  does not announce.
- A NIC whose name does not start with `en` or `eth` is not published.
- If setting the live hostname fails before the one restart, `%H` can be the
  image hostname until the next boot. `/etc/hostname` is already the VM name,
  and the next start of resolved reads it.
- A VM created before this change does not publish until it is created again.
- Anyone on the LAN who resolves mDNS can find the guest. That exposure is
  the exposure bridge already chose.
