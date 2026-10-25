package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/snapshot"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// checkSnapshots applies the read-only rule and the -o requirement for a new
// run. It returns a nonzero exit code to stop with.
func checkSnapshots(cfg *config.AgentConfig, output string) int {
	if !cfg.Snapshots.Enabled {
		return 0
	}
	if cfg.ReadOnly {
		fmt.Fprintln(os.Stderr, "read-only mode: workspace snapshots are not taken")
		cfg.Snapshots.Enabled = false
		return 0
	}
	if output == "" {
		fmt.Fprintln(os.Stderr, "error: --snapshots requires -o/--output: the trajectory file indexes the snapshots for --rewind")
		return 2
	}
	return 0
}

// openSnapshots opens the snapshot store when snapshots are on. The
// trajectory files, the event log and the tool output directory are left
// out of every snapshot.
func openSnapshots(fs *flag.FlagSet, o options, cfg config.AgentConfig, root string, trajectories ...string) (agent.Snapshotter, error) {
	if !cfg.Snapshots.Enabled {
		return nil, nil
	}
	var exclude []string
	for _, p := range trajectories {
		if abs, err := filepath.Abs(p); err == nil {
			exclude = append(exclude, canonicalFile(abs))
		}
	}
	if on, p := logDBPath(fs, o, cfg.LogDB, root); on {
		exclude = append(exclude, canonicalFile(p))
	}
	// tool_output_files saves tinyagent's own output; a rewind must not
	// delete it (the default directory is under .tinyagent already).
	on := cfg.ToolOutputFiles.Enabled
	if flagWasSet(fs, "tool-output-files") {
		on = o.toolOutputFiles
	}
	if dir, err := tools.ResolveOutputDir(root, cfg.ToolOutputFiles.Dir); on && err == nil {
		exclude = append(exclude, canonicalFile(dir))
	}
	s, err := snapshot.Open(root, snapshot.Options{MaxFileBytes: cfg.Snapshots.MaxFileBytes, Exclude: exclude})
	if err != nil {
		return nil, fmt.Errorf("snapshots: %w", err)
	}
	return s, nil
}

// canonicalFile resolves symlinks in p's directory, so it compares equal to
// paths found by walking the canonical root.
func canonicalFile(p string) string {
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(dir, filepath.Base(p))
	}
	return p
}

// pickSnapshot resolves --to against the trajectory's snapshots. note
// explains a step that had no snapshot of its own.
func pickSnapshot(snaps []trajectory.Snapshot, to string) (snap trajectory.Snapshot, note string, err error) {
	if to == "" || to == "start" {
		for _, s := range snaps {
			if s.Kind == "baseline" {
				return s, "", nil
			}
		}
		return snaps[0], "", nil
	}
	if n, convErr := strconv.Atoi(to); convErr == nil {
		if n < 0 {
			return snap, "", fmt.Errorf("--to %q: step must be >= 0", to)
		}
		found := -1
		for i, s := range snaps {
			if (s.Kind == "baseline" || s.Kind == "step") && s.Step <= n && (found < 0 || s.Step > snaps[found].Step) {
				found = i
			}
		}
		if found < 0 {
			return snap, "", fmt.Errorf("--to %d: no snapshot at or before step %d", n, n)
		}
		s := snaps[found]
		if s.Step != n {
			note = fmt.Sprintf("no snapshot at step %d; using %s, the latest at or before it (%s, step %d)", n, s.ID, s.Kind, s.Step)
		}
		return s, note, nil
	}
	for _, s := range snaps {
		if s.ID == to {
			return s, "", nil
		}
	}
	return snap, "", fmt.Errorf("--to %q: no such snapshot in this trajectory (use start, a step number, or a snapshot id)", to)
}

// loadForRewind reads a trajectory for --rewind. Unlike resume it accepts
// any finished or unfinished run, including ones that cannot be resumed
// (MCP, --output-schema), but refuses fields it doesn't know so that saving
// the pre-rewind snapshot never drops data.
func loadForRewind(path string) (*trajectory.Trajectory, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading trajectory: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("trajectory %s must be a regular file", path)
	}
	if fi.Size() > 64<<20 {
		return nil, fmt.Errorf("trajectory %s exceeds 64 MiB", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading trajectory: %w", err)
	}
	var t *trajectory.Trajectory
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil || t == nil {
		return nil, fmt.Errorf("%s is not a trajectory JSON this version of tinyagent understands", path)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%s: trailing data after the trajectory JSON", path)
	}
	return t, nil
}

