package playbooks

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/cwdpath"
)

//go:embed samples/*.md
var samples embed.FS

// Samples returns the embedded sample playbooks by file name.
func Samples() map[string][]byte {
	out := map[string][]byte{}
	entries, _ := fs.ReadDir(samples, "samples")
	for _, e := range entries {
		data, _ := samples.ReadFile("samples/" + e.Name())
		out[e.Name()] = data
	}
	return out
}

// Install writes the samples into root/playbooks. An existing file is
// skipped unless force is set. It returns the files written and skipped.
func Install(root string, force bool) (written, skipped []string, err error) {
	dir, err := cwdpath.Resolve(root, Dir(root))
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	all := Samples()
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		path := filepath.Join(dir, n)
		if _, err := cwdpath.Resolve(root, path); err != nil {
			return written, skipped, err
		}
		if _, err := os.Lstat(path); err == nil && !force {
			skipped = append(skipped, path)
			continue
		}
		if err := os.WriteFile(path, all[n], 0o644); err != nil {
			return written, skipped, fmt.Errorf("write %s: %w", path, err)
		}
		written = append(written, path)
	}
	return written, skipped, nil
}

// SampleNames lists the sample playbooks' names.
func SampleNames() []string {
	var out []string
	for n := range Samples() {
		out = append(out, strings.TrimSuffix(n, ".md"))
	}
	sort.Strings(out)
	return out
}
