package trajectory

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

const submitMarker = "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"

// finishedCheckpoint is checkpoint() ending in a final assistant turn.
func finishedCheckpoint(status string) *Trajectory {
	c := checkpoint()
	c.ExitStatus = status
	final := model.Message{Role: "assistant", Content: "all done"}
	if status == "Submitted" {
		final.Content += "\n" + submitMarker
	}
	c.Messages = append(c.Messages, final)
	c.Steps = 3
	return c
}

// withFollowUp appends a follow-up boundary and one new tool turn, leaving an
// unfinished (step-limited) continuation of c.
func withFollowUp(c *Trajectory) *Trajectory {
	prev := c.ExitStatus
	c.Resume = &ResumeInfo{Source: "first.json", ExitStatus: prev, Steps: c.Steps, Message: len(c.Messages), FollowUp: true, Previous: c.Resume}
	c.Messages = append(c.Messages,
		model.Message{Role: "user", Content: FollowUpNotice + "\n\nfix it"},
		model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "c", Type: "function", Function: model.FunctionCall{Name: "list_files", Arguments: `{}`}}}},
		model.Message{Role: "tool", ToolCallID: "c", Content: "listing"},
	)
	c.Steps++
	c.ExitStatus = "StepLimitExceeded"
	return c
}

func TestFinishedCheckpointContract(t *testing.T) {
	cases := []struct {
		name string
		make func() *Trajectory
		want string
	}{
		{"submitted", func() *Trajectory { return finishedCheckpoint("Submitted") }, ""},
		{"completed", func() *Trajectory { return finishedCheckpoint("Completed") }, ""},
		{"submitted without marker", func() *Trajectory {
			c := finishedCheckpoint("Submitted")
			c.Messages[len(c.Messages)-1].Content = "done"
			return c
		}, "final assistant"},
		{"completed with marker", func() *Trajectory {
			c := finishedCheckpoint("Completed")
			c.Messages[len(c.Messages)-1].Content = submitMarker
			return c
		}, "final assistant"},
		{"ends with tool calls", func() *Trajectory {
			c := checkpoint()
			c.ExitStatus = "Submitted"
			c.Messages = c.Messages[:6]
			c.Messages[5].Content = submitMarker
			return c
		}, "final assistant"},
		{"final mid-history without boundary", func() *Trajectory {
			c := finishedCheckpoint("Completed")
			c.Messages = append(c.Messages, model.Message{Role: "user", Content: "smuggled"}, model.Message{Role: "assistant", Content: "again"})
			c.Steps++
			return c
		}, "history"},
		{"follow-up continuation", func() *Trajectory { return withFollowUp(finishedCheckpoint("Submitted")) }, ""},
		{"follow-up then finished", func() *Trajectory {
			c := withFollowUp(finishedCheckpoint("Completed"))
			c.Messages = append(c.Messages, model.Message{Role: "assistant", Content: "fixed"})
			c.Steps++
			c.ExitStatus = "Completed"
			return c
		}, ""},
		{"two-deep chain", func() *Trajectory {
			c := withFollowUp(finishedCheckpoint("Submitted"))
			c.Messages = append(c.Messages, model.Message{Role: "assistant", Content: "fixed\n" + submitMarker})
			c.Steps++
			c.ExitStatus = "Submitted"
			return withFollowUp(c)
		}, ""},
		{"follow-up wrong notice", func() *Trajectory {
			c := withFollowUp(finishedCheckpoint("Submitted"))
			c.Messages[c.Resume.Message].Content = ContinuationNotice
			return c
		}, "!"},
		{"follow-up from unfinished", func() *Trajectory {
			c := withFollowUp(finishedCheckpoint("Submitted"))
			c.Resume.ExitStatus = "StepLimitExceeded"
			return c
		}, "!"},
		{"follow-up after tool turn", func() *Trajectory {
			c := checkpoint()
			c.Resume = &ResumeInfo{Source: "x.json", ExitStatus: "Completed", Steps: 2, Message: len(c.Messages), FollowUp: true}
			c.Messages = append(c.Messages, model.Message{Role: "user", Content: FollowUpNotice + "\n\nmore"}, model.Message{Role: "assistant", Content: "ok"})
			c.Steps, c.ExitStatus = 3, "Completed"
			return c
		}, "history"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.make().Validate()
			switch {
			case tc.want == "!" && err == nil:
				t.Fatal("invalid follow-up accepted")
			case tc.want == "!":
			case tc.want == "" && err != nil, tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
