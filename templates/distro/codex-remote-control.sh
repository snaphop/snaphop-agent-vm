#!/bin/sh
# Start Codex's remote-control daemon for every interactive account on the VM.
#
# Two things have to happen at boot, and neither can happen when the image is
# built. The accounts do not exist yet -- cloud-init creates them at first boot
# -- and the daemon itself dies with the VM, so it is started again on every
# boot rather than baked in.
#
# Nothing here is fatal. `codex remote-control start` needs credentials, which
# are per-VM and never come from a base image or a seed (SECURITY.md), so on a
# VM where nobody has run `codex login` it fails to connect. That is a normal
# state for a fresh VM, not a broken boot: each failure is reported to the
# journal and this script still exits 0. Once an account is logged in,
# `systemctl start agent-vm-codex-remote-control` starts the daemon.
set -eu

# The standalone package the OpenAI installer put in the image, shared by every
# account instead of copied into each home -- it is ~300 MiB.
shared_standalone=/usr/local/lib/codex/packages/standalone/current

# codex remote-control starts and updates its app-server from this fixed path
# under the account's own CODEX_HOME, and refuses to run when it is missing. A
# symlink to the shared package is what satisfies it without a second copy; the
# rest of ~/.codex stays per-account, because that is where the account's
# credentials and configuration live.
link_standalone() {
    name="$1"
    home="$2"
    uid="$3"
    gid="$4"

    link="${home}/.codex/packages/standalone/current"
    if [ -e "${link}" ] || [ -L "${link}" ]; then
        return 0
    fi
    install -d -m 0755 -o "${uid}" -g "${gid}" \
        "${home}/.codex" "${home}/.codex/packages" "${home}/.codex/packages/standalone"
    ln -s "${shared_standalone}" "${link}"
    chown -h "${uid}:${gid}" "${link}"
}

start_for_account() {
    name="$1"
    home="$2"
    uid="$3"
    gid="$4"

    [ -d "${home}" ] || return 0
    link_standalone "${name}" "${home}" "${uid}" "${gid}" || {
        echo "codex remote control: could not prepare ${home}/.codex for ${name}" >&2
        return 0
    }

    # runuser without -l keeps root's environment, so HOME is passed
    # explicitly: it is what decides which account's CODEX_HOME, and so which
    # account's credentials, the daemon uses. The timeout bounds a daemon that
    # never reports itself ready, so a single account cannot hold up the boot.
    if ! timeout 60 runuser -u "${name}" -- env HOME="${home}" \
        codex remote-control start >/dev/null; then
        echo "codex remote control: not started for ${name} (run 'codex login' as ${name}, then: systemctl start agent-vm-codex-remote-control)" >&2
    fi
}

if ! command -v codex >/dev/null 2>&1; then
    echo "codex remote control: no codex in this image; nothing to start" >&2
    exit 0
fi

if [ ! -e "${shared_standalone}/codex" ]; then
    echo "codex remote control: no standalone package at ${shared_standalone}; codex was not installed by OpenAI's installer and remote control cannot run" >&2
    exit 0
fi

# root first: its home exists in the image and an agent may well be running as
# root, so it is an account here like any other.
start_for_account root /root 0 0

while IFS=: read -r name _pw uid gid _gecos home _shell; do
    # Interactive accounts only. System accounts and nobody are left alone.
    case "${uid}" in "" | *[!0-9]*) continue ;; esac
    [ "${uid}" -ge 1000 ] && [ "${uid}" -lt 65534 ] || continue

    start_for_account "${name}" "${home}" "${uid}" "${gid}"
done < /etc/passwd
