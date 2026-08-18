#!/usr/bin/env bash
# The menu an interactive SSH session lands on.
#
# Work in a VM is nearly always work in tmux: an agent that loses its
# connection has lost the run unless what it was doing lives in a session that
# outlived the shell. This menu makes that the default path rather than
# something to remember, and it is only ever reached from a real terminal --
# the profile script that starts it checks. Choosing "q" leaves a plain shell,
# so nothing here takes the machine away from someone who wants it.
set -u

readonly PROMPT_ATTACH="Attach to (number, name, TAB completes, q cancels): "

show_menu() {
    echo
    echo "tmux — $(tmux list-sessions 2>/dev/null | wc -l) session(s) on $(uname -n)"
    echo "  n) new session"
    echo "  a) attach to a session"
    echo "  l) list sessions"
    echo "  q) exit to the shell"
}

# A generated name is short on purpose: it has to be typeable at the attach
# prompt. /proc is where a UUID comes from because uuidgen is packaged
# separately on Ubuntu and is not installed in these images.
generated_name() {
    local uuid
    if uuid=$(cat /proc/sys/kernel/random/uuid 2>/dev/null) && [ -n "$uuid" ]; then
        printf 'agent-%s' "${uuid%%-*}"
    else
        printf 'agent-%s' "$(date +%H%M%S)"
    fi
}

new_session() {
    local name
    while :; do
        # A failed read is end of input, not an empty name: returning keeps
        # ^D from spinning this loop forever.
        read -r -p "Session name (empty for a generated one): " name || return
        if [ -z "$name" ]; then
            name=$(generated_name)
            break
        elif [[ $name =~ ^[A-Za-z0-9_-]+$ ]]; then
            break
        fi
        echo "Only letters, numbers, underscore and dash."
    done

    if tmux has-session -t "=$name" 2>/dev/null; then
        echo "Session $name already exists; attaching to it."
        tmux attach-session -t "=$name"
        return
    fi
    echo "Starting session $name."
    tmux new-session -s "$name"
}

# read_with_completion fills the global `input` from one keypress at a time,
# completing against the session names in `sessions` when TAB is pressed.
#
# It is a key-at-a-time loop rather than `read -e` because readline completes
# filenames there, and a directory listing is not what the prompt is asking
# for.
read_with_completion() {
    local key rest match matches session
    input=""
    printf '%s' "$PROMPT_ATTACH"
    while IFS= read -rsn1 key; do
        case "$key" in
        '')
            printf '\n'
            return 0
            ;;
        $'\t')
            matches=()
            for session in "${sessions[@]}"; do
                [[ $session == "$input"* ]] && matches+=("$session")
            done
            if ((${#matches[@]} == 1)); then
                match=${matches[0]}
                rest=${match:${#input}}
                printf '%s' "$rest"
                input+=$rest
            elif ((${#matches[@]} > 1)); then
                printf '\n'
                printf '  %s\n' "${matches[@]}"
                printf '%s%s' "$PROMPT_ATTACH" "$input"
            fi
            ;;
        $'\177' | $'\b')
            if [ -n "$input" ]; then
                printf '\b \b'
                input=${input%?}
            fi
            ;;
        $'\e')
            # An arrow key is ESC plus two more bytes. Swallowing only the ESC
            # would leave "[A" in the name being typed.
            read -rsn2 -t 0.01 _ || true
            ;;
        *)
            printf '%s' "$key"
            input+=$key
            ;;
        esac
    done
    # End of input: treat it as a cancel rather than attaching to whatever was
    # typed so far.
    input=""
    printf '\n'
    return 1
}

attach_session() {
    local sessions=() input index session matches=()
    mapfile -t sessions < <(tmux list-sessions -F '#{session_name}' 2>/dev/null)
    if ((${#sessions[@]} == 0)); then
        echo "No sessions yet — choose n to start one."
        return
    fi

    echo
    for index in "${!sessions[@]}"; do
        printf '  %2d) %s\n' "$((index + 1))" "${sessions[index]}"
    done
    echo

    read_with_completion || return
    case "$input" in
    "" | q | Q)
        echo "Cancelled."
        return
        ;;
    esac

    # A number picks from the list above; anything else is a name, exact first
    # and then as a unique prefix, which is what TAB was completing.
    if [[ $input =~ ^[0-9]+$ ]] && ((input >= 1 && input <= ${#sessions[@]})); then
        tmux attach-session -t "=${sessions[input - 1]}"
        return
    fi
    for session in "${sessions[@]}"; do
        [[ $session == "$input"* ]] && matches+=("$session")
        if [[ $session == "$input" ]]; then
            tmux attach-session -t "=$session"
            return
        fi
    done
    if ((${#matches[@]} == 1)); then
        tmux attach-session -t "=${matches[0]}"
    else
        echo "No session matches $input."
    fi
}

list_sessions() {
    echo
    tmux list-sessions 2>/dev/null || echo "No sessions yet — choose n to start one."
}

main() {
    if ! command -v tmux >/dev/null 2>&1; then
        echo "tmux is not installed in this VM; skipping the session menu." >&2
        return 0
    fi

    local choice
    while :; do
        show_menu
        # ^D at the menu leaves a shell, the same as q: a login must never be
        # a loop with no way out.
        read -r -n1 -p "Choice: " choice || break
        # The terminal itself echoes the key, so this is only the newline after
        # it. Printing the key here as well doubles it on a real login.
        echo
        case "$choice" in
        n | N) new_session ;;
        a | A) attach_session ;;
        l | L) list_sessions ;;
        q | Q) break ;;
        *) echo "Choose n, a, l, or q." ;;
        esac
    done
}

main "$@"
