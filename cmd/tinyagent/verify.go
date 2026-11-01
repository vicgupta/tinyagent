package main

import (
	"flag"

	"github.com/vicgupta/tinyagent/internal/config"
)

// verifyFlags are the verify-before-stop guard's command-line flags.
type verifyFlags struct {
	enabled      bool
	commands     stringList
	checks       stringList
	review       bool
	maxReprompts int
}

// verifyGroup lists the verify-before-stop flags in --help --verbose.
var verifyGroup = struct {
	name  string
	flags [][3]string
}{"Verify before stop", [][3]string{{"", "max-verify-reprompts", "n"}, {"", "verify-check", "cmd"}, {"", "verify-command", "cmd"}, {"", "verify-on-stop", ""}, {"", "verify-review", ""}}}

func registerVerifyFlags(fs *flag.FlagSet, v *verifyFlags) {
	fs.BoolVar(&v.enabled, "verify-on-stop", false, "don't accept a final answer while files changed since the last successful test/build run: send the model back to verify, up to --max-verify-reprompts times, then mark the run unverified (default off, on for --workflow stages; config verify_on_stop.enabled)")
	fs.Var(&v.commands, "verify-command", "a bash command that counts as verification for --verify-on-stop when it exits 0, e.g. \"go test ./...\"; repeatable, replaces verify_on_stop.commands")
	fs.Var(&v.checks, "verify-check", "a computational check for --verify-on-stop: a fast, deterministic bash command (lint, type check, tests) that passes on exit 0; when the model finishes with unverified changes tinyagent runs the checks itself in the given order, cheapest first, and sends the model back with the first failure; repeatable, replaces verify_on_stop.checks")
	fs.BoolVar(&v.review, "verify-review", false, "with --verify-on-stop and at least one --verify-check, after every check passes ask the model once, without tools, to review the run's changes and report its findings (report only: never re-prompts or changes the status; default off; config verify_on_stop.review)")
	fs.IntVar(&v.maxReprompts, "max-verify-reprompts", config.DefaultVerifyMaxReprompts, "with --verify-on-stop, how many times to send the model back to verify before stopping unverified (1 to 10; config verify_on_stop.max_reprompts)")
}

// applyVerifyFlags lets the verify flags override the config and checks
// the result.
func applyVerifyFlags(fs *flag.FlagSet, cfg *config.AgentConfig, v verifyFlags) error {
	if flagWasSet(fs, "verify-on-stop") {
		cfg.VerifyOnStop.Enabled = v.enabled
	}
	if flagWasSet(fs, "verify-command") {
		cfg.VerifyOnStop.Commands = append([]string(nil), v.commands...)
	}
	if flagWasSet(fs, "verify-check") {
		cfg.VerifyOnStop.Checks = append([]string(nil), v.checks...)
	}
	if flagWasSet(fs, "verify-review") {
		cfg.VerifyOnStop.Review = v.review
	}
	if flagWasSet(fs, "max-verify-reprompts") {
		cfg.VerifyOnStop.MaxReprompts = v.maxReprompts
	}
	return cfg.VerifyOnStop.Validate()
}
