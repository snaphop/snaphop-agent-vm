# Arch Linux base image for agent-vm.
#
# Same contract as the other families: a kernel and initramfs, an init system,
# cloud-init, sshd, sudo, and the guest agent. No credentials, no per-VM state.
#
# Arch is a rolling target: a rebuilt image is not last week's image, so
# reproducibility comes from the digest recorded in the manifest rather than
# from the tag (ADR-0006).
#
# BASE_IMAGE is passed in pinned to a digest. Do not add a default.
ARG BASE_IMAGE
FROM ${BASE_IMAGE}

RUN pacman -Syu --noconfirm --needed \
      linux \
      mkinitcpio \
      systemd \
      cloud-init \
      openssh \
      sudo \
      qemu-guest-agent \
      iproute2 \
 && pacman -Scc --noconfirm

# The linux package's install hook may be skipped in a container, so the
# initramfs is generated explicitly. This produces /boot/initramfs-linux.img.
RUN mkinitcpio -P

# The tools an agent expects to find on a working machine.
#
# These live in the base image rather than in per-VM cloud-init packages: a
# base image is built once, content-addressed and cached, so paying for the
# download here keeps `agent-vm create` in the seconds it advertises instead of
# installing the same package set on every first boot. Nothing installed here
# is per-VM state and nothing is secret, so the base image stays shareable
# (SECURITY.md).
RUN pacman -Syu --noconfirm --needed \
      iputils \
      traceroute \
      bind \
      openbsd-netcat \
      curl \
      wget \
      rsync \
      git \
      base-devel \
      python \
      python-pip \
      jq \
      zip \
      unzip \
      xz \
      less \
      vim \
      nano \
      tmux \
      htop \
      procps-ng \
      psmisc \
      file \
      tree \
      man-db \
 && pacman -Scc --noconfirm

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

# Node.js: what wrangler and Playwright run on, and what a guest handed a
# JavaScript repository builds with. Arch is a rolling target, so its own
# package is always current enough and no third-party repository is involved.
# The version assertion below is what catches it if that changes.
RUN pacman -Syu --noconfirm --needed \
      nodejs \
      npm \
 && pacman -Scc --noconfirm

# 22.19 or newer is the floor this image promises: wrangler and Playwright both
# want a current release, and a guest handed a JavaScript repository is likelier
# to need a new Node than an old one. A guest that cannot run what it was given
# is indistinguishable from a broken image until someone SSHes in hours later,
# so a too-old Node fails the build here instead of shipping.
RUN set -eu; \
    major="$(node -p 'process.versions.node.split(".")[0]')"; \
    minor="$(node -p 'process.versions.node.split(".")[1]')"; \
    if [ "$major" -lt 22 ] || { [ "$major" -eq 22 ] && [ "$minor" -lt 19 ]; }; then \
      echo "node $(node -v) is too old: this image requires >= 22.19" >&2; \
      exit 1; \
    fi

# mise, the tool-version manager the coding agents and the JVM toolchain below
# are installed with.
#
# The binary goes in /usr/local/bin so that `mise` is on the default PATH for
# every account, including the non-interactive `ssh <vm> mise install` an agent
# may run. What it installs is per account -- see below.
RUN set -eu; \
    curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh

# The coding agents every guest comes up with.
#
# This is the one place in the image that installs software from outside the
# distro's own repositories: none of these five are packaged by any distro.
# claude, opencode and pi come from mise's registry -- the names below resolve
# to each vendor's own release archive -- so they are versioned the same way
# the JDK below is: an account that needs another release runs
# `mise use -g claude@<version>` for itself, where a root-owned global npm
# prefix would have needed sudo. They are the vendors' native builds and carry
# no Node dependency of their own. agy has no such release and comes from its
# vendor's installer, pointed at /usr/local/bin so that every account on the VM
# finds it rather than only root. That binary self-updates in the background
# and cannot write to /usr/local/bin as a non-root user, so guests keep the
# version the image was built with.
#
# The versions are deliberately unpinned, like every other package here: these
# tools ship several releases a week and a pinned one would be stale before the
# image was rebuilt. Reproducibility comes from the digest the manifest records
# for the source image, not from the agent versions (ADR-0006).
#
# The install goes into /etc/skel, like everything else mise manages here, so
# the agents are in place before anyone logs in rather than being downloaded
# per account inside a guest that may have no network at all. See the JVM
# toolchain below for why the destination is skel and not one shared directory.
RUN set -eu; \
    MISE_DATA_DIR=/etc/skel/.local/share/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
      mise use --global --yes claude opencode pi; \
    rm -rf /tmp/mise-cache /etc/skel/.local/share/mise/downloads

