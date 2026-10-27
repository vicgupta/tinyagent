package eval

import (
	"fmt"
	"io"
)

// Estimator returns the per-trial upper-bound cost in USD for a model under
// the given limits, or ok=false with a reason when no bound exists.
type Estimator func(model string, l Limits) (usd float64, ok bool, reason string)

// DryRun prints the expanded matrix and a cost upper bound without running
// anything. defaults are the forwarded run limits a task may override.
func DryRun(w io.Writer, s *Suite, defaults Limits, est Estimator) {
	trials := s.Expand()
	fmt.Fprintf(w, "%d trial(s): %d task(s), %d trial(s) each per configuration\n", len(trials), len(s.Tasks), s.Trials)
	type cfg struct{ m, p string }
	var order []cfg
	counts := map[cfg]int{}
	for _, tr := range trials {
		c := cfg{tr.Model, tr.Profile}
		if counts[c] == 0 {
			order = append(order, c)
		}
		counts[c]++
	}
	for _, c := range order {
		fmt.Fprintf(w, "  %s / %s: %d trial(s)\n", c.m, c.p, counts[c])
	}
	var total float64
	bounded := true
	for _, tr := range trials {
		l := tr.Task.Limits
		if l.StepLimit == 0 {
			l.StepLimit = defaults.StepLimit
		}
		if l.CostLimit == 0 {
			l.CostLimit = defaults.CostLimit
		}
		usd, ok, reason := est(tr.Model, l)
		if ok {
			total += usd
			fmt.Fprintf(w, "  %s  base %.12s  <= $%.4f\n", tr.ID, tr.Task.baseCommit, usd)
		} else {
			bounded = false
			fmt.Fprintf(w, "  %s  base %.12s  no bound (%s)\n", tr.ID, tr.Task.baseCommit, reason)
		}
	}
	if bounded {
		fmt.Fprintf(w, "worst-case cost: <= $%.4f\n", total)
	} else {
		fmt.Fprintf(w, "worst-case cost: unbounded (bounded trials sum to $%.4f); set limits.cost_limit or limits.step_limit\n", total)
	}
}
