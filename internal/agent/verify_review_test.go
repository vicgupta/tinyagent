package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

func TestVerifyReviewRunsLastAfterChecksPass(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
		{Role: "assistant", Content: "high: f.txt: greeting is lowercase."},
	}}
	a, kinds, fields := checkAgent(t, m, "true lint", "true test")
	a.Config.VerifyOnStop.Review = true
	var errBuf strings.Builder
	a.Err = &errBuf
	tr := a.Run(context.Background(), "write a greeting")
	if tr.ExitStatus != StatusCompleted || tr.Submission != "Done." || tr.Unverified || tr.VerifyReprompts != 0 {
		t.Fatalf("trajectory = %+v", tr)
	}
	if strings.Join(*kinds, ",") != "verify.check,verify.check,verify.review" {
		t.Fatalf("events = %v (review must come after every check)", *kinds)
	}
	if (*fields)[2]["findings"] != "high: f.txt: greeting is lowercase." || tr.VerifyReview != "high: f.txt: greeting is lowercase." {
		t.Errorf("findings not recorded: %v, %q", (*fields)[2], tr.VerifyReview)
	}
	if !strings.Contains(errBuf.String(), "greeting is lowercase") {
		t.Errorf("findings not on stderr: %q", errBuf.String())
	}
	req := m.seen[2]
	if len(req) != 2 || req[0].Role != "system" || !strings.Contains(req[1].Content, "write a greeting") || !strings.Contains(req[1].Content, `write_file {"path":"f.txt","content":"hi"}`) {
		t.Errorf("review request = %+v", req)
	}
	if verifyNotices(tr.Messages) != 0 {
		t.Error("the review re-prompted the model")
	}
	if tr.Cost < 0.029 {
		t.Errorf("review cost not counted: %v", tr.Cost)
	}
}

func TestVerifyReviewSkippedWhenCheckFails(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
		{Role: "assistant", Content: "Fixed."},
		{Role: "assistant", Content: "Still done."},
	}}
	a, kinds, _ := checkAgent(t, m, "false")
	a.Config.VerifyOnStop.Review = true
	tr := a.Run(context.Background(), "task")
	if !tr.Unverified || tr.VerifyReview != "" {
		t.Fatalf("trajectory = %+v", tr)
	}
	for _, k := range *kinds {
		if k == "verify.review" {
			t.Fatal("review ran although a check failed")
		}
	}
}

func TestVerifyReviewOffByDefault(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
	}}
	a, kinds, _ := checkAgent(t, m, "true")
	tr := a.Run(context.Background(), "task")
	if tr.VerifyReview != "" || strings.Join(*kinds, ",") != "verify.check" || len(m.seen) != 2 {
		t.Fatalf("review ran while off: %v, %d requests", *kinds, len(m.seen))
	}
}

func TestVerifyReviewFailureReportedOnly(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
		// No reply left for the review request: it fails.
	}}
	a, kinds, fields := checkAgent(t, m, "true")
	a.Config.VerifyOnStop.Review = true
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Submission != "Done." || tr.Unverified {
		t.Fatalf("a failed review changed the run: %+v", tr)
	}
	if strings.Join(*kinds, ",") != "verify.check,verify.review" || (*fields)[1]["error"] == nil {
		t.Errorf("events = %v %v", *kinds, *fields)
	}
}

func TestVerifyChangeRecordCapped(t *testing.T) {
	big := strings.Repeat("x", maxVerifyReviewChars+10)
	msgs := []model.Message{
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("a", "write_file", `{"content":"`+big+`"}`), call("b", "bash", `{"command":"ls"}`)}},
	}
	got := verifyChangeRecord(msgs)
	if !strings.HasSuffix(got, "[change record truncated]") || strings.Contains(got, "bash") {
		t.Errorf("record not capped or included bash: %d chars", len(got))
	}
	if verifyChangeRecord(msgs[:1]) != "" {
		t.Error("record for a run with no changes is not empty")
	}
}
