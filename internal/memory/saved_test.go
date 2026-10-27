package memory

import (
	"path/filepath"
	"testing"
)

func TestWriterTakeSaved(t *testing.T) {
	w := NewWriter(filepath.Join(t.TempDir(), "memory.json"), ApprovalAuto, Limits{MaxWritesPerRun: 5, MaxEntries: 5}, nil, Source{}, nil)
	if got := w.TakeSaved(); len(got) != 0 {
		t.Fatalf("fresh writer saved %v", got)
	}
	a, err := w.Add("use make test")
	if err != nil {
		t.Fatal(err)
	}
	w.MarkUntrusted()
	if _, err := w.Forget(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Forget("missing"); err == nil {
		t.Fatal("forgetting an unknown id succeeded")
	}
	got := w.TakeSaved()
	if len(got) != 2 {
		t.Fatalf("TakeSaved = %+v, want 2 writes", got)
	}
	if got[0].Op != "add" || got[0].ID != a.ID || got[0].UntrustedContext {
		t.Errorf("first = %+v", got[0])
	}
	f := got[1].Fields()
	if f["op"] != "forget" || f["id"] != a.ID || f["status"] != "forgotten" || f["untrusted_context"] != true {
		t.Errorf("second fields = %v", f)
	}
	if _, ok := f["text"]; ok {
		t.Error("fields include entry text")
	}
	if again := w.TakeSaved(); len(again) != 0 {
		t.Errorf("second TakeSaved = %v, want empty", again)
	}
}
