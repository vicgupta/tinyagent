package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func workerSummaryTrajectory(top string) *trajectory.Trajectory {
	wt := func(n string) string { return filepath.Join(top, ".tinyagent", "worktrees", n) }
	return &trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, Steps: 3, Subagents: []trajectory.Subagent{
		{ID: "d1", Status: agent.StatusSubmitted},
		{ID: "w1", Kind: "worker", Label: "rename", Status: agent.StatusSubmitted, Branch: "tinyagent/run-w1", Worktree: wt("run-w1"),
			BaseCommit: "base", Commit: "1111111aaaaaaa", Applied: true},
		{ID: "w2", Kind: "worker", Status: agent.StatusCostLimit, Branch: "tinyagent/run-w2", Worktree: wt("run-w2"),
			BaseCommit: "base", Commit: "2222222bbbbbbb"},
		{ID: "w3", Kind: "worker", Status: agent.StatusSubmitted, BaseCommit: "base"}, // no changes: removed
	}}
}

func TestReportWorkersListsKeptBranchesWithCleanupCommands(t *testing.T) {
	top := filepath.Join(string(filepath.Separator)+"src", "my repo")
	var b bytes.Buffer
	reportWorkers(&b, workerSummaryTrajectory(top))
	got := b.String()
	repo := shellQuote(top)
	for _, want := range []string{
		"workers: 2 branches kept, 1 not applied\n",
		"  w2: " + agent.StatusCostLimit + ", branch tinyagent/run-w2, commit 2222222, not applied\n",
		"    review:   git -C " + repo + " show --stat 2222222bbbbbbb\n",
		"    discard:  git -C " + repo + " worktree remove --force " + shellQuote(filepath.Join(top, ".tinyagent", "worktrees", "run-w2")) + " && git -C " + repo + " branch -D tinyagent/run-w2\n",
		"  w1 (rename): Submitted, branch tinyagent/run-w1, commit 1111111, applied\n",
		"    clean up: git -C " + repo + " worktree remove --force",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "w2:") > strings.Index(got, "w1 (rename)") {
		t.Errorf("unapplied workers should be listed first:\n%s", got)
	}
	if strings.Contains(got, "w3") || strings.Contains(got, "d1") {
		t.Errorf("workers without changes and delegate children must not be listed:\n%s", got)
	}
}

func TestReportWorkersSilentWithoutKeptBranches(t *testing.T) {
	var b bytes.Buffer
	reportWorkers(&b, &trajectory.Trajectory{Subagents: []trajectory.Subagent{{ID: "w1", Kind: "worker", Status: agent.StatusSubmitted}}})
	reportWorkers(&b, &trajectory.Trajectory{})
	if b.Len() != 0 {
		t.Errorf("expected no output, got %q", b.String())
	}
}

func TestReportSummaryIncludesWorkerBranches(t *testing.T) {
	var b bytes.Buffer
	reportSummary(&b, "openai/gpt-4o", workerSummaryTrajectory(filepath.Join(string(filepath.Separator)+"r")), time.Second, "")
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if !strings.HasPrefix(lines[0], "Submitted after 3 steps") || len(lines) < 2 || !strings.HasPrefix(lines[1], "workers: 2 branches kept") {
		t.Errorf("unexpected summary:\n%s", b.String())
	}
}

func TestWorkerRepoNeedsWorktreeLayout(t *testing.T) {
	top := filepath.Join(string(filepath.Separator)+"a", "b")
	if got := workerRepo(filepath.Join(top, ".tinyagent", "worktrees", "x")); got != top {
		t.Errorf("workerRepo = %q, want %q", got, top)
	}
	if got := workerRepo(filepath.Join(top, "elsewhere", "x")); got != "" {
		t.Errorf("workerRepo for a foreign path = %q, want empty", got)
	}
	s := trajectory.Subagent{Branch: "tinyagent/r-w1", Worktree: filepath.Join(top, "elsewhere", "x")}
	if got := workerCleanup(s); strings.Contains(got, " -C ") || !strings.HasSuffix(got, "git branch -D tinyagent/r-w1") {
		t.Errorf("cleanup without a known repo = %q", got)
	}
}

func TestResultFieldsListWorkerBranches(t *testing.T) {
	f := resultFields(workerSummaryTrajectory(filepath.Join(string(filepath.Separator)+"r")), time.Second)
	kept, ok := f["worker_branches"].([]map[string]any)
	if !ok || len(kept) != 2 {
		t.Fatalf("worker_branches = %#v", f["worker_branches"])
	}
	if kept[0]["id"] != "w1" || kept[0]["applied"] != true || kept[1]["id"] != "w2" || kept[1]["applied"] != false {
		t.Errorf("worker_branches = %#v", kept)
	}
	if c, _ := kept[1]["cleanup"].(string); !strings.Contains(c, "branch -D tinyagent/run-w2") {
		t.Errorf("cleanup = %q", c)
	}
	if _, ok := resultFields(&trajectory.Trajectory{ExitStatus: agent.StatusSubmitted}, time.Second)["worker_branches"]; ok {
		t.Error("worker_branches should be absent without workers")
	}
}
