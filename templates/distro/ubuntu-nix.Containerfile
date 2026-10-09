# Ubuntu nix base image for agent-vm.
#
# Same contract as ubuntu.Containerfile -- a kernel and initramfs, an init
# system, cloud-init, sshd, sudo, and the guest agent, with no credentials and
# no per-VM state -- built the same way, from the same ubuntu packages. What
# differs is where the guest *tooling* comes from: one shared nix expression,
# templates/distro/agent-tools.nix, instead of this family's package manager
# and mise (ADR-0012).
#
# The blocks that make a container image boot as a VM and be reachable are
# deliberately identical to the full and slim recipes': when one of them
# changes there, change it here in the same edit.
# TestNixContainerfiles_KeepTheBootAndCloudInitContract enforces it.
#
# Two things are still installed from ubuntu, because a nix profile cannot
# supply a running system daemon wired into the distro's units: Docker and the
# nested virtualization stack. Both blocks are copied from the full recipe.
#
# mise survives here for the tools nixpkgs does not package: pi, herdr and agy
# from its registry, and grok from its npm backend. It installs no toolchain
# and no runtime. claude and opencode come from nixpkgs, and codex from
# OpenAI's installer, exactly as they do in the full recipe.
# TestNixContainerfiles_LeaveTheToolingToTheNixFile enforces the boundary.
#
# BASE_IMAGE is passed in pinned to a digest. Do not add a default that would
# let an unpinned tag be built by accident.

ARG BASE_IMAGE
FROM ${BASE_IMAGE}

ENV DEBIAN_FRONTEND=noninteractive

# lsb-release is listed explicitly because --no-install-recommends is used
# below. cloud-init only recommends it, but cloud-init's apt module rewrites
# /etc/apt/sources.list.d/ubuntu.sources on first boot and asks lsb_release for
# the codename to write into it. Without the command, cloud-init substitutes
# the literal string UNAVAILABLE, and every guest comes up with sources
# pointing at a suite named "UNAVAILABLE" — `apt update` then 404s on every
# repository. Nothing else in the image reveals the omission.

# linux-image-virtual is the kernel flavour built for guests; the full kernel
# would add drivers no virtual machine has.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      linux-image-virtual \
      initramfs-tools \
      systemd \
      systemd-sysv \
      systemd-resolved \
      chrony \
      cloud-init \
      openssh-server \
      sudo \
      qemu-guest-agent \
      iproute2 \
      ca-certificates \
      lsb-release \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# The tmux configuration every guest gets.
#
# It goes into /etc/skel so that useradd — which is what cloud-init uses to
# create the login user — copies it into each account's home directory, and
# into /root as well because that home already exists here and skel is only
# consulted when a home directory is created. Both land at ~/.tmux.conf, which
# is the path the reload binding inside the file sources.
COPY tmux.conf /etc/skel/.tmux.conf
RUN install -m 0644 /etc/skel/.tmux.conf /root/.tmux.conf

# The tmux session menu an interactive login lands on.
#
# The menu is a program in /usr/local/bin so that every account finds it and
# can run it again after leaving it, and the profile script that starts it is
# separate so an operator can drop the one file — or set AGENT_VM_NO_MENU — to
# get a plain shell without losing the command. See both files for the
# reasoning; the guards in the profile script are what keep this out of
# `ssh <vm> some-command`.
COPY tmux-menu.sh /usr/local/bin/agent-vm-menu
RUN chmod 0755 /usr/local/bin/agent-vm-menu

COPY tmux-menu-profile.sh /etc/profile.d/zz-agent-vm-tmux-menu.sh
RUN chmod 0644 /etc/profile.d/zz-agent-vm-tmux-menu.sh

# The prerequisites the nix installer itself needs: a TLS-capable downloader
# and an xz decompressor for the release tarball.
#
# They are added here rather than by editing the boot block above, because that
# block is shared word for word with the full and slim recipes and this variant
# is the only one that needs them.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      curl \
      xz-utils \
      ca-certificates \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# Nix's own configuration for the rest of this build.
#
# Everything below runs as root inside `podman build`, and nix defaults
# build-users-group to "nixbld" when it runs as root -- then refuses to start
# because that group does not exist yet. Creating the build users this early
# would not help: root's builds would move into a sandbox this container cannot
# set up, having neither privileged mounts nor nested user namespaces. So the
# build gets single-user settings, and the multi-user block further down
# replaces this file with the one the guest actually uses.
RUN set -eu; \
    install -d -m 0755 /etc/nix; \
    printf '%s\n' \
      'build-users-group =' \
      'sandbox = false' \
      > /etc/nix/nix.conf

# Nix itself (ADR-0012).
#
# The installer runs in single-user mode. A multi-user install starts nix-daemon
# and then talks to it, and there is no running systemd inside a `podman build`
# to start one with, so it cannot complete here. --no-daemon lays down /nix and
# a working nix owned by root, which is all the rest of this build needs.
#
# --no-channel-add is not an optimisation. What this image installs is pinned by
# agent-tools.nix, and a channel left in the image would be a second, unpinned
# source of packages for anyone who ran nix-env inside a guest.
#
# The nix version is deliberately unpinned, like mise and the vendor installers
# in the full recipe. The manifest records only the source image's digest, not
# the installer's version or the nixpkgs revision agent-tools.nix follows, so a
# rebuild is not reproducible (ADR-0006, ADR-0012).
RUN set -eu; \
    curl -fsSL -o /tmp/nix-install https://nixos.org/nix/install; \
    sh /tmp/nix-install --no-daemon --no-channel-add; \
    rm -f /tmp/nix-install

# nix's own binaries, for the rest of this build only. The guest gets its PATH
# from /etc/environment and the profile script further down.
ENV PATH=/root/.nix-profile/bin:${PATH}

# The tool set, built from the shared expression and installed as the system
# default profile so every account has it without a per-account install.
#
# It is built and set in one step: --set replaces the profile with a single
# closure, so the profile is never left half-populated the way a sequence of
# nix-env installs can leave it when one of them fails. The expression is kept
# in the image because it is the record of what this profile is, and because
# rebuilding the profile in a guest is how an operator changes it.
COPY agent-tools.nix /etc/agent-vm/agent-tools.nix
RUN set -eu; \
    tools="$(nix-build --no-out-link /etc/agent-vm/agent-tools.nix -A env)"; \
    nix-env --profile /nix/var/nix/profiles/default --set "${tools}"

