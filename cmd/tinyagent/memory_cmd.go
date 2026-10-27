package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/memory"
)

// memoryCmd holds the project memory management flags (issue #170). Each
// one inspects or edits the memory store for --cwd and exits without
// reading credentials or making a model request.
type memoryCmd struct {
	list    bool
	path    bool
	approve string
	forget  string
	yes     bool
	// enabled is --memory: load project memory into this run.
	enabled bool
}

// memoryCommandFlags are the management flags; exactly one may be set.
var memoryCommandFlags = []string{"memory-list", "memory-path", "memory-approve", "memory-forget"}

// memoryAllowedFlags are the only other flags a management command accepts.
var memoryAllowedFlags = map[string]bool{"cwd": true, "json": true, "memory-yes": true}

// registerMemoryFlags adds the management flags to fs.
func registerMemoryFlags(fs *flag.FlagSet, m *memoryCmd) {
	fs.BoolVar(&m.list, "memory-list", false, "print the project memory entries for --cwd (id, status, date, source, text; --json for a JSON array) and exit; makes no model request")
	fs.BoolVar(&m.path, "memory-path", false, "print the project memory store path for --cwd and exit")
	fs.StringVar(&m.approve, "memory-approve", "", "approve the pending project memory entry with this id, or all pending entries, and exit")
	fs.StringVar(&m.forget, "memory-forget", "", "delete the project memory entry with this id, or all entries (all requires --memory-yes), and exit")
	fs.BoolVar(&m.yes, "memory-yes", false, "with --memory-forget all: confirm deleting every entry")
	fs.BoolVar(&m.enabled, "memory", false, "load this project's active memory entries into the system prompt (overrides memory.enabled; --memory=false turns it off); a resume keeps the checkpoint's setting")
}

// memoryCommandSet reports whether any management flag was given.
func memoryCommandSet(fs *flag.FlagSet) bool {
	return flagWasSet(fs, memoryCommandFlags...) || flagWasSet(fs, "memory-yes")
}

// runMemoryCommand implements the memory management flags. Usage errors and
// conflicts with run or mode flags exit 2; a store that can't be read or
// changed exits 1.
func runMemoryCommand(o *options, fs *flag.FlagSet, stdout, stderr io.Writer) int {
	var set []string
	for _, name := range memoryCommandFlags {
		if flagWasSet(fs, name) {
			set = append(set, "--"+name)
		}
	}
	if len(set) == 0 {
		fmt.Fprintln(stderr, "error: --memory-yes applies only to --memory-forget all")
		return 2
	}
	if len(set) > 1 {
		fmt.Fprintf(stderr, "error: %s can't be combined; run one memory command at a time\n", strings.Join(set, " and "))
		return 2
	}
	var conflict string
	fs.Visit(func(f *flag.Flag) {
		if conflict != "" || memoryAllowedFlags[f.Name] {
			return
		}
		for _, name := range memoryCommandFlags {
			if f.Name == name {
				return
			}
		}
		conflict = f.Name
	})
	if conflict != "" {
		dash := "--"
		if len(conflict) == 1 {
			dash = "-"
		}
		fmt.Fprintf(stderr, "error: %s only manages the project memory store; it can't be combined with %s%s\n", set[0], dash, conflict)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected positional argument %q\n", fs.Arg(0))
		return 2
	}
	m := o.memory
	if m.yes && !(flagWasSet(fs, "memory-forget") && m.forget == "all") {
		fmt.Fprintln(stderr, "error: --memory-yes applies only to --memory-forget all")
		return 2
	}
	if o.jsonOutput && !m.list {
		fmt.Fprintln(stderr, "error: --json applies only to --memory-list among the memory commands")
		return 2
	}
	if flagWasSet(fs, "memory-approve") && strings.TrimSpace(m.approve) == "" {
		fmt.Fprintln(stderr, "error: --memory-approve needs an entry id or all")
		return 2
	}
	if flagWasSet(fs, "memory-forget") && strings.TrimSpace(m.forget) == "" {
		fmt.Fprintln(stderr, "error: --memory-forget needs an entry id or all")
		return 2
	}
	if fi, err := os.Stat(o.cwd); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "error: --cwd %q is not a directory\n", o.cwd)
		return 2
	}
	path, err := memory.StorePath("", o.cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error: memory:", err)
		return 1
	}
	switch {
	case m.path:
		fmt.Fprintln(stdout, path)
		return 0
	case m.list:
		return memoryList(path, o.jsonOutput, stdout, stderr)
	case flagWasSet(fs, "memory-approve"):
		return memoryApprove(path, strings.TrimSpace(m.approve), stdout, stderr)
	default:
		return memoryForget(path, strings.TrimSpace(m.forget), m.yes, stdout, stderr)
	}
}

