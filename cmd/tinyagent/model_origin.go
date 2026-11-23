package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// Where the main model came from (issue #429). resolveModel keeps the
// precedence (-m/--model, then $AGENT_MODEL, then the config's model:,
// then the built-in default); resolveModelOrigin reports the source next
// to it, so startup can note an $AGENT_MODEL that overrides a different
// model: in the config file, and the missing-key error and hint can say
// where the model came from. Only model specs and paths are printed, never
// a key's value.

type modelOriginKind int

const (
	originUnknown    modelOriginKind = iota // not tracked: say nothing extra
	originFlag                              // -m/--model
	originEnv                               // $AGENT_MODEL
	originConfig                            // model: in the config file
	originDefault                           // the built-in default
	originCheckpoint                        // chat/web --resume's saved model
)

// modelOrigin is a resolved model spec and where it came from.
type modelOrigin struct {
	spec     string
	kind     modelOriginKind
	cfgModel string // the config's model:, even when something outranks it
	cfgPath  string // the config file it was read from; "" for none
}

// resolveModelOrigin resolves the model the way resolveModel does and
// records its source. cfgPath names the config file cfgModel came from.
func resolveModelOrigin(fs *flag.FlagSet, flagModel, cfgModel, cfgPath string) modelOrigin {
	mo := modelOrigin{spec: resolveModel(fs, flagModel, cfgModel), cfgModel: cfgModel, cfgPath: cfgPath}
	switch {
	case flagWasSet(fs, "model", "m"):
		mo.kind = originFlag
	case os.Getenv("AGENT_MODEL") != "":
		mo.kind = originEnv
	case cfgModel != "":
		mo.kind = originConfig
	default:
		mo.kind = originDefault
	}
	return mo
}

// configPathFor is the config file a run with o would read, for naming it
// in a note; "" when there is none.
func configPathFor(o *options) string {
	return config.Locate(o.configPath, o.cwd).Path
}

// originFor returns mo when it describes spec, else an untracked origin
// for spec (for example after chat's /model switched the model).
func originFor(mo modelOrigin, spec string) modelOrigin {
	if mo.spec == spec {
		return mo
	}
	return modelOrigin{spec: spec}
}

// configLabel names the config file as the user would type it.
func (mo modelOrigin) configLabel() string {
	p := mo.cfgPath
	switch {
	case p == "":
		return "the config"
	case filepath.IsAbs(p), strings.HasPrefix(p, "."+string(filepath.Separator)), strings.HasPrefix(p, ".."+string(filepath.Separator)):
		return p
	}
	return "." + string(filepath.Separator) + p
}

// source names where the model came from; "" when not tracked.
func (mo modelOrigin) source() string {
	switch mo.kind {
	case originFlag:
		return "-m/--model"
	case originEnv:
		return "$AGENT_MODEL"
	case originConfig:
		return "model: in " + mo.configLabel()
	case originDefault:
		return "the built-in default"
	case originCheckpoint:
		return "the --resume checkpoint"
	}
	return ""
}

// label is "model <spec> (from <source>)", or "model <spec>" untracked.
func (mo modelOrigin) label() string {
	if s := mo.source(); s != "" {
		return fmt.Sprintf("model %s (from %s)", mo.spec, s)
	}
	return "model " + mo.spec
}

// overridesConfig reports whether $AGENT_MODEL replaced a different
// model: from the config file.
func (mo modelOrigin) overridesConfig() bool {
	return mo.kind == originEnv && mo.cfgModel != "" && mo.cfgModel != mo.spec
}

// unsetAdvice tells how to get the file's model back, when $AGENT_MODEL
// overrode it.
func (mo modelOrigin) unsetAdvice() string {
	return fmt.Sprintf("unset AGENT_MODEL to use model: %s in %s", mo.cfgModel, mo.configLabel())
}

// overrideNote is the one-line startup note for an $AGENT_MODEL that
// overrides a different model: in the config file; "" otherwise.
func (mo modelOrigin) overrideNote() string {
	if !mo.overridesConfig() {
		return ""
	}
	return fmt.Sprintf("note: using $AGENT_MODEL=%s instead of model: %s in %s (unset AGENT_MODEL to use the file)", mo.spec, mo.cfgModel, mo.configLabel())
}

// noteModelOverride prints mo's override note, if any, to w.
func noteModelOverride(w io.Writer, mo modelOrigin) {
	if n := mo.overrideNote(); n != "" {
		fmt.Fprintln(w, n)
	}
}

// sourceSentence is "the model <spec> is from <source>", plus how to get
// the file's model back when $AGENT_MODEL overrode it; "" untracked.
func (mo modelOrigin) sourceSentence() string {
	s := mo.source()
	if s == "" {
		return ""
	}
	out := fmt.Sprintf("the model %s is from %s", mo.spec, s)
	if mo.overridesConfig() {
		out += " (" + mo.unsetAdvice() + ")"
	}
	return out
}

// missingKey is the missing-API-key message naming the model's source.
func (mo modelOrigin) missingKey(env string) string {
	if mo.source() == "" {
		return env + " is not set"
	}
	return fmt.Sprintf("%s is not set for %s", env, mo.label())
}

// hintFrom is model.Hint for mo's model with the model's source added.
func hintFrom(mo modelOrigin, errMsg string) string {
	h := model.Hint(mo.spec, errMsg)
	if h == "" {
		return ""
	}
	if s := mo.sourceSentence(); s != "" {
		h += "; " + s
	}
	return h
}

// printHintFrom is printHint naming the model's source.
func printHintFrom(w io.Writer, mo modelOrigin, errMsg string) {
	if h := hintFrom(mo, errMsg); h != "" {
		fmt.Fprintln(w, styleFor(w).Yellow("hint:"), h)
	}
}

// withHintFrom is withHint naming the model's source in the hint.
func withHintFrom(mo modelOrigin, msg, errMsg string) error {
	if h := hintFrom(mo, errMsg); h != "" {
		msg += "\nhint: " + h
	}
	return errors.New(msg)
}

// attemptModelError names attempt n's model, its source when tracked and,
// when $AGENT_MODEL overrode the config's model:, how to get it back.
func attemptModelError(n int, spec string, from modelOrigin, err error) error {
	src := from.source()
	if src == "" {
		return fmt.Errorf("attempt %d model %q: %w", n, spec, err)
	}
	if from.overridesConfig() {
		return fmt.Errorf("attempt %d model %q (from %s): %w; %s", n, from.spec, src, err, from.unsetAdvice())
	}
	return fmt.Errorf("attempt %d model %q (from %s): %w", n, from.spec, src, err)
}
