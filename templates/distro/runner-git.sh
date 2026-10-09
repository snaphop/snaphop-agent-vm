#!/bin/sh
# Install git and the GitHub CLI into a runner image.
#
# Runs as root during the image build. A workflow job runs as
# github-runner and calls both commands. git is also in the slim recipe;
# installing it here keeps that command on the runner image. gh is the
# distro package (github-cli on Arch), the same package a full image
# installs.
#
# The install names those packages only. A full upgrade could replace the
# kernel after the slim recipe built the initramfs this image direct-boots
# (ADR-0013). The commands are left logged out: a base image is shared by
# every VM built on it, and a job brings its own GitHub credentials
# (SECURITY.md).
set -eu

if [ -e /etc/debian_version ]; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        git \
        gh
    apt-get clean
    rm -rf /var/lib/apt/lists/*
elif [ -e /etc/fedora-release ]; then
    dnf -y install \
        git \
        gh
    dnf clean all
elif [ -f /etc/arch-release ]; then
    # The slim layers already synced the databases. -S --needed installs
    # these packages against that snapshot. -Sy or -Su would move the rest
    # of the system, including the kernel. gh is packaged as github-cli.
    pacman -S --noconfirm --needed \
        git \
        github-cli
    pacman -Scc --noconfirm
else
    echo "runner-git: no git install for this distro" >&2
    exit 1
fi

for cmd in git gh; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "runner-git: ${cmd} is not on PATH after the package install" >&2
        exit 1
    fi
    if ! "$cmd" --version >/dev/null 2>&1; then
        echo "runner-git: ${cmd} is installed but cannot run" >&2
        exit 1
    fi
done
