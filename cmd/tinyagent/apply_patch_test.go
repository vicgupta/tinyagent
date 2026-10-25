package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sentToolNames returns the tool names in a recorded provider request.
func sentToolNames(body map[string]any) []string {
	var names []string
	list, _ := body["tools"].([]any)
	for _, raw := range list {
		tool, _ := raw.(map[string]any)
		if name, ok := tool["name"].(string); ok {
			names = append(names, name)
		} else if fn, ok := tool["function"].(map[string]any); ok {
			names = append(names, fn["name"].(string))
		}
	}
	return names
}

// TestApplyPatchFlagAndConfig covers enabling the tool (AC1, AC2, AC9).
func TestApplyPatchFlagAndConfig(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	onCfg := filepath.Join(dir, "on.yaml")
	os.WriteFile(onCfg, []byte("apply_patch: true\n"), 0o644)
	for _, c := range []struct {
		name string
		want bool
		args []string
	}{
		{"default", false, []string{"-c", emptyCfg}},
		{"flag", true, []string{"-c", emptyCfg, "--apply-patch"}},
		{"config", true, []string{"-c", onCfg}},
		{"flag beats config", false, []string{"-c", onCfg, "--apply-patch=false"}},
		{"read-only", false, []string{"-c", emptyCfg, "--apply-patch", "--read-only"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResponses{replies: []string{responsesSubmit}}
			t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
			var code int
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--cwd", dir, "--openai-api", "responses", "--no-stream"}, c.args...)...)
			})
			if code != 0 || len(f.bodies) != 1 {
				t.Fatalf("exit %d, %d requests; stderr:\n%s", code, len(f.bodies), stderr)
			}
			names := sentToolNames(f.bodies[0])
			if got := slices.Contains(names, "apply_patch"); got != c.want {
				t.Errorf("apply_patch sent = %v, want %v (tools %v)", got, c.want, names)
			}
			if c.name != "read-only" && (!slices.Contains(names, "edit_file") || !slices.Contains(names, "write_file")) {
				t.Errorf("tools = %v", names)
			}
		})
	}
}

func TestResumeApplyPatchSettingMustMatch(t *testing.T) { // AC13
	tr, source, output, cfg := resumeFixture(t)
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--apply-patch")
	})
	if code != 2 || !strings.Contains(stderr, "--apply-patch conflicts with the checkpoint") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
