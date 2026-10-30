package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestProgressBarKnownLength(t *testing.T) {
	var out bytes.Buffer
	p := &ProgressBar{W: &out, Total: 1000}
	for i := 0; i < 4; i++ {
		p.Write(make([]byte, 250))
	}
	p.Finish()
	s := out.String()
	for _, want := range []string{" 25%", " 50%", " 75%", "100%", "1000 B / 1000 B", "[" + strings.Repeat("#", BarWidth) + "]"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
	if strings.Count(s, "\r") != 4 || !strings.HasSuffix(s, "\n") {
		t.Errorf("want 4 redraws and a final newline: %q", s)
	}
	// Writes that don't change the percent don't redraw.
	out.Reset()
	p = &ProgressBar{W: &out, Total: 1 << 20}
	p.Write([]byte{1})
	p.Write([]byte{1})
	if strings.Count(out.String(), "\r") != 1 {
		t.Errorf("redrew without progress: %q", out.String())
	}
}

func TestProgressBarUnknownLength(t *testing.T) {
	var out bytes.Buffer
	p := &ProgressBar{W: &out, Total: -1}
	for i := 0; i < 5; i++ {
		p.Write(make([]byte, ChunkSize/2))
	}
	p.Finish()
	if got, want := out.String(), "  ## 640.0 KiB\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(out.String(), "\r") {
		t.Error("unknown-length bar must not redraw")
	}
}

func TestRunReportsSteps(t *testing.T) {
	bin := bytes.Repeat([]byte("x"), 3*ChunkSize)
	sum := sha256.Sum256(bin)
	u := newTestUpdater(t, fakeRelease(t, "v0.2.0", bin, hex.EncodeToString(sum[:])), "v0.1.0")
	var out bytes.Buffer
	u.Report = &TextReporter{W: &out}
	if _, err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	order := []string{
		"update: current version: v0.1.0 at ",
		"update: query latest release: http",
		"update: query latest release: found v0.2.0 (current v0.1.0)",
		"update: select asset: tinyagent-linux-arm64 for linux/arm64",
		"update: download checksums: SHA256SUMS",
		"update: download binary: http",
		"update: download binary: received 768.0 KiB (786432 bytes)",
		"update: verify checksum: sha256 " + hex.EncodeToString(sum[:]) + " matches",
		"update: install: replacing ",
		"update: done: updated v0.1.0 -> v0.2.0 at ",
	}
	at := 0
	for _, w := range order {
		i := strings.Index(s[at:], w)
		if i < 0 {
			t.Fatalf("missing or out of order %q in:\n%s", w, s)
		}
		at += i + len(w)
	}
	if strings.Contains(s, "\r") || strings.Contains(s, "#") {
		t.Errorf("non-terminal output has a bar:\n%s", s)
	}

	// On a terminal the download draws the bar between the download lines.
	u = newTestUpdater(t, fakeRelease(t, "v0.2.0", bin, hex.EncodeToString(sum[:])), "v0.1.0")
	out.Reset()
	u.Report = &TextReporter{W: &out, Bar: true}
	if _, err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "100%  768.0 KiB / 768.0 KiB\nupdate: download binary: received") {
		t.Errorf("no bar:\n%s", out.String())
	}
}

func TestRunUpToDateReportsSteps(t *testing.T) {
	u := newTestUpdater(t, fakeRelease(t, "v0.1.0", nil, ""), "v0.1.0")
	var out bytes.Buffer
	u.Report = &TextReporter{W: &out}
	if _, err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "update: query latest release: latest is v0.1.0; already up to date") || strings.Contains(out.String(), "download") {
		t.Errorf("got:\n%s", out.String())
	}
}

func TestRunErrorsNameTheStep(t *testing.T) {
	bad := newTestUpdater(t, fakeRelease(t, "v0.2.0", []byte("tampered"), strings.Repeat("a", 64)), "v0.1.0")
	plat := newTestUpdater(t, fakeRelease(t, "v0.2.0", nil, ""), "v0.1.0")
	plat.GOOS = "plan9"
	down := newTestUpdater(t, fakeRelease(t, "v0.2.0", nil, ""), "v0.1.0")
	down.APIURL += "-missing"
	for _, c := range []struct {
		u    *Updater
		step string
	}{{bad, StepVerify}, {plat, StepSelect}, {down, StepQuery}} {
		_, err := c.u.Run(context.Background())
		var se *StepError
		if !errors.As(err, &se) || se.Step != c.step || !strings.HasPrefix(err.Error(), "update failed at "+c.step+": ") {
			t.Errorf("want failure at %q, got %v", c.step, err)
		}
	}
}

func TestJSONReporter(t *testing.T) {
	var out bytes.Buffer
	(&JSONReporter{W: &out}).Step(StepQuery, "found v1")
	if got := out.String(); got != `{"detail":"found v1","step":"query latest release","type":"update.step"}`+"\n" {
		t.Errorf("got %q", got)
	}
}
