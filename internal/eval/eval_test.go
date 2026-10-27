package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func gitRepo(t *testing.T) (dir, sha string) {
	t.Helper()
	dir = t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(out))
}

func writeSuite(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "suite.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValidation(t *testing.T) {
	repo, _ := gitRepo(t)
	notGit := t.TempDir()
	ok := "version: 1\ntrials: 1\nmodels: [openai/gpt-4o]\ntasks:\n  - id: a\n    repo: " + repo + "\n    base: HEAD\n    task: do it\n    checks: [{argv: [git, --version]}]\n"
	s, err := Load(writeSuite(t, ok), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.Profiles, []string{"default"}) || s.Tasks[0].Checks[0].TimeoutSeconds != DefaultCheckTimeout {
		t.Errorf("defaults not applied: %+v", s)
	}
	cases := map[string]struct{ body, want string }{
		"unknown key":    {strings.Replace(ok, "task: do it", "task: do it\n    bogus: 1", 1), "field bogus not found"},
		"no trials":      {strings.Replace(ok, "trials: 1\n", "", 1), "trials is required"},
		"version":        {strings.Replace(ok, "version: 1", "version: 2", 1), "version must be 1"},
		"no models":      {strings.Replace(ok, "models: [openai/gpt-4o]", "models: []", 1), "at least one model"},
		"dup model":      {strings.Replace(ok, "[openai/gpt-4o]", "[a/b, a/b]", 1), "duplicate entry"},
		"no checks":      {strings.Replace(ok, "    checks: [{argv: [git, --version]}]\n", "", 1), "at least one check"},
		"empty argv":     {strings.Replace(ok, "argv: [git, --version]", "argv: []", 1), "argv must be"},
		"no task":        {strings.Replace(ok, "task: do it", "task: \"\"", 1), "task or task_file is required"},
		"both task":      {strings.Replace(ok, "task: do it", "task: x\n    task_file: y.md", 1), "not both"},
		"missing file":   {strings.Replace(ok, "task: do it", "task_file: nope.md", 1), "task_file"},
		"not git":        {strings.Replace(ok, "repo: "+repo, "repo: "+notGit, 1), "not a Git repository"},
		"bad base":       {strings.Replace(ok, "base: HEAD", "base: no-such-ref", 1), "does not resolve"},
		"bad id":         {strings.Replace(ok, "id: a", "id: \"a b\"", 1), "id \"a b\""},
		"unknown prof":   {ok + "profiles: [ci]\n", "unknown profile \"ci\""},
		"negative limit": {strings.Replace(ok, "task: do it", "task: do it\n    limits: {step_limit: -1}", 1), "limits must be"},
		"ambiguous id":   {strings.Replace(ok, "[openai/gpt-4o]", "[a/b, a-b]", 1), "ambiguous"},
	}
	for name, c := range cases {
		_, err := Load(writeSuite(t, c.body), func(p string) bool { return p == "known" })
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestExpandAndTaskFile(t *testing.T) {
	repo, _ := gitRepo(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "t.md"), []byte("from file"), 0o600)
	p := filepath.Join(dir, "s.yaml")
	os.WriteFile(p, []byte("version: 1\nmodels: [openai/gpt-4o, anthropic/claude-x]\nprofiles: [default, ci]\ntrials: 2\ntasks:\n"+
		"  - {id: a, repo: "+repo+", base: HEAD, task_file: t.md, checks: [{argv: [x]}]}\n"+
		"  - {id: b, repo: "+repo+", base: HEAD, task: y, model: openai/o3, profile: ci, checks: [{argv: [x]}]}\n"), 0o600)
	s, err := Load(p, func(n string) bool { return n == "ci" })
	if err != nil {
		t.Fatal(err)
	}
	if s.Tasks[0].prompt != "from file" {
		t.Errorf("prompt = %q", s.Tasks[0].prompt)
	}
	var ids []string
	for _, tr := range s.Expand() {
		ids = append(ids, tr.ID)
	}
	want := []string{
		"a.openai-gpt-4o.default.1", "a.openai-gpt-4o.default.2", "a.openai-gpt-4o.ci.1", "a.openai-gpt-4o.ci.2",
		"a.anthropic-claude-x.default.1", "a.anthropic-claude-x.default.2", "a.anthropic-claude-x.ci.1", "a.anthropic-claude-x.ci.2",
		"b.openai-o3.ci.1", "b.openai-o3.ci.2",
	}
	if !slices.Equal(ids, want) {
		t.Errorf("ids = %v\nwant %v", ids, want)
	}
}

func TestPassAtK(t *testing.T) {
	for _, c := range []struct {
		n, c, k int
		want    float64
	}{{5, 0, 1, 0}, {5, 5, 1, 1}, {5, 2, 1, 0.4}, {5, 2, 2, 0.7}, {5, 4, 2, 1}, {3, 1, 3, 1}, {2, 1, 3, 0}} {
		if got := PassAtK(c.n, c.c, c.k); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("PassAtK(%d,%d,%d) = %v, want %v", c.n, c.c, c.k, got, c.want)
		}
	}
	if got := PassHatK(5, 2, 2); math.Abs(got-0.1) > 1e-9 {
		t.Errorf("PassHatK = %v", got)
	}
}

