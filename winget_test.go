package tinyagent_test

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestWinGetDocs keeps the install command and the release secret contract
// in the pages a user or maintainer actually reads.
func TestWinGetDocs(t *testing.T) {
	for file, wants := range map[string][]string{
		"README.md": {
			"winget install VicGupta.TinyAgent",
			"tinyagent-windows-amd64.zip",
			"docs/install.md",
			"docs/release.md",
		},
		"docs/install.md": {
			"winget install VicGupta.TinyAgent",
			"VicGupta.TinyAgent",
			"tinyagent.exe",
			"tinyagent-windows-amd64.zip",
			"tinyagent-windows-arm64.zip",
		},
		"docs/release.md": {
			"WINGET_GITHUB_TOKEN",
			"public_repo",
			"microsoft/winget-pkgs",
			"vicgupta/winget-pkgs",
			"reviewed manually",
			"tinyagent.exe",
			"vedantmgoyal9/winget-releaser",
		},
		"CONTRIBUTING.md": {
			"WINGET_GITHUB_TOKEN",
			"public_repo",
			"microsoft/winget-pkgs",
			"vicgupta/winget-pkgs",
			"manually",
			"tinyagent.exe",
		},
		"CHANGELOG.md": {
			"winget install VicGupta.TinyAgent",
			"WINGET_GITHUB_TOKEN",
			"manually",
		},
	} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if file == "CHANGELOG.md" {
			rest, ok := strings.CutPrefix(text, "# Changelog\n\n## [Unreleased]\n")
			if !ok {
				t.Fatal("CHANGELOG.md: Unreleased section missing")
			}
			next := strings.Index(rest, "\n## [")
			if next < 0 {
				t.Fatal("CHANGELOG.md: no release after Unreleased")
			}
			text = rest[:next]
		}
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s: missing %q", file, want)
			}
		}
	}
}

// TestWinGetReleaseWorkflow checks the tag workflow publishes WinGet only
// after the GitHub release, and skips without failing when the secret or
// the winget-pkgs package is absent.
func TestWinGetReleaseWorkflow(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Needs string `yaml:"needs"`
			Steps []struct {
				Name string            `yaml:"name"`
				ID   string            `yaml:"id"`
				Run  string            `yaml:"run"`
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	release, ok := wf.Jobs["release"]
	if !ok {
		t.Fatal("release job missing")
	}
	var built, published bool
	for _, s := range release.Steps {
		if strings.TrimSpace(s.Run) == "make dist" {
			built = true
		}
		if s.Uses == "softprops/action-gh-release@v2" {
			published = true
		}
	}
	if !built || !published {
		t.Fatalf("release job must make dist and publish with softprops/action-gh-release, steps=%+v", release.Steps)
	}

	winget, ok := wf.Jobs["winget"]
	if !ok {
		t.Fatal("winget job missing")
	}
	if winget.Needs != "release" {
		t.Fatalf("winget needs = %q, want release", winget.Needs)
	}
	var decided, publishedWinGet bool
	for _, s := range winget.Steps {
		if s.ID == "winget" {
			decided = true
			for _, want := range []string{
				"WINGET_GITHUB_TOKEN",
				"::notice title=WinGet skipped::",
				"publish=false",
				"HTTP 404",
				"VicGupta.TinyAgent",
			} {
				if !strings.Contains(s.Run, want) {
					t.Errorf("decide step missing %q", want)
				}
			}
		}
		if s.Uses == "vedantmgoyal9/winget-releaser@v2" {
			publishedWinGet = true
			if s.If != "steps.winget.outputs.publish == 'true'" {
				t.Errorf("winget publish if = %q", s.If)
			}
			if s.With["identifier"] != "VicGupta.TinyAgent" {
				t.Errorf("identifier = %q", s.With["identifier"])
			}
			re := s.With["installers-regex"]
			if !strings.Contains(re, "tinyagent-windows-") || !strings.Contains(re, `\.zip$`) {
				t.Errorf("installers-regex = %q", re)
			}
			if s.With["token"] != "${{ secrets.WINGET_GITHUB_TOKEN }}" {
				t.Errorf("token input = %q", s.With["token"])
			}
		}
	}
	if !decided || !publishedWinGet {
		t.Fatalf("winget job missing decide step or releaser: %+v", winget.Steps)
	}
}

// TestWindowsZipLayout runs the Makefile recipe that WinGet publishes and
// checks each zip contains only tinyagent.exe with the exe's bytes.
func TestWindowsZipLayout(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not installed")
	}
	if _, err := exec.LookPath("zip"); err != nil {
		t.Skip("zip not installed")
	}
	dir := t.TempDir()
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{
		"tinyagent-windows-amd64.exe": []byte("amd64-bytes"),
		"tinyagent-windows-arm64.exe": []byte("arm64-bytes"),
	}
	for name, body := range payloads {
		if err := os.WriteFile(filepath.Join(dist, name), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("make", "windows-zips", "DISTDIR="+dist)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make windows-zips: %v\n%s", err, out)
	}
	for name, body := range payloads {
		zipName := strings.TrimSuffix(name, ".exe") + ".zip"
		zr, err := zip.OpenReader(filepath.Join(dist, zipName))
		if err != nil {
			t.Fatal(err)
		}
		if len(zr.File) != 1 || zr.File[0].Name != "tinyagent.exe" {
			names := make([]string, len(zr.File))
			for i, f := range zr.File {
				names[i] = f.Name
			}
			zr.Close()
			t.Fatalf("%s entries = %v, want [tinyagent.exe]", zipName, names)
		}
		rc, err := zr.File[0].Open()
		if err != nil {
			zr.Close()
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		zr.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("%s: tinyagent.exe bytes = %q, want %q", zipName, got, body)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := os.Stat(filepath.Join(dist, ".zip-"+arch)); !os.IsNotExist(err) {
			t.Fatalf("staging dir .zip-%s left behind: %v", arch, err)
		}
	}
}
