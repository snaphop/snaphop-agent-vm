#!/bin/sh
# First-boot setup for every interactive account on the VM.
#
# Both jobs here can only be done once the accounts exist, and the accounts are
# created by cloud-init at first boot -- long after this image was built. The
# login user agent-vm asks for gets its group memberships from the generated
# cloud-init user-data as well, at the moment the account is created, because a
# group added to an account that has already logged in does not apply to that
# session and the first SSH session can land before this unit has run. This
# script is the backstop for every other interactive account: ones an
# operator's own --cloud-init file creates, and ones on a VM whose seed
# predates that change.
set -eu

# docker for the daemon's socket, libvirt and kvm for the nested VMs the guest
# can run. A group the image does not have is skipped rather than created: on a
# base image built before that software was installed, its absence is the
# truth.
groups='docker libvirt kvm'

# The mise store every account shares. An account created from /etc/skel
# already has the symlink below; this is for the ones that were not -- an
# operator's own --cloud-init user with no_create_home undone by hand, or any
# account made with `useradd --skel`. Without it that account has no toolchain
# at all, because there is no per-account copy any more.
mise_store='/usr/local/lib/mise'

while IFS=: read -r name _pw uid gid _gecos home _shell; do
    # Interactive accounts only. System accounts and nobody are left alone.
    case "$uid" in "" | *[!0-9]*) continue ;; esac
    [ "$uid" -ge 1000 ] && [ "$uid" -lt 65534 ] || continue

    for group in ${groups}; do
        getent group "${group}" >/dev/null 2>&1 || continue
        gpasswd -a "${name}" "${group}" >/dev/null
    done

    # Everything below writes into the account's home, so an account that has
    # none is done with here.
    if [ ! -d "${home}" ]; then
        continue
    fi

    # The mise data directory, as a symlink to the shared store. An existing
    # one is left alone whatever it is: on a VM created from a base image that
    # predates the shared store it is a real directory holding that account's
    # own copy of the toolchain, and replacing it would throw away whatever the
    # account has installed since.
    if [ -d "${mise_store}" ] && [ ! -e "${home}/.local/share/mise" ]; then
        install -d -m 0755 -o "${uid}" -g "${gid}" "${home}/.local" "${home}/.local/share"
        ln -sfn "${mise_store}" "${home}/.local/share/mise"
        chown -h "${uid}:${gid}" "${home}/.local/share/mise"
    fi

    # An SSH key pair for the account, so an agent can authenticate to a git
    # forge or reach a nested VM without a key being pasted in by hand.
    #
    # It is generated here, inside the guest, and never leaves it: no private
    # key is ever placed in a base image or a cloud-init seed (SECURITY.md).
    # An existing key is left exactly as it is -- the operator may have
    # supplied their own through --cloud-init.
    if [ -e "${home}/.ssh/id_ed25519" ]; then
        continue
    fi
    install -d -m 0700 -o "${uid}" -g "${gid}" "${home}/.ssh"
    # uname -n rather than hostname: coreutils is always present, and Arch
    # does not install a hostname binary at all.
    ssh-keygen -q -t ed25519 -N '' -C "${name}@$(uname -n)" -f "${home}/.ssh/id_ed25519"
    chown "${uid}:${gid}" "${home}/.ssh/id_ed25519" "${home}/.ssh/id_ed25519.pub"
    chmod 0600 "${home}/.ssh/id_ed25519"
    chmod 0644 "${home}/.ssh/id_ed25519.pub"
done < /etc/passwd
