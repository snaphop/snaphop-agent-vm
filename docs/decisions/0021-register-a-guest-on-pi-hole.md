# 21. Register a guest on Pi-hole

Date: 2026-10-18

## Status

Accepted

Extends [ADR-0005](./0005-support-nat-and-bridged-networking.md) and
[ADR-0009](./0009-orchestrate-existing-host-cli-tools.md). NAT and bridge stay
the libvirt attachments. This decision adds an opt-in DNS record on a Pi-hole
the operator already runs. It does not add a network mode, a host package, or
a guest package.

## Context

A bridged guest publishes `<name>.local` with mDNS
([ADR-0020](./0020-publish-ssh-with-mdns-on-a-bridged-guest.md)). That name
works for machines on the same LAN that resolve mDNS. It does not help a
machine that uses a Pi-hole as its DNS server, and it does not give the
operator a name under a domain they chose.

The useful record is `<vm-name>.agent` on that Pi-hole, pointing at the
address libvirt reports. The operator asked for `.agent`. The value stored
and printed has no leading dot, so the name is `build-01.agent`.

The password that can change Pi-hole is a host credential. The guest is
untrusted. Putting the password in cloud-init, a base image, or the guest
would hand it to every agent that runs there. A Tailscale auth key and a
GitHub Actions registration token cross into the guest once, on stdin, because
the guest is what consumes them
([ADR-0014](./0014-join-a-tailscale-network-from-the-guest.md),
[ADR-0015](./0015-register-a-github-actions-runner-with-gh.md)). This password
is consumed on the client. It has no reason to enter the guest at all.

Pi-hole v6 (FTL) keeps local DNS in `dns.hosts`, an array of `IP hostname`
lines. `PUT` and `DELETE` of one line rewrite the hosts file and reload
dnsmasq. There is no local CLI that manages a Pi-hole on another machine.
[ADR-0009](./0009-orchestrate-existing-host-cli-tools.md) says to call the
tool that already does the job, and to record why when none does. An HTTP
client in this process is that case. Pi-hole v5 (`api.php?customdns`,
`dns.hostRecord`) is a different API and is not spoken here.

A URL in the config file must not register every VM. Pi-hole is shared DNS.
Creating a VM and publishing a name are different decisions.

A NAT guest's address is reachable from the hypervisor. The record is still
useful on that machine, and refusing NAT would reject a request the operator
made. The success text says who can open the name.

`agent-vm ssh` must keep using the address libvirt reports. `<name>.agent`
can be stale, or another party can answer for it. The same rule already
keeps `ssh` off `<name>.local`.

## Decision

`agent-vm create --pihole` registers the guest on a Pi-hole v6 server after
libvirt reports an address, and before the guest update. It is off unless
that flag is set. A configured URL, password file, or domain does not
register a VM on its own. Other `--pihole-*` flags without `--pihole` are a
usage error.

- **The password is read from a file.** `--pihole-password-file`,
  `pihole.password_file`, or `AGENT_VM_PIHOLE_PASSWORD_FILE` names it. There
  is no flag that takes the password as a value. The file is a regular file,
  mode `600`, at most 4096 bytes, and one line. It is checked before anything
  is created, including under `--dry-run`. Its contents are never logged,
  never placed in a URL or an argument vector, and never written to a base
  image, a cloud-init seed, the guest, or `vm.json`. The path may be stored,
  so `destroy` and `start` can find the file.
- **The client speaks Pi-hole v6.** `POST /api/auth` with
  `{"password":"…"}` returns `session.sid`. Later calls send that value in
  `X-FTL-SID` and `sid`. Local DNS is `GET /api/config/dns/hosts`, then
  `PUT` or `DELETE` of `/api/config/dns/hosts/<IP hostname>` with the line
  escaped in the path (`%20` for the space). A line that already has the
  name and the address is left alone. A line that has the name at another
  address is removed as Pi-hole returned it, and any other names on that
  line are written back. `DELETE` of a missing name is success. HTTP
  redirects are not followed, so the password is not replayed. The client
  does not use an HTTP proxy. The session id is not logged. A logout that
  fails after Pi-hole accepted the change is a warning; the name is still
  recorded, because a failed command would skip `vm.json` while the record
  already exists. The session expires on its own.
- **TLS.** The default client verifies the certificate. `--pihole-insecure`,
  `pihole.insecure`, or `AGENT_VM_PIHOLE_INSECURE` skips verification. That
  setting does not authenticate the server. It is the operator's choice for
  a LAN Pi-hole that presents a certificate nobody signed.
- **The name.** The default domain is `agent`. `--pihole-domain` overrides
  it. A leading dot is rejected (`agent`, not `.agent`). A trailing dot is
  removed. The full name is a DNS name of at most 253 characters.
- **The address** is the one libvirt reports, in canonical form. NAT and
  bridge are both accepted. On success a NAT create says the name points at
  an address only this host can reach. A bridge create says other machines
  that use this Pi-hole can open the name. Registration does not change host
  routes, firewalls, or the guest. Machines resolve the name when they use
  that Pi-hole. `.agent` is not a special-use name on the public DNS.
- **The record omits the password.** `vm.json` gains an optional `pihole`
  object: `url`, `name`, `address`, `passwordFile`, and `insecure` when
  verification was skipped. `schemaVersion` stays 1. `list` appends
  `+pihole`. `info` shows `name -> address`. `agent-vm ssh` uses the address
  libvirt reported.
- **Lifecycle.** `destroy` deletes a recorded name before it undefines the
  domain, after the GitHub SSH key and the Actions runner. It uses the URL
  stored in `vm.json` unless this invocation passes `--pihole-url`, so a
  later config file cannot delete the name from a different server. The
  password file is the flag, environment, or config file when one of those
  set it, and the recorded path otherwise. `--keep-disk` still removes the
  name and clears the object. `stop` leaves the name in place. `start` and
  `restart` update it when libvirt reports a different address. The same
  address does not call Pi-hole, so start works without the password file
  until the address changes. An address that is missing or unparsable leaves
  the old record and does not fail start.
- **Failure after the VM is recorded leaves the VM in place.** A failed
  registration does not write `pihole` and does not continue into the guest
  update, Tailscale, or GitHub. An update failure leaves a name that was
  already recorded. `--dry-run` prints the planned call and the password-file
  path. It does not log in and does not change Pi-hole or `vm.json`.
- **`doctor` does not require Pi-hole.** Absence of a server is not a host
  that is unready. Pi-hole v5 answers with a remedy that names the v6 path.
  HTTP 401 and 403 name `webserver.api.app_sudo`: an application password
  can change config only when that setting is true.

## Consequences

Easier:

- A bridged guest can be opened as `<name>.agent` by machines that use the
  operator's Pi-hole, without a host route or a firewall change.
- The password stays on the client. A guest, a seed, and a base image never
  receive it.
- `destroy` removes the name it created. Exposure is visible in `vm.json`,
  `list`, and `info`.

Harder:

- The operator runs Pi-hole v6, and the client must be allowed to change
  `dns.hosts`. An application password needs `webserver.api.app_sudo`.
- `--pihole` needs a boot wait, because the address comes from libvirt after
  the guest boots.
- A NAT name points at an address other machines cannot reach. The record
  does not make that guest reachable.
- `--pihole-insecure` accepts whatever certificate the named host presents.
- A Pi-hole that is gone blocks `destroy` until the operator fixes the URL
  or removes the `pihole` object from `vm.json`.
- `agent-vm ssh` does not follow `<name>.agent`. That is intentional: the
  tool must not be redirected by DNS.
