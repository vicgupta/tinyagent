package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Report is the aggregate written to report.json.
type Report struct {
	SchemaVersion    int            `json:"schema_version"`
	Suite            string         `json:"suite"`
	TinyagentVersion string         `json:"tinyagent_version"`
	Trials           int            `json:"trials_per_cell"`
	MinPassRate      *float64       `json:"min_pass_rate,omitempty"`
	Configs          []ConfigReport `json:"configs"`
	BelowThreshold   []string       `json:"below_threshold,omitempty"`
}

// ConfigReport aggregates one (model, profile) configuration.
type ConfigReport struct {
	Model         string             `json:"model"`
	Profile       string             `json:"profile"`
	Trials        int                `json:"trials"`
	Attempted     int                `json:"attempted"`
	Passed        int                `json:"passed"`
	PassRate      float64            `json:"pass_rate"`
	PassAtK       map[string]float64 `json:"pass_at_k"`
	PassHatK      map[string]float64 `json:"pass_hat_k"`
	Outcomes      map[string]int     `json:"outcomes"`
	CostUSD       float64            `json:"cost_usd"`
	MeanCostUSD   float64            `json:"mean_cost_usd"`
	CostPerPass   *float64           `json:"cost_per_pass_usd"`
	InputTokens   int                `json:"input_tokens"`
	OutputTokens  int                `json:"output_tokens"`
	MeanSteps     float64            `json:"mean_steps"`
	MeanWallTimeS float64            `json:"mean_wall_time_s"`
	Tasks         []TaskReport       `json:"tasks"`
}

// TaskReport is one task's results within a configuration.
type TaskReport struct {
	TaskID    string `json:"task_id"`
	Attempted int    `json:"attempted"`
	Passed    int    `json:"passed"`
}

// binom returns C(n, k) as a float64.
func binom(n, k int) float64 {
	if k < 0 || k > n {
		return 0
	}
	r := 1.0
	for i := 1; i <= k; i++ {
		r = r * float64(n-k+i) / float64(i)
	}
	return r
}

// PassAtK is the unbiased estimator 1 - C(n-c, k)/C(n, k) of the chance that
// at least one of k samples passes, given c passes in n trials.
func PassAtK(n, c, k int) float64 {
	if k > n || n == 0 {
		return 0
	}
	if n-c < k {
		return 1
	}
	return 1 - binom(n-c, k)/binom(n, k)
}

// PassHatK is C(c, k)/C(n, k), the chance that all k samples pass.
func PassHatK(n, c, k int) float64 {
	if k > n || n == 0 {
		return 0
	}
	return binom(c, k) / binom(n, k)
}

// BuildReport aggregates records by configuration in first-seen order.
func BuildReport(o Options, recs []Record) Report {
	rep := Report{SchemaVersion: SchemaVersion, Suite: o.SuitePath, TinyagentVersion: o.Version, Trials: o.Suite.Trials}
	if o.MinPassRate >= 0 {
		v := o.MinPassRate
		rep.MinPassRate = &v
	}
	type key struct{ m, p string }
	idx := map[key]int{}
	type tk struct{ n, c int }
	perTask := []map[string]*tk{}
	taskOrder := [][]string{}
	for _, r := range recs {
		k := key{r.Model, r.Profile}
		i, ok := idx[k]
		if !ok {
			i = len(rep.Configs)
			idx[k] = i
			rep.Configs = append(rep.Configs, ConfigReport{Model: r.Model, Profile: r.Profile, Outcomes: map[string]int{}})
			perTask = append(perTask, map[string]*tk{})
			taskOrder = append(taskOrder, nil)
		}
		c := &rep.Configs[i]
		c.Trials++
		c.Outcomes[r.Outcome]++
		t := perTask[i][r.TaskID]
		if t == nil {
			t = &tk{}
			perTask[i][r.TaskID] = t
			taskOrder[i] = append(taskOrder[i], r.TaskID)
		}
		if r.Outcome == OutcomeSkipped {
			continue
		}
		t.n++
		c.Attempted++
		if r.Outcome == OutcomePassed {
			t.c++
			c.Passed++
		}
		c.CostUSD += r.CostUSD
		c.InputTokens += r.InputTokens
		c.OutputTokens += r.OutputTokens
		c.MeanSteps += float64(r.Steps)
		c.MeanWallTimeS += float64(r.WallTimeMS) / 1000
	}
	for i := range rep.Configs {
		c := &rep.Configs[i]
		c.PassAtK, c.PassHatK = map[string]float64{}, map[string]float64{}
		if c.Attempted > 0 {
			n := float64(c.Attempted)
			c.PassRate = float64(c.Passed) / n
			c.MeanCostUSD = c.CostUSD / n
			c.MeanSteps /= n
			c.MeanWallTimeS /= n
		}
		if c.Passed > 0 {
			v := c.CostUSD / float64(c.Passed)
			c.CostPerPass = &v
		}
		// pass@k averages per-task estimates; only k every task reached.
		minN := -1
		for _, id := range taskOrder[i] {
			t := perTask[i][id]
			c.Tasks = append(c.Tasks, TaskReport{TaskID: id, Attempted: t.n, Passed: t.c})
			if minN < 0 || t.n < minN {
				minN = t.n
			}
		}
		for k := 1; k <= minN; k++ {
			var at, hat float64
			for _, id := range taskOrder[i] {
				t := perTask[i][id]
				at += PassAtK(t.n, t.c, k)
				hat += PassHatK(t.n, t.c, k)
			}
			nt := float64(len(taskOrder[i]))
			c.PassAtK[fmt.Sprint(k)] = at / nt
			c.PassHatK[fmt.Sprint(k)] = hat / nt
		}
		if o.MinPassRate >= 0 && (c.Attempted == 0 || c.PassRate < o.MinPassRate) {
			rep.BelowThreshold = append(rep.BelowThreshold, c.Model+" / "+c.Profile)
		}
	}
	return rep
}

