# 24. Share the full image's Rust home through a rustup group

Date: 2026-10-27

## Status

Accepted

Narrows the shared Rust installation described in
[`cli.md`](../cli.md#go-and-rust) for the full images. The runner image keeps
the arrangement in
[ADR-0022](./0022-install-java-and-rust-on-the-runner-image.md).

## Context

The full images (`ubuntu`, `fedora`, `arch`) install Rust once into
`/usr/local/rustup` with `CARGO_HOME=/usr/local/cargo`, and set
`RUSTUP_HOME=/usr/local/rustup` in `/etc/environment`. The tree was
`root:root`, mode 755. A project that pins a toolchain in
`rust-toolchain.toml` makes cargo install that toolchain on first use, as the
account that ran cargo. In a guest that failed:

```text
error: could not create temp file /usr/local/rustup/tmp/...: Permission denied (os error 13)
```

The only way round it was `sudo`, which an agent running `cargo build` does
not know to use.

The mise store, `/usr/local/lib/mise`, is already shared by every account.
Every directory in it is mode 1777 and files keep the modes they were
installed with. That works for mise because it never changes an install in
place: each version is a directory of its own. rustup does change installs in
place. It adds components and targets to an existing toolchain, rewrites
`settings.toml` and `update-hashes/`, and `rustup update` replaces `stable`
where it stands. Under 1777 the files root installed during the build stay
read-only to every other account, and the sticky bit stops one account from
updating or removing a toolchain another installed.

Setgid directories keep new entries in the directory's group. Under the usual
umask of 022, the new directories are still mode 755 to that group. A
toolchain one account installs is then closed to the next: on a test image,
a second account's `rustup component add rustfmt` failed with "could not
rename 'component' file ... Permission denied" on a toolchain the first
account had installed. A default ACL on each directory fixes that, because it
overrides the umask for what is created inside it.

podman does not keep POSIX ACLs in a committed layer. A test build that ran
`setfacl -R -d -m g:rustup:rwX` in one `RUN` step showed no default ACL in
the next. The mode bits, the owner, the group, and the setgid bit were kept.
The images run systemd, and `systemd-tmpfiles` can set ACLs itself, without
`setfacl` in the image.

## Decision

- The full recipes create a system group, `rustup`, in the same `RUN` step
  that installs Rust. That step also runs `chgrp -R rustup` and
  `chmod -R g+rwX` on `/usr/local/rustup`, and sets the setgid bit on every
  directory in it. A later step would copy the whole toolchain, about
  1.5 GiB, into a second layer.
- `/etc/tmpfiles.d/agent-vm-rustup.conf` holds one rule,
  `A+ /usr/local/rustup - - - - default:group:rustup:rwx`. Every boot, it adds
  a default ACL for the group to every directory in the tree. Files are left
  alone. The rule also covers anything `agent-vm update` or an account
  installed since the last boot.
- The generated cloud-init user-data declares `rustup` and puts the login
  user in it, beside `docker`, `libvirt`, and `kvm`. That makes the first SSH
  session able to install a toolchain. `agent-vm-user-setup` adds every other
  interactive account and skips the group on an image that has none.
- The build fails if any entry in `/usr/local/rustup` is outside the group,
  or if any directory is not setgid and group-writable. A root `rustup`
  command added after the group step would cause either.
- `/usr/local/cargo` stays root's. `CARGO_HOME` stays unset, so
  `cargo install` writes into each account's own `~/.cargo`.
- The runner image is unchanged. Its `RUSTUP_HOME` is owned by
  `github-runner`, the account its jobs run as, and `agent-vm update` hands
  the tree back to that account after `rustup update` (ADR-0022). The slim
  and nix images ship no rustup.

## Consequences

Easier:

- Any interactive account can run `rustup toolchain install`,
  `rustup component add`, `rustup target add`, and `rustup update` without
  `sudo`. That includes the install cargo starts for a pinned toolchain.
- A toolchain one account installs is shared with every other account, which
  can update or remove it.
- No image gains a package for this, and the rule needs no `setfacl`.

Harder:

- Any account in the group can change or remove a toolchain every other
  account uses. The accounts inside a VM are not a security boundary
  (SECURITY.md), and the login user already has passwordless `sudo`.
- The generated user-data changes: the login user is in one more group. On a
  base image without the group, including the slim, nix, and runner images
  and a full image built before this change, cloud-init creates an empty
  `rustup` group, and nothing changes.
- The ACL depends on the guest's systemd being built with ACL support. Ubuntu,
  Fedora, and Arch all build it that way. The rule walks the whole tree at
  every boot. On a tmpfs tree with as many entries as the shipped toolchain,
  about 68,000, the walk took about 0.1 seconds. It has not been timed on a
  guest's first boot.
- An image already cached gains none of this until it is rebuilt with
  `agent-vm image build <family> --force`, and a VM gains it when it is created
  again from that image.
