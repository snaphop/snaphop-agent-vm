# Fedora base image for agent-vm.
#
# Same contract as the other families: a kernel and initramfs, an init system,
# cloud-init, sshd, sudo, and the guest agent. No credentials, no per-VM state.
#
# BASE_IMAGE is passed in pinned to a digest. Do not add a default.
ARG BASE_IMAGE
FROM ${BASE_IMAGE}

# kernel-core is the kernel without the firmware and driver packages a virtual
# machine has no use for.
RUN dnf -y install \
      kernel-core \
      dracut \
      systemd \
      systemd-networkd \
      systemd-resolved \
      chrony \
      cloud-init \
      cloud-utils-growpart \
      openssh-server \
      sudo \
      qemu-guest-agent \
      iproute \
 && dnf clean all

# growpart above is what lets cloud-init grow the root partition to the size of
# the VM's disk at first boot. Without it the guest is stuck with the base
# filesystem, which carries only the slack the image build gives it -- and a
# first boot that copies /etc/skel into a new account fills that and fails with
# ENOSPC, taking cloud-final and sshd down with it. Ubuntu's cloud-init pulls
# growpart in as a dependency; Fedora's does not.

# Installing kernel-core inside a container does not run kernel-install, so two
# things it would normally do are done here instead.
#
# First, kernel-core ships the kernel as /usr/lib/modules/<version>/vmlinuz and
# relies on kernel-install to place it in /boot; without that step /boot is
# empty and there is nothing to extract. Second, the initramfs has to be
# generated explicitly. --no-hostonly matters: a host-specific initramfs built
# here would carry this build machine's hardware assumptions into every guest.
RUN set -eu; \
    version="$(ls /usr/lib/modules | head -n1)"; \
    cp "/usr/lib/modules/${version}/vmlinuz" "/boot/vmlinuz-${version}"; \
    dracut --force --no-hostonly "/boot/initramfs-${version}.img" "${version}"

# The tools an agent expects to find on a working machine.
#
# These live in the base image rather than in per-VM cloud-init packages: a
# base image is built once, content-addressed and cached, so paying for the
# download here keeps `agent-vm create` in the seconds it advertises instead of
# installing the same package set on every first boot. Nothing installed here
# is per-VM state and nothing is secret, so the base image stays shareable
# (SECURITY.md).
#
# wget2-wget rather than wget: Fedora 41 replaced the wget package with
# wget2, and this shim is what still provides /usr/bin/wget — the name every
# script an agent writes actually calls.
RUN dnf -y install \
      iputils \
      traceroute \
      bind-utils \
      nmap-ncat \
      curl \
      wget2-wget \
      rsync \
      openssh-clients \
      git \
      gcc \
      gcc-c++ \
      make \
      automake \
      pkgconf-pkg-config \
      python3 \
      python3-pip \
      jq \
      zip \
      unzip \
      xz \
      less \
      vim-enhanced \
      nano \
      tmux \
      htop \
      procps-ng \
      psmisc \
      diffutils \
      findutils \
      which \
      file \
      tree \
      man-db \
      words \
 && dnf clean all

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

# mise, the tool-version manager the Node.js runtime, the coding agents and the
# JVM toolchain below are all installed with.
#
# The binary goes in /usr/local/bin so that `mise` is on the default PATH for
# every account, including the non-interactive `ssh <vm> mise install` an agent
# may run.
#
# What it installs goes in /usr/local/lib/mise, one store shared by every
# account, and each account's ~/.local/share/mise is a symlink to it. The
# alternative -- a full copy in /etc/skel, which useradd copies into each new
# home -- is what this image used to do, and it cost every single VM about nine
# seconds of first boot and 1.7 GiB of writes into its copy-on-write overlay
# before cloud-init could even get to the SSH keys. A symlink costs neither.
#
# Sharing the store is safe because mise splits the two things cleanly: the
# store holds tool *installs*, keyed by name and version, while *which* version
# an account uses is its own MISE_CONFIG_DIR under its home. Two accounts
# wanting different Node releases get two directories in the shared store and
# one config file each; they never contend for the same install.
#
# What they do share is the ability to write there, which is granted below.
#
# mise skips a release for 24 hours after it is published unless
# MISE_MINIMUM_RELEASE_AGE is 0. A zero duration is how it turns that cutoff
# off, so the tools below and a later `mise use` in the guest are the newest
# release rather than yesterday's. /etc/environment is what sshd gives a
# non-interactive session through PAM. A build step does not read that file,
# so each mise invocation below sets the variable itself. The bootstrap
# installer is a shell script that only accepts a duration with a unit, and a
# bare 0 makes it abort, so that one command uses 0s -- the same cutoff.
RUN set -eu; \
    printf 'MISE_MINIMUM_RELEASE_AGE=0\n' >> /etc/environment; \
    curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise MISE_MINIMUM_RELEASE_AGE=0s sh; \
    install -d -m 0755 /usr/local/lib/mise

