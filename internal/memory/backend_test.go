package memory

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// backends returns a fresh store path for each backend.
func backends(t *testing.T) map[string]string {
	t.Helper()
	d := t.TempDir()
	return map[string]string{
		"markdown": filepath.Join(d, "md", "memory"),
		"sqlite":   filepath.Join(d, "sq", "memory", "memory.sqlite"),
		"json":     filepath.Join(d, "js", "proj.json"),
	}
}

// TestBackendConformance runs the same store life cycle on every backend.
func TestBackendConformance(t *testing.T) {
	for name, path := range backends(t) {
		t.Run(name, func(t *testing.T) {
			if got := load(t, path); len(got) != 0 || Exists(path) {
				t.Fatalf("fresh store: %v, exists %v", got, Exists(path))
			}
			w := NewWriter(path, ApprovalPending, DefaultLimits(), nil, Source{RunID: "r1", Model: "m", Version: "v"}, nil)
			w.Tags = []string{"Proj"}
			a, err := w.AddTagged("integration tests need `make db-up`", []string{"build", "proj"})
			if err != nil {
				t.Fatal(err)
			}
			b, err := w.Add("second: use go 1.22 — ünïcode ok")
			if err != nil {
				t.Fatal(err)
			}
			if !Exists(path) {
				t.Fatal("store not created")
			}
			got := load(t, path)
			if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
				t.Fatalf("order/ids: %+v", got)
			}
			if got[0].Status != StatusPending || !reflect.DeepEqual(got[0].Tags, []string{"proj", "build"}) || got[0].Source.Model != "m" {
				t.Fatalf("entry %+v", got[0])
			}
			if n, err := Approve(path, a.ID, false); err != nil || n != 1 {
				t.Fatalf("approve: %d %v", n, err)
			}
			w2 := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{}, load(t, path))
			r, err := w2.Replace(a.ID, "integration tests need `make db-up` first")
			if err != nil || r.Status != StatusActive {
				t.Fatalf("replace: %+v %v", r, err)
			}
			got = load(t, path)
			if got[0].Text != "integration tests need `make db-up` first" || !reflect.DeepEqual(got[0].Tags, []string{"proj", "build"}) {
				t.Fatalf("replaced %+v", got[0])
			}
			if err := Forget(path, b.ID); err != nil {
				t.Fatal(err)
			}
			if got = load(t, path); len(got) != 1 {
				t.Fatalf("after forget %+v", got)
			}
			if n, err := ForgetAll(path); err != nil || n != 1 {
				t.Fatalf("forget all: %d %v", n, err)
			}
			if got = load(t, path); len(got) != 0 {
				t.Fatalf("after forget all %+v", got)
			}
			if err := Forget(path, "nope"); err == nil {
				t.Error("forgetting an unknown id succeeded")
			}
		})
	}
}

