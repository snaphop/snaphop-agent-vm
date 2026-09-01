#!/bin/sh
# Start a Herdr server for every interactive account on the VM.
#
# Herdr is a terminal workspace manager: the server owns the panes, and the
# agents running in them, while a client attaches and detaches -- `herdr` in an
# SSH session on the guest, or `herdr --remote <vm>` from the operator's own
# machine. Starting it at boot rather than at first attach is what makes those
# panes outlive the connection that created them: an agent left running in a
# pane keeps working while nobody is attached.
#
# Neither half of this can happen when the image is built. The accounts do not
# exist yet -- cloud-init creates them at first boot -- and a server dies with
# the VM, so one is started again on every boot.
#
# Each account's server is its own instance of agent-vm-herdr@.service, so
# systemd owns the process rather than this script: its output is in the
# journal, a crash is restarted, and `systemctl status agent-vm-herdr@<account>`
# says what it is doing. Nothing here is fatal -- an account whose server will
# not start is reported and the boot carries on.
set -eu

start_for_account() {
    name="$1"
    home="$2"

    [ -d "${home}" ] || return 0

    # An account with no herdr of its own gets no server. The image installs
    # herdr into the shared mise store, and every account reaches it through
    # the ~/.local/share/mise symlink -- from /etc/skel when cloud-init creates
    # the account, or from agent-vm-user-setup.service for one the distro baked
    # into its own image, like Ubuntu's `ubuntu`. An account that has neither
    # has no mise data directory at all, and /usr/local/bin/herdr is a mise
    # shim, so for exactly those accounts it exits at once with "herdr is not a
    # valid shim".
    if [ ! -x "${home}/.local/share/mise/shims/herdr" ]; then
        echo "herdr: no herdr installed for ${name} (run 'mise use -g herdr' as ${name}, then: systemctl start agent-vm-herdr@${name})" >&2
        return 0
    fi

    if ! systemctl start "agent-vm-herdr@${name}.service"; then
        echo "herdr: server not started for ${name} (see: systemctl status agent-vm-herdr@${name})" >&2
    fi
}

if ! command -v herdr >/dev/null 2>&1; then
    echo "herdr: no herdr in this image; nothing to start" >&2
    exit 0
fi

# root first: its home exists in the image and an agent may well be running as
# root, so it is an account here like any other.
start_for_account root /root

while IFS=: read -r name _pw uid _gid _gecos home shell; do
    # Interactive accounts only. System accounts and nobody are left alone.
    case "${uid}" in "" | *[!0-9]*) continue ;; esac
    [ "${uid}" -ge 1000 ] && [ "${uid}" -lt 65534 ] || continue

    # The uid range alone does not identify an account a person logs into:
    # Ubuntu gives libvirt-qemu uid 64055, well inside it. A server for an
    # account with no login shell has nobody to attach to it and, unlike the
    # one-shot jobs beside it, it would be restarted for the life of the VM --
    # so the shell is what decides, not the number.
    case "${shell}" in
        "" | */nologin | */false | */sync | */shutdown | */halt) continue ;;
    esac

    start_for_account "${name}" "${home}"
done < /etc/passwd