# Node.js: what wrangler and Playwright run on, and what a guest handed a
# JavaScript repository builds with.
#
# node and npm come from mise rather than from a distro package or a
# third-party repository, so they are versioned the way every other toolchain
# in this image is: an account that needs another release runs
# `mise use -g node@<version>` for itself, where a root-owned
# /usr/lib/node_modules would have needed sudo. It also decouples the image
# from whatever Node its distro happened to freeze -- Ubuntu 24.04 still ships
# Node 18, past end of life -- so all three recipes get the same runtime by the
# same route.
#
# Like everything else mise installs here this goes into the shared store, so
# it is in place before anyone logs in rather than being downloaded per account
# inside a guest that may have no network at all.
#
# libatomic first: the official Node binaries mise downloads are linked against
# libatomic.so.1, which Fedora's base image does not carry -- a distro package
# would have pulled it in as a dependency, and mise cannot. Without it `node -v`
# fails at the end of the install and takes the build down with it.
RUN dnf -y install libatomic && dnf clean all

RUN set -eu; \
    MISE_DATA_DIR=/usr/local/lib/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
    MISE_MINIMUM_RELEASE_AGE=0 \
      mise use --global --yes node@latest; \
    rm -rf /tmp/mise-cache /usr/local/lib/mise/downloads

# 22.19 or newer is the floor this image promises: wrangler and Playwright both
# want a current release, and a guest handed a JavaScript repository is likelier
# to need a new Node than an old one. A guest that cannot run what it was given
# is indistinguishable from a broken image until someone SSHes in hours later,
# so a too-old Node fails the build here instead of shipping. It runs the Node
# just installed into the shared store -- `mise where` is what turns a version
# mise resolved into a path -- because that is the only Node in the image now;
# there is no packaged one behind it.
RUN set -eu; \
    export MISE_DATA_DIR=/usr/local/lib/mise \
           MISE_CONFIG_DIR=/etc/skel/.config/mise \
           MISE_STATE_DIR=/etc/skel/.local/state/mise; \
    export PATH="$(mise where node)/bin:$PATH"; \
    major="$(node -p 'process.versions.node.split(".")[0]')"; \
    minor="$(node -p 'process.versions.node.split(".")[1]')"; \
    if [ "$major" -lt 22 ] || { [ "$major" -eq 22 ] && [ "$minor" -lt 19 ]; }; then \
      echo "node $(node -v) is too old: this image requires >= 22.19" >&2; \
      exit 1; \
    fi

# The coding agents every guest comes up with.
#
# None of them is packaged by any distro. claude, opencode, pi and agy come
# from mise's registry -- the names below resolve to each vendor's own release
# archive; `agy` is aqua:google-antigravity/antigravity-cli -- so they are
# versioned the same way the JDK below is: an account that needs another
# release runs `mise use -g claude@<version>` for itself, where a root-owned
# global npm prefix would have needed sudo. They are the vendors' native
# builds and carry no Node dependency of their own. agy used to come from its
# vendor's installer, which wrote a root-owned binary into /usr/local/bin that
# a non-root account could not replace and that `agent-vm update` therefore
# left alone; the registry install is what lets the update move it.
#
# The versions are deliberately unpinned, like every other package here: these
# tools ship several releases a week and a pinned one would be stale before the
# image was rebuilt. Reproducibility comes from the digest the manifest records
# for the source image, not from the agent versions (ADR-0006).
#
# The install goes into the shared store, like everything else mise manages
# here, so the agents are in place before anyone logs in rather than being
# downloaded per account inside a guest that may have no network at all.
RUN set -eu; \
    MISE_DATA_DIR=/usr/local/lib/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
    MISE_MINIMUM_RELEASE_AGE=0 \
      mise use --global --yes claude opencode pi agy; \
    rm -rf /tmp/mise-cache /usr/local/lib/mise/downloads

