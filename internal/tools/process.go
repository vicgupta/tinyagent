package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxProcesses bounds concurrent background processes when
	// Executor.MaxProcesses is 0.
	DefaultMaxProcesses = 4
	// DefaultProcessBufferBytes caps each background process's retained
	// output when Executor.ProcessBufferBytes is 0.
	DefaultProcessBufferBytes = 1 << 20

	// startWait is how long start_process waits for first output.
	startWait = 500 * time.Millisecond
	// maxReadWaitMs caps how long read_process may block.
	maxReadWaitMs = 60_000
)

// outBuffer is a concurrency-safe output buffer that keeps only the most
// recent max bytes. Offsets count every byte ever written, so readers can
// tell when output they haven't read was dropped.
type outBuffer struct {
	mu      sync.Mutex
	data    []byte
	dropped int // bytes discarded from the front
	max     int
	notify  chan struct{} // closed and replaced on every write
}

func newOutBuffer(max int) *outBuffer {
	return &outBuffer{max: max, notify: make(chan struct{})}
}

func (b *outBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if over := len(b.data) - b.max; over > 0 {
		b.data = append([]byte(nil), b.data[over:]...)
		b.dropped += over
	}
	close(b.notify)
	b.notify = make(chan struct{})
	return len(p), nil
}

// since returns output written at or after offset, the new end offset, and
// how many bytes after offset were dropped by the cap.
func (b *outBuffer) since(offset int) (string, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	lost := 0
	if offset < b.dropped {
		lost = b.dropped - offset
		offset = b.dropped
	}
	return string(b.data[offset-b.dropped:]), b.dropped + len(b.data), lost
}

func (b *outBuffer) end() (int, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped + len(b.data), b.notify
}

type process struct {
	id      string
	command string
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	out     *outBuffer
	read    int // offset of the next unread byte
	done    chan struct{}
	code    int   // exit code, valid after done
	waitErr error // non-exit error from Wait, valid after done
}

func (p *process) status() string {
	select {
	case <-p.done:
		if p.waitErr != nil {
			return "exited (" + p.waitErr.Error() + ")"
		}
		return fmt.Sprintf("exited with code %d", p.code)
	default:
		return "running"
	}
}

// unread returns output since the last read, optionally only its last tail
// lines, and marks it read.
func (p *process) unread(tail int) string {
	s, end, lost := p.out.since(p.read)
	p.read = end
	var note string
	if lost > 0 {
		note = fmt.Sprintf("[%d bytes of earlier output dropped: buffer cap]\n", lost)
	}
	if tail > 0 {
		lines := strings.SplitAfter(s, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > tail {
			note += fmt.Sprintf("[%d earlier lines omitted]\n", len(lines)-tail)
			s = strings.Join(lines[len(lines)-tail:], "")
		}
	}
	return note + s
}

type registry struct {
	mu     sync.Mutex
	procs  map[string]*process
	nextID int
}

func (e *Executor) maxProcesses() int {
	if e.MaxProcesses > 0 {
		return e.MaxProcesses
	}
	return DefaultMaxProcesses
}

func (e *Executor) bufferBytes() int {
	if e.ProcessBufferBytes > 0 {
		return e.ProcessBufferBytes
	}
	return DefaultProcessBufferBytes
}

// StartProcess launches command with bash -c in the background and returns
// its id with any output produced in the first moment.
func (e *Executor) StartProcess(command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command is required")
	}
	e.procs.mu.Lock()
	if e.procs.procs == nil {
		e.procs.procs = map[string]*process{}
	}
	running := 0
	for _, p := range e.procs.procs {
		if p.status() == "running" {
			running++
		}
	}
	if running >= e.maxProcesses() {
		e.procs.mu.Unlock()
		return "", fmt.Errorf("too many background processes (%d running, max %d); stop one with stop_process first", running, e.maxProcesses())
	}
	e.procs.nextID++
	id := "p" + strconv.Itoa(e.procs.nextID)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := shellCommand(ctx, e.Sandbox, command)
	cmd.Dir = e.Root
	out := newOutBuffer(e.bufferBytes())
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		e.procs.mu.Unlock()
		cancel()
		return "", err
	}
	p := &process{id: id, command: command, cmd: cmd, cancel: cancel, out: out, done: make(chan struct{})}
	e.procs.procs[id] = p
	e.procs.mu.Unlock()

	go func() {
		err := cmd.Wait()
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			p.code = exitErr.ExitCode()
		case err != nil:
			p.waitErr = err
		}
		close(p.done)
	}()

	waitForOutput(p, startWait)
	return fmt.Sprintf("<process_id>%s</process_id>\n<status>%s</status>\n<output>\n%s</output>", id, p.status(), p.unread(0)), nil
}

