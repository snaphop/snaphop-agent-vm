# Ubuntu base image for agent-vm.
#
# This adds what a VM needs and a container image lacks: a kernel and
# initramfs, an init system, cloud-init for first-boot configuration, an SSH
# server, sudo, and the QEMU guest agent. Nothing else — a base image is shared
# by every VM built on it and cached indefinitely, so it must contain no
# credentials and no per-VM state (SECURITY.md).
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
      cloud-init \
      openssh-server \
      sudo \
      qemu-guest-agent \
      iproute2 \
      ca-certificates \
      lsb-release \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# The tools an agent expects to find on a working machine.
#
# These live in the base image rather than in per-VM cloud-init packages: a
# base image is built once, content-addressed and cached, so paying for the
# download here keeps `agent-vm create` in the seconds it advertises instead of
# installing the same package set on every first boot. Nothing installed here
# is per-VM state and nothing is secret, so the base image stays shareable
# (SECURITY.md).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      iputils-ping \
      iputils-tracepath \
      traceroute \
      dnsutils \
      netcat-openbsd \
      curl \
      wget \
      rsync \
      openssh-client \
      git \
      build-essential \
      pkg-config \
      python3 \
      python3-pip \
      python3-venv \
      jq \
      zip \
      unzip \
      xz-utils \
      less \
      vim \
      nano \
      tmux \
      htop \
      procps \
      psmisc \
      file \
      tree \
      man-db \
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

# Node.js, which the agent CLIs below run on.
#
# It comes from NodeSource rather than Ubuntu's own repository, the only
# third-party repository in any of these recipes. Ubuntu 24.04 ships Node
# 18, and the agents need 22.19 or newer, so there is no version of this that
# stays inside the distro. The key is fetched and dearmoured explicitly
# instead of piping NodeSource's setup script into a shell, so the repository
# and the key it is trusted under are both visible here.
RUN apt-get update \
 && apt-get install -y --no-install-recommends gnupg \
 && install -d -m 0755 /usr/share/keyrings \
 && curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
      | gpg --dearmor -o /usr/share/keyrings/nodesource.gpg \
 && printf '%s\n' \
      'deb [signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_24.x nodistro main' \
      > /etc/apt/sources.list.d/nodesource.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends nodejs \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# The agents need Node 22.19 or newer -- pi and claude both refuse to start on
# anything older. A guest whose agents will not start is indistinguishable from
# a broken image until someone SSHes in hours later, so a too-old Node fails
# the build here instead of shipping.
RUN set -eu; \
    major="$(node -p 'process.versions.node.split(".")[0]')"; \
    minor="$(node -p 'process.versions.node.split(".")[1]')"; \
    if [ "$major" -lt 22 ] || { [ "$major" -eq 22 ] && [ "$minor" -lt 19 ]; }; then \
      echo "node $(node -v) is too old: the agent CLIs require >= 22.19" >&2; \
      exit 1; \
    fi

# The coding agents every guest comes up with.
#
# This is the one place in the image that installs software from outside the
# distro's own repositories: none of these five are packaged by any distro.
# Four publish to npm and are installed with it. agy is a Go binary with no npm
# package, so it comes from its vendor's installer, pointed at /usr/local/bin
# so that every account on the VM finds it rather than only root. That binary
# self-updates in the background and cannot write to /usr/local/bin as a
# non-root user, so guests keep the version the image was built with.
#
# The versions are deliberately unpinned, like every other package here: these
# tools ship several releases a week and a pinned one would be stale before the
# image was rebuilt. Reproducibility comes from the digest the manifest records
# for the source image, not from the agent versions (ADR-0006).
# npm is held to the 11 line because npm 12 does not run these packages'
# postinstall scripts, and claude and opencode both download their native
# binary in one. npm 12 installs them without complaint and the commands then
# fail at the first run with "native binary not installed" -- a broken guest
# that looks like a successful build. Arch hits this today (it packages npm 12
# against whatever Node it currently ships); Ubuntu and Fedora will when their
# npm catches up, so all three are pinned rather than only the one that breaks
# now. The smoke test below is what will say when this pin can be lifted.
RUN npm install -g npm@11 \
 && npm cache clean --force

RUN npm install -g \
      @anthropic-ai/claude-code \
      @openai/codex \
      opencode-ai \
      @earendil-works/pi-coding-agent \
 && npm cache clean --force

RUN curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin

# Run every agent once, and fail the build if any of them cannot start.
#
# Installing an agent and having a working agent are different things: a
# package whose postinstall did not run installs cleanly and only fails when
# someone finally types the command, inside a VM, long after the image was
# built and cached. This is the step that turns that into a failed build, and
# it is what makes the npm pin above self-policing.
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
# gh is in Ubuntu's own repository. Chromium's dependencies are left to
# Playwright, which knows them per release and installs them below with
# --with-deps; getting that list wrong shows up as a browser that exits
# immediately with a missing .so, not as a failed build.
RUN apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

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

# Playwright, and the one Chromium in this image.
#
# The browsers go to /opt/ms-playwright rather than the per-user default under
# ~/.cache, so that every account on the VM shares one copy instead of each
# downloading its own on first use -- which a network-isolated guest could not
# do at all.
#
# PLAYWRIGHT_BROWSERS_PATH goes in /etc/environment rather than a profile
# script because PAM applies it to every session, including the
# non-interactive `ssh <vm> node script.js` that an agent actually uses. A
# profile.d file would leave exactly that case pointing at an empty ~/.cache.
RUN npm install -g playwright \
 && npm cache clean --force

RUN apt-get update \
 && PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright playwright install --with-deps chromium \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*
RUN printf 'PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright\n' >> /etc/environment

