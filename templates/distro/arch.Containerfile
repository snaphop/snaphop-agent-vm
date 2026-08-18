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

# Arch ships Docker in its own repositories, so no third-party repository or
# convenience script is involved.
RUN pacman -Syu --noconfirm --needed \
      docker \
      docker-compose \
      docker-buildx \
 && pacman -Scc --noconfirm

# Docker starts at boot so an agent finds a working daemon without asking.
RUN systemctl --root=/ enable docker.service containerd.service

# Give the accounts cloud-init creates access to the Docker socket.
#
# It cannot be done here — the accounts do not exist until first boot. The
# login user agent-vm asks for is placed in the group by the generated
# cloud-init user-data, which is what makes the membership effective in the
# very first SSH session. This one-shot unit is the backstop for every other
# interactive account: ones an operator's own --cloud-init file creates, and
# ones created on a VM whose seed predates that change.
RUN printf '%s\n' \
      '#!/bin/sh' \
      'set -eu' \
      'getent group docker >/dev/null 2>&1 || exit 0' \
      'while IFS=: read -r name _pw uid _rest; do' \
      '  case "$uid" in "" | *[!0-9]*) continue ;; esac' \
      '  [ "$uid" -ge 1000 ] && [ "$uid" -lt 65534 ] || continue' \
      '  gpasswd -a "$name" docker >/dev/null' \
      'done < /etc/passwd' \
      > /usr/local/sbin/agent-vm-docker-group \
 && chmod 0755 /usr/local/sbin/agent-vm-docker-group

RUN printf '%s\n' \
      '[Unit]' \
      'Description=Add interactive users to the docker group' \
      'After=cloud-final.service docker.service' \
      'Wants=cloud-final.service' \
      '' \
      '[Service]' \
      'Type=oneshot' \
      'RemainAfterExit=yes' \
      'ExecStart=/usr/local/sbin/agent-vm-docker-group' \
      '' \
      '[Install]' \
      'WantedBy=multi-user.target' \
      > /usr/lib/systemd/system/agent-vm-docker-group.service \
 && systemctl --root=/ enable agent-vm-docker-group.service

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

RUN printf '/dev/vda1 / ext4 defaults 0 1\n' > /etc/fstab

# NoCloud only: a guest must never probe a metadata service on the network.
RUN printf 'datasource_list: [ NoCloud, None ]\n' > /etc/cloud/cloud.cfg.d/90-agent-vm-datasource.cfg

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
