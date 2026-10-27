package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/vicgupta/tinyagent/internal/permissions"
)

// terminalPermissionApprover answers a policy "ask" through the controlling
// terminal. When the terminal can't be opened it returns a zero Answer, and
// the agent resolves the ask with the policy's non-interactive decision.
func terminalPermissionApprover(ctx context.Context, req permissions.Request) permissions.Answer {
	fmt.Fprintf(os.Stderr, "\nPermission required for %s", req.Tool)
	if req.Subagent != "" {
		fmt.Fprintf(os.Stderr, " (subagent %s)", req.Subagent)
	}
	fmt.Fprint(os.Stderr, ":\n  ")
	fmt.Fprintln(os.Stderr, indentArg(req.Argument))
	if req.Rule >= 0 {
		fmt.Fprintf(os.Stderr, "matched permission rule %d (ask)\n", req.Rule)
	}
	fmt.Fprint(os.Stderr, "[y]es once  [a]llow always  [n]o (add a reason after n): ")
	line, err := readTerminalLine(ctx)
	if err != nil {
		return permissions.Answer{}
	}
	return parseTerminalAnswer(line)
}

// parseTerminalAnswer turns one line of terminal input into an answer: y
// allows once, a allows this tool and argument for the rest of the run, and n
// (optionally with a reason) denies. Anything else denies.
func parseTerminalAnswer(line string) permissions.Answer {
	line = strings.TrimSpace(line)
	switch line {
	case "y", "Y":
		return permissions.Answer{Decision: permissions.DecisionAllow, Channel: "terminal"}
	case "a", "A":
		return permissions.Answer{Decision: permissions.DecisionAllow, Always: true, Channel: "terminal"}
	case "n", "N", "":
		return permissions.Answer{Decision: permissions.DecisionDeny, Channel: "terminal"}
	}
	if strings.HasPrefix(line, "n ") || strings.HasPrefix(line, "N ") {
		return permissions.Answer{Decision: permissions.DecisionDeny, Reason: strings.TrimSpace(line[2:]), Channel: "terminal"}
	}
	return permissions.Answer{Decision: permissions.DecisionDeny, Channel: "terminal"}
}

func indentArg(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(no argument)"
	}
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}
