package selftune

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/harness"
)

// Apply writes candidate id's exact change after a person approved it by
// its digest. It needs a validation that accepted the change and the
// target file unchanged since the proposal; anything else is refused.
func Apply(settings config.SelfTuning, root, id, digest string, now time.Time) (*Candidate, error) {
	if !settings.Enabled {
		return nil, ErrOff
	}
	c, err := Load(root, id)
	if err != nil {
		return nil, err
	}
	switch c.Status {
	case StatusValidated:
	case StatusApplied:
		return nil, fmt.Errorf("candidate %s is already applied", c.ID)
	case StatusProposed:
		return nil, fmt.Errorf("candidate %s is not validated yet; run tinyagent tune validate %s first", c.ID, c.ID)
	default:
		return nil, fmt.Errorf("candidate %s is %s and can't be applied", c.ID, c.Status)
	}
	if c.Validation == nil || c.Validation.Verdict != VerdictAccepted {
		return nil, fmt.Errorf("candidate %s has no accepted validation", c.ID)
	}
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return nil, fmt.Errorf("applying %s needs explicit approval: review it with tinyagent tune show %s, then pass --tune-digest %s", c.ID, c.ID, c.Digest)
	}
	if digest != c.Digest {
		return nil, fmt.Errorf("--tune-digest %q does not match candidate %s (digest %s); review the change again with tinyagent tune show %s", digest, c.ID, c.Digest, c.ID)
	}
	tp, err := resolveTarget(root, c.Target)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(tp)
	if err != nil {
		return nil, err
	}
	cur, err := readRegular(tp, harness.MaxSkillBytes)
	if err != nil {
		return nil, err
	}
	if sha(string(cur)) != c.BaseSHA256 {
		return nil, fmt.Errorf("%s changed since candidate %s was proposed; nothing applied, propose again", c.Target, c.ID)
	}
	if err := writeAtomic(tp, []byte(c.After), fi.Mode().Perm()); err != nil {
		return nil, err
	}
	c.Status = StatusApplied
	c.log(now, "applied", "digest "+c.Digest)
	if err := Save(root, c); err != nil {
		return nil, fmt.Errorf("applied %s to %s but could not record it: %w", c.ID, c.Target, err)
	}
	return c, nil
}

// Revert restores the target's content from before an applied candidate,
// only while the target still holds exactly the applied change.
func Revert(settings config.SelfTuning, root, id string, now time.Time) (*Candidate, error) {
	if !settings.Enabled {
		return nil, ErrOff
	}
	c, err := Load(root, id)
	if err != nil {
		return nil, err
	}
	if c.Status != StatusApplied {
		return nil, fmt.Errorf("candidate %s is %s; only an applied candidate can be reverted", c.ID, c.Status)
	}
	tp, err := resolveTarget(root, c.Target)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(tp)
	if err != nil {
		return nil, err
	}
	cur, err := readRegular(tp, harness.MaxSkillBytes)
	if err != nil {
		return nil, err
	}
	if sha(string(cur)) != c.NewSHA256 {
		return nil, fmt.Errorf("%s changed after candidate %s was applied; nothing reverted, undo the change by hand (see tinyagent tune show %s)", c.Target, c.ID, c.ID)
	}
	if err := writeAtomic(tp, []byte(c.Before), fi.Mode().Perm()); err != nil {
		return nil, err
	}
	c.Status = StatusReverted
	c.log(now, "reverted", "")
	if err := Save(root, c); err != nil {
		return nil, fmt.Errorf("reverted %s in %s but could not record it: %w", c.ID, c.Target, err)
	}
	return c, nil
}
