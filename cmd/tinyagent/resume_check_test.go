package main

import (
	"errors"
	"testing"

	"github.com/vicgupta/tinyagent/internal/resume"
)

func TestRenderResumeCheckExitCodes(t *testing.T) {
	if code := renderResumeCheck(nil); code != 0 {
		t.Fatalf("nil: exit %d, want 0", code)
	}
	if code := renderResumeCheck(&resume.Incompatibility{Flag: "--lsp", Recorded: "recorded lsp setting", Remedy: resume.Remedy, Usage: true}); code != 2 {
		t.Fatalf("usage conflict: exit %d, want 2", code)
	}
	if code := renderResumeCheck(&resume.Incompatibility{Flag: "--images", Recorded: "recorded images setting", Remedy: resume.Remedy}); code != 1 {
		t.Fatalf("conflict: exit %d, want 1", code)
	}
	if code := renderResumeCheck(errors.New("other")); code != 1 {
		t.Fatalf("other error: exit %d, want 1", code)
	}
}