// memoryListEntry is one --memory-list --json element.
type memoryListEntry struct {
	ID        string        `json:"id"`
	Status    string        `json:"status"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
	Source    memory.Source `json:"source"`
	Replaces  string        `json:"replaces,omitempty"`
	Text      string        `json:"text"`
}

// sortedEntries returns the entries pending first, then newest first.
func sortedEntries(entries []memory.Entry) []memory.Entry {
	out := append([]memory.Entry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Status == memory.StatusPending, out[j].Status == memory.StatusPending
		if pi != pj {
			return pi
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func memoryList(path string, jsonOut bool, stdout, stderr io.Writer) int {
	entries, err := memory.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "error: memory:", err)
		return 1
	}
	entries = sortedEntries(entries)
	if jsonOut {
		out := make([]memoryListEntry, 0, len(entries))
		for _, e := range entries {
			out = append(out, memoryListEntry{
				ID: e.ID, Status: e.Status,
				CreatedAt: e.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
				UpdatedAt: e.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
				Source:    e.Source, Replaces: e.Replaces, Text: e.Text,
			})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, "error: memory:", err)
			return 1
		}
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "no project memory entries (store: %s)\n", path)
		return 0
	}
	for _, e := range entries {
		fmt.Fprintf(stdout, "%s  %-7s  %s  %s\n", e.ID, e.Status, e.CreatedAt.UTC().Format("2006-01-02"), describeSource(e))
		fmt.Fprintf(stdout, "    %s\n", e.Text)
	}
	return 0
}

// describeSource summarizes an entry's provenance on one line.
func describeSource(e memory.Entry) string {
	var parts []string
	if e.Source.RunID != "" {
		parts = append(parts, "run "+e.Source.RunID)
	}
	if e.Source.Model != "" {
		parts = append(parts, "model "+e.Source.Model)
	}
	if e.Source.Version != "" {
		parts = append(parts, "tinyagent "+e.Source.Version)
	}
	if e.Source.UntrustedContext {
		parts = append(parts, "untrusted context")
	}
	if e.Replaces != "" {
		parts = append(parts, "replaces "+e.Replaces)
	}
	if len(parts) == 0 {
		return "source unknown"
	}
	return strings.Join(parts, ", ")
}

func memoryApprove(path, id string, stdout, stderr io.Writer) int {
	all := id == "all"
	n, err := memory.Approve(path, id, all)
	if err != nil {
		return memoryErr(err, id, stderr)
	}
	if all {
		fmt.Fprintf(stdout, "approved %d pending %s\n", n, plural(n, "entry", "entries"))
		return 0
	}
	fmt.Fprintf(stdout, "approved %s\n", id)
	return 0
}

func memoryForget(path, id string, yes bool, stdout, stderr io.Writer) int {
	if id == "all" {
		if !yes {
			fmt.Fprintln(stderr, "error: --memory-forget all deletes every entry for this project; add --memory-yes to confirm")
			return 2
		}
		n, err := memory.ForgetAll(path)
		if err != nil {
			return memoryErr(err, id, stderr)
		}
		fmt.Fprintf(stdout, "forgot %d %s\n", n, plural(n, "entry", "entries"))
		return 0
	}
	if err := memory.Forget(path, id); err != nil {
		return memoryErr(err, id, stderr)
	}
	fmt.Fprintf(stdout, "forgot %s\n", id)
	return 0
}

// memoryErr reports a store error: an unknown id exits 2 (a usage error),
// anything else (a corrupt store, a held lock, I/O) exits 1.
func memoryErr(err error, id string, stderr io.Writer) int {
	if errors.Is(err, memory.ErrNotFound) {
		fmt.Fprintf(stderr, "error: memory: no entry with id %q (see --memory-list)\n", id)
		return 2
	}
	fmt.Fprintln(stderr, "error: memory:", err)
	return 1
}
