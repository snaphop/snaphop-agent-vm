# Make SDKMAN usable in a login shell.
#
# sdk is a shell function, not a program, so it only exists once this file has
# been sourced -- which means it reaches interactive login shells and not a
# non-interactive `ssh <vm> sdk install java`. That is how SDKMAN works
# everywhere, not something this image imposes; a script should source
# "$SDKMAN_DIR/bin/sdkman-init.sh" itself.
#
# Each account gets its own SDKMAN under $HOME, copied from /etc/skel when the
# account is created, because installing a JDK writes into it. A single shared
# copy would have every user on the VM writing to the same directory.
export SDKMAN_DIR="$HOME/.sdkman"
[ -s "$SDKMAN_DIR/bin/sdkman-init.sh" ] && . "$SDKMAN_DIR/bin/sdkman-init.sh"
