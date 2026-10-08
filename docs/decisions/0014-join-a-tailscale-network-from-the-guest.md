# 14. Join a Tailscale network from inside the guest

Date: 2026-10-04

## Status

Accepted

Extends [ADR-0005](./0005-support-nat-and-bridged-networking.md) (NAT and bridged
networking). The libvirt attachment is unchanged. Tailscale is an overlay the
guest brings up for itself after it boots.

## Context

An agent VM often has to reach a private service that is only on a tailnet, or
the operator wants to reach the VM by a tailnet address. NAT and bridge do not
provide that. NAT keeps the guest off the LAN. Bridge puts it on the LAN,
which is a different and wider exposure than membership of one tailnet.

Doing the join on the host is the wrong place. It would mean routing, firewall,
or Tailscale changes on the operator's machine, which this tool does not make
([ADR-0005](./0005-support-nat-and-bridged-networking.md), `SECURITY.md`). The
guest already has outbound networking through whichever libvirt mode was
selected, and that is enough to reach a coordination server.

The credential cannot travel the way an SSH public key does. A Tailscale auth
key is a secret. `SECURITY.md` forbids putting one in a base image, a
cloud-init seed, `vm.json`, a log, or an argument vector. A base image is
shared by every VM built from it. The seed sits on disk for the life of the
VM. Argument vectors are logged. The GitHub Actions runner faces the same
constraint and is registered after boot, with the token supplied then
([ADR-0013](./0013-self-hosted-github-actions-runner-variant.md)). When an
organization is named, `create` fetches that token on the client and passes
it on stdin
([ADR-0015](./0015-register-a-github-actions-runner-with-gh.md)).

Tailscale's own `tailscale up --auth-key=file:…` reads the key from a file.
The three families this tool supports do not all ship that package, and a
cached base image has to stay bootable without a rebuild, so the package is
installed on first join rather than baked into the image.

## Decision

`agent-vm create --tailscale-auth-key-file <path>` joins that guest to a
Tailscale network after SSH accepts a login. It is off unless the flag is
given. No config key and no environment variable turns it on. NAT remains the
default libvirt network mode, and `--network bridge` is unchanged.

- **The key is read from a file.** There is no flag that takes the key as a
  value. The file is checked before anything is created. Its contents are
  never logged, never written to the seed or to `vm.json`, and never placed
  in an argument vector.
- **The guest joins itself.** Once SSH is up, `create` copies an embedded
  script to the guest and runs it as root. `install` fetches Tailscale's
  installer only when `tailscale` is not already on `PATH`, then starts
  `tailscaled`. `up` reads the auth key on stdin, writes it under `/run`,
  runs `tailscale up --auth-key=file:… --reset`, so this join is the whole
  configuration, and removes the file before it exits. The key is sent only
  to `up`, after install has returned.
- **What else may be set, all opt-in:** `--tailscale-hostname` (the VM name
  when omitted), `--tailscale-login-server` (an `https` coordination server,
  for a network other than Tailscale's), `--tailscale-advertise-tag`
  (repeatable), `--tailscale-ephemeral`, and `--tailscale-ssh`.
- **The join is recorded without the key.** `vm.json` gains an optional
  `tailscale` object: hostname, and, when they were set, the login server,
  tags, ephemeral, Tailscale SSH, and the IPv4 address the guest reported.
  `list` and `info` show that the guest is on a tailnet. The address is
  display only. SSH still uses the address libvirt reported.
- **Failure after the VM is recorded leaves the VM in place,** as a GitHub
  key registration failure does. The auth key is not retried from disk,
  because it was not stored.
- **Destroy does not remove the tailnet node.** An ephemeral auth key, or
  `--tailscale-ephemeral`, removes the node when the guest stops. Any other
  node stays until it is removed in the tailnet's admin console.

The guest is untrusted and can read the key while `tailscale up` runs. The
key should be one-time and ephemeral, and tagged when the tailnet uses tags,
so it can add this node and nothing else.

## Consequences

Easier:

- A VM can join a tailnet without a host route, a host firewall change, or a
  secret in the image or the seed.
- Cached base images keep working. Tailscale is installed into the guest that
  asked for it.
- Exposure is visible afterwards: the flag is on the command, and `vm.json`
  records the join.

Harder:

- The guest needs outbound network and a working SSH login. `--wait-for-ssh 0`
  is refused when a join was requested.
- The first join downloads Tailscale's installer. A guest that already has
  `tailscale` on `PATH` skips that download.
- For the minute `tailscale up` runs, the auth key is on the guest. A
  reusable admin key is the wrong key to give it.
- A node that was not ephemeral survives `destroy` and has to be removed in
  the tailnet.