# herdr, the terminal workspace manager a guest runs as a daemon.
#
# It owns the panes the agents above run in and keeps them alive across
# disconnections: a server is started for every account at boot -- see the unit
# further down -- an SSH session attaches to it by running `herdr`, and an
# operator on the host attaches to the same server with `herdr --remote <vm>`.
# Like the agents it comes from mise's registry, so the name resolves to the
# vendor's own release binary and an account that needs another release runs
# `mise use -g herdr@<version>` for itself. The version is unpinned for the same
# reason theirs are: a pin on a tool that ships several releases a week is soon
# a release behind. The manifest records only the source image's digest, not
# this version, so a rebuild is not reproducible (ADR-0006).
#
# The install goes into the shared store, like everything else mise manages
# here, so herdr is in place before anyone logs in rather than being downloaded
# per account inside a guest that may have no network at all.
RUN set -eu; \
    MISE_DATA_DIR=/usr/local/lib/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
    MISE_MINIMUM_RELEASE_AGE=0 \
      mise use --global --yes herdr; \
    rm -rf /tmp/mise-cache /usr/local/lib/mise/downloads

# The JVM toolchain itself: the newest Temurin JDK mise offers, and Maven.
#
# Neither version is pinned, like every other package here -- a pinned one would
# be a release behind before the image was rebuilt. The manifest records only
# the source image's digest, not these versions, so a rebuild is not
# reproducible (ADR-0006).
# The JDK is `java@temurin` and not `java@latest`, which is an Oracle build of
# OpenJDK: mise names a distribution by prefix, and an unprefixed version takes
# whichever one it defaults to. A version mise cannot resolve or install fails
# the build rather than silently shipping a guest with no JDK.
#
# This writes into the shared store, so the JDK and Maven are in place before
# anyone logs in instead of being downloaded per account inside a guest that may
# have no network at all. A JDK is the clearest case for sharing: it is the
# largest thing mise installs here, and copying it per account was most of the
# nine seconds first boot used to spend in useradd.
#
# The cache is a build-time scratch directory and is discarded: it holds the
# downloaded archives, which are of no use once they have been extracted, and
# shipping them would be wasted space in every guest.
RUN set -eu; \
    MISE_DATA_DIR=/usr/local/lib/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
    MISE_MINIMUM_RELEASE_AGE=0 \
      mise use --global --yes java@temurin maven@latest; \
    rm -rf /tmp/mise-cache /usr/local/lib/mise/downloads

# The Go toolchain and golangci-lint, both from mise.
#
# Every family packages some Go, and the versions are years apart: an image
# built on Ubuntu's golang-go and one built on Arch's go are not the same
# toolchain, and a guest whose Go is older than the `go` directive of the
# repository it was handed cannot build that repository at all. Ubuntu does not
# package golangci-lint at all, and where it is packaged the version differs per
# family. mise keeps all three images on the same releases by the same route as
# the JDK above, and an account that needs another one runs
# `mise use -g go@1.25` for itself rather than asking an operator to unpack a
# tarball into /usr/local. Neither is pinned, for the same reason the JDK is
# not: a pin here would be a release behind before the image was rebuilt. The
# manifest records only the source image's digest, not these versions, so a
# rebuild is not reproducible (ADR-0006).
#
# Rust is not here. mise's `rust` is rustup underneath and re-reads
# RUSTUP_HOME and CARGO_HOME from the environment of whoever runs cargo, so it
# would re-run rustup-init per account rather than use what the store already
# holds; the image keeps the one shared rustup installation below instead.
#
# Both go into the shared store, so nothing is downloaded per account inside a
# guest that may have no network at all.
RUN set -eu; \
    MISE_DATA_DIR=/usr/local/lib/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
    MISE_MINIMUM_RELEASE_AGE=0 \
      mise use --global --yes go@latest golangci-lint@latest; \
    rm -rf /tmp/mise-cache /usr/local/lib/mise/downloads

