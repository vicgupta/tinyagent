package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
)

// completionValues maps a flag name to the fixed values it accepts. Only
// flags with a small static set belong here; the scripts embed the lists, so
// Tab still never runs tinyagent or reads config. Dynamic values (models,
// profiles, snapshots) are deliberately left out.
var completionValues = map[string][]string{
	"command-env":      {"all", "core", "none"},
	"completion":       {"bash", "zsh", "fish"},
	"env":              {"local", "container"},
	"openai-api":       model.OpenAIAPIs,
	"reasoning-effort": model.ReasoningEfforts,
	"sandbox-backend":  {"auto", "landlock", "bwrap"},
}

// completionValueFlags returns the flag names in completionValues, sorted, so
// the generated scripts are deterministic.
func completionValueFlags() []string {
	names := make([]string, 0, len(completionValues))
	for name := range completionValues {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// completionCaseArms returns shell case arms that set the variable values to
// each value flag's list, matching both -name and --name as the flag package
// does. Bash wants "pat) values='a b' ;;", Zsh "(pat) values=(a b) ;;".
func completionCaseArms(zsh bool, indent string) string {
	var b strings.Builder
	for _, name := range completionValueFlags() {
		vals := strings.Join(completionValues[name], " ")
		if zsh {
			fmt.Fprintf(&b, "%s(-%s|--%s) values=(%s) ;;\n", indent, name, name, vals)
		} else {
			fmt.Fprintf(&b, "%s-%s|--%s) values='%s' ;;\n", indent, name, name, vals)
		}
	}
	return b.String()
}
