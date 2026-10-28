package attempts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func readEvents(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var evs []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not a JSON object: %q: %v", sc.Text(), err)
		}
		evs = append(evs, m)
	}
	return evs
}

func TestTagLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"type":"step","n":1}`, `{"attempt":2,"n":1,"type":"step"}`},
		{`{"type":"x","attempt":9}`, `{"attempt":2,"type":"x"}`},
		{`plain text`, `plain text`},
		{`[1,2]`, `[1,2]`},
		{`{broken`, `{broken`},
		{``, ``},
	} {
		if got := string(tagLine([]byte(c.in), 2)); got != c.want {
			t.Errorf("tagLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTagWriterSplitsLines(t *testing.T) {
	var out bytes.Buffer
	w := &tagWriter{out: &out, attempt: 3}
	io.WriteString(w, `{"type":"a"}`+"\n"+`{"ty`)
	io.WriteString(w, `pe":"b"}`+"\n"+`{"type":"c"}`)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := `{"attempt":3,"type":"a"}` + "\n" + `{"attempt":3,"type":"b"}` + "\n" + `{"attempt":3,"type":"c"}` + "\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

// jsonAgent wraps fakeAgent so each child run prints JSONL events.
func jsonAgent(f *fakeAgent) func(context.Context, []string, io.Writer, io.Writer) error {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		n := len(f.calls) + 1
		fmt.Fprintf(stdout, `{"type":"run.started","model":"m%d"}`+"\n", n)
		err := f.run(ctx, args, io.Discard, stderr)
		fmt.Fprintf(stdout, `{"type":"result","submission":"submission %d"}`+"\n", n)
		return err
	}
}

func TestJSONEvents(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted", "Submitted"}, cost: 0.01}
	out := filepath.Join(t.TempDir(), "run.json")
	var stderr, stdout, events bytes.Buffer
	rep, code := Run(context.Background(), Options{
		Models: []string{"m1", "m2", "m3"}, BaseName: "base",
		Checks: []Check{{Command: "echo token=s3cr3t-value; test $(cat n) -ge 2", TimeoutSeconds: 10}},
		Output: out, Stderr: &stderr, Stdout: &stdout, Events: &events, RunAgent: jsonAgent(f),
		Redact: func(s string) string { return strings.ReplaceAll(s, "s3cr3t-value", "[REDACTED]") },
	})
	if code != 0 || rep.Winner == nil || *rep.Winner != 2 {
		t.Fatalf("code=%d rep=%+v stderr=%s", code, rep, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q; with Events the winner's output is not replayed", stdout.String())
	}
	if strings.Contains(events.String(), "s3cr3t-value") {
		t.Fatalf("secret leaked into events:\n%s", events.String())
	}
	evs := readEvents(t, events.Bytes())
	var types []string
	for _, e := range evs {
		ty := e["type"].(string)
		if _, ok := e["attempt"]; !ok && ty != EventAttemptsResult {
			t.Fatalf("event without attempt: %v", e)
		}
		types = append(types, fmt.Sprintf("%s@%v", ty, e["attempt"]))
	}
	want := []string{
		"attempt.started@1", "run.started@1", "result@1", "attempt.check@1", "attempt.result@1",
		"attempt.started@2", "run.started@2", "result@2", "attempt.check@2", "attempt.result@2",
		"attempts.result@<nil>",
	}
	if strings.Join(types, " ") != strings.Join(want, " ") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(types, " "), strings.Join(want, " "))
	}
	if evs[2]["submission"] != "submission 1" {
		t.Fatalf("child event changed: %v", evs[2])
	}
	chk := evs[3]
	if chk["exit_code"].(float64) != 1 || chk["skipped"] != false || !strings.Contains(chk["output"].(string), "[REDACTED]") {
		t.Fatalf("attempt.check: %v", chk)
	}
	res := evs[9]
	if res["passed"] != true || res["status"] != "Submitted" || res["branch"] != "tinyagent/base-a2" {
		t.Fatalf("attempt.result: %v", res)
	}
	final := evs[10]
	if final["winner"].(float64) != 2 || final["stop_reason"] != rep.StopReason || final["winner_branch"] != rep.WinnerBranch ||
		final["attempts_run"].(float64) != 2 || final["schema_version"].(float64) != SchemaVersion {
		t.Fatalf("attempts.result: %v", final)
	}
	if p, _ := final["report"].(string); !strings.HasSuffix(p, "run.attempts.json") {
		t.Fatalf("attempts.result report = %v", final["report"])
	}
}

func TestJSONEventsNoWinner(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"StepLimitExceeded"}}
	var stderr, events bytes.Buffer
	_, code := Run(context.Background(), Options{
		Models: []string{"a"}, BaseName: "x", Checks: []Check{{Command: "true"}},
		Stderr: &stderr, Events: &events, RunAgent: jsonAgent(f),
	})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	evs := readEvents(t, events.Bytes())
	chk, final := evs[3], evs[len(evs)-1]
	if chk["type"] != EventAttemptCheck || chk["skipped"] != true {
		t.Fatalf("skipped check event: %v", chk)
	}
	if final["type"] != EventAttemptsResult || final["winner"] != nil || final["stop_reason"] != StopExhausted {
		t.Fatalf("attempts.result: %v", final)
	}
	if _, ok := final["report"]; ok {
		t.Fatalf("no -o, but report set: %v", final)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestJSONEventsWriteError(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted"}}
	var stderr bytes.Buffer
	_, code := Run(context.Background(), Options{
		Models: []string{"a"}, BaseName: "x", Checks: []Check{{Command: "true"}},
		Stderr: &stderr, Events: failWriter{}, RunAgent: jsonAgent(f),
	})
	if code != 1 || !strings.Contains(stderr.String(), "write --json events") {
		t.Fatalf("code %d stderr %s", code, stderr.String())
	}
}