# wrangler, Playwright, cf, and the Grok CLI: the npm packages in the image.
#
# None of them is packaged by any family and all are published only to npm, so
# they come from mise's npm backend -- `npm:` names rather than the registry
# names the agents use, because npm is the only place they exist. grok is
# xAI's CLI (`npm:@xai-official/grok`); the package's bin is `grok`. They are
# installed here, with everything else mise manages, so that they are in the
# shared store before the build runs `playwright install chromium` a few steps
# later. What each is for, and the environment each needs, is
# further down.
RUN set -eu; \
    export MISE_DATA_DIR=/usr/local/lib/mise \
           MISE_CONFIG_DIR=/etc/skel/.config/mise \
           MISE_STATE_DIR=/etc/skel/.local/state/mise \
           MISE_CACHE_DIR=/tmp/mise-cache \
           MISE_MINIMUM_RELEASE_AGE=0; \
    export PATH="$(mise where node)/bin:$PATH"; \
    mise use --global --yes npm:wrangler npm:playwright npm:cf npm:@xai-official/grok; \
    rm -rf /tmp/mise-cache /tmp/fslock /usr/local/lib/mise/downloads

# /tmp/fslock is where mise's npm backend takes the lock it holds while it
# installs a package, and the directory belongs to whichever account created it
# first, at mode 0755. Every install above ran as root, so committing that
# directory would ship a root-owned lock directory in every guest: the first
# `mise use -g npm:<package>` the agent account runs then fails with "failed to
# acquire project lock: Permission denied" before it downloads anything, and so
# does the unelevated half of `agent-vm update`. The rm above keeps the
# build's copy out of the image; this rule recreates the directory at every
# boot with /tmp's own permissions, so whichever account installs first no
# longer locks the others out.
RUN printf 'd /tmp/fslock 1777 root root -\n' > /etc/tmpfiles.d/agent-vm-mise-fslock.conf

# Open the shared store to every account, now that everything is installed in
# it.
#
# Each directory gets the sticky bit along with write permission, which is
# /tmp's arrangement and the one /opt/ms-playwright below uses for the same
# reason: any account may install a tool, none may remove another's. A shared
# writable directory is defensible here on the same grounds the permissive agent
# configuration is -- the VM is the sandbox, single-tenant and disposable, and
# the accounts inside it are not a security boundary (SECURITY.md). Files keep
# the modes they were installed with, so no binary in the store is writable by
# anyone but root.
RUN set -eu; \
    chmod -R a+rX /usr/local/lib/mise; \
    find /usr/local/lib/mise -type d -exec chmod 1777 {} +

# Point every account's mise data directory at the shared store.
#
# /etc/skel is what useradd copies into each new home, and it copies a symlink
# as a symlink -- so the account cloud-init creates gets this link and not 1.7
# GiB of toolchain. root needs its own because it is created before /etc/skel
# holds anything and never consults skel.
#
# The config and state directories stay per account: the config is what selects
# a tool version, and an account that runs `mise use -g node@24` must be able to
# change its own without changing everyone's. Both are a few kilobytes, so
# copying them costs nothing.
RUN set -eu; \
    mkdir -p /etc/skel/.local/share /root/.local/share /root/.local/state /root/.config; \
    ln -sfn /usr/local/lib/mise /etc/skel/.local/share/mise; \
    ln -sfn /usr/local/lib/mise /root/.local/share/mise; \
    cp -a /etc/skel/.local/state/mise /root/.local/state/mise; \
    cp -a /etc/skel/.config/mise /root/.config/mise

COPY mise.sh /etc/profile.d/agent-vm-mise.sh
RUN chmod 0644 /etc/profile.d/agent-vm-mise.sh

# The mise-installed commands, on the default PATH of every account.
#
# A shim is a symlink to the mise binary, which dispatches on the name it was
# called by and resolves the version from the calling account's own mise
# configuration -- so a single symlink in /usr/local/bin serves every account
# without pointing into any account's home. That is what keeps
# `ssh <vm> node script.js`, `ssh <vm> claude -p ...` and
# `ssh <vm> wrangler deploy` working: an ssh command runs no login shell, so it
# never sources the profile script above that puts the per-account shim
# directory on PATH. node and npm are in the list because mise is the only
# place they come from now -- there is no packaged /usr/bin/node behind them.
RUN set -eu; \
    for command in node npm npx go gofmt golangci-lint claude opencode pi agy \
                   herdr wrangler playwright cf grok; do \
      ln -sf /usr/local/bin/mise "/usr/local/bin/${command}"; \
    done

