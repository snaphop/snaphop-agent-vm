#!/bin/sh
# Join this guest to a Tailscale network.
#
# agent-vm copies this script over SSH and runs it as root (ADR-0014).
# `install` adds Tailscale's package when the guest does not already have
# one, and does not read stdin. `up` reads an auth key from stdin, passes it
# to `tailscale up` as a file under /run, and removes that file before
# exiting. The key is never printed.
set -eu

keyfile=/run/agent-vm-tailscale-authkey

die() {
    printf '%s\n' "agent-vm-tailscale-join: $*" >&2
    exit 2
}

install_tailscale() {
    if ! command -v tailscale >/dev/null 2>&1; then
        # Tailscale's installer selects the package for this distro. It is
        # fetched here so a cached base image does not have to carry it.
        # stdin is discarded: the auth key is sent only to `up`.
        installer=/tmp/agent-vm-tailscale-install.sh
        umask 077
        curl -fsSL https://tailscale.com/install.sh -o "$installer"
        sh "$installer" </dev/null
        rm -f "$installer"
    fi
    if command -v systemctl >/dev/null 2>&1; then
        systemctl enable --now tailscaled
    fi
}

join_up() {
    hostname=
    operator=
    ephemeral=0
    enable_ssh=0
    login_server=
    tags=

    while [ $# -gt 0 ]; do
        case $1 in
            --hostname=*) hostname=${1#--hostname=} ;;
            --operator=*) operator=${1#--operator=} ;;
            --login-server=*) login_server=${1#--login-server=} ;;
            --advertise-tags=*) tags=${1#--advertise-tags=} ;;
            --ephemeral) ephemeral=1 ;;
            --ssh) enable_ssh=1 ;;
            *) die "unknown argument" ;;
        esac
        shift
    done

    if [ -z "$hostname" ] || [ -z "$operator" ]; then
        die "missing --hostname or --operator"
    fi

    # Read the key before anything else in this subcommand can. /run is
    # tmpfs. The trap is installed first so a failed write still removes the
    # file, including when this subcommand exits on an error.
    umask 077
    trap 'rm -f "$keyfile"' EXIT
    cat > "$keyfile"
    if [ ! -s "$keyfile" ]; then
        die "the auth key was empty"
    fi

    if command -v systemctl >/dev/null 2>&1; then
        systemctl enable --now tailscaled
    fi

    # --reset makes this the whole configuration. A later flag that was left
    # unset must not keep a value from a previous attempt.
    set -- \
        tailscale up \
        --auth-key="file:${keyfile}" \
        --hostname="$hostname" \
        --operator="$operator" \
        --timeout=90s \
        --reset
    if [ "$ephemeral" -eq 1 ]; then
        set -- "$@" --ephemeral
    fi
    if [ "$enable_ssh" -eq 1 ]; then
        set -- "$@" --ssh
    fi
    if [ -n "$login_server" ]; then
        set -- "$@" --login-server="$login_server"
    fi
    if [ -n "$tags" ]; then
        set -- "$@" --advertise-tags="$tags"
    fi

    "$@"
    rm -f "$keyfile"
    trap - EXIT
    # The address is informational. A tailnet with no IPv4 still joined.
    tailscale ip -4 || true
}

if [ $# -lt 1 ]; then
    die "missing command"
fi
action=$1
shift
case $action in
    install) install_tailscale ;;
    up) join_up "$@" ;;
    *) die "unknown command" ;;
esac
