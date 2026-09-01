# Put the mise-managed toolchain on PATH in a login shell.
#
# $HOME/.local/share/mise is a symlink to /usr/local/lib/mise, the store every
# account shares. The link comes from /etc/skel, which useradd copies into each
# new home; agent-vm-user-setup.service makes one for any account that was
# created without skel. Which tool version an account uses is still its own --
# that lives in $HOME/.config/mise -- so only the installs are shared.
#
# The shims are real executables -- symlinks to the mise binary, which dispatches
# on the name it was called by -- rather than shell functions, so a script that
# needs java or mvn in a non-interactive `ssh <vm> mvn package` can put this
# directory on PATH itself; there is nothing to source. node, npm, claude,
# opencode and pi are mise-installed too, and have a symlink each in
# /usr/local/bin for the same reason -- they are the commands an agent
# supervisor invokes over ssh most.
if [ -d "$HOME/.local/share/mise/shims" ]; then
  PATH="$HOME/.local/share/mise/shims:$PATH"
  export PATH
fi
