# Arch Linux runner base image for agent-vm.
#
# The slim recipe plus the GitHub Actions self-hosted runner (ADR-0013),
# Docker (ADR-0016), git, and the GitHub CLI.
# From ARG BASE_IMAGE through the end of the slim recipe, this file is that
# recipe. The section after it installs the runner, unconfigured, the
# command that registers a guest after boot, Docker, git, and gh.
# TestRunnerContainerfiles_AreTheSlimRecipePlusTheRunner requires the slim
# body to stay byte-identical and the runner section to stay the same on
# every family.
#
# The job account is github-runner, with passwordless sudo and a nologin
# shell. A workflow job runs as that account. The compiler toolchain comes
# from the slim recipe. The account is in the docker group so the job can
# use the daemon. The agent account is unchanged.
# This recipe contains no GitHub URL and no registration value: a base image
# is shared by every VM built on it (SECURITY.md).
#
# BASE_IMAGE is passed in pinned to a digest. Do not add a default that would
# let an unpinned tag be built by accident.

ARG BASE_IMAGE
FROM ${BASE_IMAGE}

RUN pacman -Syu --noconfirm --needed \
      linux \
      mkinitcpio \
      systemd \
      chrony \
      cloud-init \
      cloud-guest-utils \
      openssh \
      sudo \
      qemu-guest-agent \
      iproute2 \
 && pacman -Scc --noconfirm

# cloud-guest-utils is growpart, which is what lets cloud-init grow the root
# partition to the size of the VM's disk at first boot. Without it the guest is
# stuck with the base filesystem, which carries only the slack the image build
# gives it -- and a first boot that copies /etc/skel into a new account fills
# that and fails with ENOSPC, taking cloud-final and sshd down with it.
# Ubuntu's cloud-init pulls growpart in as a dependency; Arch's does not.

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
      words \
 && pacman -Scc --noconfirm

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

# agent-vm-runner-section
#
# GitHub Actions self-hosted runner (ADR-0013, ADR-0017), Docker
# (ADR-0016), git, and the GitHub CLI. The blocks above are the slim
# recipe. This section installs the runner, unconfigured, the command that
# registers it after boot, Docker from the distro's own packages, and git
# and gh. Nothing in this section is a credential. Jobs run as
# github-runner, which has passwordless sudo. Docker is installed after
# that account exists, because the account has to join the docker group
# and the group arrives with the packages. git and gh are installed from
# the distro packages and checked on PATH before the image is committed.
COPY github-runner.sh /tmp/agent-vm-github-runner-install.sh
COPY github-runner-configure.sh /usr/local/sbin/agent-vm-github-runner
COPY runner-docker.sh /tmp/agent-vm-runner-docker.sh
COPY runner-git.sh /tmp/agent-vm-runner-git.sh
RUN chmod 0755 /tmp/agent-vm-github-runner-install.sh /usr/local/sbin/agent-vm-github-runner /tmp/agent-vm-runner-docker.sh /tmp/agent-vm-runner-git.sh \
 && ln -sf /usr/local/sbin/agent-vm-github-runner /usr/sbin/agent-vm-github-runner \
 && /tmp/agent-vm-github-runner-install.sh \
 && /tmp/agent-vm-runner-git.sh \
 && /tmp/agent-vm-runner-docker.sh \
 && rm /tmp/agent-vm-github-runner-install.sh /tmp/agent-vm-runner-docker.sh /tmp/agent-vm-runner-git.sh
