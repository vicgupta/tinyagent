package main

import (
	"strings"
	"testing"
)

func TestResumeImagesSettingMustMatch(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--images")
	})
	if code == 0 || !strings.Contains(stderr, "--images conflicts with the checkpoint") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
