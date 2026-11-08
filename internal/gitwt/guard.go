package gitwt

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Decide reports how TinyAgent should treat a git invocation it is about to
// run. block is set when the command is a commit that would record a
// tinyagent.yaml (added, modified, or deleted versus the commit's parent).
// args, when non-nil, replaces the arguments: git add is rewritten so a
// tinyagent.yaml already in the index keeps its staged blob and an untracked
// one is not added, even when a gitignore negation such as !tinyagent.yaml
// would otherwise stage it. Decide does not change the index or the work tree.
//
// gitBin is the real git binary. cwd is the command's working directory.
// A directory that is not a work tree is left to git itself.
func Decide(gitBin, cwd string, args []string) (block bool, message string, rewrite []string, err error) {
	globals, sub, rest, ok := splitGitInvocation(args)
	if !ok {
		return false, "", nil, nil
	}
	g := gitRunner{bin: gitBin, cwd: cwd, globals: globals}
	switch sub {
	case "add":
		if addPassthrough(rest) {
			return false, "", nil, nil
		}
		return false, "", withConfigExclude(globals, sub, rest), nil
	case "commit":
		c := parseCommitArgs(rest)
		if c.help || c.dry {
			return false, "", nil, nil
		}
		inside, qerr := g.insideWorkTree()
		if qerr != nil || !inside {
			return false, "", nil, nil
		}
		paths, rerr := g.commitWouldRecord(c)
		if rerr != nil {
			return false, "", nil, rerr
		}
		if len(paths) == 0 {
			return false, "", nil, nil
		}
		return true, fmt.Sprintf("tinyagent: refusing to commit tinyagent.yaml (%s); the index and working copy were not changed", strings.Join(paths, ", ")), nil, nil
	default:
		return false, "", nil, nil
	}
}

// commitArgs is the part of `git commit` that decides whether tinyagent.yaml
// would be recorded.
type commitArgs struct {
	all     bool
	include bool
	help    bool
	dry     bool
	paths   []string
}

func (g gitRunner) commitWouldRecord(c commitArgs) ([]string, error) {
	staged, err := g.configPaths("diff", "--cached", "--name-only", "-z", "--no-renames")
	if err != nil {
		return nil, err
	}
	if len(c.paths) > 0 && !c.all {
		var args []string
		if _, err := g.output("rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
			args = append([]string{"diff", "--name-only", "-z", "HEAD", "--"}, c.paths...)
		} else {
			args = append([]string{"diff", "--cached", "--name-only", "-z", "--no-renames", "--"}, c.paths...)
		}
		matched, err := g.configPaths(args...)
		if err != nil {
			return nil, err
		}
		if c.include {
			return unionPaths(staged, matched), nil
		}
		return matched, nil
	}
	if c.all {
		unstaged, err := g.configPaths("diff", "--name-only", "-z", "--", ":(glob)**/tinyagent.yaml")
		if err != nil {
			return nil, err
		}
		return unionPaths(staged, unstaged), nil
	}
	return staged, nil
}

func unionPaths(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range append(append([]string{}, a...), b...) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

type gitRunner struct {
	bin     string
	cwd     string
	globals []string
}

func (g gitRunner) insideWorkTree() (bool, error) {
	out, err := g.output("rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(out) == "true", nil
}

func (g gitRunner) configPaths(args ...string) ([]string, error) {
	out, err := g.output(args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if isLocalConfigPath(p) {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func (g gitRunner) output(args ...string) (string, error) {
	cmd := exec.Command(g.bin, append(append([]string{}, g.globals...), args...)...)
	cmd.Dir = g.cwd
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(msg)
	}
	return stdout.String(), nil
}

// splitGitInvocation separates global options from the subcommand.
// ok is false when there is no subcommand (for example `git --version`).
func splitGitInvocation(args []string) (globals []string, sub string, rest []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return globals, "", nil, false
		}
		switch {
		case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" || a == "--super-prefix" || a == "--config-env":
			if i+1 >= len(args) {
				return globals, "", nil, false
			}
			globals = append(globals, a, args[i+1])
			i++
		case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") || strings.HasPrefix(a, "--namespace=") || strings.HasPrefix(a, "--super-prefix=") || strings.HasPrefix(a, "--config-env=") || strings.HasPrefix(a, "-c"):
			globals = append(globals, a)
		case a == "--bare" || a == "--no-pager" || a == "--paginate" || a == "-p" || a == "--no-replace-objects" || a == "--no-optional-locks" || a == "--literal-pathspecs" || a == "--glob-pathspecs" || a == "--noglob-pathspecs" || a == "--icase-pathspecs" || a == "--exec-path" || strings.HasPrefix(a, "--exec-path="):
			globals = append(globals, a)
		case strings.HasPrefix(a, "-"):
			globals = append(globals, a)
		default:
			return globals, a, args[i+1:], true
		}
	}
	return globals, "", nil, false
}

func parseCommitArgs(rest []string) commitArgs {
	var c commitArgs
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			c.paths = append(c.paths, rest[i+1:]...)
			break
		}
		if a == "--help" || a == "-h" {
			c.help = true
			continue
		}
		if a == "--dry-run" {
			c.dry = true
			continue
		}
		if a == "--all" {
			c.all = true
			continue
		}
		if a == "--include" {
			c.include = true
			continue
		}
		if a == "--only" {
			continue
		}
		if strings.HasPrefix(a, "--") {
			name, _, hasVal := strings.Cut(a, "=")
			if !hasVal && commitOptTakesArg(name) && i+1 < len(rest) {
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			chars := a[1:]
			for j := 0; j < len(chars); j++ {
				switch chars[j] {
				case 'a':
					c.all = true
				case 'i':
					c.include = true
				case 'h':
					c.help = true
				case 'n', 's', 'v', 'q', 'e', 'o':
				case 'm', 'F', 'c', 'C', 't', 'u':
					if j+1 < len(chars) {
						j = len(chars)
					} else if i+1 < len(rest) {
						i++
					}
				case 'S':
					if j+1 < len(chars) {
						j = len(chars)
					}
				}
			}
			continue
		}
		c.paths = append(c.paths, a)
	}
	return c
}

func commitOptTakesArg(name string) bool {
	switch name {
	case "--message", "--file", "--author", "--date", "--cleanup", "--template",
		"--reedit-message", "--reuse-message", "--fixup", "--squash", "--trailer",
		"--untracked-files", "--pathspec-from-file":
		return true
	default:
		return false
	}
}

func addPassthrough(rest []string) bool {
	for _, a := range rest {
		switch a {
		case "--help", "-h", "--interactive", "--patch", "-p", "-i", "--pathspec-from-file":
			return true
		}
		if strings.HasPrefix(a, "--pathspec-from-file=") {
			return true
		}
	}
	return false
}

func withConfigExclude(globals []string, sub string, rest []string) []string {
	out := append([]string{}, globals...)
	out = append(out, sub)
	dash := false
	for _, a := range rest {
		if a == "--" {
			dash = true
		}
		out = append(out, a)
	}
	if !dash {
		out = append(out, "--")
	}
	out = append(out, LocalConfigExclude)
	return out
}