# codex comes from OpenAI's own installer rather than from npm.
#
# `codex remote-control` -- the daemon this image starts at every boot -- runs
# only against the standalone package that installer lays down: it starts and
# updates its app-server from a fixed path,
# $CODEX_HOME/packages/standalone/current, and refuses to run when that
# directory is absent, which is what an npm-installed codex leaves behind.
#
# The package is installed once and shared: it goes to /usr/local/lib/codex and
# the command to /usr/local/bin, rather than into one account's home, because
# it is ~300 MiB and root and every account cloud-init creates need it. Each
# account gets a symlink to it at first boot -- see codex-remote-control.sh.
# CODEX_NON_INTERACTIVE stops the installer prompting for a shell it does not
# have, and /usr/local/bin already being on PATH is what stops it appending a
# PATH block to a shell profile.
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
# Installing an agent and having a working agent are different things: an
# archive for the wrong architecture, or one whose entry point cannot find what
# it needs, unpacks cleanly and only fails when someone finally types the
# command, inside a VM, long after the image was built and cached. This is the
# step that turns that into a failed build.
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

# The GitHub CLI, and the libraries headless Chromium links against.
#
# gh is in Fedora's own repository. Playwright's --with-deps only knows how to
# install dependencies on Debian and Ubuntu, so the browser's shared libraries
# are named explicitly here. The smoke test at the end of this file is what
# catches an incomplete list: a missing one of these is a browser that exits at
# once with a linker error, which no amount of successful installing reveals.
RUN dnf -y install \
      gh \
      nss \
      nspr \
      atk \
      at-spi2-atk \
      cups-libs \
      libdrm \
      libXcomposite \
      libXdamage \
      libXfixes \
      libXrandr \
      libxkbcommon \
      mesa-libgbm \
      alsa-lib \
      pango \
      cairo \
 && dnf clean all

# tea, the Gitea CLI.
#
# Only Arch packages it, and on Ubuntu the name is already taken by an
# unrelated text editor -- installing "tea" there would silently give a guest
# the wrong program. So it comes from Gitea's own release server on all three,
# which also keeps the version the same everywhere.
RUN set -eu; \
    case "$(uname -m)" in \
      x86_64 | amd64) arch=amd64 ;; \
      aarch64 | arm64) arch=arm64 ;; \
      *) echo "no tea release for $(uname -m)" >&2; exit 1 ;; \
    esac; \
    version="$(curl -fsSL https://dl.gitea.com/tea/ | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | sort -uV | tail -n1)"; \
    if [ -z "${version}" ]; then \
      echo "could not work out the latest tea version from Gitea's release index" >&2; \
      exit 1; \
    fi; \
    curl -fsSL -o /usr/local/bin/tea \
      "https://dl.gitea.com/tea/${version}/tea-${version}-linux-${arch}"; \
    chmod 0755 /usr/local/bin/tea

# wrangler, Cloudflare's CLI.
#
# The command itself is installed with mise above, from npm, which is the only
# place Cloudflare publishes it. The version is unpinned like every other one
# here (ADR-0006).
#
# No credential is baked in: `wrangler login` is an OAuth flow and an API
# credential is per-VM, arriving through --cloud-init if at all, because a base
# image is shared by every VM built on it (SECURITY.md). A fresh guest has the
# command and no Cloudflare account attached to it.

# Wrangler reports anonymous usage metrics unless told not to, and a disposable
# VM an agent drives is not a machine whose operator chose to opt in. This goes
# in /etc/environment for the same reason PLAYWRIGHT_BROWSERS_PATH does: it has
# to reach the non-interactive `ssh <vm> wrangler deploy` an agent actually
# runs, which reads that file through PAM but no profile script.
RUN printf 'WRANGLER_SEND_METRICS=false\n' >> /etc/environment

# Playwright, and the one Chromium in this image.
#
# The `playwright` command is installed with mise above; what is left here is
# the browser it drives. The browsers go to /opt/ms-playwright rather than the
# per-user default under ~/.cache, so that every account on the VM shares one
# copy instead of each downloading its own on first use -- which a
# network-isolated guest could not do at all. It also keeps them out of the
# per-account mise copy, which would otherwise carry a browser per user.
#
# PLAYWRIGHT_BROWSERS_PATH goes in /etc/environment rather than a profile
# script because PAM applies it to every session, including the
# non-interactive `ssh <vm> node script.js` that an agent actually uses. A
# profile.d file would leave exactly that case pointing at an empty ~/.cache.
RUN PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright playwright install chromium
RUN printf 'PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright\n' >> /etc/environment

# That directory has to be writable by every account, not only by the root that
# filled it during the build.
#
# Playwright takes a lock at $PLAYWRIGHT_BROWSERS_PATH/__dirlock for any
# install and records which package needs which build under .links, so a plain
# `playwright install` fails as any other account with "EACCES: permission
# denied, mkdir '/opt/ms-playwright/__dirlock'" -- and that command is exactly
# what an agent runs after mise moves playwright to a release pinning a newer
# browser build, because it is what Playwright's own "browser not found" error
# tells it to run.
#
# The sticky bit makes this /tmp's arrangement: any account may add a browser
# build, none may remove another's. A shared directory writable by every
# account is defensible here for the reason the agents' permissive
# configuration is -- the VM is the sandbox, single-tenant and disposable, and
# the accounts inside it are not a security boundary (SECURITY.md).
RUN install -d -m 1777 /opt/ms-playwright /opt/ms-playwright/.links

# Expose that browser as `chromium`, so it is usable without going through
# Playwright. See the script for why there is only one Chromium here.
COPY chromium.sh /usr/local/bin/chromium
RUN chmod 0755 /usr/local/bin/chromium

# Run each of these once, and fail the build if any of them does not work.
#
# Chromium is the reason this step exists. A browser with one shared library
# missing installs perfectly and then exits the moment it is launched, so
# "playwright install succeeded" says nothing about whether a guest can
# actually drive a page. Launching it here is the only thing that does.
RUN set -eu; \
    gh --version >/dev/null || { echo "gh installed but cannot run" >&2; exit 1; }; \
    tea --version >/dev/null || { echo "tea installed but cannot run" >&2; exit 1; }; \
    wrangler --version >/dev/null || { echo "wrangler installed but cannot run" >&2; exit 1; }; \
    cf --version >/dev/null || { echo "cf installed but cannot run" >&2; exit 1; }; \
    playwright --version >/dev/null || { echo "playwright installed but cannot run" >&2; exit 1; }; \
    if ! chromium --headless=new --no-sandbox --disable-gpu --dump-dom about:blank >/dev/null 2>/tmp/chromium-smoke.log; then \
      echo "chromium installed but cannot start headless:" >&2; \
      tail -n 20 /tmp/chromium-smoke.log >&2; \
      exit 1; \
    fi; \
    rm -f /tmp/chromium-smoke.log; \
    mise --version >/dev/null || { echo "mise installed but cannot run" >&2; exit 1; }; \
    [ -d /usr/local/lib/mise/shims ] \
      || { echo "the mise shims are missing from the shared store, so no account will have the agents, java or mvn" >&2; exit 1; }; \
    [ -L /etc/skel/.local/share/mise ] \
      || { echo "/etc/skel/.local/share/mise is not a symlink to the shared store; every account cloud-init creates would copy the whole toolchain at first boot" >&2; exit 1; }; \
    bash -lc 'command -v java' >/dev/null 2>&1 \
      || { echo "no java on the path of a login shell; the shims are not on PATH or the mise java install did not take" >&2; exit 1; }; \
    bash -lc 'java -version' >/dev/null 2>&1 \
      || { echo "java is on the path of a login shell but cannot run; the mise java install did not take" >&2; exit 1; }; \
    bash -lc 'mvn -version' >/dev/null 2>&1 \
      || { echo "no Maven on the path of a login shell; the mise maven install did not take" >&2; exit 1; }

