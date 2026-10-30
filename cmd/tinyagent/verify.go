package main

import (
	"flag"

	"github.com/vicgupta/tinyagent/internal/config"
)

// verifyFlags are the verify-before-stop guard's command-line flags.
type verifyFlags struct {
	enabled      bool
	commands     stringList
	maxReprompts int
}

// verifyGroup lists the verify-before-stop flags in --help --verbose.
var verifyGroup = struct {
	name  string
	flags [][3]string
}{"Verify before stop", [][3]string{{"", "max-verify-reprompts", "n"}, {"", "verify-command", "cmd"}, {"", "verify-on-stop", ""}}}

func registerVerifyFlags(fs *flag.FlagSet, v *verifyFlags) {
	fs.BoolVar(&v.enabled, "verify-on-stop", false, "don't accept a final answer while files changed since the last successful test/build run: send the model back to verify, up to --max-verify-reprompts times, then mark the run unverified (default off; config verify_on_stop.enabled)")
	fs.Var(&v.commands, "verify-command", "a bash command that counts as verification for --verify-on-stop when it exits 0, e.g. \"go test ./...\"; repeatable, replaces verify_on_stop.commands")
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
	if flagWasSet(fs, "max-verify-reprompts") {
		cfg.VerifyOnStop.MaxReprompts = v.maxReprompts
	}
	return cfg.VerifyOnStop.Validate()
}
