// Package tinygo manages the TinyGo compiler TinyAgent uses to build Go
// plugin source into WebAssembly.
//
// TinyGo is an external dependency shipped separately by the TinyGo
// project; it is NOT part of TinyAgent. It is never embedded in the
// TinyAgent binary (this package has no go:embed and bundles no archives)
// and never committed to the repository. TinyAgent downloads the pinned
// release below on first use (or with `tinyagent tinygo install`), checks
// it against the SHA-256 pinned in this file before extracting anything,
// and caches it under the user cache directory. An existing TinyGo can be
// used instead with the tinygo config key or TINYAGENT_TINYGO.
//
// Bumping the pinned version: set Version, replace every Checksums entry
// with the sha256 of the matching tinygo<ver>.<os>-<arch>.tar.gz release
// asset (`gh release view v<ver> -R tinygo-org/tinygo --json assets` lists
// them as digests), and run `tinyagent tinygo update`.
package tinygo

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Version is the pinned TinyGo release.
const Version = "0.44.0"

// Checksums are the pinned sha256 digests of the release archives, by
// GOOS/GOARCH.
var Checksums = map[string]string{
	"linux/amd64":  "ed0f45543a49ff2a99120a5a1b8814a0750e1fda1a66f8e2a0a172576134aaf9",
	"linux/arm64":  "48cece9e1e886a176caad0918182d90f8348ad3084774e6239f42e5e11e40990",
	"darwin/amd64": "405b8241e20825cd3e7267706eb27c4f40fe372f3548a4d7f80ef2eb640957a7",
	"darwin/arm64": "696a37db149c2c37ed0da96cc1f2cca2630fca0c4dd19823a2a481c9b4a6a748",
}

// DefaultBaseURL serves the release archives.
const DefaultBaseURL = "https://github.com/tinygo-org/tinygo/releases/download"

// EnvOverride names the environment variable pointing at an existing
// tinygo executable.
const EnvOverride = "TINYAGENT_TINYGO"

// MaxArchiveBytes caps a download.
const MaxArchiveBytes = 1 << 30

// ErrMissing is returned by Resolve when no TinyGo is available and
// downloading wasn't allowed.
var ErrMissing = errors.New("TinyGo is not installed; run `tinyagent tinygo install` (it downloads the pinned TinyGo release, a separate project, not part of TinyAgent) or set the tinygo config key / " + EnvOverride)

// Manager resolves, installs and updates the TinyGo toolchain.
type Manager struct {
	CacheDir  string // holds tinygo-<version>/
	BaseURL   string
	Version   string
	Checksums map[string]string
	GOOS      string
	GOARCH    string
	Override  string // an existing tinygo executable
	HTTP      *http.Client
}

// Default is the Manager for this build: the pinned release, cached under
// os.UserCacheDir()/tinyagent/toolchains, with override (config) or
// TINYAGENT_TINYGO taking precedence.
func Default(override string) (*Manager, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("tinygo: no user cache directory: %w", err)
	}
	if override == "" {
		override = os.Getenv(EnvOverride)
	}
	return &Manager{
		CacheDir: filepath.Join(dir, "tinyagent", "toolchains"),
		BaseURL:  DefaultBaseURL, Version: Version, Checksums: Checksums,
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Override: override,
	}, nil
}

func (m *Manager) platform() string { return m.GOOS + "/" + m.GOARCH }

// Checksum is the pinned digest for this platform.
func (m *Manager) Checksum() (string, error) {
	c, ok := m.Checksums[m.platform()]
	if !ok {
		return "", fmt.Errorf("tinygo: no pinned TinyGo %s archive for %s; install TinyGo yourself and set the tinygo config key or %s", m.Version, m.platform(), EnvOverride)
	}
	return c, nil
}

// Dir is the cache directory of the pinned version.
func (m *Manager) Dir() string { return filepath.Join(m.CacheDir, "tinygo-"+m.Version) }

// Bin is the tinygo executable inside Dir.
func (m *Manager) Bin() string {
	exe := "tinygo"
	if m.GOOS == "windows" {
		exe += ".exe"
	}
	return filepath.Join(m.Dir(), "bin", exe)
}

const marker = ".tinyagent-verified"

// cached reports whether Dir holds an install verified against the pinned
// checksum.
func (m *Manager) cached() bool {
	want, err := m.Checksum()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(m.Dir(), marker))
	if err != nil || strings.TrimSpace(string(b)) != want {
		return false
	}
	fi, err := os.Stat(m.Bin())
	return err == nil && fi.Mode().IsRegular()
}

// Resolve returns the tinygo executable and where it came from
// ("override", "cache" or "download"). Without download it returns
// ErrMissing when nothing is cached.
func (m *Manager) Resolve(ctx context.Context, download bool) (string, string, error) {
	if m.Override != "" {
		fi, err := os.Stat(m.Override)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			return "", "", fmt.Errorf("tinygo: override %s is not an executable file", m.Override)
		}
		return m.Override, "override", nil
	}
	if m.cached() {
		return m.Bin(), "cache", nil
	}
	if !download {
		return "", "", ErrMissing
	}
	p, _, err := m.Install(ctx)
	return p, "download", err
}