# Rust through rustup, installed once into /usr/local and shared by every
# account.
#
# rustup installs per account under ~/.rustup by default, which would leave
# each account on the VM downloading its own toolchain at first use, possibly
# inside a guest with no network at all -- and going through mise, which is
# rustup underneath, would do the same thing at 1.5 GiB per account. A shared
# installation costs two things and is worth them: RUSTUP_HOME has to be
# visible to every account (below), and `rustup update` needs sudo because the
# directory is root-owned. Crates a user installs are unaffected -- CARGO_HOME
# is deliberately left unset, so `cargo install` writes into that account's own
# ~/.cargo.
#
# The version is unpinned like every other one here, and the installer is piped
# to sh straight from the vendor. The manifest records only the source image's
# digest, not either of them, so a rebuild is not reproducible (ADR-0006).
RUN set -eu; \
    export RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo; \
    curl -fsSL https://sh.rustup.rs \
      | sh -s -- -y --no-modify-path --profile default --default-toolchain stable; \
    for proxy in "${CARGO_HOME}"/bin/*; do \
      ln -sf "${proxy}" "/usr/local/bin/$(basename "${proxy}")"; \
    done; \
    chmod -R a+rX /usr/local/rustup /usr/local/cargo

# RUSTUP_HOME goes in /etc/environment rather than a profile script for the
# same reason PLAYWRIGHT_BROWSERS_PATH does: sshd reads that file through PAM,
# so the `ssh <vm> cargo build` an agent actually runs sees it, while a profile
# script would leave cargo looking for a toolchain in an empty ~/.rustup.
RUN printf 'RUSTUP_HOME=/usr/local/rustup\n' >> /etc/environment

# $HOME/go/bin and $HOME/.cargo/bin on the PATH of a login shell, for what a
# user installs later. The toolchains themselves need no PATH entry.
COPY toolchains.sh /etc/profile.d/agent-vm-toolchains.sh
RUN chmod 0644 /etc/profile.d/agent-vm-toolchains.sh

# Run each of these once, and fail the build if any of them cannot start.
#
# The same reasoning as the smoke tests above: a toolchain that unpacked but
# cannot run is indistinguishable from a working one until someone types the
# command inside a VM, hours after the image was built and cached. cargo is
# reached the way a guest reaches it, through /etc/environment, because the
# shared RUSTUP_HOME is the part of this that can be wrong while every file is
# in place.
RUN set -eu; \
    go version >/dev/null || { echo "the Go toolchain installed but cannot run" >&2; exit 1; }; \
    printf 'package main\n' | gofmt >/dev/null || { echo "gofmt installed but cannot run" >&2; exit 1; }; \
    golangci-lint --version >/dev/null || { echo "golangci-lint installed but cannot run" >&2; exit 1; }; \
    set -a; . /etc/environment; set +a; \
    rustc --version >/dev/null || { echo "the Rust toolchain installed but rustc cannot run" >&2; exit 1; }; \
    cargo --version >/dev/null || { echo "cargo installed but cannot run" >&2; exit 1; }; \
    rustup --version >/dev/null || { echo "rustup installed but cannot run" >&2; exit 1; }; \
    cargo fmt --version >/dev/null || { echo "rustfmt is missing from the Rust toolchain" >&2; exit 1; }; \
    cargo clippy --version >/dev/null || { echo "clippy is missing from the Rust toolchain" >&2; exit 1; }

# moby-engine is Fedora's build of the Docker daemon; docker-ce is not in the
# distro repositories, and adding Docker's own would mean a second registry and
# GPG key in every build.
RUN dnf -y install \
      moby-engine \
      containerd \
      docker-compose \
      docker-buildx \
 && dnf clean all

# Docker starts at boot so an agent finds a working daemon without asking.
RUN systemctl --root=/ enable docker.service containerd.service

# Cross-architecture container builds: docker build --platform linux/arm64.
#
# The usual recipe for this is `docker run --privileged --rm tonistiigi/binfmt
# --install arm64` inside the running machine. That is the wrong shape for a
# base image: it needs the daemon up and a registry reachable, so it costs a
# pull on a VM that may have no route out, and the registration it makes lives
# in the kernel of that one boot only. The distro's own qemu-user-static
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

RUN dnf -y install \
      qemu-user-static-aarch64 \
      qemu-user-static-x86 \
 && dnf clean all

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
# a guest can run agent-vm, or virt-install and virsh directly. libvirt starts
# its own dnsmasq per network; the package is not enabled as a system-wide
# resolver here and must not be, or it would fight with those instances.
RUN dnf -y install \
      qemu-kvm \
      qemu-img \
      libvirt \
      libvirt-client \
      virt-install \
      dnsmasq \
      guestfs-tools \
      podman \
 && dnf clean all

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
    podman --version >/dev/null || { echo "podman installed but cannot run" >&2; exit 1; }; \
    dnsmasq --version >/dev/null || { echo "dnsmasq installed but cannot run" >&2; exit 1; }

# SELinux is turned off in this image, and has to be.
#
# The root filesystem is built by virt-make-fs from a flattened container
# export, so not one file carries a security.selinux xattr. The upstream
# container image has no policy installed and does not care, but
# container-selinux — pulled in by podman and libvirt above — brings
# selinux-policy-targeted with it, and that ships /etc/selinux/config set to
# enforcing. systemd then tries to relabel an entirely unlabeled filesystem on
# first boot, fails, and freezes PID 1 with "Failed to allocate manager object"
# about three seconds in. The guest never reaches networking, so the symptom an
# operator sees is `agent-vm create` timing out waiting for an address, with no
# hint that init died.
#
# Disabling the policy rather than removing it keeps podman and libvirt
# installable, since container-selinux depends on it. Doing it here rather than
# with selinux=0 on the kernel command line keeps one command line shared across
# all three families (internal/image/distro), which is a recorded contract.
# SELinux stays enabled in the kernel with no policy loaded, and a kernel with
# no policy loaded permits everything.
#
# The config file is written from scratch when it is absent, because whether it
# exists at all depends on a dependency this recipe does not ask for directly.
RUN set -eu; \
    mkdir -p /etc/selinux; \
    if [ -f /etc/selinux/config ]; then \
      sed -i 's/^SELINUX=.*/SELINUX=disabled/' /etc/selinux/config; \
    else \
      printf 'SELINUX=disabled\nSELINUXTYPE=targeted\n' > /etc/selinux/config; \
    fi; \
    grep -q '^SELINUX=disabled$' /etc/selinux/config \
      || { echo "/etc/selinux/config is not disabled; this guest would freeze at boot" >&2; exit 1; }

