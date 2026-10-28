# 25. Create an optional swap file on the guest root filesystem

Date: 2026-10-28

## Status

Accepted

Extends [ADR-0007](./0007-default-vm-resource-profile.md). The default resource
profile is unchanged. A guest has no swap file unless the operator asks for
one.

## Context

Memory is the binding constraint on a developer laptop (ADR-0007). A build
that briefly needs more RAM than the guest was given is killed. Swap lets
that build finish, slowly, by using the guest's disk.

Three ways to provide it were considered.

A second virtio disk used only for swap would survive a full root filesystem
and would show up as its own device. It would also change the `virt-install`
argument vector, which is a public contract, and it would add a `qemu-img`
invocation and a partition for every VM that asked.

A shell script in `runcmd` would create the file with `fallocate`, `mkswap`,
and an `fstab` line. cloud-init's mounts module already does that work, and a
`runcmd` list in the settings part would replace an operator `runcmd`.

cloud-init's mounts module reads a `swap` key, creates the file after
`growpart` and `resizefs` have expanded the root filesystem to the virtual
disk, marks it mode 600, runs `mkswap`, appends it to `/etc/fstab`, and
activates it. The module is in cloud-init's default init-stage list on every
supported distro, so a base image already cached can create the file. No host
tool is added (ADR-0009).

The file is written, so a thin overlay grows by about the swap size on the
host. Unused virtual disk does not. The size is explicit. ADR-0007 leaves
sizing with the operator, so `auto` is not accepted. cloud-init creates the
file in whole mebibytes and truncates any other size, so a size that is not a
whole number of mebibytes is refused before a VM exists. At least 1 GiB of
the virtual disk stays for the root filesystem. The image's own files also
occupy that filesystem. When the remainder is still too small, cloud-init
logs a warning and the guest boots without the file. `create` cannot see the
free space until the guest has resized the filesystem.

The same `agent-vm create` command has to keep producing the same machine
(ADR-0007). Swap therefore stays off unless a size is set. `0` clears a size
that came from the config file or the environment. A VM that already exists
does not gain a file; it gets one when it is created again. `agent-vm update`
does not add or resize one.

`vm.json` records the size and omits the field when there is none, so a
record written before the field existed stays valid at `schemaVersion` 1.

When `--cloud-init` also sets a `swap` key, the generated settings part is
applied after the operator's and replaces that key. A create with no swap
size leaves the operator's key as written.

## Decision

`agent-vm create --swap <size>` creates `/swapfile` on the guest's root
filesystem at first boot. The same size can be set with `[defaults] swap` in
the config file or with `AGENT_VM_SWAP`. Unset means no swap file.

The generated user-data carries:

```yaml
swap:
  filename: /swapfile
  size: "<size>"
```

`vm.json` records the size as `resources.swap` when one was set.

## Consequences

Easier:

- A guest can absorb a short spike past its RAM without a second disk and
  without a base image rebuild.
- The default VM is unchanged. Existing user-data, `virt-install` arguments,
  and `vm.json` records stay as they were.

Harder:

- The swap file consumes real space on the host, because it is written into
  the thin overlay.
- A size that would leave less than 1 GiB of virtual disk is refused. A size
  that still does not fit beside the image's own files is a cloud-init
  warning, and the guest boots without swap.
- Changing the size means creating the VM again.
