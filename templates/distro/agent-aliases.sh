# Run the Antigravity CLI unattended.
#
# Unlike claude, codex and opencode, agy has no configuration file for tool
# permissions -- `--dangerously-skip-permissions` is the only way to stop it
# prompting -- so it gets an alias where the others get real config files.
#
# An alias only reaches interactive shells. A non-interactive caller, such as
# `ssh <vm> agy -p '...'`, does not read this file and has to pass the flag
# itself. pi is absent here on purpose: it does not gate tool calls at all.
alias agy='agy --dangerously-skip-permissions'
