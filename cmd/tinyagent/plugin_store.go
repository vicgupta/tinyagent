package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/vicgupta/tinyagent/internal/plugins"
)

// moduleStore is the plugin module version store of a working directory,
// kept under .tinyagent/ (out of git, like snapshots and undo data).
func moduleStore(root string) *plugins.ModuleStore {
	return &plugins.ModuleStore{Dir: filepath.Join(root, ".tinyagent", "modules")}
}

// runPluginStore handles --plugins-prune and --plugins-rollback.
func runPluginStore(o *options, stdout, stderr io.Writer) int {
	root, err := canonicalRoot(o.cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	st := moduleStore(root)
	if o.pluginsRollback != "" {
		mod, hash, ok := strings.Cut(o.pluginsRollback, "@")
		if !ok || mod == "" || hash == "" {
			fmt.Fprintln(stderr, "error: --plugins-rollback wants <module>@<sha256>")
			return 2
		}
		if err := st.Rollback(mod, hash, nil); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: current is now %s\n", mod, hash)
		return 0
	}
	if o.pluginsKeep < 0 {
		fmt.Fprintln(stderr, "error: --plugins-keep must be >= 0")
		return 2
	}
	mods, err := st.Modules()
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	total := 0
	for _, m := range mods {
		removed, err := st.Prune(m, o.pluginsKeep)
		total += len(removed)
		if err != nil {
			fmt.Fprintf(stderr, "error: %s: %v\n", m, err)
			return 1
		}
		for _, h := range removed {
			fmt.Fprintf(stdout, "%s: removed %s\n", m, h)
		}
	}
	fmt.Fprintf(stdout, "pruned %d version(s) in %s\n", total, st.Dir)
	return 0
}
