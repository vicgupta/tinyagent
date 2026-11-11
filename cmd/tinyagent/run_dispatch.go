package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/update"
)

// The phases of run (issue #416): parse the command line, dispatch the
// commands that exit early, validate the flags shared by the run modes,
// dispatch a mode (triage, web, chat, workflow, eval, attempts, ACP), then
// the single-run commands (version, init, update, check, rewind, resume)
// and finally a one-shot run (run_oneshot.go). Each dispatcher returns
// done=true with the exit code when it handled the invocation; the checks
// run in the order run has always used, so the first error reported for a
// bad command line is unchanged.

// parseCommandLine handles the bare-terminal help and subcommand help,
// expands subcommands, parses the flags and applies --color and --help.
func parseCommandLine() (o *options, fs *flag.FlagSet, code int, done bool) {
	// Plain "tinyagent" typed at a terminal prints the same brief help as
	// --help. With stdin piped or redirected it keeps the usage error (exit
	// 2), so scripts see the same result as before.
	if len(os.Args) == 1 && stdinIsTerminal() {
		fmt.Fprint(helpOutput, helpBrief)
		return nil, nil, 0, true
	}
	args, sh, serr := expandSubcommand(os.Args[1:])
	if serr != nil {
		fmt.Fprintln(flagOutput, "error:", serr)
		return nil, nil, 2, true
	}
	if sh != nil {
		printSubcommandHelp(helpOutput, sh.sub)
		return nil, nil, 0, true
	}
	os.Args = append([]string{os.Args[0]}, args...)
	o = &options{}
	fs = newFlagSet(o)
	// Parse quietly so an unknown flag gets a one-line error instead of the
	// full usage; other parse errors print as the flag package would.
	fs.SetOutput(io.Discard)
	usage := fs.Usage
	fs.Usage = func() {}
	err := fs.Parse(os.Args[1:])
	fs.SetOutput(flagOutput)
	fs.Usage = usage
	if err != nil {
		if !reportUnknownFlag(fs.Output(), fs, os.Args[1:], err) {
			fmt.Fprintln(fs.Output(), err)
			fs.Usage()
		}
		return o, fs, 2, true
	}
	if !validColorMode(o.color) {
		fmt.Fprintf(fs.Output(), "error: --color %q must be auto, always or never\n", o.color)
		return o, fs, 2, true
	}
	setColor(o.color)
	if o.showHelp {
		printHelp(helpOutput, fs, o.verbose)
		return o, fs, 0, true
	}
	return o, fs, 0, false
}

// dispatchMaintenance runs the commands that inspect or manage local state
// and exit: profiles, plugins, TinyGo, the plugin store, skills
// validation, memory, ratchet and tune.
func dispatchMaintenance(o *options, fs *flag.FlagSet) (int, bool) {
	switch {
	case o.listProfiles:
		return runListProfiles(o, fs, os.Stdout), true
	case o.listPlugins:
		return runListPlugins(o, fs, os.Stdout), true
	case o.tinygoInstall || o.tinygoUpdate || o.tinygoStatus:
		return runTinygo(o, os.Stdout, os.Stderr), true
	case o.pluginsPrune || flagWasSet(fs, "plugins-rollback"):
		return runPluginStore(o, os.Stdout, os.Stderr), true
	case len(o.skillsValidate) > 0:
		if o.task != "" || o.workflow != "" {
			fmt.Fprintln(os.Stderr, "error: --skills-validate can't be combined with -t or --workflow")
			return 2, true
		}
		return runSkillsValidate(o.skillsValidate, os.Stdout, os.Stderr), true
	case memoryCommandSet(fs):
		return runMemoryCommand(o, fs, os.Stdout, os.Stderr), true
	case ratchetCommandSet(fs):
		return runRatchetCommand(o, fs, os.Stdout, os.Stderr), true
	case tuneCommandSet(fs):
		return runTuneCommand(o, fs, os.Stdout, os.Stderr), true
	}
	return 0, false
}