# The JVM toolchain itself: the newest Temurin JDK mise offers, and Maven.
#
# Neither version is pinned, like every other package here -- a pinned one would
# be a release behind before the image was rebuilt, and reproducibility comes
# from the digest the manifest records for the source image (ADR-0006).
# The JDK is `java@temurin` and not `java@latest`, which is an Oracle build of
# OpenJDK: mise names a distribution by prefix, and an unprefixed version takes
# whichever one it defaults to. A version mise cannot resolve or install fails
# the build rather than silently shipping a guest with no JDK.
#
# This writes into /etc/skel, the copy every account created later inherits, so
# the JDK and Maven are in place before anyone logs in instead of being
# downloaded per account inside a guest that may have no network at all. It goes
# to /etc/skel rather than one shared directory because installing a tool writes
# into mise's data directory, so a single shared one would have every user on
# the VM writing to the same place.
#
# The cache is a build-time scratch directory and is discarded: it holds the
# downloaded archives, which are of no use once they have been extracted, and
# every account inheriting a copy of them would be wasted space in every guest.
RUN set -eu; \
    MISE_DATA_DIR=/etc/skel/.local/share/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
      mise use --global --yes java@temurin maven@latest; \
    rm -rf /tmp/mise-cache /etc/skel/.local/share/mise/downloads

# wrangler and Playwright, the two npm packages left in the image.
#
# Neither is packaged by any family and both are published only to npm, so they
# come from mise's npm backend -- `npm:` names rather than the registry names
# the agents use, because npm is the only place they exist. They are installed
# here, with everything else mise manages, so that root inherits them in the
# copy below and the build can run `playwright install chromium` a few steps
# later. What each is for, and the environment each needs, is further down.
RUN set -eu; \
    MISE_DATA_DIR=/etc/skel/.local/share/mise \
    MISE_CONFIG_DIR=/etc/skel/.config/mise \
    MISE_STATE_DIR=/etc/skel/.local/state/mise \
    MISE_CACHE_DIR=/tmp/mise-cache \
      mise use --global --yes npm:wrangler npm:playwright; \
    rm -rf /tmp/mise-cache /etc/skel/.local/share/mise/downloads

# root is created before /etc/skel exists in this form and never inherits from
# it, so it gets the same toolchain copied in explicitly. mkdir -p rather than a
# plain copy of .local and .config: the agent configuration below lands in
# both as well.
RUN set -eu; \
    mkdir -p /root/.local/share /root/.local/state /root/.config; \
    cp -a /etc/skel/.local/share/mise /root/.local/share/mise; \
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
# `ssh <vm> claude -p ...` and `ssh <vm> wrangler deploy` working: an ssh
# command runs no login shell, so it never sources the profile script above
# that puts the per-account shim directory on PATH.
RUN set -eu; \
    for command in claude opencode pi wrangler playwright; do \
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

RUN curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin

# Run every agent once, and fail the build if any of them cannot start.
#
# Installing an agent and having a working agent are different things: an
# archive for the wrong architecture, or one whose entry point cannot find what
# it needs, unpacks cleanly and only fails when someone finally types the
# command, inside a VM, long after the image was built and cached. This is the
# step that turns that into a failed build.
RUN set -eu; \
    for agent in claude codex opencode pi agy; do \
      if ! "$agent" --version >/dev/null 2>&1; then \
        echo "the ${agent} CLI installed but cannot run:" >&2; \
        "$agent" --version >&2 || true; \
        exit 1; \
      fi; \
    done

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
# skel. pi is absent because it does not gate tool calls at all.
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
RUN set -eu; \
    for file in .claude/settings.json .codex/config.toml .config/opencode/opencode.json; do \
      install -D -m 0644 "/etc/skel/${file}" "/root/${file}"; \
    done

# agy is the exception: it has no configuration file for tool permissions, so
# the flag is the only way to run it unattended and it gets an alias. This
# reaches interactive shells only -- see the file for what that leaves out.
COPY agent-aliases.sh /etc/profile.d/agent-vm-agents.sh
RUN chmod 0644 /etc/profile.d/agent-vm-agents.sh

# The GitHub CLI, and the libraries headless Chromium links against.
#
# gh is packaged as github-cli here. Playwright's --with-deps only knows how to
# install dependencies on Debian and Ubuntu, so the browser's shared libraries
# are named explicitly. The smoke test at the end of this file is what catches
# an incomplete list: a missing one of these is a browser that exits at once
# with a linker error, which installing cleanly never reveals.
RUN pacman -Syu --noconfirm --needed \
      github-cli \
      nss \
      nspr \
      atk \
      at-spi2-atk \
      cups \
      libdrm \
      libxcomposite \
      libxdamage \
      libxfixes \
      libxrandr \
      libxkbcommon \
      mesa \
      alsa-lib \
      pango \
      cairo \
 && pacman -Scc --noconfirm

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
    playwright --version >/dev/null || { echo "playwright installed but cannot run" >&2; exit 1; }; \
    if ! chromium --headless=new --no-sandbox --disable-gpu --dump-dom about:blank >/dev/null 2>/tmp/chromium-smoke.log; then \
      echo "chromium installed but cannot start headless:" >&2; \
      tail -n 20 /tmp/chromium-smoke.log >&2; \
      exit 1; \
    fi; \
    rm -f /tmp/chromium-smoke.log; \
    mise --version >/dev/null || { echo "mise installed but cannot run" >&2; exit 1; }; \
    [ -d /etc/skel/.local/share/mise/shims ] \
      || { echo "the mise shims are missing from /etc/skel, so accounts cloud-init creates will not have the agents, java or mvn" >&2; exit 1; }; \
    bash -lc 'command -v java' >/dev/null 2>&1 \
      || { echo "no java on the path of a login shell; the shims are not on PATH or the mise java install did not take" >&2; exit 1; }; \
    bash -lc 'java -version' >/dev/null 2>&1 \
      || { echo "java is on the path of a login shell but cannot run; the mise java install did not take" >&2; exit 1; }; \
    bash -lc 'mvn -version' >/dev/null 2>&1 \
      || { echo "no Maven on the path of a login shell; the mise maven install did not take" >&2; exit 1; }

# The Go and Rust toolchains, both from upstream rather than from the family's
# own packages.
#
# Every family packages some Go, and the versions are years apart: an image
# built on Ubuntu's golang-go and one built on Arch's go are not the same
# toolchain, and a guest whose Go is older than the `go` directive of the
# repository it was handed cannot build that repository at all. Upstream Go
# and rustup keep all three images on the same release -- the same reasoning as
# tea above. Neither is pinned, for the same reason mise's JDK is not:
# reproducibility comes from the digest the manifest records (ADR-0006), and a
# pin here would be a release behind before the image was rebuilt.
#
# Both install into /usr/local with their entry points symlinked into
# /usr/local/bin, which is on the default PATH, so a non-interactive
# `ssh <vm> go build` finds them without sourcing a profile script.
RUN set -eu; \
    case "$(uname -m)" in \
      x86_64) goarch=amd64 ;; \
      aarch64) goarch=arm64 ;; \
      *) echo "no Go release is published for $(uname -m)" >&2; exit 1 ;; \
    esac; \
    version="$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -n1)"; \
    case "${version}" in \
      go[0-9]*) ;; \
      *) echo "https://go.dev/VERSION named no release (got '${version}'); its format must have changed" >&2; exit 1 ;; \
    esac; \
    echo "installing ${version} for linux-${goarch}"; \
    curl -fsSL "https://go.dev/dl/${version}.linux-${goarch}.tar.gz" -o /tmp/go.tar.gz; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; \
    rm -f /tmp/go.tar.gz; \
    ln -sf /usr/local/go/bin/go /usr/local/go/bin/gofmt /usr/local/bin/

