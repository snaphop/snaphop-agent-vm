# 18. Set up a supported host from the command

Date: 2026-10-08

## Status

Accepted

## Context

Preparing a host was a document. `docs/host-setup.md` listed the packages,
the libvirt service, the groups, and the search grant the QEMU account needs
before it can open a disk under a home directory. An operator typed those
commands, and the package names differ by family and, on Ubuntu, by
architecture. `doctor` could report what was missing. It could not install it.

The guest images already install this stack, so a VM can run agent-vm. The
host should be able to reach the same state from one command, on the same
releases those images support: Ubuntu 24.04 and 26.04, Fedora 43 and 44, and
Arch Linux.

Arch's guest tags are container images of the rolling release. A host reports
`ID=arch` and a `VERSION_ID` that is a build stamp, not one of those tags.
Every Arch Linux host is the supported rolling release. A derivative that only
sets `ID_LIKE` is a different distribution and is not accepted.

Installing packages, starting a service, changing group membership, and
granting an ACL outside the state directory are host changes the rest of the
tool is forbidden to make. They belong on one command the operator asks for,
and they stop at the boundary the host-setup guide already draws: no firewall
edits, no bridges, and no change to the profile QEMU runs under.

## Decision

`agent-vm setup` prepares the hypervisor named by the libvirt URI.

- It accepts Ubuntu and Fedora hosts whose `VERSION_ID` is one of that
  family's supported guest tags, and Arch hosts with `ID=arch`.
- It installs that family's packages through the family's package manager,
  enables `libvirtd` when that unit exists and `virtqemud` otherwise, and adds
  the account agent-vm runs as to the groups `doctor` already requires.
- On a system connection it grants the QEMU account search permission, and
  only search permission, on directories above the state directory that the
  account cannot search.
- Privileged commands run under `sudo -n`. A password prompt fails with the
  command to run first. `--yes` skips the confirmation. `--dry-run` prints the
  plan.
- When the state directory is under a hidden home directory
  (`/home/<user>/.*` or `/root/.*`) and virt-aa-helper's AppArmor profile
  denies `@{HOME}/.*/**`, it writes one local rule granting that helper read
  and lock on the state directory only, then reloads the profile with
  `apparmor_parser`. The rule is what lets the helper add a qcow2 backing
  file to the per-VM profile. A state directory the shipped profile already
  allows, a host without that profile, and a profile that does not carry the
  deny are left untouched. An existing copy of the same rule is left in place.
- It does not change firewall rules, bridges, routing, nested-virtualization
  module options, or SELinux. It does not disable AppArmor, put a profile in
  complain mode, or change the profile QEMU runs under. `--github` is the
  only way it installs the optional GitHub CLI, and it installs that on the
  client. A remote hypervisor does not get `gh`, because `gh` does not run
  there.

The package lists live in `internal/hostsetup`, next to the guest releases in
`internal/image/distro`. A new supported tag is a host release as well as a
guest release.

## Consequences

Easier:

- A supported host goes from a fresh install to `agent-vm doctor` without
  transcribing package names.
- The releases a host can be are the releases a guest can be, so the two do
  not drift.

Harder:

- Setup mutates the host outside the state directory. That exception is
  specific to this command and is stated in `SECURITY.md`.
- Group membership applies to the next login, not to the current one. Setup
  says so.
- Ubuntu 26.04's virtual `qemu-kvm` package cannot be installed by that name.
  The command installs `qemu-system-x86` or `qemu-system-arm`, which is also
  what the guest image installs.
- The virt-aa-helper rule is a real AppArmor change, limited to one helper
  and one directory. It exists because the shipped deny hides the default
  state directory, and a Unix search grant does not fix that. QEMU's own
  profile stays the one libvirt generates for the domain.

## Amendment

2026-10-09. The original decision left AppArmor completely alone. On Ubuntu
the shipped virt-aa-helper profile denies `@{HOME}/.*/**`, so a state
directory under `~/.local` cannot be read by the helper. The helper then
omits the overlay's backing file from the per-VM profile, and QEMU reports
`Permission denied` on `base.qcow2` even when the QEMU account can search
every directory on the path. Setup now writes the one local override
described above. SELinux and QEMU's profile stay out of reach.