# Playwright's browsers, and the one Chromium in this image.
#
# There is exactly one Chromium here, the build Playwright pins, for the same
# reason the full recipe gives: a distro chromium alongside it would be several
# hundred megabytes Playwright never touches.
#
# The store path is reachable through a stable /opt/ms-playwright symlink so
# that PLAYWRIGHT_BROWSERS_PATH, the chromium wrapper's default, and anything
# an operator wrote against the full image all keep pointing at the same place.
# The gcroot is what stops the collection below from deleting the browsers: a
# symlink in /opt is not one, and nix would happily reclaim several hundred
# megabytes that nothing else references.
RUN set -eu; \
    browsers="$(nix-build --no-out-link /etc/agent-vm/agent-tools.nix -A browsers)"; \
    install -d -m 0755 /opt /nix/var/nix/gcroots; \
    ln -sfn "${browsers}" /nix/var/nix/gcroots/agent-vm-playwright-browsers; \
    ln -sfn "${browsers}" /opt/ms-playwright; \
    printf 'PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright\n' >> /etc/environment

# Expose that browser as `chromium`, so it is usable without going through
# Playwright. See the script for how it finds the binary.
COPY chromium.sh /usr/local/bin/chromium
RUN chmod 0755 /usr/local/bin/chromium

# Drop everything the builds above needed and nothing in the profile does.
#
# This is not housekeeping. A nix store carrying four toolchains and a browser
# is the largest single thing in this image, and every byte of it is copied
# into the base disk and then read through a copy-on-write overlay by every VM
# built on it.
#
# The obvious companion, `nix-store --optimise`, is deliberately absent. It
# deduplicates by renaming a temporary hardlink over an existing store path, and
# on the overlay filesystem every `podman build` layer lives on that rename
# fails with ESTALE partway through -- so the step cannot succeed here whatever
# the store contains. Deduplication is left to the guest, where the store sits
# on a real ext4 root.
RUN set -eu; \
    nix-collect-garbage --delete-old >/dev/null

# Multi-user nix for the guest.
#
# The single-user install above is a root-owned /nix, which would leave the
# agent account unable to install anything for itself. The rest of a multi-user
# install is a build-user group, a nix.conf naming it, and the two unit files
# the nix package already ships -- so it is assembled here rather than by
# rerunning the installer at first boot, which would cost every VM the time
# `agent-vm create` advertises.
#
# The build users are what let nix-daemon build derivations under unprivileged
# accounts. Thirty-two is the installer's own number.
#
# The last two steps are what the single-user install leaves undone, and both
# are silent when missing. The shipped socket unit carries
# ConditionPathIsReadWrite=/nix/var/nix/daemon-socket, so without that directory
# systemd skips the socket at every boot and reports the unit enabled and
# healthy while no daemon ever listens; and a root-owned /nix/store gives the
# build users nowhere to write, which fails the first unprivileged install after
# the daemon is finally reached. 1775 is the mode the installer's own multi-user
# script sets.
RUN set -eu; \
    groupadd --system nixbld; \
    for i in $(seq 1 32); do \
      useradd --system --home-dir /var/empty --shell /bin/false \
              --gid nixbld --groups nixbld \
              --comment "Nix build user ${i}" "nixbld${i}"; \
    done; \
    install -d -m 0755 /etc/nix; \
    printf '%s\n' \
      'build-users-group = nixbld' \
      'experimental-features = nix-command flakes' \
      > /etc/nix/nix.conf; \
    install -d -m 0755 /nix/var/nix/daemon-socket; \
    chgrp nixbld /nix/store; \
    chmod 1775 /nix/store

# The daemon's units come out of the nix package's own store path rather than
# being written here, so they stay correct across nix releases. A nix package
# that does not ship them is a failed build: a guest without the daemon looks
# exactly like a working one until an unprivileged account first tries to
# install something.
RUN set -eu; \
    nixstore="$(dirname "$(dirname "$(readlink -f /nix/var/nix/profiles/default/bin/nix)")")"; \
    for unit in nix-daemon.service nix-daemon.socket; do \
      if [ ! -f "${nixstore}/lib/systemd/system/${unit}" ]; then \
        echo "the nix package at ${nixstore} ships no ${unit}; guests built on this image would have no multi-user nix" >&2; \
        exit 1; \
      fi; \
      install -m 0644 "${nixstore}/lib/systemd/system/${unit}" "/etc/systemd/system/${unit}"; \
    done; \
    systemctl --root=/ enable nix-daemon.socket

# The profile on the default PATH of every account.
#
# /etc/environment is what makes `ssh <vm> go build` work: it runs no login
# shell, so it never reads the profile script below, but sshd applies
# /etc/environment through pam_env before the command runs. The existing PATH
# line is replaced rather than appended to, because a second one would win or
# lose depending on the distro and this must not vary by family.
RUN set -eu; \
    touch /etc/environment; \
    sed -i '/^PATH=/d' /etc/environment; \
    printf 'PATH=/nix/var/nix/profiles/default/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n' \
      >> /etc/environment

COPY nix.sh /etc/profile.d/agent-vm-nix.sh
RUN chmod 0644 /etc/profile.d/agent-vm-nix.sh

# The commands an agent supervisor invokes over ssh, in /usr/local/bin.
#
# The PATH above already covers them; these symlinks are the belt to its
# braces, for the same reason the full recipe symlinks mise's shims. A command
# missing here is a failed build rather than a guest that is missing a tool:
# this list and agent-tools.nix have to agree, and the build is the only place
# that disagreement is cheap to find.
RUN set -eu; \
    for command in node npm npx go gofmt golangci-lint python3 \
                   java javac mvn \
                   cargo-fmt cargo-clippy \
                   cargo rustc rustfmt claude opencode gh tea wrangler; do \
      if [ ! -x "/nix/var/nix/profiles/default/bin/${command}" ]; then \
        echo "the nix profile has no ${command}; this list and agent-tools.nix disagree" >&2; \
        exit 1; \
      fi; \
      ln -sf "/nix/var/nix/profiles/default/bin/${command}" "/usr/local/bin/${command}"; \
    done

# mise, for the tools nixpkgs does not package.
#
# pi, herdr and agy come from mise's registry -- the names below resolve to
# each vendor's own release binary; `agy` is aqua:google-antigravity/antigravity-cli
# -- and grok comes from mise's npm backend (`npm:@xai-official/grok`), which
# is the only place it is published. None of them is in nixpkgs. Removing mise
# entirely would mean removing all four, and herdr is not optional: this image
# starts a herdr server for every account at boot, so a guest without it would
# come up with a failed unit every time.
#
# This is the whole of mise's role in a nix image. It installs no toolchain
# and no runtime -- node, which the npm backend needs, comes from the nix
# profile already linked into /usr/local/bin -- so the "which copy of Go am I
# running" ambiguity the nix variant exists to remove does not come back with
# it.
#
# The binary goes in /usr/local/bin so that `mise` is on the default PATH for
# every account, and what it installs goes in /usr/local/lib/mise, one store
# shared by every account rather than a copy in each home.
#
# mise skips a release for 24 hours after it is published unless
# MISE_MINIMUM_RELEASE_AGE is 0s. That zero duration is how it turns the cutoff
# off, so the tools below and a later `mise use` in the guest are the newest
# release rather than yesterday's. A bare 0 is not a duration: mise 2026.10.2
# rejects it while choosing a self-update, and the bootstrap installer rejects
# it too. /etc/environment is what sshd gives a non-interactive session through
# PAM. A build step does not read that file, so the mise invocation below
# sets the variable itself.
RUN set -eu; \
    printf 'MISE_MINIMUM_RELEASE_AGE=0s\n' >> /etc/environment; \
    curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise MISE_MINIMUM_RELEASE_AGE=0s sh; \
    install -d -m 0755 /usr/local/lib/mise

# The versions are deliberately unpinned, like the vendor installers below:
# these tools ship several releases a week and a pinned one would be stale
# before the image was rebuilt. This is the one part of a nix image that the
# nixpkgs pin does not cover.
RUN set -eu; \
    MISE_DATA_DIR=/usr/local/lib/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
    MISE_MINIMUM_RELEASE_AGE=0s \
      PATH="/usr/local/bin:${PATH}" \
      mise use --global --yes pi herdr agy npm:@xai-official/grok; \
    rm -rf /tmp/mise-cache /tmp/fslock /usr/local/lib/mise/downloads

# /tmp/fslock is where mise's npm backend takes the lock it holds while it
# installs a package, and the directory belongs to whichever account created it
# first, at mode 0755. The grok install above uses that backend, and so do
# `agent-vm update` and any `mise use -g npm:<package>` an agent runs later:
# without this rule the first one to run as root locks every other account out
# with "failed to acquire project lock: Permission denied". The rule recreates
# the directory at every boot with /tmp's own permissions.
RUN printf 'd /tmp/fslock 1777 root root -\n' > /etc/tmpfiles.d/agent-vm-mise-fslock.conf

# Open the shared store to every account, now that the tools are installed in
# it.
#
# Each directory gets the sticky bit along with write permission, which is
# /tmp's arrangement: any account may install a tool, none may remove another's.
# A shared writable directory is defensible here on the same grounds the
# permissive agent configuration is -- the VM is the sandbox, single-tenant and
# disposable, and the accounts inside it are not a security boundary
# (SECURITY.md). Files keep the modes they were installed with, so no binary in
# the store is writable by anyone but root.
RUN set -eu; \
    chmod -R a+rX /usr/local/lib/mise; \
    find /usr/local/lib/mise -type d -exec chmod 1777 {} +

# Point every account's mise data directory at the shared store.
#
# /etc/skel is what useradd copies into each new home, and it copies a symlink
# as a symlink -- so the account cloud-init creates gets this link rather than a
# second copy of the store. root needs its own because its home is created
# before /etc/skel holds anything and never consults skel.
#
# The config and state directories stay per account: the config is what selects
# a tool version, and an account that runs `mise use -g herdr@<version>` must be
# able to change its own without changing everyone's.
RUN set -eu; \
    mkdir -p /etc/skel/.local/share /root/.local/share /root/.local/state /root/.config; \
    ln -sfn /usr/local/lib/mise /etc/skel/.local/share/mise; \
    ln -sfn /usr/local/lib/mise /root/.local/share/mise; \
    cp -a /etc/skel/.local/state/mise /root/.local/state/mise; \
    cp -a /etc/skel/.config/mise /root/.config/mise

COPY mise.sh /etc/profile.d/agent-vm-mise.sh
RUN chmod 0644 /etc/profile.d/agent-vm-mise.sh

# The mise-installed commands on the default PATH of every account.
#
# A shim is a symlink to the mise binary, which dispatches on the name it was
# called by and resolves the version from the calling account's own mise
# configuration -- so a single symlink in /usr/local/bin serves every account
# without pointing into any account's home. That is what keeps
# `ssh <vm> herdr ...` and `ssh <vm> grok -p '...'` working: an ssh command
# runs no login shell, so it never reads the profile script above. The list is
# these four and no more; everything else in /usr/local/bin points into the
# nix profile.
RUN set -eu; \
    for command in pi herdr agy grok; do \
      ln -sf /usr/local/bin/mise "/usr/local/bin/${command}"; \
    done

# codex comes from OpenAI's own installer rather than from nixpkgs, which does
# not package it.
#
# `codex remote-control` -- the daemon this image starts at every boot -- runs
# only against the standalone package that installer lays down: it starts and
# updates its app-server from a fixed path,
# $CODEX_HOME/packages/standalone/current, and refuses to run when that
# directory is absent.
#
# The package is installed once and shared: it goes to /usr/local/lib/codex and
# the command to /usr/local/bin, rather than into one account's home, because
# it is ~300 MiB and root and every account cloud-init creates need it. Each
# account gets a symlink to it at first boot -- see codex-remote-control.sh.
RUN set -eu; \
    export CODEX_NON_INTERACTIVE=1 \
           CODEX_INSTALL_DIR=/usr/local/bin \
           CODEX_HOME=/usr/local/lib/codex; \
    curl -fsSL https://chatgpt.com/codex/install.sh | sh; \
    chmod -R a+rX /usr/local/lib/codex; \
    test -x /usr/local/lib/codex/packages/standalone/current/codex \
      || { echo 'the codex installer did not produce a standalone package; codex remote-control would refuse to start in every VM built on this image' >&2; exit 1; }

# Run every agent once, and fail the build if any of them cannot start.
#
# Installing an agent and having a working agent are different things: a build
# for the wrong architecture, or one whose entry point cannot find what it
# needs, installs cleanly and only fails when someone finally types the command,
# inside a VM, long after the image was built and cached. This is the step that
# turns that into a failed build.
#
# All six are here: claude and opencode from nixpkgs, codex from OpenAI's
# installer, and pi, agy and grok from mise.
RUN set -eu; \
    for agent in claude codex opencode pi agy grok; do \
      if ! "$agent" --version >/dev/null 2>&1; then \
        echo "the ${agent} CLI installed but cannot run:" >&2; \
        "$agent" --version >&2 || true; \
        exit 1; \
      fi; \
    done

