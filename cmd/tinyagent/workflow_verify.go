package main

import "flag"

// workflowStageArgs returns the flags forwarded to every --workflow stage.
// Workflow stages verify before they stop by default: unless the command
// line set --verify-on-stop (on or off), each stage gets --verify-on-stop.
// Precedence: the flag, then this workflow default (on), then the config.
func workflowStageArgs(fs *flag.FlagSet, args []string) []string {
	out := forwardArgs(fs, args, workflowOwn)
	if !flagWasSet(fs, "verify-on-stop") {
		out = append(out, "--verify-on-stop")
	}
	return out
}
