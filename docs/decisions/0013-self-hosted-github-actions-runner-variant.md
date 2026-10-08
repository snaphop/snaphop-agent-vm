# 13. Self-hosted GitHub Actions runner as a fourth image variant

Date: 2026-10-03

## Status

Accepted

Narrowed by [ADR-0015](./0015-register-a-github-actions-runner-with-gh.md).
When an organization is set, `create` of a `-runner` image registers the guest
after SSH, and `destroy` removes that registration. The image, the seed, and
the in-guest command are unchanged. A create that names no organization still
leaves registration to the operator.

Extends [ADR-0006](./0006-initial-guest-distro-support.md) (initial guest distro
support) and [ADR-0012](./0012-nix-provided-guest-tooling.md) (Nix-provided
guest tooling) with another variant of each existing family. Bounded by
[ADR-0003](./0003-oci-container-images-as-vm-root-disks.md): the boot layer is
the slim recipe, unchanged.

## Context

A self-hosted GitHub Actions runner needs a machine that can boot, accept a
job, and be thrown away afterwards. That is already what a slim guest is: a
kernel, cloud-init, sshd, sudo, and ordinary Linux tooling, without the coding
agents, browser, Docker, and nested virtualization stack that make a full
image large and slow to build.

The runner itself is a published program, not something this repository should
reimplement. GitHub ships `actions/runner` as a tarball with `config.sh`, a
dependency installer for Debian and Fedora, and systemd unit templates.
`config.sh` writes `svc.sh` when a runner is configured. The tarball does not
contain `svc.sh`, and `config.sh` refuses to run as root.

Registration cannot be part of the image. A base image is shared by every VM
built on it and cached indefinitely, and `SECURITY.md` forbids this tool from
writing a GitHub token into an image, a cloud-init seed, or generated
user-data. A registration token is also single purpose and short lived, so
baking one in would be both a credential leak and a stale one. The guest has
to be registered by the operator, after boot, with a value the operator
supplies then.

Two further constraints come from how the upstream runner behaves:

- A workflow job should not have the `agent` account's passwordless sudo. The
  runner needs its own system account, with no sudo and a `nologin` shell.
- Replacing an already configured runner requires removing the local
  configuration first. Upstream `config.sh` refuses to configure a directory
  that already has a `.runner` file, including when `--replace` is set.
  `--replace` is what replaces a runner of the same name that still exists on
  the GitHub side. `config.sh remove --local` deletes `.runner` and
  `.credentials` and leaves that GitHub runner in place. `config.sh remove
  --token` is a different operation: it deletes the runner at GitHub and
  accepts a removal token. A registration token is rejected there, and using
  it to clear the directory would also be the wrong outcome, because a
  successful removal deletes the runner `--replace` was going to replace.
  `svc.sh install` records the user it was given, and when that argument is
  omitted it falls back to `SUDO_USER`, which on these guests is `agent`. The
  service therefore has to be installed explicitly as `runner`, from the
  runner directory, because `svc.sh` treats the directory it was started in
  as the runner root. The service has to be uninstalled before either form of
  `config.sh remove`; `config.sh` refuses the removal while it is installed.

GitHub's `bin/installdependencies.sh` supports Debian and Fedora and exits on
Arch. Arch still needs the native libraries the bundled .NET runtime loads:
`icu`, `openssl`, `krb5`, `zlib`, and `lttng-ust`.

## Decision

Add a **runner** variant of every supported family: `ubuntu-runner`,
`fedora-runner`, `arch-runner`.

- **It is the slim image plus the runner.** From `ARG BASE_IMAGE` through the
  end of the slim recipe, `<family>-runner.Containerfile` is that family's
  slim file, byte for byte. One shared section after the marker
  `# agent-vm-runner-section` installs the runner.
  `TestRunnerContainerfiles_AreTheSlimRecipePlusTheRunner` requires the slim
  body to stay identical and the section to stay the same on every family.
  `TestRunnerContainerfiles_KeepTheBootAndCloudInitContract` requires the guest
  contract. The variant is a base image of its own — its own cache directory,
  manifest, and `vm.json` name — reusing `distro.Variant`. It is not a new
  distro family and not a new source image.
