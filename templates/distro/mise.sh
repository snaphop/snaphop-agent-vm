# Put the mise-managed toolchain on PATH in a login shell.
#
# Each account gets its own mise under $HOME, copied from /etc/skel when the
# account is created, because installing a tool writes into mise's data
# directory. A single shared one would have every user on the VM writing to the
# same place.
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
