package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// runCompletion writes the completion script for shell to w. It
// runs before any config, credential or task handling, so it works offline
// and with a broken user config.
func runCompletion(shell string, fs *flag.FlagSet, w io.Writer) int {
	switch shell {
	case "bash":
		fmt.Fprint(w, bashCompletion(fs))
	case "zsh":
		fmt.Fprint(w, zshCompletion(fs))
	default:
		fmt.Fprintf(os.Stderr, "error: --completion %q is not supported; supported shells are bash and zsh: tinyagent --completion bash|zsh\n", shell)
		return 2
	}
	return 0
}

// completionFlags returns every registered flag as it is typed on the
// command line, sorted: -x for one-letter aliases, --name otherwise.
func completionFlags(fs *flag.FlagSet) []string {
	var names []string
	fs.VisitAll(func(f *flag.Flag) {
		if len(f.Name) == 1 {
			names = append(names, "-"+f.Name)
		} else {
			names = append(names, "--"+f.Name)
		}
	})
	sort.Strings(names)
	return names
}

// bashCompletion returns a sourceable Bash script that completes flag names
// from a fixed list, so pressing Tab never runs tinyagent. Words that don't
// start with "-" get no candidates, and -o default falls back to filenames.
func bashCompletion(fs *flag.FlagSet) string {
	return `# Bash completion for tinyagent flags.
# Load it with: source <(tinyagent --completion bash)
_tinyagent() {
    local cur=${COMP_WORDS[COMP_CWORD]}
    COMPREPLY=()
    [[ $cur == -* ]] || return 0
    local flags='` + strings.Join(completionFlags(fs), " ") + `'
    COMPREPLY=($(compgen -W "$flags" -- "$cur"))
}
complete -o default -F _tinyagent tinyagent
`
}

// zshCompletion returns a sourceable Zsh script that registers flag-name
// completion with compdef (no bashcompinit) from a fixed list, so pressing
// Tab never runs tinyagent. Words that don't start with "-", and flag
// prefixes that match nothing, fall back to filenames, like Bash's -o default.
func zshCompletion(fs *flag.FlagSet) string {
	return `#compdef tinyagent
# Zsh completion for tinyagent flags.
# Load it after compinit with: source <(tinyagent --completion zsh)
if (( ! $+functions[compdef] )); then
    print -u2 'tinyagent: run "autoload -Uz compinit; compinit" before sourcing this script'
    return 1
fi
_tinyagent() {
    local -a flags
    flags=(` + strings.Join(completionFlags(fs), " ") + `)
    if [[ $PREFIX == -* ]] && compadd -a flags; then
        return 0
    fi
    _files
}
compdef _tinyagent tinyagent
`
}
