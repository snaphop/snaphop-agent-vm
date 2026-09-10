# Put the nix-provided tooling on PATH in a login shell.
#
# The tools themselves are in one profile, /nix/var/nix/profiles/default, built
# from templates/distro/agent-tools.nix when the image was built and shared by
# every account -- there is no per-account install to do at first boot, and a
# guest with no network still has everything the image shipped.
#
# This file is for login shells only. What makes `ssh <vm> go build` work --
# which runs no login shell and so never reads this -- is the PATH line the
# recipe writes into /etc/environment, which sshd applies through pam_env, plus
# the symlinks in /usr/local/bin for the handful of commands an agent
# supervisor invokes most. This script is still worth having: it puts the
# profile ahead of the distro's own directories for an interactive session, so
# `python3` and `git` mean the versions this image installed.
if [ -d /nix/var/nix/profiles/default/bin ]; then
  PATH="/nix/var/nix/profiles/default/bin:$PATH"
  export PATH
fi

# Point TLS at the profile's CA bundle when the distro's own is not where a
# nix-built binary looks for it. Without this, curl and git from the profile
# fail every HTTPS request in a guest while the distro's copies work, which
# reads as a broken network rather than a missing bundle.
if [ -z "${NIX_SSL_CERT_FILE:-}" ] && \
   [ -f /nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt ]; then
  NIX_SSL_CERT_FILE=/nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt
  export NIX_SSL_CERT_FILE
fi