# There is no running systemd inside a build, so units are enabled offline with
# --root=/, which only writes the symlinks an enable would create. This must not
# be allowed to fail quietly: a guest without sshd looks exactly like a guest
# that failed to boot, hours later and from the outside.
RUN systemctl --root=/ enable \
      sshd.service \
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

RUN printf '/dev/vda1 / ext4 defaults 0 1\n' > /etc/fstab

# NoCloud only: a guest must never probe a metadata service on the network.
#
# The 99 prefix is load-bearing. cloud-init reads /etc/cloud/cloud.cfg.d in
# sorted order and the last file to set a key wins. This file was once named
# 90-agent-vm-datasource.cfg, which sorts before Ubuntu's own 90_dpkg.cfg
# ('-' is 0x2D, '_' is 0x5F) -- that file lists Ec2 and every other network
# datasource, so the pin was silently overridden there. This family does not
# ship such a file today, but it is named consistently across all three so the
# guarantee does not depend on which distro is being built.
RUN printf 'datasource_list: [ NoCloud, None ]\n' > /etc/cloud/cloud.cfg.d/99-agent-vm-datasource.cfg

# Prove the pin actually wins rather than trusting the prefix.
#
# Sorting last is a property of every *other* file in the directory, so it is
# not something this recipe can guarantee on its own: a distro that adds a
# later-sorting datasource_list at any point would take the guarantee away
# again, silently. Checking it here means that turns into a failed build.
RUN set -eu; \
    last="$(grep -l '^datasource_list:' /etc/cloud/cloud.cfg.d/*.cfg | sort | tail -n1)"; \
    if [ "${last}" != "/etc/cloud/cloud.cfg.d/99-agent-vm-datasource.cfg" ]; then \
      echo "${last} sets datasource_list after this image's own pin does;" >&2; \
      echo "cloud-init would use it and the guest could probe a metadata service" >&2; \
      exit 1; \
    fi

# Apply the hostname agent-vm asked for, not this family's fallback FQDN.
#
# cloud-init's RHEL/Fedora distro class sets prefer_fqdn = True, so when both a
# hostname and an FQDN are available it applies the FQDN. The generated
# user-data sets `hostname:` and deliberately does not set `fqdn:` — a
# disposable VM has no domain — so cloud-init falls back to the *system* FQDN
# for that value, which on Fedora is systemd's compiled-in fallback, the
# literal string "fedora". Every guest then came up named "fedora" no matter
# what the VM was called, and `agent-vm ssh <vm> hostname` answered for a name
# that identified nothing.
#
# Ubuntu and Arch do not prefer the FQDN, so this belongs to this family rather
# than to the shared user-data template — which is a golden-pinned public
# contract (AGENTS.md §8) and should not gain a field to work around one
# distro's default.
RUN printf 'prefer_fqdn_over_hostname: false\n' > /etc/cloud/cloud.cfg.d/99-agent-vm-hostname.cfg

# Prove nothing later in the directory turns the preference back on, for the
# same reason the datasource pin is checked rather than trusted.
RUN set -eu; \
    last="$(grep -l '^prefer_fqdn_over_hostname:' /etc/cloud/cloud.cfg.d/*.cfg | sort | tail -n1)"; \
    if [ "${last}" != "/etc/cloud/cloud.cfg.d/99-agent-vm-hostname.cfg" ]; then \
      echo "${last} sets prefer_fqdn_over_hostname after this image's own pin does;" >&2; \
      echo "guests would be named for the fallback FQDN instead of the VM" >&2; \
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
