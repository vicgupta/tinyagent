//go:build windows

package tools

// GitGuardArg is unused on Windows. The bash git function is Unix-only.
const GitGuardArg = "__git_guard"

func MaybeRunGitGuard() {}

func guardGitCommand(command string) string { return command }
