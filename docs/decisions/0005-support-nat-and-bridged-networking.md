# 5. Support NAT and bridged networking, with NAT as the default

Date: 2026-08-17

## Status

Accepted

## Context

Agent VMs need outbound network access — package installs, `git`, API calls.
Some workflows also need *inbound* access from other machines: a teammate opening
a dev server the agent started, CI reaching the VM, or a device on the LAN talking
to it.

The two modes libvirt gives us differ in exactly the way that matters here:

- **NAT** (libvirt-managed network with `dnsmasq` DHCP behind a `virbr`
  interface): the guest reaches the internet and the host; nothing on the LAN can
  reach the guest. Works on laptops, works on wireless, needs no host network
  configuration, and works under `qemu:///session` unprivileged.
- **Bridge** (guest attached to a host bridge): the guest is a peer on the LAN
  with its own DHCP address. Requires a pre-existing bridge, requires
  `qemu:///system`, cannot be done over most wireless interfaces, and removes the
  NAT boundary that keeps an untrusted guest unreachable.

Since the guest is untrusted by design, putting it on the LAN by default would be
handing an agent a machine anyone on the network can connect to — a boundary
removed silently, for a capability most tasks never use.

We also considered offering only NAT with per-port host forwarding. That covers
"reach one service in the guest" but not "the guest must look like a LAN host"
(mDNS, DHCP reservations, multiple ports, non-TCP protocols), and it would mean
owning port allocation and firewall rules on the host — which we have decided not
to do.

## Decision

Support both modes. **NAT is the default**, on a libvirt network named
`agent-vm-nat` that the tool defines and activates on first use.

**Bridged mode is opt-in per VM** and always requires an explicit flag:

```bash
agent-vm create agent-04 --network bridge --bridge br0
```

The bridge must already exist. `agent-vm` validates that it is present and up
and then attaches to it; it never creates, modifies, or deletes host network
interfaces, and never edits host firewall rules. A missing or down bridge is a
host-readiness failure (exit `3`) reporting the `nmcli`/`ip` command needed to fix
it — not a partially created VM.

A default bridge may be configured (`[network.bridge] interface = "br0"`) so the
name need not be repeated, but the `--network bridge` choice itself is never
implicit. Bridge setup is documented in `docs/host-setup.md`; the security
implication is stated in `SECURITY.md`.

## Consequences

Easier:

- The safe mode is the zero-configuration mode: NAT works on a fresh laptop with
  no host networking changes and no root.
- Choosing exposure is explicit and per-VM, visible in the command line, in
  `vm.json`, and in the domain XML.
- Host networking stays the operator's responsibility, which keeps this tool out
  of the business of reconfiguring interfaces and firewalls.

Harder:

- Two network paths to implement, test, and document; address discovery differs
  (libvirt DHCP leases for NAT, guest agent or LAN DHCP for bridge), and the
  integration suite must cover both.
- Bridged mode has host prerequisites we cannot satisfy for the user, so some
  users will hit exit `3` and need the setup guide.
- Reaching a service in a NAT'd guest from another machine requires either
  bridged mode or the operator's own SSH forwarding; we deliberately provide no
  port-forwarding feature.
- Bridged VMs consume LAN addresses and appear on the network as unknown hosts,
  which can surprise network administrators.
