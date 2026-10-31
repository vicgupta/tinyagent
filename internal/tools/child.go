package tools

import "reflect"

// ChildSpec names what a child executor changes relative to its parent.
// Every other setting follows the inheritance policy documented on
// Executor.Child.
type ChildSpec struct {
	// Root is the child's working directory. Empty keeps the parent's
	// Root. A different Root "moves" the child: settings tied to the
	// parent's root (OutputFiles, FileAccess) are then not inherited.
	Root string
	// ReadOnly makes the child read-only. A child of a read-only parent is
	// always read-only too: a child never gains write access its parent
	// lacks.
	ReadOnly bool
	// Only is a tool-name filter applied after inheritance (a worker's
	// configured tools). It is never copied from the parent; nil allows the
	// child's whole catalog.
	Only []string
	// Env replaces the parent's command environment. nil inherits it.
	Env Environment
	// FileAccess replaces the parent's file_access rules. When the child
	// keeps the parent's Root, nil inherits the parent's rules. When Root
	// moves, the parent's rules (resolved against the parent's root) are
	// never reused: FileAccess is the child's rules, and nil denies
	// nothing.
	FileAccess *FileAccess
}

// childOverridden are the Executor fields Child sets from ChildSpec (or
// from a rule tied to it) instead of copying them.
var childOverridden = map[string]bool{
	"Root":        true,
	"ReadOnly":    true,
	"Only":        true,
	"Env":         true,
	"FileAccess":  true,
	"OutputFiles": true,
}

// childReset are the Executor fields a child starts without. They are
// the parent run's own opt-in tools and per-run state: a child never
// delegates, spawns workers, plans, asks the user, fetches the web, uses
// LSP, skills, memory or MCP tools, streams bash output, or shares the
// parent's undo session.
var childReset = map[string]bool{
	"Stream":             true,
	"ExternalSpecs":      true,
	"ExternalCall":       true,
	"ExternalCallImages": true,
	"Plan":               true,
	"Images":             true,
	"MaxImageBytes":      true,
	"Skills":             true,
	"Delegate":           true,
	"TaskTypes":          true,
	"WebFetch":           true,
	"LSP":                true,
	"AskUser":            true,
	"Workers":            true,
	"Remember":           true,
	"MemoryTools":        true,
	"UndoSession":        true,
}

// Child returns a new executor for a child agent (a delegate or a worker)
// that inherits the parent's settings, with the fields in spec overridden.
//
// Inherited (copied from the parent): every exported field that is not
// listed below, including BashTimeout, MaxOutputChars, MaxProcesses,
// ProcessBufferBytes, CommandTools, CommandLog, ApplyPatch, ExtraDirs and
// Redactor. A field added to Executor later is inherited by default; to
// keep it from children, add it to the reset list.
//
// Overridden from spec: Root, ReadOnly, Only, Env and FileAccess (see
// ChildSpec). OutputFiles is inherited only when the child keeps the
// parent's Root, because the saved-output directory lives inside that
// root; a moved child uses plain truncation.
//
// Reset (the child starts without them): Stream, ExternalSpecs,
// ExternalCall, ExternalCallImages, Plan, Images, MaxImageBytes, Skills,
// Delegate, TaskTypes, WebFetch, LSP, AskUser, Workers, Remember,
// MemoryTools and UndoSession, plus all unexported run state (background
// processes, undo snapshots).
func (e *Executor) Child(spec ChildSpec) *Executor {
	c := &Executor{}
	src := reflect.ValueOf(e).Elem()
	dst := reflect.ValueOf(c).Elem()
	t := src.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() || childOverridden[f.Name] || childReset[f.Name] {
			continue
		}
		dst.Field(i).Set(src.Field(i))
	}

	c.Root = e.Root
	if spec.Root != "" {
		c.Root = spec.Root
	}
	moved := c.Root != e.Root
	c.ReadOnly = e.ReadOnly || spec.ReadOnly
	c.Only = spec.Only
	c.Env = e.Env
	if spec.Env != nil {
		c.Env = spec.Env
	}
	switch {
	case spec.FileAccess != nil || moved:
		c.FileAccess = spec.FileAccess
	default:
		c.FileAccess = e.FileAccess
	}
	if !moved {
		c.OutputFiles = e.OutputFiles
	}
	return c
}
