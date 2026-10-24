package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Exercise the real wire adapters with a saved, loaded multi-tool history,
// including IDs reused in a later turn. No credentials or live requests.
func TestResumeProviderWireHistory(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "codex"} {
		for _, streamed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", provider, streamed), func(t *testing.T) {
				a, tr := unfinishedRun(t, 2)
				p := filepath.Join(t.TempDir(), "checkpoint.json")
				if err := tr.Save(p); err != nil {
					t.Fatal(err)
				}
				loaded, err := trajectory.Load(p)
				if err != nil {
					t.Fatal(err)
				}
				requests := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					switch provider {
					case "openai":
						var msgs []model.Message
						if err := json.Unmarshal(body["messages"], &msgs); err != nil {
							t.Error(err)
							return
						}
						if len(msgs) != len(tr.Messages)+1 || !reflect.DeepEqual(msgs[:len(tr.Messages)], tr.Messages) || msgs[len(msgs)-1].Content != resumeNotice {
							t.Error("OpenAI history or continuation notice changed")
						}
						if streamed {
							io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						} else {
							io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
						}
					case "anthropic":
						var system string
						_ = json.Unmarshal(body["system"], &system)
						var wire []struct {
							Role    string
							Content []map[string]any
						}
						if err := json.Unmarshal(body["messages"], &wire); err != nil {
							t.Error(err)
							return
						}
						if system != tr.Messages[0].Content || len(wire) != 5 {
							t.Error("Anthropic system or turns changed")
							return
						}
						for _, i := range []int{1, 3} {
							if wire[i].Role != "assistant" || len(wire[i].Content) != 2 || wire[i].Content[0]["id"] != "same" || wire[i].Content[1]["id"] != "read" {
								t.Error("Anthropic tool calls changed")
							}
							results := wire[i+1].Content
							if wire[i+1].Role != "user" || len(results) < 2 || results[0]["tool_use_id"] != "same" || results[0]["content"] != "ok" || results[1]["tool_use_id"] != "read" || !strings.Contains(fmt.Sprint(results[1]["content"]), "historical") {
								t.Error("Anthropic results changed")
							}
						}
						last := wire[4].Content
						if len(last) != 3 || last[2]["text"] != resumeNotice {
							t.Error("Anthropic notice missing")
						}
						if streamed {
							io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"done\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
						} else {
							io.WriteString(w, `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`)
						}
					case "codex":
						var instructions string
						_ = json.Unmarshal(body["instructions"], &instructions)
						var input []map[string]any
						if err := json.Unmarshal(body["input"], &input); err != nil {
							t.Error(err)
							return
						}
						if instructions != tr.Messages[0].Content || len(input) != 10 {
							t.Error("Codex instructions or input changed")
							return
						}
						for _, i := range []int{1, 5} {
							if input[i]["type"] != "function_call" || input[i]["call_id"] != "same" || input[i+1]["call_id"] != "read" || input[i+2]["type"] != "function_call_output" || input[i+2]["call_id"] != "same" || input[i+2]["output"] != "ok" || input[i+3]["call_id"] != "read" {
								t.Error("Codex calls/results changed")
							}
						}
						content, _ := input[9]["content"].([]any)
						if len(content) != 1 || content[0].(map[string]any)["text"] != resumeNotice {
							t.Error("Codex notice missing")
						}
						io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
					}
				}))
				defer srv.Close()
				var onText func(string)
				if streamed {
					onText = func(string) {}
				}
				switch provider {
				case "openai":
					a.Model = &model.OpenAI{BaseURL: srv.URL, Model: "gpt-4o", OnText: onText}
				case "anthropic":
					a.Model = &model.Anthropic{BaseURL: srv.URL, Model: "claude-sonnet-4", OnText: onText}
				case "codex":
					auth := filepath.Join(t.TempDir(), "auth.json")
					if err := os.WriteFile(auth, []byte(`{"tokens":{"access_token":"fake","refresh_token":"fake","account_id":"fake"}}`), 0o600); err != nil {
						t.Fatal(err)
					}
					a.Model = &model.Codex{BaseURL: srv.URL, Model: "gpt-5", AuthFile: auth, OnText: onText}
				}
				a.Resume, a.Config.StepLimit = loaded, 3
				got := a.Run(context.Background(), "")
				if requests != 1 || got.ExitStatus != StatusCompleted {
					t.Fatalf("requests %d, status %s: %s", requests, got.ExitStatus, got.Submission)
				}
			})
		}
	}
}
