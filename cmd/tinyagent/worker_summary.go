package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/vicgupta/tinyagent/internal/gitwt"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// keptWorkers returns the worker runs whose worktree and branch are still
// on disk: every worker that committed changes, applied or not. Workers
// that changed nothing already had their worktree and branch removed.
func keptWorkers(t *trajectory.Trajectory) []trajectory.Subagent {
	var out []trajectory.Subagent
	for _, s := range t.Subagents {
		if s.Kind == "worker" && s.Branch != "" && s.Commit != "" {
			out = append(out, s)
		}
	}
	return out
}

// workerRepo returns the repository root a worker's worktree was created
// under (<top>/.tinyagent/worktrees/<name>), or "" when the path doesn't
// have that shape.
func workerRepo(worktree string) string {
	if worktree == "" {
		return ""
	}
	parent := filepath.Dir(filepath.Clean(worktree))
	suffix := string(filepath.Separator) + filepath.FromSlash(gitwt.Dir)
	if !strings.HasSuffix(parent, suffix) {
		return ""
	}
	return strings.TrimSuffix(parent, suffix)
}

// workerGit renders a git command for the worker's repository.
func workerGit(repo string, args ...string) string {
	parts := []string{"git"}
	if repo != "" {
		parts = append(parts, "-C", shellQuote(repo))
	}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// workerCleanup is the ready-to-paste command that removes a worker's
// worktree and deletes its branch.
func workerCleanup(s trajectory.Subagent) string {
	repo := workerRepo(s.Worktree)
	var cmds []string
	if s.Worktree != "" {
		cmds = append(cmds, workerGit(repo, "worktree", "remove", "--force", s.Worktree))
	}
	cmds = append(cmds, workerGit(repo, "branch", "-D", s.Branch))
	return strings.Join(cmds, " && ")
}

// reportWorkers prints, after the run summary, every worker branch the run
// left behind: unapplied ones first with review and discard commands, then
// applied ones with their cleanup command. It prints nothing when no
// worker branch is left. tinyagent never removes these on its own.
func reportWorkers(w io.Writer, t *trajectory.Trajectory) {
	kept := keptWorkers(t)
	if len(kept) == 0 {
		return
	}
	unapplied := 0
	for _, s := range kept {
		if !s.Applied {
			unapplied++
		}
	}
	noun := "branches"
	if len(kept) == 1 {
		noun = "branch"
	}
	fmt.Fprintf(w, "workers: %d %s kept, %d not applied\n", len(kept), noun, unapplied)
	for _, applied := range []bool{false, true} {
		for _, s := range kept {
			if s.Applied != applied {
				continue
			}
			label := ""
			if s.Label != "" {
				label = " (" + s.Label + ")"
			}
			state := "not applied"
			if applied {
				state = "applied"
			}
			fmt.Fprintf(w, "  %s%s: %s, branch %s, commit %s, %s\n", s.ID, label, s.Status, s.Branch, shortCommit(s.Commit), state)
			if !applied {
				fmt.Fprintln(w, "    review:   "+workerGit(workerRepo(s.Worktree), "show", "--stat", s.Commit))
				fmt.Fprintln(w, "    discard:  "+workerCleanup(s))
			} else {
				fmt.Fprintln(w, "    clean up: "+workerCleanup(s))
			}
		}
	}
}

// workerResultFields lists the kept worker branches for the --json
// result event; nil when there are none.
func workerResultFields(t *trajectory.Trajectory) []map[string]any {
	var out []map[string]any
	for _, s := range keptWorkers(t) {
		f := map[string]any{"id": s.ID, "branch": s.Branch, "commit": s.Commit, "applied": s.Applied, "status": s.Status, "cleanup": workerCleanup(s)}
		if s.Worktree != "" {
			f["worktree"] = s.Worktree
		}
		out = append(out, f)
	}
	return out
}