// fakeAgent writes a trajectory to the -o path with the status the prompt
// asks for ("status=X") and records the args it saw.
type fakeAgent struct {
	calls  [][]string
	onCall func(n int)
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func (f *fakeAgent) run(ctx context.Context, args []string, log io.Writer) error {
	f.calls = append(f.calls, args)
	if f.onCall != nil {
		f.onCall(len(f.calls))
	}
	status, _ := strings.CutPrefix(argAfter(args, "-t"), "status=")
	status, _, _ = strings.Cut(status, " ")
	if status == "none" {
		return io.ErrUnexpectedEOF
	}
	// Leave a file behind so checks can see the agent ran in the worktree.
	os.WriteFile(filepath.Join(argAfter(args, "--cwd"), "agent.txt"), []byte("x"), 0o600)
	b, _ := json.Marshal(map[string]any{"exit_status": status, "steps": 2, "cost": 0.5, "input_tokens": 10, "output_tokens": 3, "active_model": argAfter(args, "-m")})
	return os.WriteFile(argAfter(args, "-o"), b, 0o600)
}

func readResults(t *testing.T, dir string) []Record {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	return recs
}

func worktrees(t *testing.T, repo string) int {
	out, _ := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
	return strings.Count(string(out), "worktree ")
}

func TestRunOutcomes(t *testing.T) {
	repo, sha := gitRepo(t)
	pass := "[git, --version]"
	fail := "[git, no-such-subcommand]"
	seeAgent := "[git, ls-files, --error-unmatch, --others, agent.txt]"
	body := "version: 1\nmodels: [openai/gpt-4o]\ntrials: 1\ntasks:\n" +
		"  - {id: pass, repo: " + repo + ", base: HEAD, task: status=Submitted, checks: [{argv: " + pass + "}, {argv: " + seeAgent + "}, {argv: [git, rev-parse, HEAD]}]}\n" +
		"  - {id: fail, repo: " + repo + ", base: HEAD, task: status=Completed, checks: [{argv: " + fail + "}]}\n" +
		"  - {id: limit, repo: " + repo + ", base: HEAD, task: status=StepLimitExceeded, limits: {step_limit: 3, cost_limit: 0.25, time_limit: 9}, checks: [{argv: " + pass + "}]}\n" +
		"  - {id: limitnochk, repo: " + repo + ", base: HEAD, task: status=TimeLimitExceeded, checks_on_limit: false, checks: [{argv: " + pass + "}]}\n" +
		"  - {id: ask, repo: " + repo + ", base: HEAD, task: status=NeedsInput, checks: [{argv: " + pass + "}]}\n" +
		"  - {id: err, repo: " + repo + ", base: HEAD, task: status=Error, checks: [{argv: " + pass + "}]}\n" +
		"  - {id: crash, repo: " + repo + ", base: HEAD, task: status=none, checks: [{argv: " + pass + "}]}\n" +
		"  - {id: slow, repo: " + repo + ", base: HEAD, task: status=Submitted, checks: [{argv: [sleep, \"5\"], timeout_seconds: 1}]}\n"
	path := writeSuite(t, body)
	s, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	fa := &fakeAgent{}
	var stderr strings.Builder
	code := Run(context.Background(), Options{Suite: s, SuitePath: path, OutDir: out, MinPassRate: -1, ExtraArgs: []string{"--no-stream"}, Version: "v-test", Stderr: &stderr, RunAgent: fa.run})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	recs := readResults(t, out)
	want := map[string]string{"pass": OutcomePassed, "fail": OutcomeFailedCheck, "limit": OutcomeLimit, "limitnochk": OutcomeLimit, "ask": OutcomeNeedsInput, "err": OutcomeError, "crash": OutcomeError, "slow": OutcomeFailedCheck}
	if len(recs) != len(want) {
		t.Fatalf("%d records", len(recs))
	}
	for _, r := range recs {
		if r.Outcome != want[r.TaskID] {
			t.Errorf("%s outcome = %s (%s), want %s", r.TaskID, r.Outcome, r.Error, want[r.TaskID])
		}
		if r.SchemaVersion != 1 || r.BaseCommit != sha || r.TinyagentVersion != "v-test" || r.Provider != "openai" || r.Profile != "default" {
			t.Errorf("%s metadata: %+v", r.TaskID, r)
		}
	}
	byID := map[string]Record{}
	for _, r := range recs {
		byID[r.TaskID] = r
	}
	p := byID["pass"]
	if len(p.Checks) != 3 || p.Checks[2].Output != sha+"\n" || p.CostUSD != 0.5 || p.Steps != 2 || p.InputTokens != 10 || p.OutputTokens != 3 {
		t.Errorf("pass record: %+v", p)
	}
	if _, err := os.Stat(p.Trajectory); err != nil {
		t.Errorf("trajectory: %v", err)
	}
	if len(byID["limit"].Checks) != 1 || len(byID["limitnochk"].Checks) != 0 || len(byID["ask"].Checks) != 0 {
		t.Error("checks_on_limit / NeedsInput check handling wrong")
	}
	if c := byID["slow"].Checks[0]; !c.TimedOut || c.ExitCode != -1 {
		t.Errorf("timeout check: %+v", c)
	}
	if byID["crash"].Error == "" {
		t.Error("crash has no error")
	}
	// Child args: forwarded flags first, then per-trial ones.
	a := fa.calls[2]
	if a[0] != "--no-stream" || argAfter(a, "-m") != "openai/gpt-4o" || argAfter(a, "--step-limit") != "3" || argAfter(a, "--cost-limit") != "0.25" || argAfter(a, "--time-limit") != "9" || slices.Contains(a, "--profile") {
		t.Errorf("child args = %v", a)
	}
	if n := worktrees(t, repo); n != 1 {
		t.Errorf("%d worktrees left, want only the main one", n)
	}
	for _, f := range []string{"report.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
	var rep Report
	b, _ := os.ReadFile(filepath.Join(out, "report.json"))
	json.Unmarshal(b, &rep)
	if len(rep.Configs) != 1 || rep.Configs[0].Passed != 1 || rep.Configs[0].Attempted != 8 || rep.Configs[0].PassAtK["1"] != 0.125 {
		t.Errorf("report: %+v", rep.Configs)
	}
	if !strings.Contains(stderr.String(), "[1/8] pass.openai-gpt-4o.default.1: passed") {
		t.Errorf("progress: %s", stderr.String())
	}
}

func TestRunBudgetsGateKeepAndInterrupt(t *testing.T) {
	repo, _ := gitRepo(t)
	body := "version: 1\nmodels: [openai/gpt-4o]\nprofiles: [default, ci]\ntrials: 2\ntasks:\n" +
		"  - {id: a, repo: " + repo + ", base: HEAD, task: status=Submitted, checks: [{argv: [git, no-such-subcommand]}]}\n"
	path := writeSuite(t, body)
	s, err := Load(path, func(p string) bool { return p == "ci" })
	if err != nil {
		t.Fatal(err)
	}
	run := func(o Options) ([]Record, int, *fakeAgent) {
		fa := &fakeAgent{}
		o.Suite, o.SuitePath, o.OutDir, o.Stderr, o.RunAgent = s, path, filepath.Join(t.TempDir(), "out"), io.Discard, fa.run
		if o.MinPassRate == 0 {
			o.MinPassRate = -1
		}
		code := Run(context.Background(), o)
		return readResults(t, o.OutDir), code, fa
	}

	// Cost budget: each trial costs $0.50, so a $0.75 budget stops after two.
	recs, code, fa := run(Options{CostLimit: 0.75})
	if code != 0 || len(fa.calls) != 2 || len(recs) != 4 || recs[2].Outcome != OutcomeSkipped || !strings.Contains(recs[3].SkipReason, "cost limit") {
		t.Errorf("cost budget: code %d, %d calls, %+v", code, len(fa.calls), recs)
	}
	// A spent time budget starts nothing new.
	recs, _, fa = run(Options{TimeLimit: time.Nanosecond})
	if len(fa.calls) != 0 || recs[0].Outcome != OutcomeSkipped || !strings.Contains(recs[0].SkipReason, "time limit") {
		t.Errorf("time budget: %d calls", len(fa.calls))
	}
	// Gate: every config fails its check, so any positive threshold fails.
	if _, code, _ = run(Options{MinPassRate: 0.5}); code != 1 {
		t.Errorf("gate exit = %d, want 1", code)
	}
	// Profile ci is passed to the child; default is not.
	_, _, fa = run(Options{})
	if slices.Contains(fa.calls[0], "--profile") || argAfter(fa.calls[2], "--profile") != "ci" {
		t.Errorf("profile args: %v / %v", fa.calls[0], fa.calls[2])
	}
	// --eval-keep leaves worktrees and records their paths.
	recs, _, _ = run(Options{Keep: true})
	if recs[0].Worktree == "" || worktrees(t, repo) != 5 {
		t.Errorf("keep: %q, %d worktrees", recs[0].Worktree, worktrees(t, repo))
	}
	for _, r := range recs {
		exec.Command("git", "-C", repo, "worktree", "remove", "--force", r.Worktree).Run()
		os.RemoveAll(filepath.Dir(r.Worktree))
	}

	// Interrupt during the first trial: it finishes, worktree is removed,
	// the rest are skipped and the exit code is 130.
	ctx, cancel := context.WithCancel(context.Background())
	fa = &fakeAgent{onCall: func(int) { cancel() }}
	out := filepath.Join(t.TempDir(), "out")
	code = Run(ctx, Options{Suite: s, SuitePath: path, OutDir: out, MinPassRate: -1, Stderr: io.Discard, RunAgent: fa.run})
	recs = readResults(t, out)
	if code != 130 || len(fa.calls) != 1 || len(recs) != 4 || recs[1].SkipReason != "interrupted" {
		t.Errorf("interrupt: code %d, %d calls, %+v", code, len(fa.calls), recs)
	}
	if n := worktrees(t, repo); n != 1 {
		t.Errorf("interrupt left %d worktrees", n)
	}
}

func TestDryRun(t *testing.T) {
	repo, _ := gitRepo(t)
	path := writeSuite(t, "version: 1\ntrials: 1\nmodels: [openai/gpt-4o, x/unpriced]\ntasks:\n  - {id: a, repo: "+repo+", base: HEAD, task: t, limits: {cost_limit: 2}, checks: [{argv: [x]}]}\n  - {id: b, repo: "+repo+", base: HEAD, task: t, checks: [{argv: [x]}]}\n")
	s, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	DryRun(&b, s, Limits{}, func(m string, l Limits) (float64, bool, string) {
		if l.CostLimit > 0 {
			return l.CostLimit, true, ""
		}
		return 0, false, "nope"
	})
	got := b.String()
	for _, w := range []string{"4 trial(s)", "x/unpriced / default: 2 trial(s)", "a.openai-gpt-4o.default.1", "<= $2.0000", "no bound (nope)", "unbounded (bounded trials sum to $4.0000)"} {
		if !strings.Contains(got, w) {
			t.Errorf("dry run missing %q:\n%s", w, got)
		}
	}
}

func TestRunMatrix(t *testing.T) {
	repo, sha := gitRepo(t)
	path := writeSuite(t, "version: 1\nmodels: [openai/a, openai/b]\ntrials: 3\ntasks:\n"+
		"  - {id: x, repo: "+repo+", base: "+sha+", task: status=Submitted, checks: [{argv: [git, --version]}]}\n"+
		"  - {id: y, repo: "+repo+", base: HEAD, task: status=Submitted, checks: [{argv: [git, --version]}]}\n")
	s, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	fa := &fakeAgent{}
	out := filepath.Join(t.TempDir(), "out")
	if code := Run(context.Background(), Options{Suite: s, SuitePath: path, OutDir: out, MinPassRate: -1, Stderr: io.Discard, RunAgent: fa.run}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	recs := readResults(t, out)
	cwds, trajs := map[string]bool{}, map[string]bool{}
	for _, a := range fa.calls {
		cwds[argAfter(a, "--cwd")] = true
		trajs[argAfter(a, "-o")] = true
	}
	if len(recs) != 12 || len(fa.calls) != 12 || len(cwds) != 12 || len(trajs) != 12 {
		t.Errorf("%d records, %d calls, %d worktrees, %d trajectories; want 12 each", len(recs), len(fa.calls), len(cwds), len(trajs))
	}
	if worktrees(t, repo) != 1 {
		t.Error("worktrees left behind")
	}
}
