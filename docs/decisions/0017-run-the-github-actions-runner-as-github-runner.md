# 17. Run the GitHub Actions runner as github-runner

Date: 2026-10-08

## Status

Accepted

Changes the job account in
[ADR-0013](./0013-self-hosted-github-actions-runner-variant.md) and the
no-sudo rule repeated by
[ADR-0016](./0016-install-docker-on-the-runner-image.md). The image is still
the slim recipe plus one shared section. Registration is still
[ADR-0015](./0015-register-a-github-actions-runner-with-gh.md).

## Context

ADR-0013 created a system account named `runner`, with a `nologin` shell and
no sudo, so a workflow job would not have the `agent` account's passwordless
sudo. ADR-0016 put that account in the `docker` group so a job could use the
daemon without sudo.

A job on this image needs to compile, and it needs to install packages and
change the guest the way a GitHub-hosted runner job can. The account that
runs the job is `github-runner`. That account has passwordless sudo. The
guest is already untrusted: a job could become root inside the guest through
a privileged container (ADR-0016), and the `agent` account on the same guest
already has passwordless sudo.

`build-essential` is the Debian package that installs gcc, g++, make, and
the headers. Ubuntu's slim recipe already installs it, and the runner recipe
is that file plus one section, so an Ubuntu runner image already had the
compiler. The runner install repeats the package so the runner image keeps
it. Fedora's slim recipe installs `gcc`, `gcc-c++`, and `make`. Arch's slim
recipe installs `base-devel`. A second package transaction on those families
could upgrade the kernel after the slim recipe built the initramfs this
image direct-boots.

`config.sh` still refuses to run as root. `svc.sh install` still has to be
told the service user. Omitted, it falls back to `SUDO_USER`, which on these
guests is `agent`.

## Decision

- The job account is `github-runner`. The image build creates it when absent
  (`useradd --system --user-group`, home `/opt/actions-runner`, shell
  `nologin`, no home directory of its own). The directory mode stays `0750`,
  owned by `github-runner:github-runner`.
- `/etc/sudoers.d/github-runner` contains
  `github-runner ALL=(ALL) NOPASSWD:ALL`, mode `0440`, owned by root. The
  build checks that file with `visudo -c -f` and fails if `visudo` is
  missing or rejects the rule.
- `configure` runs `config.sh` as `github-runner` and then
  `./svc.sh install github-runner`. `remove` and `remove --local` also run
  as `github-runner`.
- `github-runner` is in the `docker` group (ADR-0016).
- On Debian and Ubuntu, the runner install also installs `build-essential`.
  Fedora and Arch keep the compiler toolchain the slim recipe already
  installed.
- The shell stays `nologin`. The account is not an SSH login. A job step
  runs `sudo` through the runner process, which does not need a login shell.
- A cached runner image keeps the old `runner` account until
  `agent-vm image build <family>-runner --force`. A VM created from the old
  image keeps that account until it is created again.

## Consequences

Easier:

- A workflow step can `sudo` and can compile C and C++ on an Ubuntu runner.
  Fedora and Arch runner images already had a compiler.
- The service user is named for what it does, and `svc.sh` is installed as
  that user rather than as `agent`.

Harder:

- A job running as `github-runner` can become root inside the guest with
  `sudo`, and it can still start a privileged container. The guest stays
  untrusted. Nothing new is reachable on the host.
- An image or VM that already exists is unchanged until the image is rebuilt
  and the VM is created again. The old account is `runner` and it cannot
  sudo.
- The registration value is still visible to other processes on the guest
  for the duration of `config.sh`. The install directory stays mode `0750`,
  so the `agent` account cannot read `.credentials`.