// runRewind restores --cwd to a snapshot. Usage errors and problems found
// before any write exit 2; a failure while restoring exits 1.
func runRewind(o *options, fs *flag.FlagSet) int {
	usage := func(format string, args ...any) int {
		fmt.Fprintf(os.Stderr, "error: rewind: "+format+"\n", args...)
		return 2
	}
	var stdout io.Writer = os.Stdout
	var events *eventlog.JSONL
	if o.jsonOutput {
		events = eventlog.NewJSONL(os.Stdout)
		stdout = io.Discard
	}
	emit := func(kind string, fields map[string]any) {
		if events != nil {
			events.Emit(kind, fields)
		}
	}
	if !flagWasSet(fs, "cwd") {
		return usage("an explicit --cwd matching the trajectory's root is required")
	}
	t, err := loadForRewind(o.rewind)
	if err != nil {
		return usage("%v", err)
	}
	if len(t.Snapshots) == 0 {
		return usage("%s has no workspace snapshots; record the run with --snapshots -o <file>", o.rewind)
	}
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		return usage("%v", err)
	}
	if root != t.Root {
		return usage("--cwd %s does not match the trajectory's root %s", root, t.Root)
	}
	target, note, err := pickSnapshot(t.Snapshots, o.rewindTo)
	if err != nil {
		return usage("%v", err)
	}
	if note != "" {
		fmt.Fprintln(stdout, note)
	}
	if want := filepath.Join(root, filepath.FromSlash(snapshot.StoreDir)); target.Store != want {
		return usage("snapshot %s names store %s; expected %s", target.ID, target.Store, want)
	}
	abs, err := filepath.Abs(o.rewind)
	if err != nil {
		return usage("%v", err)
	}
	opts := snapshot.Options{Exclude: []string{canonicalFile(abs)}}
	store, err := snapshot.OpenExisting(root, target.Store, opts)
	if err != nil {
		return usage("%v", err)
	}
	m, err := store.Load(target.ID)
	if err != nil {
		return usage("%v", err)
	}
	// The current tree is compared within the target's own scope.
	for _, x := range m.Excluded {
		opts.Exclude = append(opts.Exclude, filepath.Join(root, filepath.FromSlash(x)))
	}
	opts.MaxFileBytes = m.MaxFileBytes
	if store, err = snapshot.OpenExisting(root, target.Store, opts); err != nil {
		return usage("%v", err)
	}
	plan, err := store.PlanRestore(m)
	if err != nil {
		return usage("%v", err)
	}
	nm, na, nd := plan.Counts()
	fmt.Fprintf(stdout, "rewind to %s (%s, step %d, %s)\n", target.ID, target.Kind, target.Step, target.Time)
	changes := make([]map[string]any, 0, len(plan.Changes))
	for _, c := range plan.Changes {
		fmt.Fprintf(stdout, "%s %s\n", c.Op, c.Path)
		changes = append(changes, map[string]any{"op": c.Op, "path": c.Path})
	}
	fmt.Fprintf(stdout, "%d modified, %d added (to remove), %d deleted (to recreate)\n", nm, na, nd)
	planFields := map[string]any{"target": target.ID, "step": target.Step, "kind": target.Kind, "changes": changes,
		"modified": nm, "added": na, "deleted": nd, "dry_run": o.dryRun}
	if note != "" {
		planFields["note"] = note
	}
	emit("rewind.plan", planFields)
	warnLarge := func() {
		for _, p := range m.Large {
			msg := fmt.Sprintf("not restored: %s was over snapshots.max_file_bytes (%d) when snapshotted", p, m.MaxFileBytes)
			fmt.Fprintln(os.Stderr, "warning:", msg)
			emit("warning", map[string]any{"message": msg})
		}
	}
	if o.dryRun {
		warnLarge()
		fmt.Fprintln(stdout, "dry run: nothing changed")
		return 0
	}
	if len(plan.Changes) == 0 {
		warnLarge()
		fmt.Fprintln(stdout, "nothing to restore")
		emit("rewind.result", map[string]any{"target": target.ID, "restored": 0, "not_restored": 0})
		return 0
	}

	pre, err := store.Take("pre-rewind", t.Steps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: rewind: pre-rewind snapshot failed; nothing restored:", err)
		return 1
	}
	t.Snapshots = append(t.Snapshots, pre)
	if err := t.Save(o.rewind); err != nil {
		fmt.Fprintln(os.Stderr, "error: rewind: recording the pre-rewind snapshot failed; nothing restored:", err)
		return 1
	}
	emit("workspace.snapshot", map[string]any{"id": pre.ID, "step": pre.Step, "kind": pre.Kind, "time": pre.Time, "files": pre.Files,
		"bytes": pre.Bytes, "skipped_ignored": pre.SkippedIgnored, "skipped_large": pre.SkippedLarge, "store": pre.Store})
	fmt.Fprintf(stdout, "pre-rewind snapshot %s; undo with: tinyagent --rewind %s --cwd %s --to %s\n", pre.ID, o.rewind, root, pre.ID)

	res := store.Apply(plan)
	warnLarge()
	result := map[string]any{"target": target.ID, "pre_rewind": pre.ID, "restored": len(res.Restored), "not_restored": len(res.NotRestored)}
	if res.Err != nil {
		fmt.Fprintln(os.Stderr, "error: rewind stopped:", res.Err)
		for _, p := range res.Restored {
			fmt.Fprintln(os.Stderr, "  restored:", p)
		}
		for _, p := range res.NotRestored {
			fmt.Fprintln(os.Stderr, "  not restored:", p)
		}
		fmt.Fprintf(os.Stderr, "the workspace before this rewind is snapshot %s (--to %s)\n", pre.ID, pre.ID)
		result["error"] = res.Err.Error()
		result["not_restored_paths"] = res.NotRestored
		emit("rewind.result", result)
		return 1
	}
	fmt.Fprintf(stdout, "restored %d paths to %s\n", len(res.Restored), target.ID)
	emit("rewind.result", result)
	if events != nil && events.Err() != nil {
		return 1
	}
	return 0
}
