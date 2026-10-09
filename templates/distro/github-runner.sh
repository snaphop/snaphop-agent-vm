#!/bin/sh
# Install the GitHub Actions self-hosted runner into /opt/actions-runner.
#
# Runs as root during the image build. The runner is left unconfigured: no
# URL and no registration value are written here, because a base image is
# shared by every VM built on it (SECURITY.md, ADR-0013). Register a guest
# after boot with agent-vm-github-runner.
set -eu

version=2.337.0
prefix=/opt/actions-runner

# Checksums published with that release. A mismatch fails the build rather
# than installing an archive we did not ask for.
x64_sha256=70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613
arm64_sha256=9b1dc70626422526e3c94767cf024896beb15da5342a3f4819bf2feac13e0393

arch=$(uname -m)
case "$arch" in
    x86_64)
        runner_arch=x64
        want=$x64_sha256
        ;;
    aarch64 | arm64)
        runner_arch=arm64
        want=$arm64_sha256
        ;;
    *)
        echo "github-runner: unsupported architecture ${arch}" >&2
        exit 1
        ;;
esac

name="actions-runner-linux-${runner_arch}-${version}.tar.gz"
url="https://github.com/actions/runner/releases/download/v${version}/${name}"

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 5 --retry-delay 2 -o "$tmp" "$url"
printf '%s  %s\n' "$want" "$tmp" | sha256sum -c -

nologin=$(command -v nologin) || {
    echo "github-runner: nologin is not installed" >&2
    exit 1
}
# Jobs run as github-runner. The shell stays nologin, so the account is not
# an SSH login. sudo comes from the drop-in below (ADR-0017).
if ! id github-runner >/dev/null 2>&1; then
    useradd --system --user-group --home-dir "$prefix" --shell "$nologin" --no-create-home github-runner
fi
sudoers=/etc/sudoers.d/github-runner
printf '%s\n' 'github-runner ALL=(ALL) NOPASSWD:ALL' > "$sudoers"
chown root:root "$sudoers"
chmod 0440 "$sudoers"
if ! command -v visudo >/dev/null 2>&1; then
    echo "github-runner: visudo is not installed" >&2
    exit 1
fi
visudo -c -f "$sudoers"

mkdir -p "$prefix"
tar -xzf "$tmp" -C "$prefix"
if [ ! -x "$prefix/config.sh" ] || [ ! -x "$prefix/bin/installdependencies.sh" ]; then
    echo "github-runner: the archive did not contain the runner" >&2
    exit 1
fi

# GitHub's dependency script covers Debian and Fedora. It exits on Arch,
# which has neither /etc/debian_version nor /etc/redhat-release, so Arch
# installs the same libraries by its own package names. -S --needed installs
# only those packages against the databases the slim layers already synced.
# A full upgrade here would replace the kernel after the slim recipe built
# the initramfs this image direct-boots. A missing database fails the build.
if [ -e /etc/debian_version ] || [ -e /etc/fedora-release ]; then
    "$prefix/bin/installdependencies.sh"
elif [ -f /etc/arch-release ]; then
    pacman -S --noconfirm --needed icu openssl krb5 zlib lttng-ust
else
    echo "github-runner: no dependency install for this distro" >&2
    exit 1
fi

# Jobs compile C and C++. build-essential is the Debian package for gcc,
# g++, make, and the headers. Fedora and Arch already install that toolchain
# in the slim recipe (gcc/make and base-devel). A second package transaction
# there could upgrade the kernel after the initramfs this image direct-boots
# was built.
if [ -e /etc/debian_version ]; then
    DEBIAN_FRONTEND=noninteractive apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends build-essential
fi

if [ -e /etc/debian_version ]; then
    rm -rf /var/lib/apt/lists/*
elif [ -e /etc/fedora-release ]; then
    dnf clean all
elif [ -f /etc/arch-release ]; then
    pacman -Scc --noconfirm
fi

printf '%s\n' "$version" > "$prefix/.agent-vm-runner-version"
chown -R github-runner:github-runner "$prefix"
chmod 0750 "$prefix"