# The same check for herdr, which is not an agent but is started as a daemon in
# every VM built on this image. A herdr that cannot start would surface as a
# failed unit on every boot of every guest instead of here, once.
RUN set -eu; \
    if ! herdr --version >/dev/null 2>&1; then \
      echo 'the herdr CLI installed but cannot run:' >&2; \
      herdr --version >&2 || true; \
      exit 1; \
    fi

# Run the toolchains once too, for the same reason.
RUN set -eu; \
    go version >/dev/null || { echo "the Go toolchain installed but cannot run" >&2; exit 1; }; \
    printf 'package main\n' | gofmt >/dev/null || { echo "gofmt installed but cannot run" >&2; exit 1; }; \
    golangci-lint --version >/dev/null || { echo "golangci-lint installed but cannot run" >&2; exit 1; }; \
    node --version >/dev/null || { echo "node installed but cannot run" >&2; exit 1; }; \
    python3 --version >/dev/null || { echo "python3 installed but cannot run" >&2; exit 1; }; \
    java -version >/dev/null 2>&1 || { echo "the JDK installed but java cannot run" >&2; exit 1; }; \
    mvn --version >/dev/null || { echo "maven installed but cannot run" >&2; exit 1; }; \
    rustc --version >/dev/null || { echo "the Rust toolchain installed but rustc cannot run" >&2; exit 1; }; \
    cargo --version >/dev/null || { echo "cargo installed but cannot run" >&2; exit 1; }; \
    cargo fmt --version >/dev/null || { echo "rustfmt is missing from the Rust toolchain" >&2; exit 1; }; \
    cargo clippy --version >/dev/null || { echo "clippy is missing from the Rust toolchain" >&2; exit 1; }; \
    chromium --version >/dev/null || { echo "chromium installed but cannot run" >&2; exit 1; }

# The agents' configuration, each set to its most permissive mode so that an
# agent works unattended instead of blocking on an approval prompt nobody is
# there to answer.
#
# That is only defensible because the VM is itself the sandbox: it is
# disposable, network-isolated by default, and nothing the host cares about is
# reachable from inside it (SECURITY.md). A second sandbox within it would only
# stop the agent doing the work the VM exists for. These files hold
# configuration and never credentials -- those are per-VM and arrive through
# --cloud-init, because a base image is shared by every VM built on it.
#
# Each lands in /etc/skel, which useradd copies into the login user cloud-init
# creates, and in /root, whose home already exists here and so never consults
# skel. pi is absent because it does not gate tool calls at all. grok's
# config sets always-approve and turns its own updater off, so it does not
# replace the binary mise installed.
# claude additionally starts its Remote Control bridge in every session, which
# is what `claude --remote-control` does from the command line. It is set here
# rather than in a project or local settings file on purpose: claude treats the
# setting as security-sensitive and ignores it from repo-scoped settings, so a
# per-account file is the only place that can turn it on. Like codex's daemon
# it needs credentials that arrive per-VM, so on a VM where nobody has run
# `claude login` it simply does not connect.
COPY claude-settings.json /etc/skel/.claude/settings.json
COPY codex-config.toml /etc/skel/.codex/config.toml
COPY opencode.json /etc/skel/.config/opencode/opencode.json
COPY grok-config.toml /etc/skel/.grok/config.toml
RUN set -eu; \
    for file in .claude/settings.json .codex/config.toml .config/opencode/opencode.json .grok/config.toml; do \
      install -D -m 0644 "/etc/skel/${file}" "/root/${file}"; \
    done

# agy is the exception: it has no configuration file for tool permissions, so
# the flag is the only way to run it unattended and it gets an alias. This
# reaches interactive shells only -- see the file for what that leaves out.
COPY agent-aliases.sh /etc/profile.d/agent-vm-agents.sh
RUN chmod 0644 /etc/profile.d/agent-vm-agents.sh

# Docker comes from the distro's own repository rather than Docker's
# convenience script: the build then needs no extra registry or GPG key, and
# the version is one the distro supports for the life of the release.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      docker.io \
      docker-compose-v2 \
      docker-buildx \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# Docker starts at boot so an agent finds a working daemon without asking.
RUN systemctl --root=/ enable docker.service containerd.service

# Cross-architecture container builds: docker build --platform linux/arm64.
#
# The usual recipe for this is `docker run --privileged --rm tonistiigi/binfmt
# --install arm64` inside the running machine. That is the wrong shape for a
# base image: it needs the daemon up and a registry reachable, so it costs a
# pull on a VM that may have no route out, and the registration it makes lives
# in the kernel of that one boot only. The distro's own user-mode QEMU
# packages ship the same interpreters with binfmt_misc rules under
# /usr/lib/binfmt.d, which systemd-binfmt.service re-registers on every boot,
# so an agent finds cross-building already working.
#
# The F (fix-binary) flag in those rules is the load-bearing part: it makes
# the kernel hold the interpreter open, so it still resolves inside a
# container's mount namespace, where the qemu binary does not exist. Without
# it, `docker build --platform linux/arm64` dies with "exec format error" as
# soon as the first RUN in the foreign-architecture stage starts.
#
# Both directions are installed rather than aarch64 alone: on an aarch64 host
# the emulation that is actually missing is x86_64. Each package omits the
# rule for the architecture it is built for, since that one needs no
# emulation, which is why the check below looks only for the foreign one.

# Ubuntu 26.04 made qemu-user-static a virtual package provided by both
# qemu-user-binfmt and qemu-user-binfmt-hwe, and apt will not choose between
# them: `apt-get install qemu-user-static` exits with "no installation
# candidate" and the image build stops. qemu-user-binfmt is the concrete
# package. It depends on qemu-user for the static interpreters, and its rules
# carry the F flag.
#
# On 24.04 and earlier that split has not happened, and qemu-user-binfmt's own
# rules are registered without the F flag. The package that still carries it
# there is qemu-user-static, so the same install line cannot be used for every
# release this recipe builds.
RUN . /etc/os-release \
 && major=${VERSION_ID%%.*} \
 && case "$major" in \
      ''|*[!0-9]*) echo "Ubuntu VERSION_ID is not a numeric release: ${VERSION_ID:-unset}" >&2; exit 1 ;; \
    esac \
 && if [ "$major" -ge 26 ]; then pkg=qemu-user-binfmt; else pkg=qemu-user-static; fi \
 && apt-get update \
 && apt-get install -y --no-install-recommends "$pkg" \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