// checkModeConflicts rejects flags that don't apply to --acp,
// --interactive or --web, then runs --transcript and --completion.
func checkModeConflicts(o *options, fs *flag.FlagSet) (int, bool) {
	if o.acp {
		if f := acpConflict(fs); f != "" {
			fmt.Fprintf(fs.Output(), "error: --acp can't be combined with %s: the editor supplies each session's prompts and cwd\n", f)
			return 2, true
		}
	}
	if o.interactive {
		if f := chatConflict(fs); f != "" {
			fmt.Fprintf(fs.Output(), "error: --interactive can't be combined with %s\n", f)
			return 2, true
		}
	}
	if o.web {
		if f := webConflict(fs); f != "" {
			fmt.Fprintf(fs.Output(), "error: --web can't be combined with %s\n", f)
			return 2, true
		}
	} else if flagWasSet(fs, "web-port", "web-no-open") {
		fmt.Fprintln(fs.Output(), "error: --web-port and --web-no-open apply only to tinyagent web (--web)")
		return 2, true
	}
	if flagWasSet(fs, "transcript") {
		return runTranscript(o, fs, os.Stdout), true
	}
	if flagWasSet(fs, "completion") {
		return runCompletion(o.completion, fs, os.Stdout), true
	}
	return 0, false
}

// validateRunFlags checks the flags every run mode shares: no positional
// arguments, valid --env, --sandbox-backend, --command-env and limits, and
// mode-specific flags only with their mode.
func validateRunFlags(o *options, fs *flag.FlagSet) (int, bool) {
	// Parsing stops at the first non-flag, so any flags after it would be
	// silently ignored.
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "error: unexpected positional argument %q; pass the task with -t/--task\n", fs.Arg(0))
		fs.Usage()
		return 2, true
	}
	if msg := runFlagError(o, fs); msg != "" {
		fmt.Fprintln(fs.Output(), "error: "+msg)
		return 2, true
	}
	return 0, false
}

// runFlagError returns the first invalid-value or misplaced-flag error
// validateRunFlags reports, without the "error: " prefix, or "".
func runFlagError(o *options, fs *flag.FlagSet) string {
	switch {
	case !config.ValidEnvironment(o.execEnv):
		return fmt.Sprintf("--env %q must be local or container", o.execEnv)
	case flagWasSet(fs, "sandbox-backend") && (o.sandboxBackend == "" || !config.ValidSandboxBackend(o.sandboxBackend)):
		return fmt.Sprintf("--sandbox-backend %q must be auto, landlock or bwrap", o.sandboxBackend)
	case flagWasSet(fs, "command-env") && (o.commandEnv == "" || !config.ValidInherit(o.commandEnv)):
		return fmt.Sprintf("--command-env %q must be all, core or none", o.commandEnv)
	}
	if msg := validateLimits(*o); msg != "" {
		return msg
	}
	switch {
	case !flagWasSet(fs, "eval") && flagWasSet(fs, "eval-out", "eval-keep", "eval-min-pass-rate"):
		return "--eval-out, --eval-keep and --eval-min-pass-rate apply only to --eval"
	case !flagWasSet(fs, "workflow") && flagWasSet(fs, "workflow-name"):
		return "--workflow-name applies only to --workflow"
	case !flagWasSet(fs, "workflow") && flagWasSet(fs, "workflow-observe"):
		return "--workflow-observe applies only to --workflow"
	case !flagWasSet(fs, "triage") && flagWasSet(fs, "triage-out", "triage-stale-days"):
		return "--triage-out and --triage-stale-days apply only to --triage"
	}
	return ""
}

