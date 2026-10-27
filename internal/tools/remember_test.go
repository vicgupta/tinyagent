package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/memory"
	"github.com/vicgupta/tinyagent/internal/model"
)

func rememberExecutor(t *testing.T, approval string, limits memory.Limits, snapshot ...memory.Entry) (*Executor, string) {
	t.Helper()
	store := filepath.Join(t.TempDir(), "mem", "p.json")
	w := memory.NewWriter(store, approval, limits, nil, memory.Source{Model: "fake/m"}, snapshot)
	return &Executor{Root: t.TempDir(), Remember: w}, store
}

func callRemember(e *Executor, args string) Result {
	return e.RunResult(context.Background(), model.ToolCall{ID: "r", Type: "function", Function: model.FunctionCall{Name: RememberToolName, Arguments: args}})
}

func loadStore(t *testing.T, path string) []memory.Entry {
	t.Helper()
	es, err := memory.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestRememberCatalog(t *testing.T) {
	e, _ := rememberExecutor(t, memory.ApprovalAuto, memory.DefaultLimits())
	if !specNames(e.Catalog())[RememberToolName] || !e.Permits(RememberToolName) {
		t.Fatal("remember missing from the catalog with memory on")
	}
	if specNames((&Executor{Root: t.TempDir()}).Catalog())[RememberToolName] {
		t.Fatal("remember offered with memory off")
	}
	e.ReadOnly = true
	if specNames(e.Catalog())[RememberToolName] || e.Permits(RememberToolName) {
		t.Fatal("remember offered in read-only mode")
	}
	r := callRemember(e, `{"op":"add","text":"x"}`)
	if !r.IsError || !strings.Contains(r.Text, "read-only") {
		t.Fatalf("forged read-only remember call: %+v", r)
	}
}

func TestRememberAddReplaceForget(t *testing.T) {
	e, store := rememberExecutor(t, memory.ApprovalAuto, memory.DefaultLimits())
	r := callRemember(e, `{"op":"add","text":"integration tests need make db-up"}`)
	if r.IsError || !strings.Contains(r.Text, "status: active") || !strings.Contains(r.Text, "future runs") {
		t.Fatalf("add: %+v", r)
	}
	es := loadStore(t, store)
	if len(es) != 1 || es[0].Text != "integration tests need make db-up" || es[0].Status != memory.StatusActive || es[0].Source.Model != "fake/m" {
		t.Fatalf("store after add: %+v", es)
	}
	id := es[0].ID
	if !strings.Contains(r.Text, id) {
		t.Fatalf("add result lacks id %s: %s", id, r.Text)
	}
	r = callRemember(e, `{"op":"replace","id":"`+id+`","text":"run make db-up first"}`)
	if r.IsError || !strings.Contains(r.Text, "replaced memory entry "+id) {
		t.Fatalf("replace: %+v", r)
	}
	if es = loadStore(t, store); len(es) != 1 || es[0].Text != "run make db-up first" {
		t.Fatalf("store after replace: %+v", es)
	}
	r = callRemember(e, `{"op":"forget","id":"`+id+`"}`)
	if r.IsError || !strings.Contains(r.Text, "forgot") {
		t.Fatalf("forget: %+v", r)
	}
	if es = loadStore(t, store); len(es) != 0 {
		t.Fatalf("store after forget: %+v", es)
	}
}

func TestRememberPendingPolicy(t *testing.T) {
	e, store := rememberExecutor(t, memory.ApprovalPending, memory.DefaultLimits())
	r := callRemember(e, `{"op":"add","text":"note"}`)
	if r.IsError || !strings.Contains(r.Text, "status: pending") || !strings.Contains(r.Text, "approves") {
		t.Fatalf("add: %+v", r)
	}
	if es := loadStore(t, store); len(es) != 1 || es[0].Status != memory.StatusPending {
		t.Fatalf("store: %+v", es)
	}
}

func TestRememberUnknownIDAndBadArgs(t *testing.T) {
	e, store := rememberExecutor(t, memory.ApprovalAuto, memory.DefaultLimits())
	for _, args := range []string{
		`{"op":"replace","id":"mdeadbeef","text":"x"}`,
		`{"op":"forget","id":"mdeadbeef"}`,
		`{"op":"replace","text":"x"}`,
		`{"op":"forget"}`,
		`{"op":"add","id":"m1","text":"x"}`,
		`{"op":"add","text":"   "}`,
		`{"op":"edit","text":"x"}`,
		`not json`,
	} {
		if r := callRemember(e, args); !r.IsError {
			t.Errorf("%s: want error, got %+v", args, r)
		}
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("store written on failed calls: %v", err)
	}
}

func TestRememberLimits(t *testing.T) {
	e, store := rememberExecutor(t, memory.ApprovalAuto, memory.Limits{MaxWritesPerRun: 2, MaxEntries: 50})
	for i := 0; i < 2; i++ {
		if r := callRemember(e, `{"op":"add","text":"note"}`); r.IsError {
			t.Fatalf("add %d: %+v", i, r)
		}
	}
	r := callRemember(e, `{"op":"add","text":"third"}`)
	if !r.IsError || !strings.Contains(r.Text, "max_writes_per_run") {
		t.Fatalf("third write: %+v", r)
	}
	if es := loadStore(t, store); len(es) != 2 {
		t.Fatalf("store changed by refused write: %d entries", len(es))
	}

	e, _ = rememberExecutor(t, memory.ApprovalAuto, memory.Limits{MaxWritesPerRun: 5, MaxEntries: 1})
	callRemember(e, `{"op":"add","text":"one"}`)
	if r := callRemember(e, `{"op":"add","text":"two"}`); !r.IsError || !strings.Contains(r.Text, "max_entries") {
		t.Fatalf("over max_entries: %+v", r)
	}

	e, _ = rememberExecutor(t, memory.ApprovalAuto, memory.DefaultLimits())
	if r := callRemember(e, `{"op":"add","text":"`+strings.Repeat("a", memory.MaxTextLen+1)+`"}`); !r.IsError || !strings.Contains(r.Text, "limit") {
		t.Fatalf("over-long text: %+v", r)
	}
}

func TestRememberRedacts(t *testing.T) {
	store := filepath.Join(t.TempDir(), "p.json")
	red := eventlog.NewValueRedactor([]string{"s3cr3tvalue"}, false)
	w := memory.NewWriter(store, memory.ApprovalAuto, memory.DefaultLimits(), red, memory.Source{}, nil)
	e := &Executor{Root: t.TempDir(), Remember: w}
	tok := "ghp_" + strings.Repeat("A", 36)
	r := callRemember(e, `{"op":"add","text":"token s3cr3tvalue and `+tok+`"}`)
	if r.IsError || !strings.Contains(r.Text, "[REDACTED]") {
		t.Fatalf("add: %+v", r)
	}
	raw, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cr3tvalue") || strings.Contains(string(raw), tok) {
		t.Fatalf("store holds a secret: %s", raw)
	}
}