RUN set -eu; \
    case "$(uname -m)" in \
      aarch64|arm64) foreign=x86_64 ;; \
      *) foreign=aarch64 ;; \
    esac; \
    conf=; \
    for candidate in "/usr/lib/binfmt.d/qemu-$foreign.conf" "/usr/lib/binfmt.d/qemu-$foreign-static.conf"; do \
      if [ -f "$candidate" ]; then conf=$candidate; fi; \
    done; \
    [ -n "$conf" ] || { echo "qemu-user-static is installed but no binfmt_misc rule for $foreign landed in /usr/lib/binfmt.d; cross-architecture container builds would have no interpreter" >&2; exit 1; }; \
    grep -q ':[A-Za-z]*F[A-Za-z]*$' "$conf" || { echo "$conf does not carry the F (fix-binary) flag; docker build --platform would fail inside the container with exec format error" >&2; exit 1; }; \
    [ -L /usr/lib/systemd/system/sysinit.target.wants/systemd-binfmt.service ] || { echo "systemd-binfmt.service is not wanted by sysinit.target; the rules would never be registered at boot" >&2; exit 1; }

# Per-account setup that can only happen once the accounts exist.
#
# It cannot be done here — the accounts do not exist until first boot. The
# login user agent-vm asks for is placed in its groups by the generated
# cloud-init user-data, which is what makes the membership effective in the
# very first SSH session. This one-shot unit is the backstop for every other
# interactive account: ones an operator's own --cloud-init file creates, and
# ones created on a VM whose seed predates that change. It also generates each
# account's SSH key pair, which the seed cannot do at all -- a private key
# never goes into an image or a seed (SECURITY.md). See the script itself.
COPY user-setup.sh /usr/local/sbin/agent-vm-user-setup
RUN chmod 0755 /usr/local/sbin/agent-vm-user-setup

# It is installed into cloud-final.service rather than multi-user.target, and
# that is load-bearing: cloud-init orders cloud-final.service *after*
# multi-user.target, so a unit wanted by that target and ordered after
# cloud-final forms an ordering cycle. systemd resolves such a cycle by
# deleting a job -- ours -- and the unit then sits enabled and inactive for the
# life of the VM, with nothing in the journal to say so. Being wanted by
# cloud-final.service instead means it is pulled in by the service it waits
# for, which is what the ordering was expressing anyway.
RUN printf '%s\n' \
      '[Unit]' \
      'Description=First-boot setup for interactive accounts' \
      'After=cloud-final.service docker.service' \
      'Wants=cloud-final.service' \
      '' \
      '[Service]' \
      'Type=oneshot' \
      'RemainAfterExit=yes' \
      'ExecStart=/usr/local/sbin/agent-vm-user-setup' \
      '' \
      '[Install]' \
      'WantedBy=cloud-final.service' \
      > /usr/lib/systemd/system/agent-vm-user-setup.service \
 && systemctl --root=/ enable agent-vm-user-setup.service

# Codex's remote-control daemon, started for every interactive account at every
# boot.
#
# It cannot be started at build time and it is not a first-boot job either: the
# daemon dies with the VM it runs in, so it is started again on each boot. It
# also needs credentials, which are per-VM and arrive after the account exists
# (SECURITY.md), so on a VM where nobody has run `codex login` this unit fails
# to connect and says so in the journal. That is not a boot failure -- the
# script reports it and exits 0 -- and running `systemctl start
# agent-vm-codex-remote-control` after logging in is what starts the daemon
# then.
COPY codex-remote-control.sh /usr/local/sbin/agent-vm-codex-remote-control
RUN chmod 0755 /usr/local/sbin/agent-vm-codex-remote-control

# Wanted by cloud-final.service for the same reason as the unit above: a unit
# ordered after cloud-final and wanted by multi-user.target forms a cycle that
# systemd breaks by silently dropping our job.
RUN printf '%s\n' \
      '[Unit]' \
      'Description=Codex remote control for interactive accounts' \
      'After=cloud-final.service agent-vm-user-setup.service network-online.target' \
      'Wants=cloud-final.service network-online.target' \
      '' \
      '[Service]' \
      'Type=oneshot' \
      'RemainAfterExit=yes' \
      'ExecStart=/usr/local/sbin/agent-vm-codex-remote-control' \
      'TimeoutStartSec=300' \
      '' \
      '[Install]' \
      'WantedBy=cloud-final.service' \
      > /usr/lib/systemd/system/agent-vm-codex-remote-control.service \
 && systemctl --root=/ enable agent-vm-codex-remote-control.service

# The Herdr servers, one per interactive account, started at every boot.
#
# Same shape and the same reasons as the codex daemon above: the accounts do
# not exist when the image is built, and a server dies with the VM, so it is
# started again on each boot. The difference is who owns the process. Each
# account's server is an instance of the template unit below, so systemd
# supervises it -- its output is in the journal, a crash is restarted, and
# `systemctl status agent-vm-herdr@<account>` says what it is doing -- and the
# script is only what starts one instance per account.
COPY herdr-server.sh /usr/local/sbin/agent-vm-herdr-server
RUN chmod 0755 /usr/local/sbin/agent-vm-herdr-server

# %i is the account name. systemd fills HOME, USER and LOGNAME from the account
# database for a unit with User= set, and HOME is what decides which account's
# configuration and socket the server uses -- both live in $HOME/.config/herdr
# -- so each instance is that account's own server and no two of them collide.
# The command is the /usr/local/bin shim, because a mise-installed herdr exists
# nowhere else that a system unit could name.
#
# The start limit is wider than the default ten seconds on purpose: with a
# five-second RestartSec only two starts fit in that window, so a server that
# cannot run at all -- a mise shim pointing at nothing, say -- would be
# restarted for the life of the VM without ever tripping the limit. At sixty
# seconds it gives up after five attempts and stays failed, where the journal
# still has the reason.
RUN printf '%s\n' \
      '[Unit]' \
      'Description=Herdr terminal workspace server for %i' \
      'After=network-online.target' \
      'StartLimitIntervalSec=60' \
      'StartLimitBurst=5' \
      '' \
      '[Service]' \
      'Type=simple' \
      'User=%i' \
      'ExecStart=/usr/local/bin/herdr server' \
      'Restart=on-failure' \
      'RestartSec=5' \
      > /usr/lib/systemd/system/agent-vm-herdr@.service

