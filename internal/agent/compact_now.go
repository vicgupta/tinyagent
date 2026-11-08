package agent

import (
	"context"
	"errors"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// CompactNow immediately shrinks the model's view of t, the same way
// chat and the web UI's /compact does (reason "requested"). Messages
// stay append-only: only t.Compactions (and usage, if a summary call
// ran) change. The result (tokens before → after, or that nothing
// could be compacted) is written to a.Err, and a "compaction" event
// is emitted when anything changed. A model error leaves the stored
// history unchanged.
//
// If compaction succeeds but the estimate is still above the
// auto-compact threshold, that is not treated as a failure: the user
// asked to compact, not to fit the window.
func (a *Agent) CompactNow(ctx context.Context, t *trajectory.Trajectory) error {
	if a.NoCompact {
		return errors.New("compaction is off (--no-compact)")
	}
	n := len(t.Compactions)
	err := a.compact(ctx, t, viewFromTrajectory(t), "requested")
	if errors.Is(err, errStillTooLong) && len(t.Compactions) > n {
		return nil
	}
	return err
}
