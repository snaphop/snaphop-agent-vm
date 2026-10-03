package cli

import (
	"context"
	"sort"
	"strings"

	"github.com/snaphop/snaphop-agent-vm/internal/image/distro"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

// completeCommandName is the hidden command the generated shell scripts call to
// get candidates. It is named with underscores so it cannot collide with a real
// command, and it is hidden from `--help` because it is an interface for shells
// rather than for operators.
const completeCommandName = "__complete"

func completionCommand() *command {
	return &command{
		name:    "completion",
		summary: "print a tab-completion script for a shell",
		usage:   "agent-vm completion <bash|zsh|fish>",
		run:     runCompletion,
	}
}

func completeCommand() *command {
	return &command{
		name:    completeCommandName,
		summary: "print completion candidates for a partial command line",
		usage:   "agent-vm " + completeCommandName + " [word]...",
		hidden:  true,
		run:     runComplete,
	}
}

func runCompletion(_ context.Context, app *App, args []string) error {
	flags := newFlagSet("completion", "agent-vm completion <bash|zsh|fish>", app.Stderr)
	if err := flags.parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return flags.usagef("missing argument")
	}

	script, ok := completionScripts[flags.Arg(0)]
	if !ok {
		return flags.usagef("unsupported shell %q: expected one of %s", flags.Arg(0), completionScriptNames())
	}
	app.out.Printf("%s", script)
	return nil
}

// runComplete answers one completion request. It always succeeds: a completion
// helper that printed an error or exited non-zero would put that text where the
// operator is typing, so an unknown command line simply produces no candidates.
func runComplete(_ context.Context, app *App, args []string) error {
	for _, candidate := range app.completionCandidates(args) {
		app.out.Printf("%s\n", candidate)
	}
	return nil
}

// argSource produces candidates for a positional argument. It is a function
// because the interesting ones — VM names, cached images — have to be read from
// the state directory at completion time.
type argSource func(app *App) []string

// completionSpec describes one command well enough to complete it: its own
// flags, what its positional arguments are, and its subcommands.
type completionSpec struct {
	// flags are the command's own flag names without dashes. The drift test in
	// completion_test.go compares this list against the flags the command
	// actually registers, so a new flag cannot be forgotten here.
	flags []string
	// args are the sources for each positional argument, in order. A command
	// that takes one VM name has one entry; once that argument is given, no
	// further candidates are offered.
	args []argSource
	// subcommands is set for commands that dispatch further, such as `image`.
	subcommands map[string]*completionSpec
}

// globalFlags are the flags accepted before a command name.
var globalFlags = []string{
	"config", "state-dir", "libvirt-uri", "output",
	"verbose", "quiet", "yes", "dry-run", "version", "help",
}

// flagValues lists the values of flags whose set of valid values is closed.
// Flags taking a path, a size, or a duration are absent on purpose: the shell
// falls back to its own file completion when we offer nothing.
var flagValues = map[string][]string{
	"output":   {"text", "json"},
	"network":  {"nat", "bridge"},
	"distro":   nil, // filled in by completionSpecs, which knows the families
	"platform": {"linux/amd64", "linux/arm64"},
}

// valuelessFlags are the boolean flags: they never consume the next word, so a
// name may still be completed directly after one.
var valuelessFlags = map[string]bool{
	"verbose": true, "quiet": true, "yes": true, "dry-run": true,
	"version": true, "help": true, "force": true, "keep-disk": true,
	"no-start": true, "all": true,
}

func distroCandidates(*App) []string { return distro.ImageNames() }

// vmNames lists the VMs recorded in the state directory. Every failure is
// swallowed: with no state directory yet, the answer is simply no candidates.
func vmNames(app *App) []string {
	store, err := app.completionStore()
	if err != nil {
		return nil
	}
	// An unreadable record is skipped rather than costing every other name.
	vms, _, err := store.ScanVMs()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(vms))
	for _, vm := range vms {
		names = append(names, vm.Name)
	}
	return names
}

// imageRefs lists the cached base images as `<distro>:<tag>`, which is the form
// `image inspect` and `image rm` take.
func imageRefs(app *App) []string {
	store, err := app.completionStore()
	if err != nil {
		return nil
	}
	manifests, err := store.ListImages()
	if err != nil {
		return nil
	}
	refs := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		refs = append(refs, manifest.Ref())
	}
	return refs
}

// completionStore opens the state directory read-only. Completing a command
// line must never create anything on the host, so this never asks for the tree
// to be created.
//
// It opens the directory wherever it actually is, which for a remote hypervisor
// means a connection per completion. That is slower than a local read, but the
// alternative is reading a path on this machine that holds nothing and offering
// no completions at all, with nothing to explain why.
func (a *App) completionStore() (*state.Store, error) { return a.openStore(false) }