func TestBackendRoundTripPreservesEveryField(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 30, 45, 123456789, time.UTC)
	want := []Entry{
		{ID: "maaaa", Text: "a: \"quoted\" --- not front matter", Status: StatusActive, CreatedAt: at, UpdatedAt: at.Add(time.Hour),
			Source: Source{RunID: "run-1", Model: "openai/gpt-4o", Version: "1.2.3", UntrustedContext: true}, Tags: []string{"build", "ci/linux"}},
		{ID: "mbbbb", Text: "plain", Status: StatusPending, CreatedAt: at.Add(time.Minute), UpdatedAt: at.Add(time.Minute), Replaces: "maaaa"},
	}
	for name, path := range backends(t) {
		t.Run(name, func(t *testing.T) {
			if err := Update(path, func([]Entry) ([]Entry, error) { return want, nil }); err != nil {
				t.Fatal(err)
			}
			got := load(t, path)
			for i := range got {
				if len(got[i].Tags) == 0 {
					got[i].Tags = nil
				}
				if !got[i].CreatedAt.Equal(want[i].CreatedAt) || !got[i].UpdatedAt.Equal(want[i].UpdatedAt) {
					t.Errorf("times %v %v", got[i].CreatedAt, got[i].UpdatedAt)
				}
				got[i].CreatedAt, got[i].UpdatedAt = want[i].CreatedAt, want[i].UpdatedAt
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("round trip\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestMarkdownFileFormat(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	w := NewWriter(dir, ApprovalAuto, DefaultLimits(), nil, Source{RunID: "r1", Model: "m", Version: "v"}, nil)
	res, err := w.AddTagged("run `make test-fast` before pushing", []string{"build"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, res.ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"---\nid: " + res.ID + "\n", "status: active\n", "tags: [build]\n", "source_session: r1\n", "model: m\n", "\n---\nrun `make test-fast` before pushing\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("file lacks %q:\n%s", want, s)
		}
	}
	e, err := UnmarshalMarkdown([]byte(strings.ReplaceAll("\ufeff"+s, "\n", "\r\n")))
	if err != nil || e.ID != res.ID || e.Text != "run `make test-fast` before pushing" {
		t.Errorf("BOM/CRLF parse: %+v %v", e, err)
	}
	// Other files and dotfiles are ignored; a bad .md makes the store corrupt
	// and is left alone.
	_ = os.WriteFile(filepath.Join(dir, "README.txt"), []byte("hi"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".hidden.md"), []byte("hi"), 0o600)
	if got := load(t, dir); len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	bad := filepath.Join(dir, "mzzzz.md")
	_ = os.WriteFile(bad, []byte("no front matter"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Error("bad entry file loaded")
	}
	if err := Forget(dir, res.ID); err == nil {
		t.Error("update of a corrupt store succeeded")
	}
	if b, _ := os.ReadFile(bad); string(b) != "no front matter" {
		t.Error("corrupt file changed")
	}
	// The file name must match the id.
	_ = os.Remove(bad)
	_ = os.Rename(filepath.Join(dir, res.ID+".md"), filepath.Join(dir, "mother.md"))
	if _, err := Load(dir); err == nil {
		t.Error("id/file name mismatch loaded")
	}
}

func TestSQLiteColumnsAndSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory", "memory.sqlite")
	w := NewWriter(path, ApprovalAuto, DefaultLimits(), nil, Source{RunID: "r1", Model: "m"}, nil)
	if _, err := w.AddTagged("note", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("PRAGMA table_info(memories)")
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name+" "+typ)
	}
	rows.Close()
	sort.Strings(cols)
	want := []string{"content TEXT", "created_at TEXT", "id TEXT", "model TEXT", "replaces TEXT", "seq INTEGER", "source_session TEXT", "status TEXT", "tags TEXT", "untrusted_context INTEGER", "updated_at TEXT", "version TEXT"}
	if !reflect.DeepEqual(cols, want) {
		t.Errorf("columns %v\nwant %v", cols, want)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != schemaVersion {
		t.Errorf("user_version %d %v", v, err)
	}
	var content, tags, status string
	if err := db.QueryRow("SELECT content, tags, status FROM memories").Scan(&content, &tags, &status); err != nil || content != "note" || tags != "a,b" || status != "active" {
		t.Errorf("row %q %q %q %v", content, tags, status, err)
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 && runtime.GOOS != "windows" {
		t.Errorf("database mode %v", fi.Mode().Perm())
	}
	// A database from a newer tinyagent is refused, not migrated down.
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("newer schema loaded")
	}
}

func TestResolveStoreDefaultsRelativeToCwd(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	abs, _ := filepath.Abs(".")
	for _, c := range []struct{ backend, path, sqlite, cwd, want string }{
		{"", "", "", ".", filepath.Join(abs, "memory")},
		{"markdown", "", "", ".", filepath.Join(abs, "memory")},
		{"sqlite", "", "", ".", filepath.Join(abs, "memory", "memory.sqlite")},
		{"markdown", "notes/mem", "", "sub", filepath.Join(abs, "sub", "notes", "mem")},
		{"sqlite", "", "db/m.db", ".", filepath.Join(abs, "db", "m.db")},
		{"markdown", filepath.Join(dir, "x"), "", "elsewhere", filepath.Join(dir, "x")},
	} {
		got, err := ResolveStore(c.backend, c.path, c.sqlite, c.cwd)
		if err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
	for _, c := range [][3]string{{"redis", "", ""}, {"markdown", "mem.sqlite", ""}, {"sqlite", "", "mem.txt"}} {
		if _, err := ResolveStore(c[0], c[1], c[2], "."); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
}

func TestImportLegacyOnceAndNonDestructive(t *testing.T) {
	legacy := storePath(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := []Entry{{ID: "maaaa", Text: "one", Status: StatusActive, CreatedAt: at, UpdatedAt: at}, {ID: "mbbbb", Text: "two", Status: StatusPending, CreatedAt: at, UpdatedAt: at}}
	if err := Update(legacy, func([]Entry) ([]Entry, error) { return old, nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(legacy)
	for name, path := range backends(t) {
		if name == "json" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			n, err := ImportLegacy(path, legacy)
			if err != nil || n != 2 {
				t.Fatalf("import: %d %v", n, err)
			}
			if got := load(t, path); len(got) != 2 || got[1].Status != StatusPending {
				t.Fatalf("imported %+v", got)
			}
			if after, _ := os.ReadFile(legacy); string(after) != string(before) {
				t.Error("legacy store changed")
			}
			if n, _ := ImportLegacy(path, legacy); n != 0 {
				t.Error("imported twice")
			}
			if _, err := ForgetAll(path); err != nil {
				t.Fatal(err)
			}
			if n, _ := ImportLegacy(path, legacy); n != 0 {
				t.Error("re-imported after forget all")
			}
		})
	}
	if n, err := ImportLegacy(filepath.Join(t.TempDir(), "memory"), filepath.Join(t.TempDir(), "missing.json")); n != 0 || err != nil {
		t.Errorf("missing legacy: %d %v", n, err)
	}
}

func TestSearchAndRecent(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	es := []Entry{
		{ID: "m1", Text: "Run make test-fast", Status: StatusActive, UpdatedAt: at, Tags: []string{"build"}},
		{ID: "m2", Text: "Deploy with make release", Status: StatusActive, UpdatedAt: at.Add(time.Hour), Tags: []string{"ops"}},
		{ID: "m3", Text: "make secret pending", Status: StatusPending, UpdatedAt: at.Add(2 * time.Hour)},
	}
	ids := func(es []Entry) string {
		var s []string
		for _, e := range es {
			s = append(s, e.ID)
		}
		return strings.Join(s, ",")
	}
	for _, c := range []struct{ q, tag, want string }{
		{"make", "", "m2,m1"}, {"MAKE test", "", "m1"}, {"", "ops", "m2"}, {"make", "build", "m1"}, {"ops", "", "m2"}, {"pending", "", ""}, {"nothing", "", ""},
	} {
		if got := ids(Search(es, c.q, c.tag, 10)); got != c.want {
			t.Errorf("Search(%q, %q) = %s, want %s", c.q, c.tag, got, c.want)
		}
	}
	if got := ids(Recent(es, 1)); got != "m2" {
		t.Errorf("Recent = %s", got)
	}
}
