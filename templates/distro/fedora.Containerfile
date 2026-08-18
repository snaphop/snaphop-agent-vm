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
      cloud-init \
      openssh-server \
      sudo \
      qemu-guest-agent \
      iproute \
 && dnf clean all

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
 && dnf clean all

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

# Give the accounts cloud-init creates access to the Docker socket.
#
# It cannot be done here — the account does not exist until first boot — and it
# cannot be done in the generated cloud-init user-data either, because that
# would name a group base images built before this change do not have, and
# those must keep booting (AGENTS.md §8). So the image carries a one-shot unit
# that adds every non-system account to the group after cloud-init has finished
# creating it.
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
