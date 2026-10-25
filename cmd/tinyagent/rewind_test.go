package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func mustWrite(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func loadTraj(t *testing.T, p string) *trajectory.Trajectory {
	t.Helper()
	tr, err := loadForRewind(p)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// snapshotRun runs one task against a fake model whose replies are bash
// commands, with snapshots on and the trajectory written outside dir.
func snapshotRun(t *testing.T, dir string, extra []string, commands ...string) string {
	t.Helper()
	newEnvOpenAI(t, "test", commands...)
	traj := filepath.Join(t.TempDir(), "run.json")
	args := append([]string{"-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--snapshots", "-o", traj}, extra...)
	if code, _, stderr := runCLI(t, args...); code != 0 {
		t.Fatalf("run exit %d: %s", code, stderr)
	}
	return traj
}

func TestSnapshotsRequireOutput(t *testing.T) { // AC2
	f := newEnvOpenAI(t, "test")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", t.TempDir(), "--snapshots")
	if code != 2 || !strings.Contains(stderr, "-o") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if len(f.bodies) != 0 {
		t.Error("provider contacted")
	}
	cfg := writeConfig(t, "snapshots: true\n")
	if code, _, _ := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", t.TempDir(), "-c", cfg); code != 2 {
		t.Errorf("config snapshots without -o: exit %d, want 2", code)
	}
	// The flag wins over config.
	dir := t.TempDir()
	if code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfg, "--snapshots=false"); code != 0 {
		t.Errorf("--snapshots=false over config: exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tinyagent")); err == nil {
		t.Error("store created with --snapshots=false")
	}
}

func TestSnapshotsReadOnlyNote(t *testing.T) {
	f := newEnvOpenAI(t, "test")
	dir := t.TempDir()
	traj := filepath.Join(t.TempDir(), "run.json")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--read-only", "--snapshots", "-o", traj)
	if code != 0 || strings.Count(stderr, "snapshots are not taken") != 1 || len(f.bodies) != 1 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if tr := loadTraj(t, traj); tr.SnapshotsEnabled || len(tr.Snapshots) != 0 {
		t.Errorf("read-only run recorded snapshots: %+v", tr.Snapshots)
	}
}

func TestSnapshotStoreUnwritable(t *testing.T) { // AC9
	f := newEnvOpenAI(t, "test")
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".tinyagent"), "not a dir", 0o644)
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--snapshots", "-o", filepath.Join(t.TempDir(), "r.json"))
	if code == 0 || !strings.Contains(stderr, "snapshot") || len(f.bodies) != 0 {
		t.Errorf("exit %d, requests %d, stderr %q", code, len(f.bodies), stderr)
	}
}