- **The release is pinned.** The image build downloads
  `actions-runner-linux-<arch>-2.337.0.tar.gz` from the `v2.337.0` GitHub
  release and checks its SHA-256 before extracting to `/opt/actions-runner`:
  `70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613` for x64
  and `9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393` for
  arm64. The version is also
  written to `/opt/actions-runner/.agent-vm-runner-version`. There is no
  `latest`. The x64 checksum was checked against the published archive on
  2026-10-03; the arm64 checksum is the one published with that release.
  Rebuilding the base image is how the pin moves. A running runner may still
  update its own copy into the VM's overlay; this decision does not pass
  `--disableupdate`.
- **The image build does not configure the runner.** It creates the system
  account `runner` when absent (`useradd --system --user-group`, home
  `/opt/actions-runner`, shell `nologin`, no home directory of its own),
  extracts the archive, installs native dependencies, and leaves the directory
  mode `0750` owned by `runner:runner`. It does not run `config.sh`. Debian
  and Fedora run GitHub's `bin/installdependencies.sh`. Arch installs
  `icu`, `openssl`, `krb5`, `zlib`, and `lttng-ust` with
  `pacman -S --needed` against the databases the slim layers already synced,
  and does not upgrade other packages. A full upgrade could replace the
  kernel after the slim recipe built the initramfs this image direct-boots.
  A missing database fails the build. The package cache is then cleaned. Any
  other distro fails the build.
- **Registration is one in-guest command.**
  `/usr/local/sbin/agent-vm-github-runner`, linked from `/usr/sbin` so `sudo`
  finds it on every family, is the only way this image registers. `configure`
  checks its arguments before it does anything else, requires root, runs
  `config.sh` as `runner` (never with `RUNNER_ALLOW_RUNASROOT`), and then runs
  `./svc.sh install runner` and `./svc.sh start` as root from
  `/opt/actions-runner`. `remove` uninstalls the service and runs
  `config.sh remove --token` as `runner`, and that value is a removal token.
  `--replace`, when a local `.runner` file exists, uninstalls the service,
  runs `config.sh remove --local` as `runner` with no token, then configures
  again with the registration token and `--replace` so a runner of the same
  name still present on GitHub is replaced. The value may be passed with
  `--token` or read from a mode `0600`/`0400` file or from stdin. The command
  does not print it. `config.sh` itself takes the value as an argument; that
  is the interface the upstream program ships, and this wrapper adds no second
  copy of it to the image.
- **The image stays out of registration.** There is no cloud-init field that
  writes a registration token. A create that names no organization leaves
  the runner unregistered; the operator runs `configure`, and later `remove`
  or the GitHub UI. When an organization is set, registration and removal
  are [ADR-0015](./0015-register-a-github-actions-runner-with-gh.md). The
  token still arrives on stdin of this command, after boot.
- **No new host tool and no schema change.** `curl`, `sha256sum`, `tar`, and
  `useradd` already exist inside the slim image. `doctor` is unchanged.
  `manifest.json` is unchanged: the variant is a different image name, the
  same way `-slim` is. `agent-vm update` already skips tooling whose command
  is absent, so a runner guest gets its distro packages updated and nothing
  else from the tooling steps.

The default integration suite stays `ubuntu,fedora,arch`. A runner image is
built only when named, for example `-distros=ubuntu-runner`.

## Consequences

Easier:

- A disposable self-hosted runner is `agent-vm create ci --distro
  ubuntu-runner` and one command inside the guest. The job runs as `runner`,
  not as an account with passwordless sudo.
- The boot contract cannot drift from slim: the recipe body is the slim file.
- The same runner section is installed on Ubuntu, Fedora, and Arch.

Harder:

- The runner pin is a dependency with a checksum. Moving it is an edit to
  `templates/distro/github-runner.sh`, with both published checksums.
- The image is much larger than slim, because the runner ships its own
  Node.js and .NET runtime. It does not include Docker; a workflow that
  needs a daemon has to provide one.
- Arch's native libraries are installed by name rather than by GitHub's
  script. A future runner can fail at job time if the bundled runtime's
  SONAMEs move away from Arch's packages. That combination is not part of the
  default integration run.
- A create that names no organization leaves a hand-registered runner at
  GitHub until the operator removes it. An ephemeral runner unregisters
  itself after one job; a normal one does not. A create that names an
  organization removes that registration on destroy (ADR-0015).
- The registration value is visible to other processes on the guest for the
  duration of `config.sh`, because that is how `config.sh` accepts it. The
  wrapper does not log it, and the `runner` directory is mode `0750` so the
  `agent` account cannot read `.credentials` afterwards.