// waitForOutput blocks until p has unread output, exits, or d passes.
func waitForOutput(p *process, d time.Duration) {
	end, notify := p.out.end()
	if end > p.read || d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-notify:
	case <-p.done:
	case <-t.C:
	}
}

func (e *Executor) lookup(id string) (*process, error) {
	e.procs.mu.Lock()
	defer e.procs.mu.Unlock()
	p, ok := e.procs.procs[id]
	if !ok {
		return nil, fmt.Errorf("no background process %q (see start_process)", id)
	}
	return p, nil
}

// ReadProcess returns output produced since the last read. With waitMs > 0 it
// blocks up to that long for new output; with tail > 0 it returns only the
// last tail lines.
func (e *Executor) ReadProcess(ctx context.Context, id string, waitMs, tail int) (string, error) {
	if waitMs < 0 || waitMs > maxReadWaitMs {
		return "", fmt.Errorf("timeout must be between 0 and %d milliseconds, got %d", maxReadWaitMs, waitMs)
	}
	if tail < 0 {
		return "", fmt.Errorf("tail must be positive, got %d", tail)
	}
	p, err := e.lookup(id)
	if err != nil {
		return "", err
	}
	if waitMs > 0 {
		end, notify := p.out.end()
		if end <= p.read {
			t := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
			select {
			case <-notify:
			case <-p.done:
			case <-t.C:
			case <-ctx.Done():
			}
			t.Stop()
		}
	}
	return fmt.Sprintf("<status>%s</status>\n<output>\n%s</output>", p.status(), p.unread(tail)), nil
}

// StopProcess kills id's whole process group and reports its exit status and
// unread output (the last tail lines when tail > 0).
func (e *Executor) StopProcess(id string, tail int) (string, error) {
	p, err := e.lookup(id)
	if err != nil {
		return "", err
	}
	e.kill(p)
	e.procs.mu.Lock()
	delete(e.procs.procs, id)
	e.procs.mu.Unlock()
	return fmt.Sprintf("<status>%s</status>\n<output>\n%s</output>", p.status(), p.unread(tail)), nil
}

// kill cancels p, which kills its process group, and waits for it to exit.
func (e *Executor) kill(p *process) {
	p.cancel()
	<-p.done
}

// cleanupTail is how many trailing lines of each process Close reports.
const cleanupTail = 20

// Close kills every background process still registered and returns a
// summary of each (status and output tail), or "" if there were none. Call
// it when the run ends so nothing is left running.
func (e *Executor) Close() string {
	e.procs.mu.Lock()
	procs := make([]*process, 0, len(e.procs.procs))
	for _, p := range e.procs.procs {
		procs = append(procs, p)
	}
	e.procs.procs = nil
	e.procs.mu.Unlock()
	if len(procs) == 0 {
		return ""
	}
	sort.Slice(procs, func(i, j int) bool {
		a, _ := strconv.Atoi(procs[i].id[1:])
		b, _ := strconv.Atoi(procs[j].id[1:])
		return a < b
	})
	var sb strings.Builder
	for _, p := range procs {
		wasRunning := p.status() == "running"
		e.kill(p)
		action := "already " + p.status()
		if wasRunning {
			action = "killed at run end, " + p.status()
		}
		fmt.Fprintf(&sb, "%s (%s): %s\n%s", p.id, p.command, action, p.unread(cleanupTail))
		if !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// RegisteredProcess describes one background process still registered.
type RegisteredProcess struct {
	ID      string
	PID     int
	Command string
	Running bool
}

// Processes lists the registered background processes in start order.
func (e *Executor) Processes() []RegisteredProcess {
	e.procs.mu.Lock()
	defer e.procs.mu.Unlock()
	out := make([]RegisteredProcess, 0, len(e.procs.procs))
	for _, p := range e.procs.procs {
		rp := RegisteredProcess{ID: p.id, Command: p.command, Running: p.status() == "running"}
		if p.cmd != nil && p.cmd.Process != nil {
			rp.PID = p.cmd.Process.Pid
		}
		out = append(out, rp)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].ID[1:])
		b, _ := strconv.Atoi(out[j].ID[1:])
		return a < b
	})
	return out
}
