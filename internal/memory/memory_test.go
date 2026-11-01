package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/eventlog"
)

func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state", "proj.json")
}

func load(t *testing.T, path string) []Entry {
	t.Helper()
	e, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestLoadMissingIsEmpty(t *testing.T) {
	e, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || len(e) != 0 {
		t.Fatalf("got %v, %v", e, err)
	}
}

func TestAddAutoIsActiveAndFileIs0600(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{RunID: "r1", Model: "m", Version: "v"}, nil)
	res, err := w.Add("integration tests need `make db-up`")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusActive || res.ID == "" || res.Redacted {
		t.Fatalf("result %+v", res)
	}
	got := load(t, path)
	if len(got) != 1 || got[0].Text != "integration tests need `make db-up`" || got[0].Source.RunID != "r1" || got[0].Source.UntrustedContext {
		t.Fatalf("store %+v", got)
	}
	if got[0].CreatedAt.IsZero() || got[0].UpdatedAt.IsZero() {
		t.Fatal("timestamps not set")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock file left behind")
	}
}

func TestPendingPolicyAndApprove(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalPending, DefaultLimits(), nil, Source{}, nil)
	res, err := w.Add("note")
	if err != nil || res.Status != StatusPending {
		t.Fatalf("%+v %v", res, err)
	}
	if r := Render(load(t, path), 0); r.Section != "" {
		t.Fatalf("pending entry rendered: %q", r.Section)
	}
	n, err := Approve(path, res.ID, false)
	if err != nil || n != 1 {
		t.Fatalf("approve %d %v", n, err)
	}
	if r := Render(load(t, path), 0); !strings.Contains(r.Section, "note") || !strings.Contains(r.Section, res.ID) {
		t.Fatalf("approved entry not rendered: %q", r.Section)
	}
	if _, err := Approve(path, res.ID, false); err == nil {
		t.Fatal("approving an active entry should fail")
	}
	if _, err := Approve(path, "mnope", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestApproveAll(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalPending, DefaultLimits(), nil, Source{}, nil)
	for i := range 3 {
		if _, err := w.Add(fmt.Sprintf("n%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := Approve(path, "", true)
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	if n, err := Approve(path, "", true); err != nil || n != 0 {
		t.Fatalf("second approve all: %d %v", n, err)
	}
	if len(Active(load(t, path))) != 3 {
		t.Fatal("not all active")
	}
}

func TestUntrustedForcesPending(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	w.MarkUntrusted()
	res, err := w.Add("from a web page")
	if err != nil || res.Status != StatusPending {
		t.Fatalf("%+v %v", res, err)
	}
	got := load(t, path)
	if !got[0].Source.UntrustedContext {
		t.Fatal("untrusted_context not recorded")
	}
	// A source that starts untrusted (a review run) behaves the same.
	w2 := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{UntrustedContext: true}, nil)
	if res, _ := w2.Add("review"); res.Status != StatusPending {
		t.Fatalf("got %+v", res)
	}
}

func TestReplaceFromUntrustedRunKeepsActiveUntilApproved(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	orig, _ := w.Add("old text")
	snap := load(t, path)

	w2 := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, snap)
	w2.MarkUntrusted()
	res, err := w2.Replace(orig.ID, "new text")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusPending || res.ID == orig.ID {
		t.Fatalf("got %+v", res)
	}
	r := Render(load(t, path), 0)
	if !strings.Contains(r.Section, "old text") || strings.Contains(r.Section, "new text") {
		t.Fatalf("section %q", r.Section)
	}
	if _, err := Approve(path, res.ID, false); err != nil {
		t.Fatal(err)
	}
	got := load(t, path)
	if len(got) != 1 || got[0].Text != "new text" || got[0].Status != StatusActive || got[0].Replaces != "" {
		t.Fatalf("after approve %+v", got)
	}
}

func TestReplaceAndForgetOnlyKnownIDs(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	a, _ := w.Add("a")
	// Another run adds b; this run didn't load it.
	other := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	b, _ := other.Add("b")
	if _, err := w.Replace(b.ID, "x"); err == nil {
		t.Fatal("replace of unknown id succeeded")
	}
	if _, err := w.Forget(b.ID); err == nil {
		t.Fatal("forget of unknown id succeeded")
	}
	if res, err := w.Replace(a.ID, "a2"); err != nil || res.ID != a.ID || res.Status != StatusActive {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := w.Forget(a.ID); err != nil {
		t.Fatal(err)
	}
	got := load(t, path)
	if len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("store %+v", got)
	}
}

func TestWriteLimit(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalAuto, Limits{MaxWritesPerRun: 2}, nil, Source{}, nil)
	for range 2 {
		if _, err := w.Add("x"); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(path)
	_, err := w.Add("third")
	if err == nil || !strings.Contains(err.Error(), "max_writes_per_run") {
		t.Fatalf("got %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("store changed after limit error")
	}
}

func TestMaxEntries(t *testing.T) {
	path := storePath(t)
	for i := range DefaultMaxEntries {
		w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
		if _, err := w.Add(fmt.Sprintf("e%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	_, err := w.Add("one too many")
	if err == nil || !strings.Contains(err.Error(), "max_entries") {
		t.Fatalf("got %v", err)
	}
	if n := len(load(t, path)); n != DefaultMaxEntries {
		t.Fatalf("store has %d entries", n)
	}
}

func TestTextTooLongRejectedNotTruncated(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	if _, err := w.Add(strings.Repeat("é", MaxTextLen)); err != nil {
		t.Fatalf("exactly %d characters should fit: %v", MaxTextLen, err)
	}
	if _, err := w.Add(strings.Repeat("a", MaxTextLen+1)); err == nil {
		t.Fatal("over-long text accepted")
	}
	if _, err := w.Add("  \n\t "); err == nil {
		t.Fatal("empty text accepted")
	}
	if n := len(load(t, path)); n != 1 {
		t.Fatalf("store has %d entries", n)
	}
	if w.Writes() != 1 {
		t.Fatalf("writes %d", w.Writes())
	}
}

func TestTextIsOneParagraph(t *testing.T) {
	path := storePath(t)
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	if _, err := w.Add("line one\n\n  line   two\n"); err != nil {
		t.Fatal(err)
	}
	if got := load(t, path)[0].Text; got != "line one line two" {
		t.Fatalf("got %q", got)
	}
}

func TestRedaction(t *testing.T) {
	path := storePath(t)
	red := eventlog.NewRedactor([]string{"API_TOKEN=s3cr3tvalue"})
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), red, Source{}, nil)
	gh := "ghp_" + strings.Repeat("A", 36)
	res, err := w.Add("token is s3cr3tvalue and " + gh)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Redacted {
		t.Fatal("redaction not reported")
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("s3cr3tvalue")) || bytes.Contains(raw, []byte(gh)) {
		t.Fatalf("secret reached the store: %s", raw)
	}
	if !bytes.Contains(raw, []byte(eventlog.Redacted)) {
		t.Fatal("no [REDACTED] marker")
	}

	// Token shapes are masked even without a configured redactor, and
	// clean text reports no redaction.
	w2 := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	if res, _ := w2.Add("use " + gh); !res.Redacted {
		t.Fatal("token shape not redacted without a redactor")
	}
	if res, _ := w2.Add("plain"); res.Redacted {
		t.Fatal("clean text reported as redacted")
	}
}

func TestCorruptStoreLeftUntouched(t *testing.T) {
	path := storePath(t)
	os.MkdirAll(filepath.Dir(path), 0o700)
	bad := []byte("{not json")
	os.WriteFile(path, bad, 0o600)
	_, err := Load(path)
	var ce *CorruptError
	if !errors.As(err, &ce) {
		t.Fatalf("got %v", err)
	}
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	if _, err := w.Add("x"); !errors.As(err, &ce) {
		t.Fatalf("add on corrupt store: %v", err)
	}
	if _, err := Approve(path, "", true); err == nil {
		t.Fatal("approve on corrupt store succeeded")
	}
	if _, err := ForgetAll(path); err == nil {
		t.Fatal("forget all on corrupt store succeeded")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, bad) {
		t.Fatal("corrupt store was modified")
	}
}

func TestUnsupportedVersionIsCorrupt(t *testing.T) {
	path := storePath(t)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"version":99,"entries":[]}`), 0o600)
	var ce *CorruptError
	if _, err := Load(path); !errors.As(err, &ce) {
		t.Fatalf("got %v", err)
	}
}

func TestConcurrentWritersKeepBothEntries(t *testing.T) {
	path := storePath(t)
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{RunID: fmt.Sprint(i)}, nil)
			_, err := w.Add(fmt.Sprintf("entry %d", i))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(path)
	var f storeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != n {
		t.Fatalf("got %d entries, want %d", len(f.Entries), n)
	}
}

func TestStaleLockIsBroken(t *testing.T) {
	path := storePath(t)
	os.MkdirAll(filepath.Dir(path), 0o700)
	lp := path + ".lock"
	os.WriteFile(lp, []byte("1\n"), 0o600)
	old := time.Now().Add(-2 * lockStale)
	os.Chtimes(lp, old, old)
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	if _, err := w.Add("x"); err != nil {
		t.Fatal(err)
	}
}

func TestHeldLockTimesOut(t *testing.T) {
	defer func(w time.Duration) { lockWait = w }(lockWait)
	lockWait = 50 * time.Millisecond
	path := storePath(t)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path+".lock", []byte("1\n"), 0o600)
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	if _, err := w.Add("x"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("got %v", err)
	}
	if w.Writes() != 0 {
		t.Fatal("failed write counted")
	}
}

func TestForgetAndForgetAll(t *testing.T) {
	path := storePath(t)
	if n, err := ForgetAll(path); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ForgetAll created a store")
	}
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, nil)
	a, _ := w.Add("a")
	w.Add("b")
	// A pending replacement of a goes with it.
	w2 := NewWriter(path, ApprovalPending, DefaultLimits(), nil, Source{}, load(t, path))
	w2.Replace(a.ID, "a2")
	if err := Forget(path, a.ID); err != nil {
		t.Fatal(err)
	}
	got := load(t, path)
	if len(got) != 1 || got[0].Text != "b" {
		t.Fatalf("store %+v", got)
	}
	if err := Forget(path, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if n, err := ForgetAll(path); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if len(load(t, path)) != 0 {
		t.Fatal("entries left")
	}
}

func TestRenderNewestFirstAndCap(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var entries []Entry
	for i := range 5 {
		ts := base.Add(time.Duration(i) * 24 * time.Hour)
		entries = append(entries, Entry{ID: fmt.Sprintf("m%d", i), Text: strings.Repeat("x", 100), Status: StatusActive, CreatedAt: ts, UpdatedAt: ts})
	}
	entries = append(entries, Entry{ID: "mp", Text: "pending", Status: StatusPending, UpdatedAt: base.Add(99 * time.Hour)})
	full := Render(entries, 0)
	if len(full.Loaded) != 5 || full.Omitted != 0 {
		t.Fatalf("loaded %d omitted %d", len(full.Loaded), full.Omitted)
	}
	if full.Loaded[0].ID != "m4" || full.Loaded[4].ID != "m0" {
		t.Fatal("not newest first")
	}
	if !strings.HasPrefix(full.Section, "# Project memory") || !strings.Contains(full.Section, "[m4, 2026-10-05]") || strings.Contains(full.Section, "pending") {
		t.Fatalf("section %q", full.Section)
	}
	if !strings.Contains(full.Section, "may be outdated") || !strings.Contains(full.Section, "AGENTS.md and the task outrank them") {
		t.Fatal("trust label missing")
	}
	capped := Render(entries, len(renderHeader)+2*130)
	if len(capped.Loaded) != 2 || capped.Omitted != 3 {
		t.Fatalf("loaded %d omitted %d", len(capped.Loaded), capped.Omitted)
	}
	if capped.Loaded[0].ID != "m4" || capped.Loaded[1].ID != "m3" {
		t.Fatal("cap didn't keep the newest entries")
	}
	if !strings.Contains(capped.Section, "3 older memory entries omitted") {
		t.Fatalf("section %q", capped.Section)
	}
	if r := Render(nil, 0); r.Section != "" || r.Loaded != nil {
		t.Fatalf("empty render %+v", r)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestStorePathSharedAcrossWorktreesNotRepos(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	git(t, repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644)
	git(t, repo, "add", "f")
	git(t, repo, "commit", "-q", "-m", "init")
	wt := filepath.Join(tmp, "wt")
	git(t, repo, "worktree", "add", "-q", wt)
	other := filepath.Join(tmp, "other")
	os.MkdirAll(other, 0o755)
	git(t, other, "init", "-q")

	dir := filepath.Join(tmp, "state")
	p1, err := StorePath(dir, repo)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := StorePath(dir, filepath.Join(repo, "sub"))
	p3, _ := StorePath(dir, wt)
	p4, _ := StorePath(dir, other)
	if p1 != p2 || p1 != p3 {
		t.Fatalf("same repo got different stores: %s %s %s", p1, p2, p3)
	}
	if p1 == p4 {
		t.Fatal("different repos share a store")
	}
	if !strings.HasPrefix(filepath.Base(p1), "repo-") || filepath.Dir(p1) != dir {
		t.Fatalf("path %s", p1)
	}
	if strings.HasPrefix(p1, repo+string(filepath.Separator)) {
		t.Fatal("store is inside the repository")
	}
}

func TestStorePathOutsideGit(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	dir := filepath.Join(t.TempDir(), "state")
	pa, err := StorePath(dir, a)
	if err != nil {
		t.Fatal(err)
	}
	pa2, _ := StorePath(dir, a)
	pb, _ := StorePath(dir, b)
	if pa != pa2 || pa == pb {
		t.Fatalf("%s %s %s", pa, pa2, pb)
	}
	if _, err := StorePath("relative", a); err == nil {
		t.Fatal("relative store dir accepted")
	}
}

func TestStorePathNeedsDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := StorePath("", t.TempDir()); err == nil {
		t.Fatal("empty dir accepted: there is no default outside --cwd")
	}
}

func TestValidApproval(t *testing.T) {
	if !ValidApproval("auto") || !ValidApproval("pending") || ValidApproval("") || ValidApproval("yes") {
		t.Fatal("ValidApproval")
	}
}
