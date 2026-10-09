#!/bin/sh
# Install Docker into a runner image and let the runner account use it.
#
# Runs as root during the image build, after github-runner.sh has created
# the runner account. A workflow job runs as that account. The account has
# no sudo, so it has to be in the docker group or every docker step fails.
# Membership in that group can start a privileged container, which is root
# inside the guest (ADR-0016). The account stays out of sudoers.
#
# Packages come from the distro, the same ones a full image installs.
# Docker's convenience script would add another registry and a GPG key.
# The install names those packages only. A full upgrade could replace the
# kernel after the slim recipe built the initramfs this image direct-boots
# (ADR-0013).
set -eu

if [ -e /etc/debian_version ]; then
    apt-get update
    apt-get install -y --no-install-recommends \
        docker.io \
        docker-compose-v2 \
        docker-buildx
    apt-get clean
    rm -rf /var/lib/apt/lists/*
elif [ -e /etc/fedora-release ]; then
    # moby-engine is Fedora's Docker daemon. docker-ce is not in the distro
    # repositories.
    dnf -y install \
        moby-engine \
        containerd \
        docker-compose \
        docker-buildx
    dnf clean all
elif [ -f /etc/arch-release ]; then
    # The slim layers already synced the databases. -S --needed installs
    # these packages against that snapshot. -Sy or -Su would move the rest
    # of the system, including the kernel.
    pacman -S --noconfirm --needed \
        docker \
        docker-compose \
        docker-buildx
    pacman -Scc --noconfirm
else
    echo "runner-docker: no Docker install for this distro" >&2
    exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
    echo "runner-docker: docker is not on PATH after the package install" >&2
    exit 1
fi

# Offline enable only writes the symlinks. There is no systemd in the build.
# A missing unit fails the build: a guest whose daemon is not enabled looks,
# from inside a job, like a job that cannot find a daemon.
systemctl --root=/ enable docker.service containerd.service

if ! id runner >/dev/null 2>&1; then
    echo "runner-docker: the runner account is missing; install the runner first" >&2
    exit 1
fi
if ! getent group docker >/dev/null 2>&1; then
    echo "runner-docker: the docker group is missing after the package install" >&2
    exit 1
fi
usermod -aG docker runner
