package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestToolOutputFilesConfig(t *testing.T) {
	d := Default().ToolOutputFiles
	if d.Enabled || d.Dir != DefaultToolOutputDir || d.MaxRuns != DefaultToolOutputMaxRuns {
		t.Errorf("defaults = %+v", d)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "tool_output_files:\n  enabled: true\n  dir: out/tools\n  max_runs: 3\n")
	c, err := load(t, p)
	if err != nil || c.ToolOutputFiles != (ToolOutputFiles{Enabled: true, Dir: "out/tools", MaxRuns: 3}) {
		t.Fatalf("config = %+v, err %v", c.ToolOutputFiles, err)
	}
	write(t, p, "tool_output_files:\n  enabled: true\n")
	if c, err := load(t, p); err != nil || c.ToolOutputFiles.Dir != DefaultToolOutputDir || c.ToolOutputFiles.MaxRuns != DefaultToolOutputMaxRuns {
		t.Errorf("partial = %+v, err %v", c.ToolOutputFiles, err)
	}
	write(t, p, "tool_output_files:\n  max_days: 3\n")
	if _, err := load(t, p); err == nil || !strings.Contains(err.Error(), "max_days") {
		t.Errorf("unknown key: err = %v", err)
	}
	write(t, p, "tool_output_files:\n  max_runs: -1\n")
	if _, err := load(t, p); err == nil || !strings.Contains(err.Error(), "tool_output_files.max_runs must be >= 0") {
		t.Errorf("negative max_runs: err = %v", err)
	}
	for _, dir := range []string{"../outside", ".", "a/../../b"} {
		write(t, p, "tool_output_files:\n  dir: "+dir+"\n")
		if _, err := load(t, p); err == nil || !strings.Contains(err.Error(), "tool_output_files.dir") {
			t.Errorf("dir %q: err = %v", dir, err)
		}
	}
}