# Rust through rustup, installed once into /usr/local and shared by every
# account.
#
# rustup installs per account under ~/.rustup by default, which would leave
# each account on the VM downloading its own toolchain at first use, possibly
# inside a guest with no network at all. A shared installation costs two
# things and is worth them: RUSTUP_HOME has to be visible to every account
# (below), and `rustup update` needs sudo because the directory is root-owned.
# Crates a user installs are unaffected -- CARGO_HOME is deliberately left
# unset, so `cargo install` writes into that account's own ~/.cargo.
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

# golangci-lint, from its own installer on all three families.
#
# Ubuntu does not package it at all, and where it is packaged the version
# differs per family, so the upstream installer is what keeps every image on
# the same one -- the same reasoning as tea above. It needs a Go toolchain to
# analyze anything, which is why it follows the one installed above.
RUN set -eu; \
    curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
      | sh -s -- -b /usr/local/bin

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

# Arch ships Docker in its own repositories, so no third-party repository or
# convenience script is involved.
RUN pacman -Syu --noconfirm --needed \
      docker \
      docker-compose \
      docker-buildx \
 && pacman -Scc --noconfirm

# Docker starts at boot so an agent finds a working daemon without asking.
RUN systemctl --root=/ enable docker.service containerd.service

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

# The virtualization stack, so a VM can create VMs of its own.
#
# This is the same set of host tools agent-vm itself drives (AGENTS.md §3), so
# a guest can run agent-vm, or virt-install and virsh directly. libvirt starts
# its own dnsmasq per network; the package is not enabled as a system-wide
# resolver here and must not be, or it would fight with those instances.
RUN pacman -Syu --noconfirm --needed \
      qemu-base \
      qemu-img \
      libvirt \
      virt-install \
      iptables-nft \
      dnsmasq \
      guestfs-tools \
      podman \
 && pacman -Scc --noconfirm

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

# There is no running systemd inside a build, so units are enabled offline with
# --root=/, which only writes the symlinks an enable would create. This must not
# be allowed to fail quietly: a guest without sshd looks exactly like a guest
# that failed to boot, hours later and from the outside.
RUN systemctl --root=/ enable \
      sshd.service \
      systemd-networkd.service \
      systemd-resolved.service

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

# systemd-firstboot must never run: it is interactive, and it blocks the boot.
#
# The image ships no /etc/machine-id, which is exactly the condition systemd
# reads as "this is a first boot", and Arch enables systemd-firstboot.service in
# sysinit.target.wants. With a serial console attached the unit decides it has a
# human in front of it and prompts — "Please enter the new timezone name or
# number" — then waits forever. Boot stops there, so cloud-init never runs, the
# guest never configures a network, and `agent-vm create` times out waiting for
# an address with no sign that anything asked a question.
#
# Masking rather than presetting a timezone and locale: nothing this unit
# configures matters to a disposable VM. cloud-init sets the hostname, and
# /etc/locale.conf is already in the image. Masking cannot be undone by a
# package update the way a preset value could be overwritten.
#
# This does not affect the machine ID. systemd itself initializes that from the
# SMBIOS/DMI UUID during early boot, independently of this unit, so every VM
# still gets its own.
RUN set -eu; \
    systemctl --root=/ mask systemd-firstboot.service; \
    test -L /etc/systemd/system/systemd-firstboot.service \
      || { echo "systemd-firstboot.service is not masked; this guest would block at boot" >&2; exit 1; }

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
