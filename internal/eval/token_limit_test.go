package eval

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTaskTokenLimitForwarded(t *testing.T) {
	repo, _ := gitRepo(t)
	body := "version: 1\nmodels: [openai/gpt-4o]\ntrials: 1\ntasks:\n" +
		"  - {id: capped, repo: " + repo + ", base: HEAD, task: status=Submitted, limits: {token_limit: 1234}, checks: [{argv: [git, --version]}]}\n" +
		"  - {id: open, repo: " + repo + ", base: HEAD, task: status=Submitted, checks: [{argv: [git, --version]}]}\n"
	s, err := Load(writeSuite(t, body), nil)
	if err != nil {
		t.Fatal(err)
	}
	fa := &fakeAgent{}
	Run(context.Background(), Options{Suite: s, OutDir: filepath.Join(t.TempDir(), "out"), MinPassRate: -1, Stderr: io.Discard, RunAgent: fa.run})
	if len(fa.calls) != 2 {
		t.Fatalf("%d calls", len(fa.calls))
	}
	if got := argAfter(fa.calls[0], "--token-limit"); got != "1234" {
		t.Errorf("capped task args %v", fa.calls[0])
	}
	if slices.Contains(fa.calls[1], "--token-limit") {
		t.Errorf("uncapped task got a token limit: %v", fa.calls[1])
	}
}

func TestTaskTokenLimitNegativeRejected(t *testing.T) {
	repo, _ := gitRepo(t)
	body := "version: 1\nmodels: [openai/gpt-4o]\ntrials: 1\ntasks:\n  - {id: bad, repo: " + repo + ", base: HEAD, task: x, limits: {token_limit: -1}, checks: [{argv: [git, --version]}]}\n"
	if _, err := Load(writeSuite(t, body), nil); err == nil || !strings.Contains(err.Error(), "limits must be >= 0") {
		t.Fatalf("err = %v", err)
	}
}
