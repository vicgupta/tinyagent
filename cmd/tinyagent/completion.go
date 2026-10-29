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
	case "fish":
		fmt.Fprint(w, fishCompletion(fs))
	default:
		fmt.Fprintf(os.Stderr, "error: --completion %q is not supported; supported shells are bash, zsh and fish: tinyagent --completion bash|zsh|fish\n", shell)
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
// from a fixed list, so pressing Tab never runs tinyagent. After a flag in
// completionValues (as "--flag value" or "--flag=value") it offers that
// flag's values. Other words that don't start with "-" get no candidates,
// and -o default falls back to filenames.
func bashCompletion(fs *flag.FlagSet) string {
	return `# Bash completion for tinyagent flags.
# Load it with: source <(tinyagent --completion bash)
_tinyagent() {
    local cur=${COMP_WORDS[COMP_CWORD]} prev= pre= values=
    (( COMP_CWORD > 0 )) && prev=${COMP_WORDS[COMP_CWORD-1]}
    COMPREPLY=()
    # --flag=value: by default Bash splits the word at "=" (COMP_WORDBREAKS).
    if [[ $cur == = ]]; then
        cur=
    elif [[ $prev == = ]] && (( COMP_CWORD > 1 )); then
        prev=${COMP_WORDS[COMP_CWORD-2]}
    elif [[ $cur == -*=* ]]; then
        prev=${cur%%=*} pre=${cur%%=*}= cur=${cur#*=}
    fi
    case $prev in
` + completionCaseArms(false, "        ") + `    esac
    if [[ -n $values ]]; then
        COMPREPLY=($(compgen -P "$pre" -W "$values" -- "$cur"))
        (( ${#COMPREPLY[@]} )) && return 0
    fi
    [[ $cur == -* ]] || return 0
    local flags='` + strings.Join(completionFlags(fs), " ") + `'
    COMPREPLY=($(compgen -W "$flags" -- "$cur"))
}
complete -o default -F _tinyagent tinyagent
`
}

// zshCompletion returns a sourceable Zsh script that registers flag-name
// completion with compdef (no bashcompinit) from a fixed list, so pressing
// Tab never runs tinyagent. After a flag in completionValues (as "--flag
// value" or "--flag=value") it offers that flag's values. Words that don't
// start with "-", and flag prefixes that match nothing, fall back to
// filenames, like Bash's -o default.
func zshCompletion(fs *flag.FlagSet) string {
	return `#compdef tinyagent
# Zsh completion for tinyagent flags.
# Load it after compinit with: source <(tinyagent --completion zsh)
if (( ! $+functions[compdef] )); then
    print -u2 'tinyagent: run "autoload -Uz compinit; compinit" before sourcing this script'
    return 1
fi
_tinyagent() {
    local -a flags values
    local flag=${words[CURRENT-1]}
    [[ $PREFIX == -*=* ]] && flag=${PREFIX%%=*}
    case $flag in
` + completionCaseArms(true, "        ") + `    esac
    if (( $#values )); then
        [[ $PREFIX == -*=* ]] && compset -P '*='
        compadd -a values && return 0
    fi
    flags=(` + strings.Join(completionFlags(fs), " ") + `)
    if [[ $PREFIX == -* ]] && compadd -a flags; then
        return 0
    fi
    _files
}
compdef _tinyagent tinyagent
`
}

// fishCompletion embeds the registered flags using Fish's native completion
// command. Flags in completionValues complete their values (-x: an argument
// is required and only those values are offered). Filename completion stays
// enabled elsewhere; Tab never invokes tinyagent.
func fishCompletion(fs *flag.FlagSet) string {
	var script strings.Builder
	script.WriteString("# Fish completion for tinyagent flags.\n# Load it with: tinyagent --completion fish | source\n")
	for _, name := range completionFlags(fs) {
		if strings.HasPrefix(name, "--") {
			long := strings.TrimPrefix(name, "--")
			if vals, ok := completionValues[long]; ok {
				fmt.Fprintf(&script, "complete -c tinyagent -l %s -x -a '%s'\n", long, strings.Join(vals, " "))
				continue
			}
			fmt.Fprintf(&script, "complete -c tinyagent -l %s\n", long)
		} else {
			fmt.Fprintf(&script, "complete -c tinyagent -s %s\n", strings.TrimPrefix(name, "-"))
		}
	}
	return script.String()
}