# Wanted by cloud-final.service for the same reason as the units above: a unit
# ordered after cloud-final and wanted by multi-user.target forms a cycle that
# systemd breaks by silently dropping our job.
RUN printf '%s\n' \
      '[Unit]' \
      'Description=Herdr servers for interactive accounts' \
      'After=cloud-final.service agent-vm-user-setup.service network-online.target' \
      'Wants=cloud-final.service network-online.target' \
      '' \
      '[Service]' \
      'Type=oneshot' \
      'RemainAfterExit=yes' \
      'ExecStart=/usr/local/sbin/agent-vm-herdr-server' \
      '' \
      '[Install]' \
      'WantedBy=cloud-final.service' \
      > /usr/lib/systemd/system/agent-vm-herdr.service \
 && systemctl --root=/ enable agent-vm-herdr.service

# The virtualization stack, so a VM can create VMs of its own.
#
# This is the same set of host tools agent-vm itself drives (AGENTS.md §3), so
# a guest can run agent-vm, or virt-install and virsh directly. dnsmasq-base
# rather than dnsmasq: libvirt starts its own dnsmasq per network, and the full
# package would additionally enable a system-wide resolver on port 53 that
# fights with both libvirt's instances and systemd-resolved.
#
# qemu-kvm is not installed by that name. On Ubuntu 26.04 it is a virtual
# package provided by both qemu-system-<arch> and qemu-system-<arch>-hwe, and
# apt will not choose: the image build stops with "no installation candidate".
# The concrete package is the one for the architecture being built. It
# provides qemu-kvm, which is the name libvirt's dependency accepts, and it is
# a real package on 24.04 as well. The hardware-enablement build is the other
# provider, and installing it would replace the release's QEMU.
#
# guestfish ships virt-copy-out. guestfs-tools ships virt-make-fs, virt-ls,
# and virt-sysprep, and only recommends the metapackage that depends on
# guestfish. --no-install-recommends would leave virt-copy-out uninstalled,
# and a nested `agent-vm doctor` would fail the same way a host setup did.
#
# uidmap ships newuidmap. passt ships pasta. podman only recommends both, and
# the same --no-install-recommends would leave a nested `agent-vm image build`
# failing at the pull with "newuidmap: executable file not found".
RUN case "$(uname -m)" in \
      aarch64|arm64) qemu_pkg=qemu-system-arm ;; \
      x86_64|amd64) qemu_pkg=qemu-system-x86 ;; \
      *) echo "no QEMU system emulator package for $(uname -m)" >&2; exit 1 ;; \
    esac \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      "$qemu_pkg" \
      qemu-utils \
      libvirt-daemon-system \
      libvirt-clients \
      virtinst \
      dnsmasq-base \
      guestfish \
      guestfs-tools \
      podman \
      uidmap \
      passt \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# Nested virtualization, the guest half of it.
#
# The host half is already in place: agent-vm asks virt-install for
# `--cpu host-passthrough`, so the guest CPU carries the host's VMX/SVM feature
# and /dev/kvm works inside the VM. This file is what lets a VM inside this VM
# nest once more. Both modules are named because an image is built once and may
# boot on either vendor's host; modprobe ignores options for a module that is
# not loaded.
RUN printf '%s\n' \
      'options kvm_intel nested=1' \
      'options kvm_amd nested=1' \
      > /etc/modprobe.d/agent-vm-nested.conf

# libvirt starts at boot, so a nested `agent-vm create` finds a running daemon
# instead of a connection error.
#
# Which units exist depends on the family and the libvirt version: the modular
# daemons (virtqemud and friends) are replacing the monolithic libvirtd, and the
# two must not both be enabled -- they contend for the same socket. So the
# monolithic daemon is preferred where the image has it, the modular set is used
# where it does not, and an image with neither fails the build rather than
# booting a guest whose nested VMs cannot start.
RUN set -eu; \
    if [ -f /usr/lib/systemd/system/libvirtd.service ]; then \
      candidates="libvirtd.service virtlogd.socket virtlockd.socket"; \
    elif [ -f /usr/lib/systemd/system/virtqemud.service ]; then \
      candidates="virtqemud.service virtnetworkd.service virtstoraged.service virtlogd.socket virtlockd.socket"; \
    else \
      echo "this image has neither libvirtd nor virtqemud; nested VMs could not start" >&2; \
      exit 1; \
    fi; \
    units=""; \
    for unit in ${candidates}; do \
      if [ -f "/usr/lib/systemd/system/${unit}" ]; then units="${units} ${unit}"; fi; \
    done; \
    systemctl --root=/ enable ${units}

# Run each of these once, and fail the build if any of them cannot start.
#
# The same reasoning as the smoke tests above: a package that installs but does
# not run is indistinguishable from a working one until someone types the
# command inside a VM, hours after the image was built and cached. The
# qemu-system check is by architecture because the binary is named for it, and
# an image missing it would install perfectly and then be unable to start a
# single VM.
RUN set -eu; \
    virsh --version >/dev/null || { echo "virsh installed but cannot run" >&2; exit 1; }; \
    virt-install --version >/dev/null || { echo "virt-install installed but cannot run" >&2; exit 1; }; \
    qemu-img --version >/dev/null || { echo "qemu-img installed but cannot run" >&2; exit 1; }; \
    command -v "qemu-system-$(uname -m)" >/dev/null \
      || { echo "no qemu-system-$(uname -m) in this image; the guest could not start a VM of its own" >&2; exit 1; }; \
    virt-make-fs --version >/dev/null || { echo "guestfs-tools installed but virt-make-fs cannot run" >&2; exit 1; }; \
    virt-copy-out --version >/dev/null || { echo "virt-copy-out is missing; image builds cannot extract a kernel" >&2; exit 1; }; \
    podman --version >/dev/null || { echo "podman installed but cannot run" >&2; exit 1; }; \
    dnsmasq --version >/dev/null || { echo "dnsmasq installed but cannot run" >&2; exit 1; }

# There is no running systemd inside a build, so units are enabled offline with
# --root=/, which only writes the symlinks an enable would create. This must not
# be allowed to fail quietly: a guest without sshd looks exactly like a guest
# that failed to boot, hours later and from the outside.
RUN systemctl --root=/ enable \
      ssh.service \
      systemd-networkd.service \
      systemd-resolved.service

