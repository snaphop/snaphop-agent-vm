# 26. Write a git identity for the VM

Date: 2026-10-28

## Status

Accepted

## Context

A guest has `git`, and the work done in it is committed from inside it. Git
refuses to commit until `user.name` and `user.email` are set. Asking the
operator to configure that after every boot is a step the VM can take itself.

The name has to be the VM's name. Two VMs that share a base image must not
share an author, and a base image is built before any VM is named, so the
identity cannot be baked into the image. It has to arrive in the cloud-init
seed, which is already where per-VM settings go (ADR-0011). A cached base
image then needs no rebuild.

The email is `<vm-name>@<domain>`. The domain is the operator's, so it is a
setting: `[git] domain` in the config file, `AGENT_VM_GIT_DOMAIN`, or
`create --git-domain`. Unset means `local`, so a create that names nothing
still has an identity. The value is a DNS domain. It is not a secret.

Three places were considered.

A `runcmd` that runs `git config` needs `git` on `PATH` at that moment and
would replace an operator `runcmd`, because the settings part replaces every
list it names.

`/etc/skel/.gitconfig` is what the login user inherits: cloud-init runs
`write_files` before it creates users. `/root` already exists and does not
consult skel, so it needs its own copy. Neither of those reaches an account
the image created earlier, such as `github-runner`, whose home is
`/opt/actions-runner`.

`/etc/gitconfig` is the file git reads for every account. The images do not
ship one. Writing it from the seed covers the login user, root, and
`github-runner` on a base image that is already cached. The same text is also
written to `/etc/skel/.gitconfig` and `/root/.gitconfig`, so the login user
and root have a `~/.gitconfig` of their own. A later `git config --global` in
the guest replaces the home file and leaves the system file underneath it.

When the operator also passes `--cloud-init`, the settings part must not name
`write_files`: that list would replace theirs. The identity is a later
cloud-config part whose merge rule appends `write_files`, the same split
ADR-0020 uses for `bootcmd`. Where both parts name one of the three paths,
cloud-init writes them in order and the identity is what remains in those
paths.

`vm.json` does not gain a field. The seed's user-data is the record of what
was written, and `schemaVersion` stays 1. A VM that already exists gets the
files when it is created again. `agent-vm update` does not rewrite them.

## Decision

Every `agent-vm create` writes a git identity for that VM:

```
[user]
    name = <vm-name>
    email = <vm-name>@<domain>
```

The domain defaults to `local`. The same file is written to
`/etc/gitconfig`, `/etc/skel/.gitconfig`, and `/root/.gitconfig`.

## Consequences

Easier:

- `git commit` inside a new VM has an author, and that author is the VM.
- The domain is one setting, with the same precedence as the other settings.
- A cached base image does not need a rebuild.

Harder:

- The generated user-data changes for every VM, including one created with
  no new flag. That is a public contract change, pinned by the golden files.
- A VM created before this change has no identity until it is created again.
- An operator `write_files` entry for one of the three paths is overwritten
  by the identity. A different author belongs in another path, or in a
  `git config --global` after boot.
