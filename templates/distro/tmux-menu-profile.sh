# Start the tmux session menu when a person logs in.
#
# Every guard here is load-bearing, because this file is read by every login
# shell on the VM and a menu in the wrong place is a hang:
#
#   * interactive shells only, so `ssh <vm> some-command` is untouched;
#   * a real terminal on both ends, so a piped or redirected session -- rsync,
#     an agent driving the VM with a forced tty -- never stops at a prompt;
#   * not inside tmux already, or attaching would nest a session in itself;
#   * AGENT_VM_NO_MENU as a documented way out for anyone who wants the plain
#     shell every time.
#
# It sorts last (zz-) so that PATH, SDKMAN, and the rest of /etc/profile.d have
# already been applied: the shells tmux starts inherit this environment.
case $- in
*i*) ;;
*) return ;;
esac

[ -t 0 ] && [ -t 1 ] || return
[ -z "${TMUX:-}" ] || return
[ -z "${AGENT_VM_NO_MENU:-}" ] || return
command -v agent-vm-menu >/dev/null 2>&1 || return

agent-vm-menu