// dispatchModes runs triage, the web UI, chat, a workflow, an eval suite,
// attempts or ACP.
func dispatchModes(o *options, fs *flag.FlagSet) (int, bool) {
	switch {
	case flagWasSet(fs, "triage"):
		return runTriage(o, fs, os.Stdin, os.Stdout, os.Stderr), true
	case o.web:
		return runWeb(o, fs), true
	case o.interactive:
		return runChat(o, fs), true
	case flagWasSet(fs, "workflow"):
		return runWorkflow(o, fs, os.Args[1:], os.Stdout, os.Stderr), true
	case flagWasSet(fs, "eval"):
		return runEval(o, fs, os.Args[1:], os.Stderr), true
	case attemptsActive(fs) && !o.check:
		return runAttempts(o, fs, os.Args[1:], os.Stdout, os.Stderr), true
	case o.acp:
		return runACP(o, fs, os.Stdin, os.Stdout), true
	}
	return 0, false
}

// dispatchSingleRun resolves the review selection and runs --version,
// init, --update, --check, --rewind and --resume, rejecting the flag
// combinations they don't accept. Otherwise it returns the review
// selection (nil outside review mode) for a one-shot run.
func dispatchSingleRun(o *options, fs *flag.FlagSet) (*reviewTarget, int, bool) {
	review, err := reviewSelection(fs, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, 2, true
	}
	if o.showVersion {
		fmt.Println("tinyagent", currentVersion())
		return nil, 0, true
	}
	if code, done := dispatchSetupCommands(o, fs); done {
		return nil, code, true
	}
	if code, done := dispatchRewindResume(o, fs); done {
		return nil, code, true
	}
	return review, 0, false
}

// dispatchSetupCommands runs init (--init-config), --update and --check.
func dispatchSetupCommands(o *options, fs *flag.FlagSet) (int, bool) {
	if (o.initForce || o.initMigrate) && !o.initConfig {
		fmt.Fprintln(os.Stderr, "error: --init-force and --init-migrate apply only to tinyagent init (--init-config)")
		return 2, true
	}
	if o.initConfig {
		if flagWasSet(fs, "task", "t", "resume", "rewind", "update") {
			fmt.Fprintln(os.Stderr, "error: --init-config can't be combined with --task, --resume, --rewind or --update")
			return 2, true
		}
		return runInitConfig(o), true
	}
	if o.doUpdate {
		u, err := update.New(currentVersion())
		if err != nil {
			return fail(err), true
		}
		return runUpdate(u, o.jsonOutput, os.Stdout, os.Stderr, isTerminal(os.Stderr)), true
	}
	if o.check {
		return runCheck(o, fs, os.Stdout), true
	}
	return 0, false
}

// dispatchRewindResume runs --rewind and --resume, rejecting the flags
// that don't combine with them.
func dispatchRewindResume(o *options, fs *flag.FlagSet) (int, bool) {
	if o.worktree.on && (o.resume != "" || o.rewind != "") {
		fmt.Fprintln(os.Stderr, "error: --worktree can't be combined with --resume or --rewind; pass the worktree path as --cwd instead")
		return 2, true
	}
	if o.rewind != "" {
		if o.resume != "" {
			fmt.Fprintln(os.Stderr, "error: --rewind and --resume are mutually exclusive")
			return 2, true
		}
		if o.profile != "" {
			fmt.Fprintln(os.Stderr, "error: --profile doesn't apply to --rewind, which reads no config")
			return 2, true
		}
		return runRewind(o, fs), true
	}
	if flagWasSet(fs, "to", "dry-run") {
		fmt.Fprintln(os.Stderr, "error: --to and --dry-run apply only to --rewind")
		return 2, true
	}
	if o.noAgentsMD && o.scopedAgentsMD {
		fmt.Fprintln(os.Stderr, "error: --no-agents-md and --scoped-agents-md can't be combined: --no-agents-md turns off all AGENTS.md handling, scoped files included; drop one of them")
		return 2, true
	}
	if o.resume != "" {
		if flagWasSet(fs, "output-schema") {
			fmt.Fprintln(os.Stderr, "error: --resume and --output-schema are mutually exclusive; runs with an output schema cannot be resumed")
			return 2, true
		}
		return runResume(o, fs), true
	}
	return 0, false
}
