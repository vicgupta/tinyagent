package agent

import (
	"context"
	"fmt"

	"github.com/vicgupta/tinyagent/internal/model"
)

// helperQuery sends one tool-less helper request for an auxiliary task.
// With a routed model for task it goes there first, and if that fails it
// is retried once on the main model; the returned cost covers both
// attempts. Helper calls are not steps, but their cost and tokens count
// toward the run's limits like any other request.
func (a *Agent) helperQuery(ctx context.Context, task string, req []model.Message) (model.Message, model.Cost, error) {
	m := a.Helpers[task]
	if m == nil {
		return a.Model.Query(ctx, req, nil)
	}
	resp, cost, err := m.Query(ctx, req, nil)
	if err == nil || ctx.Err() != nil {
		return resp, cost, err
	}
	if a.Err != nil {
		fmt.Fprintf(a.Err, "warning: %s model failed (%v); retrying on the main model\n", task, err)
	}
	resp, c2, err := a.Model.Query(ctx, req, nil)
	cost.USD += c2.USD
	cost.InputTokens += c2.InputTokens
	cost.OutputTokens += c2.OutputTokens
	cost.CacheReadTokens += c2.CacheReadTokens
	cost.CacheWriteTokens += c2.CacheWriteTokens
	return resp, cost, err
}