func writeReports(dir string, rep Report) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(markdown(rep)), 0o600)
}

func ks(m map[string]float64) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		return len(out[i]) < len(out[j]) || (len(out[i]) == len(out[j]) && out[i] < out[j])
	})
	return out
}

func passKString(m map[string]float64) string {
	var parts []string
	for _, k := range ks(m) {
		parts = append(parts, fmt.Sprintf("@%s=%.2f", k, m[k]))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func summaryTable(rep Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-40s %-12s %9s %7s %-24s %10s %10s\n", "MODEL", "PROFILE", "PASSED", "RATE", "PASS@K", "COST", "$/PASS")
	for _, c := range rep.Configs {
		cpp := "-"
		if c.CostPerPass != nil {
			cpp = fmt.Sprintf("$%.4f", *c.CostPerPass)
		}
		fmt.Fprintf(&b, "%-40s %-12s %9s %6.0f%% %-24s %10s %10s\n", c.Model, c.Profile, fmt.Sprintf("%d/%d", c.Passed, c.Attempted), c.PassRate*100, passKString(c.PassAtK), fmt.Sprintf("$%.4f", c.CostUSD), cpp)
	}
	return b.String()
}

func markdown(rep Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# tinyagent eval: %s\n\n", rep.Suite)
	fmt.Fprintf(&b, "tinyagent %s, %d trial(s) per task and configuration.\n\n", rep.TinyagentVersion, rep.Trials)
	b.WriteString("| Model | Profile | Passed | Pass rate | pass@k | pass^k | Cost | Cost/pass | Mean steps | Mean time |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Configs {
		cpp := "-"
		if c.CostPerPass != nil {
			cpp = fmt.Sprintf("$%.4f", *c.CostPerPass)
		}
		fmt.Fprintf(&b, "| %s | %s | %d/%d | %.0f%% | %s | %s | $%.4f | %s | %.1f | %.1fs |\n", c.Model, c.Profile, c.Passed, c.Attempted, c.PassRate*100, passKString(c.PassAtK), passKString(c.PassHatK), c.CostUSD, cpp, c.MeanSteps, c.MeanWallTimeS)
	}
	b.WriteString("\n## Outcomes\n\n| Model | Profile | ")
	outs := []string{OutcomePassed, OutcomeFailedCheck, OutcomeLimit, OutcomeNeedsInput, OutcomeError, OutcomeSkipped}
	b.WriteString(strings.Join(outs, " | ") + " |\n|---|---|" + strings.Repeat("---|", len(outs)) + "\n")
	for _, c := range rep.Configs {
		fmt.Fprintf(&b, "| %s | %s |", c.Model, c.Profile)
		for _, o := range outs {
			fmt.Fprintf(&b, " %d |", c.Outcomes[o])
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Per task\n\n| Task | Model | Profile | Passed |\n|---|---|---|---|\n")
	for _, c := range rep.Configs {
		for _, t := range c.Tasks {
			fmt.Fprintf(&b, "| %s | %s | %s | %d/%d |\n", t.TaskID, c.Model, c.Profile, t.Passed, t.Attempted)
		}
	}
	if rep.MinPassRate != nil {
		if len(rep.BelowThreshold) == 0 {
			fmt.Fprintf(&b, "\nAll configurations met the minimum pass rate %.2f.\n", *rep.MinPassRate)
		} else {
			fmt.Fprintf(&b, "\nBelow the minimum pass rate %.2f: %s.\n", *rep.MinPassRate, strings.Join(rep.BelowThreshold, ", "))
		}
	}
	return b.String()
}
