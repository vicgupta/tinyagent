//go:build !windows

package gitwt

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// RunGuard is the process TinyAgent puts in front of git for commands it
// starts. It refuses a commit that would record tinyagent.yaml and rewrites
// git add so that path is left untouched in the index. It does not return
// on success: the real git replaces this process.
func RunGuard(args []string) int {
	real := os.Getenv("TINYAGENT_REAL_GIT")
	if real == "" {
		if p, err := exec.LookPath("git"); err == nil {
			real = p
		}
	}
	if real == "" {
		fmt.Fprintln(os.Stderr, "tinyagent: git is not on PATH")
		return 127
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tinyagent: %v\n", err)
		return 1
	}
	block, message, rewrite, err := Decide(real, cwd, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tinyagent: %v\n", err)
		return 1
	}
	if block {
		fmt.Fprintln(os.Stderr, message)
		return 1
	}
	use := args
	if rewrite != nil {
		use = rewrite
	}
	argv := append([]string{real}, use...)
	if err := syscall.Exec(real, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "tinyagent: could not run git: %v\n", err)
		return 127
	}
	return 0
}
