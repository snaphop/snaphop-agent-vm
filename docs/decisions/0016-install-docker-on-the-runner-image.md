# 16. Install Docker on the runner image

Date: 2026-10-08

## Status

Accepted

Extends [ADR-0013](./0013-self-hosted-github-actions-runner-variant.md). The
runner variant stays the slim recipe plus one shared section. That section
also installs Docker.

The account name and the no-sudo rule below are changed by
[ADR-0017](./0017-run-the-github-actions-runner-as-github-runner.md). The
job account is `github-runner`. It is in the `docker` group and it has
passwordless sudo.

## Context

A GitHub Actions job on a self-hosted runner commonly runs `docker`. ADR-0013
left Docker out so the image stayed small, and said a workflow that needs a
daemon has to provide one. Providing one from the job is circular: the job
needs the daemon in order to install the daemon, and the `runner` account has
no sudo.

The full and nix images already install Docker from the distro repositories
and enable the units offline. Ubuntu installs `docker.io`, `docker-compose-v2`,
and `docker-buildx`. Fedora installs `moby-engine`, `containerd`,
`docker-compose`, and `docker-buildx`. Arch installs `docker`,
`docker-compose`, and `docker-buildx`. Docker's convenience script would add
a second registry and a GPG key, which those images already refused.

The shared section cannot run a full package upgrade. The slim recipe has
built the initramfs this image direct-boots, and a later kernel upgrade would
leave that initramfs behind (ADR-0013). Arch therefore installs the named
packages with `pacman -S --needed` against the databases the slim layers
already synced.

Jobs run as `runner`. The Docker socket is owned by the `docker` group.
Without membership, every `docker` step fails, and the account cannot use
sudo to work around it. Membership can start a privileged container, which is
root inside the guest. The account stays out of sudoers, and its shell stays
`nologin`. The guest is already untrusted.

Cross-architecture `binfmt_misc` stays on the full and nix images. A runner
image does not install the user-mode QEMU packages. `docker build --platform`
for a foreign architecture is not part of this variant.

## Decision

- The shared runner section installs Docker after the runner account exists,
  from `templates/distro/runner-docker.sh`. The script is the same on Ubuntu,
  Fedora, and Arch. Any other distro fails the build.
- Package names match the full image: Ubuntu `docker.io`, `docker-compose-v2`,
  `docker-buildx`; Fedora `moby-engine`, `containerd`, `docker-compose`,
  `docker-buildx`; Arch `docker`, `docker-compose`, `docker-buildx`.
- `docker.service` and `containerd.service` are enabled offline, so the daemon
  is running when a job starts.
- `runner` is added to the `docker` group. The build fails if the account or
  the group is missing, or if `docker` is not on `PATH` after the install.
- No full system upgrade. Arch does not pass `-Sy` or `-Su`.
- No user-mode QEMU and no `binfmt_misc` rules on the runner image.
- A cached runner image gains Docker when it is rebuilt with
  `agent-vm image build <family>-runner --force`.

## Consequences

Easier:

- A workflow step can run `docker`, `docker compose`, and `docker buildx` as
  the `runner` user. The daemon is up at boot.
- The packages and the enablement are the ones the full image already uses,
  so a runner guest and a full guest agree on where Docker came from.

Harder:

- The runner image is larger than the slim image it starts from by the runner
  runtime and by Docker.
- The `runner` account can start containers, including privileged ones, and
  that is root inside the guest. It is still not in sudoers. The guest stays
  untrusted.
- `docker build --platform` for a foreign architecture remains a full or nix
  image capability.
- An already cached runner image keeps the old contents until it is rebuilt.
