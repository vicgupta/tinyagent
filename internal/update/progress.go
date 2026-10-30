package update

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Step names, in the order Run goes through them. Errors from Run name the
// step that failed ("update failed at <step>: ...").
const (
	StepCurrent  = "current version"
	StepQuery    = "query latest release"
	StepSelect   = "select asset"
	StepChecksum = "download checksums"
	StepDownload = "download binary"
	StepVerify   = "verify checksum"
	StepInstall  = "install"
	StepDone     = "done"
)

// Reporter receives Run's progress. Step is called once per step with a
// human-readable detail; Download is called before the binary download and
// returns a writer fed every byte as it arrives (nil for none) and a finish
// func called once the body is read or fails.
type Reporter interface {
	Step(step, detail string)
	Download(name string, total int64) (io.Writer, func())
}

// TextReporter prints one "update: <step>: <detail>" line per step to W.
// With Bar set (W is a terminal), the download draws a '#' progress bar;
// otherwise the download prints only plain lines, with no carriage returns.
type TextReporter struct {
	W   io.Writer
	Bar bool
}

func (r *TextReporter) Step(step, detail string) {
	fmt.Fprintf(r.W, "update: %s: %s\n", step, detail)
}

func (r *TextReporter) Download(name string, total int64) (io.Writer, func()) {
	if !r.Bar {
		return nil, func() {}
	}
	p := &ProgressBar{W: r.W, Total: total}
	return p, p.Finish
}

// BarWidth is the number of cells in a known-length progress bar.
const BarWidth = 40

// ChunkSize is how many bytes one '#' stands for when the length is unknown.
const ChunkSize = 256 << 10

// ProgressBar draws download progress to W. With Total > 0 it redraws one
// line, "\r  [####....]  42%  1.2 MiB / 2.9 MiB", as bytes arrive; with
// Total <= 0 it appends one '#' per ChunkSize bytes. Finish ends the line.
type ProgressBar struct {
	W     io.Writer
	Total int64
	n     int64
	drawn int // known: last percent drawn (-1 before any); unknown: '#'s printed
	began bool
}

func (p *ProgressBar) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if p.Total > 0 {
		pct := int(p.n * 100 / p.Total)
		if pct > 100 {
			pct = 100
		}
		if !p.began || pct != p.drawn {
			p.began = true
			p.drawn = pct
			p.draw(pct)
		}
		return len(b), nil
	}
	if !p.began {
		p.began = true
		fmt.Fprint(p.W, "  ")
	}
	for want := int(p.n / ChunkSize); p.drawn < want; p.drawn++ {
		fmt.Fprint(p.W, "#")
	}
	return len(b), nil
}

func (p *ProgressBar) draw(pct int) {
	fill := pct * BarWidth / 100
	fmt.Fprintf(p.W, "\r  [%s%s] %3d%%  %s / %s", strings.Repeat("#", fill), strings.Repeat(".", BarWidth-fill), pct, Size(p.n), Size(p.Total))
}

// Finish ends the progress line.
func (p *ProgressBar) Finish() {
	if p.Total > 0 {
		if !p.began {
			p.draw(0)
		}
		fmt.Fprintln(p.W)
		return
	}
	if !p.began {
		fmt.Fprint(p.W, "  ")
	}
	fmt.Fprintf(p.W, " %s\n", Size(p.n))
}

// Size formats a byte count as B, KiB or MiB.
func Size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// JSONReporter writes each step as a JSONL event
// {"type":"update.step","step":...,"detail":...} to W, and no progress bar.
type JSONReporter struct{ W io.Writer }

func (r *JSONReporter) Step(step, detail string) {
	b, _ := json.Marshal(map[string]string{"type": "update.step", "step": step, "detail": detail})
	fmt.Fprintf(r.W, "%s\n", b)
}

func (r *JSONReporter) Download(string, int64) (io.Writer, func()) { return nil, func() {} }

type nopReporter struct{}

func (nopReporter) Step(string, string)                        {}
func (nopReporter) Download(string, int64) (io.Writer, func()) { return nil, func() {} }

// StepError is an error from one step of Run.
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return fmt.Sprintf("update failed at %s: %v", e.Step, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }
