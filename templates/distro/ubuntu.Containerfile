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
