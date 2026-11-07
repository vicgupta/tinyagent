package tools

import "fmt"

// Actionable formats a tool error for the model: the cause, then a concrete
// next step after "; " — the same shape as edit_file's
// "old_str occurs N times; add surrounding context to make it unique".
// Prefer this over a bare "failed" so the model can fix the call instead of
// retrying blindly.
func Actionable(cause, next string) error {
	return fmt.Errorf("%s; %s", cause, next)
}

// Actionablef is Actionable with a formatted cause.
func Actionablef(next, causeFmt string, args ...any) error {
	return Actionable(fmt.Sprintf(causeFmt, args...), next)
}
