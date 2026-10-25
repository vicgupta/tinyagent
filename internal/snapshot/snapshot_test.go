package snapshot

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func paths(m *Manifest) []string {
	var out []string
	for _, e := range m.Entries {
		out = append(out, e.Path)
	}
	return out
}

func planLines(p *Plan) []string {
	var out []string
	for _, c := range p.Changes {
		out = append(out, c.Op+" "+c.Path)
	}
	return out
}

func rewindTo(t *testing.T, s *Store, id string) (*Plan, Result) {
	t.Helper()
	m, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanRestore(m)
	if err != nil {
		t.Fatal(err)
	}
	return plan, s.Apply(plan)
}

func TestTakeRecordsAndStorePermissions(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "alpha", 0o644)
	write(t, root, "dir/b.txt", "alpha", 0o644) // same content, one object
	s, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Take("baseline", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Files != 2 || rec.Bytes != 10 || rec.Kind != "baseline" || rec.Step != 0 || !strings.HasPrefix(rec.ID, "snap-") {
		t.Errorf("record = %+v", rec)
	}
	if rec.Store != filepath.Join(root, ".tinyagent", "snapshots") {
		t.Errorf("store = %s", rec.Store)
	}
	var objects int
	filepath.WalkDir(s.objects(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			objects++
		}
		return nil
	})
	if objects != 1 {
		t.Errorf("objects = %d, want 1 (content-addressed)", objects)
	}
	if runtime.GOOS == "windows" {
		return
	}
	filepath.WalkDir(filepath.Join(root, ".tinyagent"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := d.Info()
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", p, fi.Mode().Perm(), want)
		}
		return nil
	})
	if got := read(t, s.Dir, ".gitignore"); got != "*\n" {
		t.Errorf(".gitignore = %q", got)
	}
}

func TestScopeExclusions(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".git/HEAD", "ref", 0o644)
	write(t, root, ".gitignore", "*.log\nbuild/\n!keep.log\n/top.txt\n", 0o644)
	write(t, root, "sub/.gitignore", "local.txt\n", 0o644)
	write(t, root, "a.log", "x", 0o644)
	write(t, root, "keep.log", "x", 0o644)
	write(t, root, "build/out.bin", "x", 0o644)
	write(t, root, "top.txt", "x", 0o644)
	write(t, root, "sub/top.txt", "x", 0o644)
	write(t, root, "sub/local.txt", "x", 0o644)
	write(t, root, "big.bin", strings.Repeat("x", 100), 0o644)
	write(t, root, "run.json", "{}", 0o644)
	write(t, root, ".tinyagent-123.json", "{}", 0o644)
	write(t, root, ".tinyagent/tinyagent.db", "db", 0o644)
	if err := os.Symlink("a.log", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, Options{MaxFileBytes: 50, Exclude: []string{filepath.Join(root, "run.json")}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Current()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "keep.log", "link", "sub/.gitignore", "sub/top.txt"}
	if got := paths(m); !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
	if !slices.Equal(m.Large, []string{"big.bin"}) {
		t.Errorf("large = %v", m.Large)
	}
	if want := []string{"a.log", "build", "sub/local.txt", "top.txt"}; !slices.Equal(m.Ignored, want) {
		t.Errorf("ignored = %v, want %v", m.Ignored, want)
	}
	for _, e := range m.Entries {
		if e.Path == "link" && (e.Type != "symlink" || e.Target != "a.log") {
			t.Errorf("link entry = %+v", e)
		}
	}
}

func TestNoGitMeansNoIgnoreRules(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", "*.log\n", 0o644)
	write(t, root, "a.log", "x", 0o644)
	s, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(paths(m), "a.log") {
		t.Errorf("outside a git repo .gitignore should not apply: %v", paths(m))
	}
}

