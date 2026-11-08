//go:build !windows

package tools

import (
	"os"
	"strings"

	"github.com/vicgupta/tinyagent/internal/gitwt"
)

// GitGuardArg is argv[1] when TinyAgent re-executes itself to stand in
// front of git for a bash or start_process command. It is not a user flag.
const GitGuardArg = "__git_guard"

// MaybeRunGitGuard exits when this process is that re-exec. Test binaries
// call it from TestMain so a git command does not start the test suite.
func MaybeRunGitGuard() {
	if len(os.Args) > 1 && os.Args[1] == GitGuardArg {
		os.Exit(gitwt.RunGuard(os.Args[2:]))
	}
}

// guardGitCommand prefixes a bash function named git onto command. The
// function runs only in that shell: it does not change PATH or the
// environment, so command_env policies stay intact, and git typed outside
// TinyAgent is not affected. A nested shell, `command git`, or an absolute
// path does not use the function.
func guardGitCommand(command string) string {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = os.Args[0]
	}
	var b strings.Builder
	b.WriteString("git() { local _tinyagent_git; _tinyagent_git=$(type -P git) || { printf 'bash: git: command not found\\n' >&2; return 127; }; TINYAGENT_REAL_GIT=\"$_tinyagent_git\" ")
	b.WriteString(shQuote(self))
	b.WriteByte(' ')
	b.WriteString(shQuote(GitGuardArg))
	b.WriteString(" \"$@\"; }\n")
	b.WriteString(command)
	return b.String()
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