# Expose that browser as `chromium`, so it is usable without going through
# Playwright. See the script for why there is only one Chromium here.
COPY chromium.sh /usr/local/bin/chromium
RUN chmod 0755 /usr/local/bin/chromium

# SDKMAN, installed into /etc/skel so each account cloud-init creates gets its
# own copy -- installing a JDK writes into it, so a single shared directory
# would have every user on the VM writing to the same place. rcupdate=false
# stops the installer appending its own block to a shell profile; the file
# copied in below does that job for every account at once.
RUN set -eu; \
    SDKMAN_DIR=/etc/skel/.sdkman bash -c 'curl -fsSL "https://get.sdkman.io?rcupdate=false" | bash'

# The JVM toolchain itself: the newest Temurin JDK SDKMAN offers, and Maven.
#
# The JDK version is resolved from `sdk list java` during the build rather than
# pinned, like every other package here -- a pinned one would be a release
# behind before the image was rebuilt, and reproducibility comes from the
# digest the manifest records for the source image (ADR-0006). The first
# Temurin identifier in that list is the newest: SDKMAN prints each vendor's
# versions in descending order. A list that no longer contains one fails the
# build rather than silently shipping a guest with no JDK.
#
# This writes into /etc/skel/.sdkman, the copy every account created later
# inherits, so the JDK and Maven are in place before anyone logs in instead of
# being downloaded per account inside a guest that may have no network at all.
RUN set -e; \
    bash -c 'set -e; \
      export SDKMAN_DIR=/etc/skel/.sdkman; \
      . "$SDKMAN_DIR/bin/sdkman-init.sh"; \
      java_id="$(sdk list java | grep -oE "[0-9][0-9.]+-tem" | head -n1)"; \
      if [ -z "$java_id" ]; then \
        echo "no Temurin JDK in SDKMAN list; the identifier format must have changed" >&2; \
        exit 1; \
      fi; \
      echo "installing Temurin $java_id"; \
      sdk install java "$java_id"; \
      sdk install maven'

RUN cp -a /etc/skel/.sdkman /root/.sdkman

COPY sdkman.sh /etc/profile.d/agent-vm-sdkman.sh
RUN chmod 0644 /etc/profile.d/agent-vm-sdkman.sh

# Run each of these once, and fail the build if any of them does not work.
#
# Chromium is the reason this step exists. A browser with one shared library
# missing installs perfectly and then exits the moment it is launched, so
# "playwright install succeeded" says nothing about whether a guest can
# actually drive a page. Launching it here is the only thing that does.
RUN set -eu; \
    gh --version >/dev/null || { echo "gh installed but cannot run" >&2; exit 1; }; \
    tea --version >/dev/null || { echo "tea installed but cannot run" >&2; exit 1; }; \
    playwright --version >/dev/null || { echo "playwright installed but cannot run" >&2; exit 1; }; \
    if ! chromium --headless=new --no-sandbox --disable-gpu --dump-dom about:blank >/dev/null 2>/tmp/chromium-smoke.log; then \
      echo "chromium installed but cannot start headless:" >&2; \
      tail -n 20 /tmp/chromium-smoke.log >&2; \
      exit 1; \
    fi; \
    rm -f /tmp/chromium-smoke.log; \
    [ -s /etc/skel/.sdkman/bin/sdkman-init.sh ] \
      || { echo "SDKMAN is missing from /etc/skel, so accounts cloud-init creates will not have it" >&2; exit 1; }; \
    bash -lc 'type sdk' >/dev/null 2>&1 \
      || { echo "sdk is not defined in a login shell; the profile script or the SDKMAN install is wrong" >&2; exit 1; }; \
    bash -lc 'java -version' >/dev/null 2>&1 \
      || { echo "no JDK on the path of a login shell; the SDKMAN java install did not take" >&2; exit 1; }; \
    bash -lc 'mvn -version' >/dev/null 2>&1 \
      || { echo "no Maven on the path of a login shell; the SDKMAN maven install did not take" >&2; exit 1; }

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

# The virtualization stack, so a VM can create VMs of its own.
#
# This is the same set of host tools agent-vm itself drives (AGENTS.md §3), so
# a guest can run agent-vm, or virt-install and virsh directly. dnsmasq-base
# rather than dnsmasq: libvirt starts its own dnsmasq per network, and the full
# package would additionally enable a system-wide resolver on port 53 that
# fights with both libvirt's instances and systemd-resolved.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      qemu-kvm \
      qemu-utils \
      libvirt-daemon-system \
      libvirt-clients \
      virtinst \
      dnsmasq-base \
      guestfs-tools \
      podman \
      golang-go \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

# golangci-lint, from its own installer on all three families.
#
# Ubuntu does not package it at all, and where it is packaged the version
# differs per family, so the upstream installer is what keeps every image on
# the same one -- the same reasoning as tea above. It needs a Go toolchain to
# analyze anything, which is why one is installed with the packages above.
RUN set -eu; \
    curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
      | sh -s -- -b /usr/local/bin

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
    dnsmasq --version >/dev/null || { echo "dnsmasq installed but cannot run" >&2; exit 1; }; \
    go version >/dev/null || { echo "the Go toolchain installed but cannot run" >&2; exit 1; }; \
    golangci-lint --version >/dev/null || { echo "golangci-lint installed but cannot run" >&2; exit 1; }

# There is no running systemd inside a build, so units are enabled offline with
# --root=/, which only writes the symlinks an enable would create. This must not
# be allowed to fail quietly: a guest without sshd looks exactly like a guest
# that failed to boot, hours later and from the outside.
RUN systemctl --root=/ enable \
      ssh.service \
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