// Install downloads the pinned release, verifies its checksum before
// extracting and caches it. already is true when a verified install was
// cached and nothing was downloaded. A mismatch leaves nothing in the cache.
func (m *Manager) Install(ctx context.Context) (bin string, already bool, err error) {
	want, err := m.Checksum()
	if err != nil {
		return "", false, err
	}
	if m.cached() {
		return m.Bin(), true, nil
	}
	if err := os.MkdirAll(m.CacheDir, 0o755); err != nil {
		return "", false, err
	}
	arch, err := m.download(ctx, want)
	if err != nil {
		return "", false, err
	}
	defer os.Remove(arch)
	tmp, err := os.MkdirTemp(m.CacheDir, ".extract-*")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(tmp)
	if err := extractTarGz(arch, tmp); err != nil {
		return "", false, fmt.Errorf("tinygo: extracting %s: %w", filepath.Base(arch), err)
	}
	top := filepath.Join(tmp, "tinygo")
	if fi, err := os.Stat(filepath.Join(top, "bin", filepath.Base(m.Bin()))); err != nil || !fi.Mode().IsRegular() {
		return "", false, errors.New("tinygo: archive has no tinygo/bin/tinygo")
	}
	if err := os.WriteFile(filepath.Join(top, marker), []byte(want+"\n"), 0o644); err != nil {
		return "", false, err
	}
	os.RemoveAll(m.Dir()) // an unverified leftover
	if err := os.Rename(top, m.Dir()); err != nil {
		return "", false, err
	}
	return m.Bin(), false, nil
}

func (m *Manager) archiveName() string {
	return fmt.Sprintf("tinygo%s.%s-%s.tar.gz", m.Version, m.GOOS, m.GOARCH)
}

// download fetches the archive into the cache dir and checks its sha256.
func (m *Manager) download(ctx context.Context, want string) (string, error) {
	url := strings.TrimRight(m.BaseURL, "/") + "/v" + m.Version + "/" + m.archiveName()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	c := m.HTTP
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("tinygo: downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tinygo: downloading %s: %s", url, resp.Status)
	}
	f, err := os.CreateTemp(m.CacheDir, ".download-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, MaxArchiveBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxArchiveBytes {
		err = fmt.Errorf("archive is larger than %d bytes", MaxArchiveBytes)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("tinygo: downloading %s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(f.Name())
		return "", fmt.Errorf("tinygo: checksum mismatch for %s: got sha256 %s, pinned %s; refusing to install", m.archiveName(), got, want)
	}
	return f.Name(), nil
}

// extractTarGz extracts into dst, refusing absolute paths, "..", entries
// outside dst, links that point outside dst, and special files.
func extractTarGz(path, dst string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dst, h.Name)
		if err != nil {
			return err
		}
		if err := noLinkInPath(dst, target); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0o755|0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("symlink %s -> %s points outside the archive", h.Name, h.Linkname)
			}
			if _, err := safeJoin(dst, filepath.Join(filepath.Dir(h.Name), h.Linkname)); err != nil {
				return fmt.Errorf("symlink %s -> %s points outside the archive", h.Name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			src, err := safeJoin(dst, h.Linkname)
			if err != nil {
				return fmt.Errorf("hard link %s -> %s points outside the archive", h.Name, h.Linkname)
			}
			if err := noLinkInPath(dst, src); err != nil {
				return err
			}
			if err := os.Link(src, target); err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("unsupported entry type %q for %s", h.Typeflag, h.Name)
		}
	}
}

// safeJoin joins a relative archive name to dst, refusing escapes.
func safeJoin(dst, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return filepath.Join(dst, clean), nil
}

// noLinkInPath refuses a target whose parent directories (below dst)
// include a symlink, so a symlink entry can't redirect later writes.
func noLinkInPath(dst, target string) error {
	rel, err := filepath.Rel(dst, filepath.Dir(target))
	if err != nil || rel == "." {
		return err
	}
	p := dst
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive path %s goes through a symlink", target)
		}
	}
	return nil
}

// Update installs the pinned version when it isn't cached and removes
// cached installs of other versions. It returns the removed directories.
func (m *Manager) Update(ctx context.Context) (bin string, already bool, removed []string, err error) {
	bin, already, err = m.Install(ctx)
	if err != nil {
		return "", false, nil, err
	}
	ents, _ := os.ReadDir(m.CacheDir)
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), "tinygo-") && e.Name() != filepath.Base(m.Dir()) {
			p := filepath.Join(m.CacheDir, e.Name())
			if err := os.RemoveAll(p); err != nil {
				return bin, already, removed, err
			}
			removed = append(removed, p)
		}
	}
	return bin, already, removed, nil
}

// Status describes the toolchain.
type Status struct {
	Pinned, Platform, Checksum string
	Override                   string
	Installed                  bool   // pinned version cached and verified
	Path                       string // executable that would be used
	Others                     []string
}

// Status reports the pinned and cached toolchain without downloading.
func (m *Manager) Status() Status {
	s := Status{Pinned: m.Version, Platform: m.platform(), Override: m.Override}
	s.Checksum, _ = m.Checksum()
	s.Installed = m.cached()
	switch {
	case m.Override != "":
		s.Path = m.Override
	case s.Installed:
		s.Path = m.Bin()
	}
	ents, _ := os.ReadDir(m.CacheDir)
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), "tinygo-") && e.Name() != filepath.Base(m.Dir()) {
			s.Others = append(s.Others, strings.TrimPrefix(e.Name(), "tinygo-"))
		}
	}
	return s
}