func TestGlobRules(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".git/info/exclude", "secret*\n", 0o644)
	write(t, root, ".gitignore", "docs/**/*.tmp\n**/cache\na?c.txt\n[0-9].txt\n", 0o644)
	ig := newIgnorer(root)
	cases := map[string]bool{
		"docs/x.tmp": true, "docs/a/b/x.tmp": true, "x.tmp": false,
		"cache": true, "a/b/cache": true, "abc.txt": true, "abbc.txt": false,
		"1.txt": true, "x.txt": false, "secret.env": true, "dir/secret": true,
	}
	for p, want := range cases {
		if got := ig.ignored(p, p == "cache" || p == "a/b/cache"); got != want {
			t.Errorf("ignored(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestRewindModifiedAddedDeleted(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "one", 0o755)
	write(t, root, "b.txt", "bee", 0o644)
	write(t, root, "keep/k.txt", "k", 0o644)
	s, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Take("baseline", 0)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.txt", "two", 0o644)
	os.Remove(filepath.Join(root, "b.txt"))
	write(t, root, "c.txt", "new", 0o644)
	write(t, root, "newdir/deep/d.txt", "new", 0o644)

	m, _ := s.Load(base.ID)
	plan, err := s.PlanRestore(m)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"M a.txt", "D b.txt", "A c.txt", "A newdir/deep/d.txt"}
	if got := planLines(plan); !slices.Equal(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	if read(t, root, "a.txt") != "two" || !exists(root, "c.txt") {
		t.Fatal("planning changed files")
	}
	res := s.Apply(plan)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if read(t, root, "a.txt") != "one" || read(t, root, "b.txt") != "bee" || exists(root, "c.txt") || exists(root, "newdir") {
		t.Error("workspace not restored")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(root, "a.txt"))
		if fi.Mode().Perm()&0o100 == 0 {
			t.Error("executable bit not restored")
		}
	}
	if !exists(root, "keep/k.txt") {
		t.Error("unchanged file removed")
	}
	again, _ := s.PlanRestore(m)
	if len(again.Changes) != 0 {
		t.Errorf("plan after restore = %v", planLines(again))
	}
}

func TestRewindLeavesOutOfScopeAlone(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".git/HEAD", "ref", 0o644)
	write(t, root, ".gitignore", "*.log\n", 0o644)
	write(t, root, "app.log", "before", 0o644)
	write(t, root, "big.bin", strings.Repeat("a", 64), 0o644)
	write(t, root, "src.go", "package x", 0o644)
	s, err := Open(root, Options{MaxFileBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Take("baseline", 0)
	if err != nil {
		t.Fatal(err)
	}
	if base.SkippedIgnored != 1 || base.SkippedLarge != 1 || !slices.Equal(base.LargePaths, []string{"big.bin"}) {
		t.Errorf("record = %+v", base)
	}
	write(t, root, "app.log", "after", 0o644)
	write(t, root, "big.bin", strings.Repeat("b", 64), 0o644)
	write(t, root, "new.log", "x", 0o644)
	write(t, root, ".git/HEAD", "changed", 0o644)
	plan, res := rewindTo(t, s, base.ID)
	if len(plan.Changes) != 0 || res.Err != nil {
		t.Fatalf("plan = %v err = %v", planLines(plan), res.Err)
	}
	if read(t, root, "app.log") != "after" || read(t, root, "big.bin") != strings.Repeat("b", 64) || !exists(root, "new.log") || read(t, root, ".git/HEAD") != "changed" {
		t.Error("out-of-scope path modified")
	}
}

func TestRewindSymlinksAndUnsafeParents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, root, "d/f.txt", "inside", 0o644)
	if err := os.Symlink("d/f.txt", filepath.Join(root, "ln")); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Take("baseline", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Replace d with a symlink out of the root and retarget ln.
	os.RemoveAll(filepath.Join(root, "d"))
	if err := os.Symlink(outside, filepath.Join(root, "d")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "ln"))
	os.Symlink("/etc/passwd", filepath.Join(root, "ln"))
	_, res := rewindTo(t, s, base.ID)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if exists(outside, "f.txt") {
		t.Fatal("rewind wrote through a symlink out of the root")
	}
	if got, _ := os.Readlink(filepath.Join(root, "ln")); got != "d/f.txt" {
		t.Errorf("ln -> %q", got)
	}
	if read(t, root, "d/f.txt") != "inside" {
		t.Error("d/f.txt not restored")
	}
}

func TestRewindStopsAtFirstFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a non-root POSIX user for a read-only directory")
	}
	root := t.TempDir()
	write(t, root, "a/1.txt", "one", 0o644)
	write(t, root, "b/2.txt", "two", 0o644)
	s, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := s.Take("baseline", 0)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "a/1.txt", "ONE", 0o644)
	write(t, root, "b/2.txt", "TWO", 0o644)
	os.Chmod(filepath.Join(root, "a"), 0o555)
	defer os.Chmod(filepath.Join(root, "a"), 0o755)
	_, res := rewindTo(t, s, base.ID)
	if res.Err == nil || !slices.Equal(res.NotRestored, []string{"a/1.txt", "b/2.txt"}) || len(res.Restored) != 0 {
		t.Errorf("result = %+v", res)
	}
	if read(t, root, "b/2.txt") != "TWO" {
		t.Error("restore continued after a failure")
	}
}

func TestLoadRejectsCorruptStore(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x", 0o644)
	s, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Take("baseline", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("../x"); err == nil {
		t.Error("path-like id accepted")
	}
	if _, err := s.Load("snap-000000000000"); err == nil {
		t.Error("unknown id accepted")
	}
	os.RemoveAll(s.objects())
	os.Mkdir(s.objects(), 0o700)
	if _, err := s.Load(rec.ID); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing object: err = %v", err)
	}
	os.WriteFile(filepath.Join(s.manifests(), rec.ID+".json"), []byte("{"), 0o600)
	if _, err := s.Load(rec.ID); err == nil {
		t.Error("corrupt manifest accepted")
	}
	if _, err := OpenExisting(root, filepath.Join(root, "nope"), Options{}); err == nil {
		t.Error("missing store accepted")
	}
}

func TestOpenFailsWhenStoreCannotBeCreated(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".tinyagent", "a file, not a dir", 0o644)
	if _, err := Open(root, Options{}); err == nil {
		t.Error("Open succeeded with .tinyagent as a file")
	}
}