# A time synchronization client is not optional here, because on aarch64 a
# guest has no clock to start from. QEMU's virt machine provides a PL031 RTC,
# but the kernel flavours these images ship do not carry the driver -- Ubuntu
# keeps rtc-pl031 in linux-modules-extra, which linux-image-virtual does not
# pull in -- so /dev/rtc0 never appears and nothing sets the clock from
# hardware. systemd falls back to its own build date, and the guest comes up
# weeks in the past: measured on an Asahi Linux host, 37 days behind.
#
# What that breaks does not look like a clock problem. apt rejects repository
# metadata as "not valid yet", TLS handshakes fail against certificates that
# have not started yet, and build tools record timestamps from the wrong month.
# x86_64 hides all of it, since the CMOS driver is built in there and hctosys
# gets boot approximately right, so it surfaces only on ARM hosts.
#
# chrony rather than systemd-timesyncd, for two reasons. timesyncd was observed
# wedging on exactly this path: started at boot before resolved could answer, it
# never acquired a server address and never retried, so a guest sat 37 days
# behind with the service reported "active" -- and a manual restart minutes
# later synchronized instantly. chrony retries name resolution, carries several
# pools instead of one name, and its default `makestep 1 3` steps the first
# updates however large the offset is, which is what a guest this far out needs.
# Fedora ships the timesyncd unit but leaves it disabled, so it was never a
# candidate there either.
#
# chrony-wait is what gives time-sync.target a meaning: it holds the target
# until the clock is actually correct. sshd is ordered behind that target below,
# which closes the window this fix would otherwise leave open.
#
# The unit names differ per family (chrony.service on Ubuntu, chronyd.service
# elsewhere), so whichever is present is enabled; none present is a build
# failure, because a guest that cannot learn the time is the bug this fixes.
RUN set -eu; \
    units=""; \
    for unit in chrony.service chronyd.service chrony-wait.service; do \
      if [ -f "/usr/lib/systemd/system/${unit}" ]; then units="${units} ${unit}"; fi; \
    done; \
    test -n "${units}"; \
    systemctl --root=/ enable ${units}

# chronyd resolves its pool address once at startup, and at boot it starts
# before a name can be resolved. The failed lookup is retried with a backoff, so
# a guest that could have synchronized five seconds in instead selects a source
# at about thirty-three -- past the bound set below, which then releases sshd on
# a timeout rather than on a clock that is actually right. Measured on x86_64:
# 33s from boot, against 4.9s for the same chronyd restarted once the network
# was up.
#
# Ordering chronyd behind network-online.target makes the first lookup the one
# that succeeds. That target is already reached early here --
# systemd-networkd-wait-online is enabled in these images and finishes about
# eight seconds in -- and a guest that never reaches it has no address at all,
# so it was never going to be reachable, or synchronized, either way.
RUN set -eu; \
    dropins=""; \
    for unit in chrony.service chronyd.service; do \
      if [ -f "/usr/lib/systemd/system/${unit}" ]; then \
        mkdir -p "/etc/systemd/system/${unit}.d"; \
        printf '[Unit]\nAfter=network-online.target nss-lookup.target\nWants=network-online.target\n' \
          > "/etc/systemd/system/${unit}.d/20-agent-vm-resolve-after-the-network.conf"; \
        dropins="${dropins} ${unit}"; \
      fi; \
    done; \
    test -n "${dropins}"

# Two NTP clients stepping one clock is a fight rather than redundancy. Arch
# enables systemd-timesyncd by default, and alongside chrony it produced
# "System clock interference detected (another NTP client?)" in chronyd's log.
# Masking rather than disabling is what stops another unit pulling it back in as
# a dependency; on a family that does not ship timesyncd this is a no-op.
RUN set -eu; \
    if [ -f /usr/lib/systemd/system/systemd-timesyncd.service ]; then \
      systemctl --root=/ mask systemd-timesyncd.service; \
    fi

# Ordering sshd after time-sync.target is what stops a VM being handed over with
# a clock that is still wrong.
#
# `create` waits for SSH to answer and calls the VM ready at that point, and
# sshd answers about five seconds into a boot while chrony steps the clock at
# about seven. Between those two an agent is already logged in, running commands
# against a clock weeks out -- and the failures there are not the retryable kind:
# apt refuses the metadata and TLS refuses the certificate, and whatever hit it
# reports the error rather than waiting. Ordering sshd behind the target makes
# `create`'s existing readiness wait mean "the clock is right" as well, with no
# change to the tool.
#
# The ordering goes on the service and not on ssh.socket, which on Ubuntu binds
# port 22 before sockets.target. time-sync.target is only reached long after
# that -- it needs the network, which needs basic.target, which needs
# sockets.target -- so ordering the socket behind it is a dependency cycle, and
# systemd breaks a cycle by dropping one of the jobs in it. Gating the service
# instead has no such loop: the socket binds early and holds the connection in
# its backlog, so a probe that arrives first stalls for those few seconds
# instead of being refused, and is answered as soon as sshd takes the socket
# over. After= rather than Requires=, so a guest that never synchronizes still
# gets an sshd.
#
# That last case is the reason for the timeout below. chrony-wait ships
# TimeoutStartSec=180, and `chronyc waitsync 0 ...` retries without limit, so a
# guest with no route to an NTP server would hold sshd for three minutes --
# longer than `create` waits at all, turning a wrong clock into a failed create.
# Thirty seconds bounds it: the VM becomes reachable half a minute late, with
# the same wrong clock it would have had anyway, and `create` still finishes
# inside its budget.
RUN set -eu; \
    mkdir -p /etc/systemd/system/chrony-wait.service.d; \
    printf '[Service]\nTimeoutStartSec=30\n' \
      > /etc/systemd/system/chrony-wait.service.d/10-agent-vm-bound-the-wait.conf; \
    dropins=""; \
    for unit in ssh.service sshd.service; do \
      if [ -f "/usr/lib/systemd/system/${unit}" ]; then \
        mkdir -p "/etc/systemd/system/${unit}.d"; \
        printf '[Unit]\nAfter=time-sync.target\n' \
          > "/etc/systemd/system/${unit}.d/10-agent-vm-wait-for-the-clock.conf"; \
        dropins="${dropins} ${unit}"; \
      fi; \
    done; \
    test -n "${dropins}"

# cloud-init renamed and split its units in 24.3 (cloud-init.service became
# cloud-init-network.service, and cloud-init-main.service appeared), and the
# three supported families ship different versions. Enabling whichever units
# this image actually has keeps the recipe working across that change, while
# still failing the build if cloud-init is not installed at all — a guest with
# no cloud-init never receives its SSH key.
RUN set -eu; \
    units=""; \
    for unit in cloud-init-local.service cloud-init.service cloud-init-main.service \
                cloud-init-network.service cloud-config.service cloud-final.service; do \
      if [ -f "/usr/lib/systemd/system/${unit}" ]; then units="${units} ${unit}"; fi; \
    done; \
    test -n "${units}"; \
    systemctl --root=/ enable ${units} cloud-init.target

