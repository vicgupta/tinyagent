package agent

import (
	"fmt"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// StatusTokenLimit ends a run whose provider-reported tokens (input plus
// output, delegates and workers included) reached the token limit.
const StatusTokenLimit = "TokenLimitExceeded"

// BudgetWarnFraction is the share of a token or cost budget at which the
// model is told, once, to wrap up.
const BudgetWarnFraction = 0.8

const budgetNotice = "Budget notice: this run has used %s. The run stops when the budget is used up, so wrap up now: finish the most important remaining work, check it, and submit your result."

// usedTokens is the run's total provider-reported token usage counted
// against the token limit. Cache tokens are part of InputTokens.
func usedTokens(t *trajectory.Trajectory) int { return t.InputTokens + t.OutputTokens }

// tokensLeft is what remains of the token limit, or 0 with no limit.
func (a *Agent) tokensLeft(t *trajectory.Trajectory) (left int, limited bool) {
	if a.Config.TokenLimit <= 0 {
		return 0, false
	}
	return a.Config.TokenLimit - usedTokens(t), true
}

// budgetUse describes each token or cost budget at or past the warning
// threshold, or "" when none is.
func (a *Agent) budgetUse(t *trajectory.Trajectory) string {
	s := ""
	add := func(part string) {
		if s != "" {
			s += " and "
		}
		s += part
	}
	if n := a.Config.TokenLimit; n > 0 && float64(usedTokens(t)) >= BudgetWarnFraction*float64(n) {
		add(fmt.Sprintf("%d%% of its token budget (%d of %d tokens)", percent(float64(usedTokens(t)), float64(n)), usedTokens(t), n))
	}
	// An unpriced model reports no cost, so only its token budget warns.
	if c := a.Config.CostLimit; c > 0 && t.Cost >= BudgetWarnFraction*c {
		add(fmt.Sprintf("%d%% of its cost budget ($%.4f of $%.4f)", percent(t.Cost, c), t.Cost, c))
	}
	return s
}

func percent(used, limit float64) int { return int(100 * used / limit) }

// warnBudget tells the model once, with a user message, that a token or
// cost budget is nearly used up. It re-arms when usage falls back under the
// threshold, as after a resume with a raised limit.
func (a *Agent) warnBudget(t *trajectory.Trajectory, v *view) {
	use := a.budgetUse(t)
	if use == "" {
		t.BudgetWarned = false
		return
	}
	if t.BudgetWarned {
		return
	}
	t.BudgetWarned = true
	t.Messages = append(t.Messages, model.Message{Role: "user", Content: fmt.Sprintf(budgetNotice, use)})
	v.add(t, t.Messages[len(t.Messages)-1])
	a.event("budget.warning", map[string]any{"step": t.Steps, "usage": use,
		"tokens": usedTokens(t), "token_limit": a.Config.TokenLimit, "cost_usd": t.Cost, "cost_limit": a.Config.CostLimit})
	if a.Verbose {
		fmt.Fprintf(a.Err, "budget warning: %s\n", use)
	}
}