func TestRememberMarksUntrustedAfterExternalTool(t *testing.T) {
	e, store := rememberExecutor(t, memory.ApprovalAuto, memory.DefaultLimits())
	e.ExternalSpecs = []model.ToolSpec{spec("mcp__docs__search", "search", props{"q": "query"}, "q")}
	e.ExternalCall = func(ctx context.Context, name, args string) (string, bool, bool) {
		if name == "mcp__docs__search" {
			return "third-party text", false, true
		}
		return "", false, false
	}
	if r := callRemember(e, `{"op":"add","text":"before"}`); r.IsError || !strings.Contains(r.Text, "status: active") {
		t.Fatalf("trusted add: %+v", r)
	}
	if r := e.RunResult(context.Background(), model.ToolCall{ID: "m", Function: model.FunctionCall{Name: "mcp__docs__search", Arguments: `{"q":"x"}`}}); r.IsError {
		t.Fatalf("mcp call: %+v", r)
	}
	if r := callRemember(e, `{"op":"add","text":"after"}`); r.IsError || !strings.Contains(r.Text, "status: pending") {
		t.Fatalf("untrusted add: %+v", r)
	}
	es := loadStore(t, store)
	if len(es) != 2 || es[0].Source.UntrustedContext || !es[1].Source.UntrustedContext || es[1].Status != memory.StatusPending {
		t.Fatalf("store: %+v", es)
	}
}

func TestRememberMarksUntrustedOnWebFetch(t *testing.T) {
	e, store := rememberExecutor(t, memory.ApprovalAuto, memory.DefaultLimits())
	f, err := NewWebFetcher([]string{"example.invalid"}, 0, 0, "test")
	if err != nil {
		t.Fatal(err)
	}
	e.WebFetch = f
	// The fetch is refused (not on the allowlist), but untrusted marking
	// happens before the call, whatever its outcome.
	e.RunResult(context.Background(), model.ToolCall{ID: "w", Function: model.FunctionCall{Name: WebFetchToolName, Arguments: `{"url":"ftp://x"}`}})
	if r := callRemember(e, `{"op":"add","text":"after fetch"}`); r.IsError || !strings.Contains(r.Text, "status: pending") {
		t.Fatalf("add after web_fetch: %+v", r)
	}
	if es := loadStore(t, store); len(es) != 1 || !es[0].Source.UntrustedContext {
		t.Fatalf("store: %+v", es)
	}
}

func TestRememberNoStorePath(t *testing.T) {
	w := memory.NewWriter("", memory.ApprovalAuto, memory.DefaultLimits(), nil, memory.Source{}, nil)
	e := &Executor{Root: t.TempDir(), Remember: w}
	if r := callRemember(e, `{"op":"add","text":"x"}`); !r.IsError || !strings.Contains(r.Text, "unavailable") {
		t.Fatalf("no store path: %+v", r)
	}
}

func TestRememberCorruptStore(t *testing.T) {
	e, store := rememberExecutor(t, memory.ApprovalAuto, memory.DefaultLimits())
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := callRemember(e, `{"op":"add","text":"x"}`); !r.IsError {
		t.Fatalf("corrupt store: %+v", r)
	}
	raw, _ := os.ReadFile(store)
	if string(raw) != "{not json" {
		t.Fatalf("corrupt store overwritten: %q", raw)
	}
}
