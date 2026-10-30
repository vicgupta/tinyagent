package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/memory"
	"github.com/vicgupta/tinyagent/internal/model"
)

func memoryToolsExecutor(t *testing.T) (*Executor, string) {
	t.Helper()
	store := filepath.Join(t.TempDir(), "memory")
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := memory.Update(store, func([]memory.Entry) ([]memory.Entry, error) {
		return []memory.Entry{
			{ID: "maaaa", Text: "run make test-fast before pushing", Status: memory.StatusActive, CreatedAt: at, UpdatedAt: at, Tags: []string{"build"}},
			{ID: "mbbbb", Text: "deploys go through make release", Status: memory.StatusActive, CreatedAt: at.Add(time.Hour), UpdatedAt: at.Add(time.Hour), Tags: []string{"ops"}},
			{ID: "mcccc", Text: "make pending-secret", Status: memory.StatusPending, CreatedAt: at.Add(2 * time.Hour), UpdatedAt: at.Add(2 * time.Hour)},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	w := memory.NewWriter(store, memory.ApprovalPending, memory.DefaultLimits(), nil, memory.Source{}, nil)
	return &Executor{Root: t.TempDir(), Remember: w, MemoryTools: true}, store
}

func callMemTool(e *Executor, name, args string) Result {
	return e.RunResult(context.Background(), model.ToolCall{ID: "c", Type: "function", Function: model.FunctionCall{Name: name, Arguments: args}})
}

func TestMemoryToolsCatalogAndPolicy(t *testing.T) {
	e, _ := memoryToolsExecutor(t)
	names := specNames(e.Catalog())
	if !names[MemorySearchToolName] || !names[MemoryListToolName] || !e.Permits(MemoryListToolName) {
		t.Fatal("memory tools missing with memory on")
	}
	e.MemoryTools = false
	if specNames(e.Catalog())[MemorySearchToolName] || !callMemTool(e, MemorySearchToolName, `{"query":"make"}`).IsError {
		t.Fatal("memory tools available without MemoryTools")
	}
	e.MemoryTools = true
	e.ReadOnly = true
	if specNames(e.Catalog())[MemoryListToolName] || !callMemTool(e, MemoryListToolName, `{}`).IsError {
		t.Fatal("memory tools available in read-only mode")
	}
	if specNames((&Executor{Root: t.TempDir(), MemoryTools: true}).Catalog())[MemoryListToolName] {
		t.Fatal("memory tools offered without a memory writer")
	}
}

func TestMemorySearchAndList(t *testing.T) {
	e, store := memoryToolsExecutor(t)
	r := callMemTool(e, MemorySearchToolName, `{"query":"make"}`)
	if r.IsError || !strings.Contains(r.Text, "mbbbb") || !strings.Contains(r.Text, "maaaa") || strings.Contains(r.Text, "pending-secret") {
		t.Fatalf("search: %+v", r)
	}
	if strings.Index(r.Text, "mbbbb") > strings.Index(r.Text, "maaaa") {
		t.Errorf("not newest first: %s", r.Text)
	}
	r = callMemTool(e, MemorySearchToolName, `{"tag":"build"}`)
	if r.IsError || !strings.Contains(r.Text, "maaaa") || strings.Contains(r.Text, "mbbbb") || !strings.Contains(r.Text, "tags: build") {
		t.Fatalf("tag search: %+v", r)
	}
	if r := callMemTool(e, MemorySearchToolName, `{"query":"nothing-like-this"}`); r.IsError || !strings.Contains(r.Text, "no matching") {
		t.Fatalf("empty search: %+v", r)
	}
	if r := callMemTool(e, MemorySearchToolName, `{}`); !r.IsError {
		t.Fatal("search without query or tag succeeded")
	}
	r = callMemTool(e, MemoryListToolName, `{"limit":1}`)
	if r.IsError || !strings.Contains(r.Text, "mbbbb") || strings.Contains(r.Text, "maaaa") {
		t.Fatalf("list: %+v", r)
	}
	if es := loadStore(t, store); len(es) != 3 {
		t.Fatalf("read-only tools changed the store: %+v", es)
	}
}

func TestRememberTagsAndEntrySize(t *testing.T) {
	store := filepath.Join(t.TempDir(), "memory")
	lim := memory.DefaultLimits()
	lim.MaxTextLen = 20
	w := memory.NewWriter(store, memory.ApprovalAuto, lim, nil, memory.Source{}, nil)
	w.Tags = []string{"proj"}
	e := &Executor{Root: t.TempDir(), Remember: w, MemoryTools: true}
	for _, s := range e.Catalog() {
		if s.Function.Name == RememberToolName && !strings.Contains(s.Function.Description, "at most 20 characters") {
			t.Errorf("description ignores max_entry_size: %s", s.Function.Description)
		}
	}
	if r := callRemember(e, `{"op":"add","text":"this note is far too long for the limit"}`); !r.IsError {
		t.Fatalf("long note saved: %+v", r)
	}
	if r := callRemember(e, `{"op":"add","text":"short note","tags":["Build"]}`); r.IsError {
		t.Fatalf("add: %+v", r)
	}
	es := loadStore(t, store)
	if len(es) != 1 || strings.Join(es[0].Tags, ",") != "proj,build" {
		t.Fatalf("store %+v", es)
	}
	if r := callRemember(e, `{"op":"forget","id":"`+es[0].ID+`","tags":["x"]}`); !r.IsError {
		t.Fatal("forget with tags accepted")
	}
}
