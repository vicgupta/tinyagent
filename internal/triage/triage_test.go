package triage

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

const fixture = `[
 {"number": 10, "title": "Add retry to the fetch tool", "body": "## Implementation notes\nsmall", "labels": [{"name": "enhancement"}], "createdAt": "2026-10-01T00:00:00Z", "updatedAt": "2026-10-30T00:00:00Z"},
 {"number": 11, "title": "Crash on empty config file", "body": "## Implementation notes\nfix", "labels": [{"name": "bug"}], "createdAt": "2026-10-02T00:00:00Z", "updatedAt": "2026-10-30T00:00:00Z"},
 {"number": 12, "title": "Add retries to fetch tool", "body": "## Implementation notes\nx", "labels": ["enhancement"], "createdAt": "2026-10-03T00:00:00Z", "updatedAt": "2026-10-30T00:00:00Z"},
 {"number": 13, "title": "Old docs question", "body": "", "labels": [], "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-08-01T00:00:00Z"},
 {"number": 14, "title": "Parked bug", "body": "## Implementation notes\n", "labels": [{"name": "bug"}, {"name": "amf:blocked"}], "createdAt": "2026-10-04T00:00:00Z", "updatedAt": "2026-10-30T00:00:00Z"},
 {"number": 15, "title": "Some PR", "body": "", "labels": [], "createdAt": "2026-10-05T00:00:00Z", "updatedAt": "2026-10-30T00:00:00Z", "pull_request": {"url": "x"}},
 {"number": 16, "title": "Another enhancement idea", "body": "## Acceptance criteria\n1. a\n2. b\n3. c\n4. d\n5. e\n6. f\n## Implementation notes\n", "labels": [{"name": "enhancement"}], "createdAt": "2026-10-06T00:00:00Z", "updatedAt": "2026-10-30T00:00:00Z"}
]`

func rankFixture(t *testing.T) []Ranked {
	t.Helper()
	issues, err := Decode([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	return Rank(issues, now, Options{StaleDays: 30})
}

func TestRankOrderAndFlags(t *testing.T) {
	got := rankFixture(t)
	var order []int
	flags := map[int][]string{}
	for i, r := range got {
		if r.Rank != i+1 {
			t.Errorf("entry %d has rank %d", i, r.Rank)
		}
		if len(r.Reasons) == 0 {
			t.Errorf("#%d has no reasons", r.Number)
		}
		order = append(order, r.Number)
		flags[r.Number] = r.Flags
	}
	// 11: bug 3/1=3; 10: 2/1=2; 16: 2/2=1; 13: 1/2=0.5 (stale);
	// then duplicate 12; then blocked 14 and pull request 15.
	if want := []int{11, 10, 16, 13, 12, 14, 15}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	for n, want := range map[int][]string{
		11: {}, 10: {}, 16: {}, 13: {"stale"}, 12: {"duplicate-of:#10"}, 14: {"blocked"}, 15: {"pull-request"},
	} {
		if !reflect.DeepEqual(flags[n], want) {
			t.Errorf("#%d flags = %v, want %v", n, flags[n], want)
		}
	}
	if len(got) != 7 {
		t.Errorf("count out = %d, want 7 (nothing dropped)", len(got))
	}
}

func TestRankReasons(t *testing.T) {
	got := rankFixture(t)
	byNum := map[int]Ranked{}
	for _, r := range got {
		byNum[r.Number] = r
	}
	r11 := byNum[11]
	if want := []string{"score 3.00 = value 3 / effort 1", "value 3: label bug", "effort 1: short body, at most 5 acceptance criteria, has implementation notes"}; !reflect.DeepEqual(r11.Reasons, want) {
		t.Errorf("#11 reasons = %q, want %q", r11.Reasons, want)
	}
	if r := byNum[16]; r.Effort != 2 || !strings.Contains(strings.Join(r.Reasons, "\n"), "6 acceptance criteria (over 5)") {
		t.Errorf("#16 = %+v", r)
	}
	if r := byNum[13]; !strings.Contains(strings.Join(r.Reasons, "\n"), "stale: not updated for 91 days (over 30)") {
		t.Errorf("#13 reasons = %q", r.Reasons)
	}
}

func TestDeterministic(t *testing.T) {
	a, err := Encode(rankFixture(t), Options{StaleDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encode(rankFixture(t), Options{StaleDays: 30})
	if !bytes.Equal(a, b) {
		t.Error("same input gave different output")
	}
	var doc Document
	if err := json.Unmarshal(a, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || doc.StaleDays != 30 || len(doc.Items) != 7 {
		t.Errorf("doc = %+v", doc)
	}
}

func TestTiesByNumber(t *testing.T) {
	issues := []Issue{{Number: 9, Title: "zebra"}, {Number: 3, Title: "apple"}, {Number: 5, Title: "mango"}}
	var order []int
	for _, r := range Rank(issues, now, Options{}) {
		order = append(order, r.Number)
	}
	if want := []int{3, 5, 9}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestEmpty(t *testing.T) {
	issues, err := Decode([]byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(Rank(issues, now, Options{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"items": []`) {
		t.Errorf("empty output = %s", b)
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, in := range []string{`{"number": 1}`, `[{"number": 0}]`, `[{"number": 1}, {"number": 1}]`, `[] []`, `not json`} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%q) succeeded", in)
		}
	}
}

func TestMarkdown(t *testing.T) {
	md := Markdown(rankFixture(t))
	if !strings.HasPrefix(md, "| Rank | Issue |") || !strings.Contains(md, "| 1 | #11 | 3.00 | 3 | 1 |  | Crash on empty config file |") {
		t.Errorf("markdown = %s", md)
	}
}
