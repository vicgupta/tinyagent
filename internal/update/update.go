// Package update replaces the running binary with the latest GitHub release.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const DefaultAPIURL = "https://api.github.com/repos/vicgupta/tinyagent/releases/latest"

type Updater struct {
	APIURL  string // latest-release endpoint
	Current string // running version, e.g. v0.1.0 or dev
	ExePath string // binary to replace
	Client  *http.Client
	GOOS    string
	GOARCH  string
	Report  Reporter // progress; nil reports nothing
}

func New(current string) (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return nil, err
	}
	return &Updater{
		APIURL:  DefaultAPIURL,
		Current: current,
		ExePath: exe,
		Client:  &http.Client{Timeout: 5 * time.Minute},
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
	}, nil
}

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Run installs the latest release if it is newer than Current and returns a
// message describing what happened. Each step is reported to u.Report, and
// an error is a *StepError naming the step that failed.
func (u *Updater) Run(ctx context.Context) (string, error) {
	r := u.Report
	if r == nil {
		r = nopReporter{}
	}
	fail := func(step string, err error) (string, error) { return "", &StepError{Step: step, Err: err} }

	r.Step(StepCurrent, fmt.Sprintf("%s at %s", u.Current, u.ExePath))
	r.Step(StepQuery, u.APIURL)
	var rel release
	if err := u.getJSON(ctx, u.APIURL, &rel); err != nil {
		return fail(StepQuery, err)
	}
	if !newer(rel.TagName, u.Current) {
		r.Step(StepQuery, fmt.Sprintf("latest is %s; already up to date", rel.TagName))
		return fmt.Sprintf("already up to date (%s)", u.Current), nil
	}
	r.Step(StepQuery, fmt.Sprintf("found %s (current %s)", rel.TagName, u.Current))

	name := fmt.Sprintf("tinyagent-%s-%s", u.GOOS, u.GOARCH)
	if u.GOOS == "windows" {
		name += ".exe"
	}
	urls := map[string]string{}
	for _, a := range rel.Assets {
		urls[a.Name] = a.URL
	}
	if urls[name] == "" {
		return fail(StepSelect, fmt.Errorf("release %s has no binary for %s/%s", rel.TagName, u.GOOS, u.GOARCH))
	}
	if urls["SHA256SUMS"] == "" {
		return fail(StepSelect, fmt.Errorf("release %s has no SHA256SUMS", rel.TagName))
	}
	r.Step(StepSelect, fmt.Sprintf("%s for %s/%s", name, u.GOOS, u.GOARCH))

	r.Step(StepChecksum, "SHA256SUMS")
	sums, err := u.get(ctx, urls["SHA256SUMS"])
	if err != nil {
		return fail(StepChecksum, err)
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return fail(StepChecksum, err)
	}

	r.Step(StepDownload, urls[name])
	bin, err := u.download(ctx, urls[name], name, r)
	if err != nil {
		return fail(StepDownload, err)
	}
	r.Step(StepDownload, fmt.Sprintf("received %s (%d bytes)", Size(int64(len(bin))), len(bin)))

	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return fail(StepVerify, fmt.Errorf("checksum mismatch for %s; not installing", name))
	}
	r.Step(StepVerify, "sha256 "+want+" matches SHA256SUMS")

	r.Step(StepInstall, "replacing "+u.ExePath)
	if err := replace(u.ExePath, bin); err != nil {
		return fail(StepInstall, err)
	}
	r.Step(StepDone, fmt.Sprintf("updated %s -> %s at %s", u.Current, rel.TagName, u.ExePath))
	return fmt.Sprintf("updated %s -> %s (%s)", u.Current, rel.TagName, u.ExePath), nil
}

// replace swaps the file at path for data via a rename in the same directory,
// so the binary is never left half-written.
func replace(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tinyagent-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// A running .exe can't be overwritten, but it can be renamed away.
		old := path + ".old"
		os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}

func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in SHA256SUMS", name)
}

// newer reports whether tag is a later version than current. A current
// version that isn't a release (e.g. "dev") is always older.
func newer(tag, current string) bool {
	t, ok := parse(tag)
	if !ok {
		return false
	}
	c, ok := parse(current)
	if !ok {
		return true
	}
	for i := range t {
		if t[i] != c[i] {
			return t[i] > c[i]
		}
	}
	return false
}

// parse reads "vMAJOR.MINOR.PATCH", ignoring any -prerelease or +build suffix.
func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func (u *Updater) get(ctx context.Context, url string) ([]byte, error) {
	resp, err := u.open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// open GETs url and returns the response when the status is 200.
func (u *Updater) open(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// download GETs url and streams the body through the reporter's progress
// writer as it arrives.
func (u *Updater) download(ctx context.Context, url, name string, r Reporter) ([]byte, error) {
	resp, err := u.open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	w, finish := r.Download(name, resp.ContentLength)
	var body io.Reader = resp.Body
	if w != nil {
		body = io.TeeReader(resp.Body, w)
	}
	data, err := io.ReadAll(body)
	finish()
	return data, err
}

func (u *Updater) getJSON(ctx context.Context, url string, out any) error {
	data, err := u.get(ctx, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