func completionSpecs() map[string]*completionSpec {
	timeoutFlags := []string{"timeout", "force"}
	vmName := []argSource{vmNames}

	return map[string]*completionSpec{
		"doctor":     {},
		"create":     {flags: []string{"distro", "vcpus", "memory", "max-memory", "disk", "network", "bridge", "cloud-init", "opencode-config", "no-start", "wait-for-ssh", "ssh-key", "host-authorized-keys", "virt-install-arg", "github-ssh-key"}},
		"list":       {},
		"info":       {args: vmName},
		"start":      {args: vmName},
		"stop":       {flags: timeoutFlags, args: vmName},
		"restart":    {flags: timeoutFlags, args: vmName},
		"ssh":        {args: vmName},
		"update":     {flags: []string{"all", "timeout"}, args: vmName},
		"console":    {args: vmName},
		"destroy":    {flags: []string{"keep-disk", "force", "timeout", "github-ssh-key"}, args: vmName},
		"licenses":   {},
		"completion": {args: []argSource{func(*App) []string { return shellNames() }}},
		"image": {subcommands: map[string]*completionSpec{
			"build":   {flags: []string{"from", "platform", "force"}, args: []argSource{distroCandidates}},
			"list":    {},
			"inspect": {args: []argSource{imageRefs}},
			"rm":      {flags: []string{"force"}, args: []argSource{imageRefs}},
		}},
	}
}

// completionCandidates returns what may follow the words already typed. The
// last word is the one being completed and may be empty; everything before it
// is context.
func (a *App) completionCandidates(words []string) []string {
	if len(words) == 0 {
		words = []string{""}
	}
	typed := words[:len(words)-1]
	current := words[len(words)-1]

	// A flag written as `--network nat` takes its value as the next word, so
	// the word being completed belongs to that flag rather than to the command.
	if len(typed) > 0 {
		if name, ok := flagName(typed[len(typed)-1]); ok && !valuelessFlags[name] {
			return withPrefix(a.flagValueCandidates(name), current)
		}
	}

	spec, positional := a.resolveSpec(typed)
	if spec == nil {
		return nil
	}

	// A flag written as `--output=j` carries its value in the word being
	// completed. Candidates keep the `--output=` part, because that is the
	// word the shell asked about; the bash script trims what bash will not
	// replace.
	if flag, partial, ok := strings.Cut(current, "="); ok && strings.HasPrefix(flag, "-") {
		name, isFlag := flagName(flag)
		if !isFlag || valuelessFlags[name] {
			return nil
		}
		values := withPrefix(a.flagValueCandidates(name), partial)
		candidates := make([]string, 0, len(values))
		for _, value := range values {
			candidates = append(candidates, flag+"="+value)
		}
		return candidates
	}

	if strings.HasPrefix(current, "-") {
		return withPrefix(dashed(spec.flags), current)
	}

	candidates := []string{}
	if spec.subcommands != nil && positional == 0 {
		candidates = append(candidates, sortedKeys(spec.subcommands)...)
	}
	if positional < len(spec.args) {
		candidates = append(candidates, spec.args[positional](a)...)
	}
	return withPrefix(candidates, current)
}

// resolveSpec walks the words already typed and returns the spec that governs
// the next word, plus how many positional arguments of it are already given.
// A nil spec means the command line is one this tool cannot complete.
func (a *App) resolveSpec(typed []string) (*completionSpec, int) {
	specs := completionSpecs()

	var current *completionSpec
	positional := 0
	skipValue := false

	for _, word := range typed {
		if skipValue {
			skipValue = false
			continue
		}
		if name, ok := flagName(word); ok {
			skipValue = !valuelessFlags[name] && !strings.Contains(word, "=")
			continue
		}
		if word == "--" {
			// Everything after `--` is the guest's command line (`agent-vm ssh
			// web -- ls`), which is the guest's business, not ours.
			return nil, 0
		}

		switch {
		case current == nil:
			spec, ok := specs[word]
			if !ok {
				return nil, 0
			}
			current = spec
		case current.subcommands != nil && positional == 0:
			spec, ok := current.subcommands[word]
			if !ok {
				return nil, 0
			}
			current = spec
		default:
			positional++
		}
	}

	if current == nil {
		// No command yet: the word being completed is the command name.
		return &completionSpec{
			flags: globalFlags,
			args:  []argSource{func(*App) []string { return commandNames() }},
		}, 0
	}
	return current, positional
}