# qemu-guest-agent is started by udev when the virtio serial port appears and
# has no [Install] section on some distros, so enabling it is best-effort — and
# says so, rather than hiding the outcome behind a blanket `|| true`.
RUN systemctl --root=/ enable qemu-guest-agent.service \
 || echo "qemu-guest-agent has no [Install] section here; it is udev-activated instead"

# Ubuntu 26.04's unit carries ConditionVirtualization=vm. A new guest is
# presented as a physical desktop, so systemd-detect-virt reports "none" and
# that condition skips the agent: the virtio port is there, udev asks for the
# service, and systemd never starts it. Bridged create learns the address only
# from this agent, so the create runs out its wait and reports that the guest
# agent is not connected, while the guest has booted and already has an
# address.
#
# An empty ConditionVirtualization= in a drop-in clears the vendor condition.
# A unit that does not carry one is left as the distro shipped it.
RUN set -eu; \
    unit=""; \
    for candidate in /usr/lib/systemd/system/qemu-guest-agent.service \
                     /lib/systemd/system/qemu-guest-agent.service; do \
      if [ -f "${candidate}" ]; then unit="${candidate}"; break; fi; \
    done; \
    test -n "${unit}"; \
    if grep -q '^ConditionVirtualization=' "${unit}"; then \
      mkdir -p /etc/systemd/system/qemu-guest-agent.service.d; \
      printf '%s\n' '[Unit]' 'ConditionVirtualization=' \
        > /etc/systemd/system/qemu-guest-agent.service.d/10-agent-vm-start-without-virt-detection.conf; \
    fi

# The kernel mounts the root filesystem itself under direct kernel boot; this
# entry exists so systemd's fstab generator agrees with it and remounts rw.
RUN printf '/dev/vda1 / ext4 defaults 0 1\n' > /etc/fstab

# cloud-init gets its configuration from the NoCloud seed virt-install
# attaches. No other datasource may be probed: a VM must never reach out to a
# metadata service on the network.
#
# The 99 prefix is load-bearing. cloud-init reads /etc/cloud/cloud.cfg.d in
# sorted order and the last file to set a key wins, and Ubuntu ships its own
# datasource_list -- listing Ec2 and every other network datasource -- in
# 90_dpkg.cfg. This file was once named 90-agent-vm-datasource.cfg, which sorts
# *before* that ('-' is 0x2D, '_' is 0x5F), so the pin was silently overridden
# and the guarantee above was not true on Ubuntu. What it looked like from the
# outside: the first boot was fine, because the seed is attached and NoCloud
# matches immediately, and every later boot hung for four minutes probing
# 169.254.169.254 before sshd came up.
RUN printf 'datasource_list: [ NoCloud, None ]\n' > /etc/cloud/cloud.cfg.d/99-agent-vm-datasource.cfg

# Prove the pin actually wins rather than trusting the prefix.
#
# Sorting last is a property of every *other* file in the directory, so it is
# not something this recipe can guarantee on its own: a distro that adds a
# later-sorting datasource_list at any point would take the guarantee away
# again, silently and in exactly the way described above. Checking it here
# means that turns into a failed build instead.
RUN set -eu; \
    last="$(grep -l '^datasource_list:' /etc/cloud/cloud.cfg.d/*.cfg | sort | tail -n1)"; \
    if [ "${last}" != "/etc/cloud/cloud.cfg.d/99-agent-vm-datasource.cfg" ]; then \
      echo "${last} sets datasource_list after this image's own pin does;" >&2; \
      echo "cloud-init would use it and the guest could probe a metadata service" >&2; \
      exit 1; \
    fi

# Pin the network renderer instead of letting cloud-init choose one per distro,
# so all three families configure networking the same way and a boot failure
# means the same thing everywhere.
RUN printf 'system_info:\n  network:\n    renderers: [ networkd ]\n' > /etc/cloud/cloud.cfg.d/91-agent-vm-network.cfg

# Repair /etc/resolv.conf at every boot.
#
# podman bind-mounts /etc/resolv.conf over the image's own copy for the
# duration of each RUN, so systemd-resolved's packaging cannot replace that
# path with the symlink it normally installs, and the file committed to the
# image stays the empty regular file the OCI base ships. Nothing fixes it
# later: systemd's own rule in /usr/lib/tmpfiles.d/systemd-resolve.conf is an
# `L`, which by design refuses to touch a path that already exists. The result
# is a guest that looks completely healthy — DHCP lease, default route,
# resolved running and holding the right DNS server — while glibc reads an
# empty resolv.conf and every name lookup fails.
#
# This file masks the vendor rule by having the same name (tmpfiles.d in /etc
# wins over /usr/lib), and `L+` is the forcing form that replaces whatever is
# already at the path. The vendor file carries only this one rule, so nothing
# else is lost by overriding it. Writing the symlink cannot be done here with
# `ln` instead: the bind mount makes /etc/resolv.conf busy during the build.
RUN printf 'L+! /etc/resolv.conf - - - - ../run/systemd/resolve/stub-resolv.conf\n' \
      > /etc/tmpfiles.d/systemd-resolve.conf

# Let a non-root user run ping.
#
# iputils' ping needs one of two things: the cap_net_raw file capability, or a
# net.ipv4.ping_group_range that includes the caller's groups so it can use an
# ICMP socket instead of a raw one. Distro packaging grants the first, through
# a security.capability xattr on /usr/bin/ping — and that xattr does not
# survive this image pipeline. The root filesystem reaches the disk as a
# `podman export` tar unpacked by virt-make-fs, which drops it, so the guest
# ends up with a ping that has no capability at all.
#
# The kernel's default range is `1 0`, an empty one, and Ubuntu and Fedora both
# leave it there because their packaging expects the capability to be present.
# The two failures then compound: the agent user gets
# "socket: Operation not permitted ... missing cap_net_raw+p capability", which
# reads exactly like a broken network on a guest whose networking is fine.
#
# Setting the range here fixes it without depending on xattrs surviving the
# build. Arch already ships this value in systemd's own 50-default.conf; the
# drop-in is written for every family anyway so that unprivileged ping does not
# depend on which vendor file a base image happens to carry. Widening it costs
# nothing here: an agent in this guest has root already (SECURITY.md), so ICMP
# sockets are not a boundary this image is defending.
RUN printf 'net.ipv4.ping_group_range = 0 2147483647\n' \
      > /etc/sysctl.d/99-agent-vm-ping.conf
