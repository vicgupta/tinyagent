package ratchet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var day = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestLoadMissingIsEmpty(t *testing.T) {
	rules, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || rules != nil {
		t.Fatalf("Load missing = %v, %v; want nil, nil", rules, err)
	}
}

func TestAddLoadRoundTrip(t *testing.T) {
	path := Path(t.TempDir())
	r1, err := Add(path, Rule{Rule: "Run go vet before finishing", Incident: "PR #12 broke vet"}, day)
	if err != nil {
		t.Fatal(err)
	}
	if r1.ID != "r1" || r1.Created != "2026-10-30" || r1.Expires != "2027-01-28" {
		t.Fatalf("r1 = %+v", r1)
	}
	r2, err := Add(path, Rule{Rule: "Never rm -rf the repo", Incident: "deleted the checkout", Review: "when sandbox is on by default", Deny: &Deny{Command: "rm -rf *"}}, day)
	if err != nil {
		t.Fatal(err)
	}
	if r2.ID != "r2" || r2.Expires != "" {
		t.Fatalf("r2 = %+v", r2)
	}
	rules, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[1].Deny == nil || rules[1].Deny.Command != "rm -rf *" || rules[1].Review == "" {
		t.Fatalf("loaded %+v", rules)
	}
	if err := Remove(path, "r1"); err != nil {
		t.Fatal(err)
	}
	r3, err := Add(path, Rule{Rule: "x", Incident: "y", Expires: "2026-12-01"}, day)
	if err != nil {
		t.Fatal(err)
	}
	if r3.ID != "r3" {
		t.Fatalf("next id after remove = %s, want r3", r3.ID)
	}
	if err := Remove(path, "r9"); err == nil {
		t.Fatal("Remove unknown id: want error")
	}
}

func TestAddValidates(t *testing.T) {
	path := Path(t.TempDir())
	for _, r := range []Rule{
		{Incident: "x"},
		{Rule: "x"},
		{Rule: "x", Incident: "y", Expires: "soon"},
		{Rule: "x", Incident: "y", Deny: &Deny{Command: "a", Path: "b"}},
		{Rule: "x", Incident: "y", Deny: &Deny{}},
	} {
		if _, err := Add(path, r, day); err == nil {
			t.Errorf("Add(%+v): want error", r)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid adds created the file: %v", err)
	}
}

func TestLoadRejectsBadFile(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"syntax":    "[[rule]\n",
		"unknown":   "[[rule]]\nid='r1'\nrule='a'\nincident='b'\ncreated='2026-01-01'\nexpires='2026-02-01'\nbogus=1\n",
		"duplicate": "[[rule]]\nid='r1'\nrule='a'\nincident='b'\ncreated='2026-01-01'\nreview='c'\n[[rule]]\nid='r1'\nrule='a'\nincident='b'\ncreated='2026-01-01'\nreview='c'\n",
		"noexpiry":  "[[rule]]\nid='r1'\nrule='a'\nincident='b'\ncreated='2026-01-01'\n",
	} {
		p := filepath.Join(dir, name+".toml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSplitAndSection(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Rule: "a", Incident: "i1", Created: "2026-01-01", Expires: "2026-10-30"},
		{ID: "r2", Rule: "b\nmore", Incident: "i2", Created: "2026-01-01", Expires: "2026-10-31"},
		{ID: "r3", Rule: "c", Incident: "i3", Created: "2026-01-01", Review: "later", Deny: &Deny{Command: "git push *"}},
	}
	active, expired := Split(rules, day)
	if len(expired) != 1 || expired[0].ID != "r1" || len(active) != 2 {
		t.Fatalf("active %v expired %v", active, expired)
	}
	s := Section(active)
	if !strings.Contains(s, "[r2] b more (incident: i2)") || !strings.Contains(s, "[r3] c (incident: i3; enforced)") || strings.Contains(s, "r1") {
		t.Fatalf("section:\n%s", s)
	}
	if Section(nil) != "" {
		t.Fatal("Section(nil) not empty")
	}
}

func TestPermissionRules(t *testing.T) {
	got := PermissionRules([]Rule{
		{ID: "r1", Rule: "a", Incident: "i"},
		{ID: "r2", Rule: "b", Incident: "i", Deny: &Deny{Command: "rm *"}},
		{ID: "r3", Rule: "c", Incident: "i", Deny: &Deny{Path: "go.sum"}},
		{ID: "r4", Rule: "d", Incident: "i", Deny: &Deny{Host: "example.com"}},
		{ID: "r5", Rule: "e", Incident: "i", Deny: &Deny{Tool: "write_file"}},
	})
	if len(got) != 4 {
		t.Fatalf("got %d rules", len(got))
	}
	want := []string{DefaultCommandTools, DefaultPathTools, DefaultHostTools, "write_file"}
	for i, r := range got {
		if r.Tool != want[i] || r.Decision != "deny" || !strings.Contains(r.Message, "project rule") {
			t.Errorf("rule %d = %+v", i, r)
		}
	}
}

func TestLoadUnderAFileIsNoRules(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".tinyagent"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rules, err := Load(Path(dir)); err != nil || rules != nil {
		t.Fatalf("Load = %v, %v", rules, err)
	}
}