// flagValueCandidates answers what a flag's value may be. Only closed sets are
// answered; a path or a size is left to the shell's own completion.
func (a *App) flagValueCandidates(name string) []string {
	if name == "distro" {
		// Both a family and a cached `<distro>:<tag>` are valid here.
		return append(distro.ImageNames(), imageRefs(a)...)
	}
	return flagValues[name]
}

// flagName reports whether a word is a flag, and returns its name without
// leading dashes or trailing value.
func flagName(word string) (string, bool) {
	if word == "--" || !strings.HasPrefix(word, "-") || word == "-" {
		return "", false
	}
	name := strings.TrimLeft(word, "-")
	name, _, _ = strings.Cut(name, "=")
	return name, true
}

// dashed renders flag names the way an operator types them.
func dashed(flags []string) []string {
	out := make([]string, 0, len(flags))
	for _, name := range flags {
		out = append(out, "--"+name)
	}
	sort.Strings(out)
	return out
}

func withPrefix(candidates []string, prefix string) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, prefix) {
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out
}

// commandNames lists the commands an operator may type, hidden ones excluded.
func commandNames() []string {
	names := []string{}
	for name, cmd := range commands() {
		if !cmd.hidden {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func shellNames() []string { return sortedKeys(completionScripts) }

// The scripts below delegate every decision to `agent-vm __complete`, so a new
// command or flag needs no change to them. Each passes the words typed so far,
// including the (possibly empty) word under the cursor, and each discards
// stderr: a message from this tool must never land in the operator's prompt.
var completionScripts = map[string]string{
	"bash": `# bash completion for agent-vm. Install with:
#   agent-vm completion bash > /etc/bash_completion.d/agent-vm
# or, for one user:
#   agent-vm completion bash > ~/.local/share/bash-completion/completions/agent-vm
_agent_vm_complete() {
    # bash's own word array is split at every COMP_WORDBREAKS character, so
    # an image ref (ubuntu:24.04) or a --flag=value would reach agent-vm in
    # pieces. The words are rebuilt from the line up to the cursor instead,
    # split on whitespace only, without the optional bash-completion package.
    # Quoting is not interpreted; nothing agent-vm completes needs it.
    local line=${COMP_LINE:0:COMP_POINT}
    local -a words
    read -r -a words <<< "$line"
    if [[ -z $line || $line == *[[:space:]] ]]; then
        words+=("")
    fi
    local cur=${words[${#words[@]}-1]}

    # bash replaces only the text after the last word-break character in the
    # current word, so that part of each candidate is all it may be given;
    # otherwise ubuntu:<Tab> would become ubuntu:ubuntu:24.04.
    local breaks="" prefix=""
    [[ $COMP_WORDBREAKS == *:* ]] && breaks+=":"
    [[ $COMP_WORDBREAKS == *=* ]] && breaks+="="
    if [[ -n $breaks && $cur == *["$breaks"]* ]]; then
        prefix=${cur%"${cur##*["$breaks"]}"}
    fi

    local candidates candidate
    mapfile -t candidates < <(agent-vm __complete "${words[@]:1}" 2>/dev/null)
    COMPREPLY=()
    for candidate in "${candidates[@]}"; do
        COMPREPLY+=("${candidate#"$prefix"}")
    done
}
# -o default falls back to filenames where agent-vm offers nothing, which is
# what --config, --ssh-key, and --cloud-init want.
complete -o default -F _agent_vm_complete agent-vm
`,
	"zsh": `#compdef agent-vm
# zsh completion for agent-vm. Install with:
#   agent-vm completion zsh > "${fpath[1]}/_agent-vm"
# and make sure compinit runs in ~/.zshrc.
_agent_vm_complete() {
    local -a candidates
    candidates=(${(f)"$(agent-vm __complete "${(@)words[2,$CURRENT]}" 2>/dev/null)"})
    if (( ${#candidates} )); then
        compadd -- ${candidates}
    else
        _files
    fi
}
compdef _agent_vm_complete agent-vm
`,
	"fish": `# fish completion for agent-vm. Install with:
#   agent-vm completion fish > ~/.config/fish/completions/agent-vm.fish
function __agent_vm_complete
    set -l tokens (commandline --current-process --tokenize --cut-at-cursor)
    agent-vm __complete $tokens[2..-1] (commandline --current-token --cut-at-cursor) 2>/dev/null
end
complete -c agent-vm -f -a '(__agent_vm_complete)'
`,
}

// completionScriptNames exists so the error message cannot disagree with the
// set of scripts actually available.
func completionScriptNames() string { return strings.Join(shellNames(), ", ") }
