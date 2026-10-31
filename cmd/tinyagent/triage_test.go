package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeIssues(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "issues.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTriageCLI(t *testing.T) {
	defer func(f func() time.Time) { triageNow = f }(triageNow)
	triageNow = func() time.Time { return time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC) }
	in := writeIssues(t, `[{"number": 2, "title": "Docs typo", "labels": [{"name": "documentation"}], "updatedAt": "2026-10-30T00:00:00Z"},
		{"number": 1, "title": "Crash on start", "labels": [{"name": "bug"}], "updatedAt": "2026-01-01T00:00:00Z"}]`)
	out := filepath.Join(t.TempDir(), "next-up.json")
	for _, args := range [][]string{{"--triage", in, "--triage-out", out}, {"triage", in, "--out", out}} {
		os.Remove(out)
		code, stdout, stderr := runWith(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, stderr %q", args, code, stderr)
		}
		if stdout != "" {
			t.Errorf("%v wrote to stdout with --triage-out: %q", args, stdout)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			SchemaVersion int `json:"schema_version"`
			Items         []struct {
				Number  int      `json:"number"`
				Reasons []string `json:"reasons"`
				Flags   []string `json:"flags"`
			} `json:"items"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.SchemaVersion != 1 || len(doc.Items) != 2 || doc.Items[0].Number != 1 || !reflect.DeepEqual(doc.Items[0].Flags, []string{"stale"}) {
			t.Errorf("%v doc = %s", args, b)
		}
	}
}

func TestTriageStdout(t *testing.T) {
	in := writeIssues(t, `[]`)
	// runWith captures only help/flag output; check the runner directly.
	o := options{triage: in, triageStaleDays: 30}
	fs := newFlagSet(&options{})
	_ = fs.Parse([]string{"--triage", in})
	var stdout, stderr strings.Builder
	if code := runTriage(&o, fs, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"items": []`) || !strings.Contains(stderr.String(), "0 issues ranked") {
		t.Errorf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
	o.triage = "-"
	stdout.Reset()
	if code := runTriage(&o, fs, strings.NewReader(`[{"number": 4, "title": "x"}]`), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"number": 4`) {
		t.Errorf("stdin: exit %d, stdout %q", code, stdout.String())
	}
}

func TestTriageErrors(t *testing.T) {
	bad := writeIssues(t, `{not json`)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--triage", bad}, "not a JSON array"},
		{[]string{"--triage", filepath.Join(t.TempDir(), "missing.json")}, "error: --triage:"},
		{[]string{"--triage", bad, "-t", "fix it"}, "can't be combined with -t"},
		{[]string{"--triage", bad, "--triage-stale-days", "-1"}, "must be 0 or more"},
		{[]string{"--triage-out", "x.json"}, "apply only to --triage"},
	} {
		code, _, stderr := runWith(t, c.args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, stderr %q; want 2 and %q", c.args, code, stderr, c.want)
		}
	}
}

func TestTriageSubcommandExpands(t *testing.T) {
	got, sh, err := expandSubcommand([]string{"triage", "issues.json", "--stale-days", "7"})
	if err != nil || sh != nil {
		t.Fatalf("err %v help %v", err, sh)
	}
	if want := []string{"--triage", "issues.json", "--triage-stale-days", "7"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, sh, _ := expandSubcommand([]string{"triage"}); sh == nil {
		t.Error("bare triage should print help")
	}
}