func TestRewindRoundTrip(t *testing.T) { // AC4, AC7, AC1-style counts
	if runtime.GOOS == "windows" {
		t.Skip("uses bash and the executable bit")
	}
	t.Setenv("OPENAI_API_KEY", "")
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a.txt"), "one\n", 0o755)
	mustWrite(t, filepath.Join(dir, "b.txt"), "bee\n", 0o644)
	traj := snapshotRun(t, dir, nil, "printf 'two\\n' > a.txt && chmod 644 a.txt && rm b.txt && echo new > c.txt")
	tr := loadTraj(t, traj)
	var kinds []string
	for _, s := range tr.Snapshots {
		kinds = append(kinds, s.Kind)
	}
	if strings.Join(kinds, ",") != "baseline,step,final" {
		t.Fatalf("snapshots = %v", kinds)
	}

	os.Unsetenv("OPENAI_API_KEY") // rewind needs no credentials
	code, out, stderr := runCLI(t, "--rewind", traj, "--cwd", dir, "--dry-run")
	if code != 0 {
		t.Fatalf("dry run exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "\nM a.txt\nD b.txt\nA c.txt\n1 modified, 1 added (to remove), 1 deleted (to recreate)\n") {
		t.Errorf("dry run output:\n%s", out)
	}
	if mustRead(t, filepath.Join(dir, "a.txt")) != "two\n" || len(loadTraj(t, traj).Snapshots) != 3 {
		t.Fatal("dry run changed something")
	}

	code, out, stderr = runCLI(t, "--rewind", traj, "--cwd", dir, "--to", "start")
	if code != 0 {
		t.Fatalf("rewind exit %d: %s", code, stderr)
	}
	fi, _ := os.Stat(filepath.Join(dir, "a.txt"))
	if mustRead(t, filepath.Join(dir, "a.txt")) != "one\n" || fi.Mode().Perm()&0o100 == 0 || mustRead(t, filepath.Join(dir, "b.txt")) != "bee\n" {
		t.Error("a.txt or b.txt not restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err == nil {
		t.Error("c.txt not removed")
	}
	snaps := loadTraj(t, traj).Snapshots
	pre := snaps[len(snaps)-1]
	if pre.Kind != "pre-rewind" || !strings.Contains(out, pre.ID) || !strings.Contains(out, "--to "+pre.ID) {
		t.Fatalf("pre-rewind %+v not reported in:\n%s", pre, out)
	}

	if code, _, stderr := runCLI(t, "--rewind", traj, "--cwd", dir, "--to", pre.ID); code != 0 {
		t.Fatalf("undo exit %d: %s", code, stderr)
	}
	if mustRead(t, filepath.Join(dir, "a.txt")) != "two\n" || mustRead(t, filepath.Join(dir, "c.txt")) != "new\n" {
		t.Error("undo did not bring back the pre-rewind state")
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err == nil {
		t.Error("undo did not remove b.txt")
	}
}

func TestRewindToStep(t *testing.T) { // AC5
	if runtime.GOOS == "windows" {
		t.Skip("uses bash")
	}
	dir := t.TempDir()
	traj := snapshotRun(t, dir, nil, "echo 1 > s.txt", "echo 2 > s.txt", "echo 3 > s.txt")
	// Step N's snapshot is the state before step N's tools ran.
	if code, _, stderr := runCLI(t, "--rewind", traj, "--cwd", dir, "--to", "3"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := mustRead(t, filepath.Join(dir, "s.txt")); got != "2\n" {
		t.Errorf("--to 3: s.txt = %q, want 2", got)
	}
	code, out, _ := runCLI(t, "--rewind", traj, "--cwd", dir, "--to", "start", "--dry-run", "--json")
	if code != 0 || !strings.Contains(out, "rewind.plan") || !strings.Contains(out, `"path":"s.txt"`) || strings.Contains(out, "\nA ") {
		t.Errorf("--json dry run: exit %d, output:\n%s", code, out)
	}
	tr := loadTraj(t, traj)
	tr.Snapshots = tr.Snapshots[:1] // keep only the baseline
	if err := tr.Save(traj); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCLI(t, "--rewind", traj, "--cwd", dir, "--to", "2", "--dry-run")
	if code != 0 || !strings.Contains(out, "no snapshot at step 2; using "+tr.Snapshots[0].ID) {
		t.Errorf("exit %d, output:\n%s", code, out)
	}
}

func TestPickSnapshot(t *testing.T) {
	snaps := []trajectory.Snapshot{{ID: "b", Kind: "baseline"}, {ID: "s2", Kind: "step", Step: 2}, {ID: "s5", Kind: "step", Step: 5}, {ID: "f", Kind: "final", Step: 6}}
	for to, want := range map[string]string{"": "b", "start": "b", "0": "b", "2": "s2", "3": "s2", "5": "s5", "9": "s5", "f": "f"} {
		s, note, err := pickSnapshot(snaps, to)
		if err != nil || s.ID != want {
			t.Errorf("--to %q = %s, %v; want %s", to, s.ID, err, want)
		}
		if (note != "") != (to == "3" || to == "9") {
			t.Errorf("--to %q note %q", to, note)
		}
	}
	for _, to := range []string{"-1", "nope"} {
		if _, _, err := pickSnapshot(snaps, to); err == nil {
			t.Errorf("--to %q accepted", to)
		}
	}
}

func TestRewindErrorsExit2WithoutWriting(t *testing.T) { // AC10
	if runtime.GOOS == "windows" {
		t.Skip("uses bash")
	}
	dir := t.TempDir()
	traj := snapshotRun(t, dir, nil, "echo changed > a.txt")
	before := mustRead(t, traj)
	noSnaps := filepath.Join(t.TempDir(), "plain.json")
	tr := loadTraj(t, traj)
	tr.Snapshots, tr.SnapshotsEnabled = nil, false
	if err := tr.Save(noSnaps); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"no --cwd":      {"--rewind", traj},
		"other root":    {"--rewind", traj, "--cwd", t.TempDir()},
		"unknown --to":  {"--rewind", traj, "--cwd", dir, "--to", "snap-ffffffffffff"},
		"unknown step":  {"--rewind", traj, "--cwd", dir, "--to", "-3"},
		"no snapshots":  {"--rewind", noSnaps, "--cwd", dir},
		"with --resume": {"--rewind", traj, "--resume", traj, "--cwd", dir},
		"stray --to":    {"-t", "x", "--to", "start"},
	}
	for name, args := range cases {
		if code, _, _ := runCLI(t, args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	if mustRead(t, filepath.Join(dir, "a.txt")) != "changed\n" || mustRead(t, traj) != before {
		t.Error("a failed rewind wrote something")
	}
	// A missing store is caught before any write too.
	os.RemoveAll(filepath.Join(dir, ".tinyagent"))
	if code, _, _ := runCLI(t, "--rewind", traj, "--cwd", dir); code != 2 || mustRead(t, filepath.Join(dir, "a.txt")) != "changed\n" {
		t.Errorf("missing store: exit %d", code)
	}
}

func TestRewindLeavesIgnoredAndLargeFiles(t *testing.T) { // AC8
	if runtime.GOOS == "windows" {
		t.Skip("uses bash")
	}
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	mustWrite(t, filepath.Join(dir, ".gitignore"), "*.log\n", 0o644)
	mustWrite(t, filepath.Join(dir, "app.log"), "old\n", 0o644)
	mustWrite(t, filepath.Join(dir, "big.bin"), strings.Repeat("a", 200), 0o644)
	cfg := writeConfig(t, "snapshots:\n  max_file_bytes: 100\n")
	traj := snapshotRun(t, dir, []string{"-c", cfg}, "echo new > app.log && printf %0200d 0 > big.bin && echo x > src.txt")
	base := loadTraj(t, traj).Snapshots[0]
	if base.SkippedIgnored != 1 || base.SkippedLarge != 1 {
		t.Errorf("baseline = %+v", base)
	}
	code, out, stderr := runCLI(t, "--rewind", traj, "--cwd", dir)
	if code != 0 || !strings.Contains(out, "A src.txt\n0 modified, 1 added") {
		t.Fatalf("exit %d, out %q, stderr %q", code, out, stderr)
	}
	if !strings.Contains(stderr, "not restored: big.bin") {
		t.Errorf("no not-restored warning: %q", stderr)
	}
	if mustRead(t, filepath.Join(dir, "app.log")) != "new\n" || !strings.HasPrefix(mustRead(t, filepath.Join(dir, "big.bin")), "000") {
		t.Error("rewind touched an ignored or oversized file")
	}
}

func TestRewindKeepsGitStateAndUserChanges(t *testing.T) { // AC6
	if _, err := exec.LookPath("git"); err != nil || runtime.GOOS == "windows" {
		t.Skip("needs git and bash")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	mustWrite(t, filepath.Join(dir, "tracked.txt"), "v1\n", 0o644)
	git("add", "tracked.txt")
	git("commit", "-q", "-m", "init")
	mustWrite(t, filepath.Join(dir, "tracked.txt"), "user edit\n", 0o644) // uncommitted
	mustWrite(t, filepath.Join(dir, "staged.txt"), "staged\n", 0o644)
	git("add", "staged.txt")
	state := func() string {
		return git("rev-parse", "HEAD") + git("stash", "list") + git("branch", "--list") + git("diff", "--cached")
	}
	before := state()
	traj := snapshotRun(t, dir, nil, "echo agent > tracked.txt && echo junk > new.txt")
	if code, _, stderr := runCLI(t, "--rewind", traj, "--cwd", dir, "--to", "start"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if mustRead(t, filepath.Join(dir, "tracked.txt")) != "user edit\n" {
		t.Error("uncommitted user change lost")
	}
	if after := state(); after != before {
		t.Errorf("git state changed:\nbefore %s\nafter %s", before, after)
	}
	if strings.Contains(git("status", "--porcelain"), "new.txt") {
		t.Error("new.txt still present")
	}
}

func TestResumeCarriesSnapshots(t *testing.T) { // AC13
	if runtime.GOOS == "windows" {
		t.Skip("uses bash")
	}
	dir := t.TempDir()
	newEnvOpenAI(t, "test", "echo 1 > s.txt", "echo 2 > s.txt")
	first := filepath.Join(t.TempDir(), "first.json")
	if code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--snapshots", "-o", first, "--step-limit", "1"); code == 0 {
		t.Fatalf("expected the step limit to stop the run: %s", stderr)
	}
	firstSnaps := loadTraj(t, first).Snapshots
	second := filepath.Join(t.TempDir(), "second.json")
	if code, _, stderr := runCLI(t, "--resume", first, "--cwd", dir, "-o", second, "--snapshots", "--no-stream", "--step-limit", "5"); code != 0 {
		t.Fatalf("resume exit %d: %s", code, stderr)
	}
	snaps := loadTraj(t, second).Snapshots
	if len(snaps) <= len(firstSnaps) || snaps[0].ID != firstSnaps[0].ID {
		t.Fatalf("resumed snapshots = %+v, first = %+v", snaps, firstSnaps)
	}
	if code, _, stderr := runCLI(t, "--rewind", second, "--cwd", dir, "--to", "1"); code != 0 {
		t.Fatalf("rewind exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "s.txt")); err == nil {
		t.Error("--to 1 should restore the state before the first step's tools")
	}
}

func TestRewindContainerCreatedFile(t *testing.T) { // AC11
	if runtime.GOOS != "linux" {
		t.Skip("container tests run on Linux")
	}
	rt := ""
	for _, c := range []string{os.Getenv("TINYAGENT_TEST_RUNTIME"), "docker", "podman"} {
		if c != "" {
			if _, err := exec.LookPath(c); err == nil && exec.Command(c, "info").Run() == nil {
				rt = c
				break
			}
		}
	}
	if rt == "" {
		t.Skip("no working docker or podman; skipping the container rewind test")
	}
	dir := t.TempDir()
	cfg := writeConfig(t, "container: {runtime: "+rt+", image: debian:bookworm-slim}\n")
	traj := snapshotRun(t, dir, []string{"-c", cfg, "--env", "container"}, "echo hi > made-in-container.txt")
	code, out, stderr := runCLI(t, "--rewind", traj, "--cwd", dir)
	if code != 0 || !strings.Contains(out, "A made-in-container.txt\n") {
		t.Fatalf("exit %d, out %q, stderr %q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "made-in-container.txt")); err == nil {
		t.Error("container-created file not removed")
	}
}

func TestRewindLeavesToolOutputDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses bash")
	}
	dir := t.TempDir()
	cfg := writeConfig(t, "tool_output_files: {enabled: true, dir: saved}\n")
	traj := snapshotRun(t, dir, []string{"-c", cfg}, "mkdir -p saved && echo out > saved/x.txt && echo y > y.txt")
	code, out, stderr := runCLI(t, "--rewind", traj, "--cwd", dir)
	if code != 0 || strings.Contains(out, "saved") || !strings.Contains(out, "A y.txt\n") {
		t.Fatalf("exit %d, out %q, stderr %q", code, out, stderr)
	}
	if mustRead(t, filepath.Join(dir, "saved", "x.txt")) != "out\n" {
		t.Error("rewind touched the tool output directory")
	}
}
