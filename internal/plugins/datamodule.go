package plugins

// Data modules (prompts, retry policies, skill files) are plain data: they
// load without the WebAssembly sandbox, with only a schema check, a
// content-hash version and an atomic switch. The previous version is kept
// for rollback.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// DataKind is a data module's schema.
type DataKind string

const (
	DataPrompt DataKind = "prompt" // markdown, YAML front-matter with name
	DataSkill  DataKind = "skill"  // markdown, YAML front-matter with description
	DataRetry  DataKind = "retry"  // JSON or TOML retry policy
)

// RetryPolicy is the retry data module's schema.
type RetryPolicy struct {
	MaxAttempts int `json:"max_attempts" toml:"max_attempts"`
	BackoffMS   int `json:"backoff_ms" toml:"backoff_ms"`
}

// DataVersion is one validated version of a data module.
type DataVersion struct {
	Hash    string // sha256 of Content, hex
	Content []byte
	Meta    map[string]any // front-matter fields (prompt, skill)
	Retry   *RetryPolicy   // retry only
}

// DataModule holds the active version of one data module.
type DataModule struct {
	Name  string
	Kind  DataKind
	Taint Tainter // optional; switches are refused once tainted
	// Store, when set, keeps every version under StoreName; Set adds the
	// version and makes it current.
	Store     *ModuleStore
	StoreName string

	mu   sync.Mutex
	cur  atomic.Pointer[DataVersion]
	prev *DataVersion
}

// Current returns the active version, or nil before the first Set.
func (d *DataModule) Current() *DataVersion { return d.cur.Load() }

// Set validates content against the module's schema and atomically makes
// it the active version. Invalid content leaves the old version active.
func (d *DataModule) Set(content []byte) (*DataVersion, error) {
	if d.Taint != nil && d.Taint.Tainted() {
		return nil, ErrTainted
	}
	v, err := validateData(d.Kind, content)
	if err != nil {
		return nil, fmt.Errorf("data module %s: %w", d.Name, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if old := d.cur.Load(); old != nil && old.Hash == v.Hash {
		return old, nil
	}
	if d.Store != nil {
		if _, err := d.Store.Put(d.StoreName, d.ext(content), content, Permissions{}, nil); err != nil {
			return nil, err
		}
		if err := d.Store.SetCurrent(d.StoreName, v.Hash); err != nil {
			return nil, err
		}
	}
	d.prev = d.cur.Swap(v)
	return v, nil
}

func (d *DataModule) ext(content []byte) string {
	if d.Kind != DataRetry {
		return "md"
	}
	if t := bytes.TrimSpace(content); len(t) > 0 && t[0] == '{' {
		return "json"
	}
	return "toml"
}

// LoadStored makes the store's verified current version active.
func (d *DataModule) LoadStored() (*DataVersion, error) {
	if d.Store == nil {
		return nil, errors.New("data module has no store")
	}
	b, _, err := d.Store.ReadCurrent(d.StoreName)
	if err != nil {
		return nil, err
	}
	v, err := validateData(d.Kind, b)
	if err != nil {
		return nil, fmt.Errorf("data module %s: %w", d.Name, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prev = d.cur.Swap(v)
	return v, nil
}

// RollbackTo moves the store's current pointer to an older hash and
// activates it; refused after taint.
func (d *DataModule) RollbackTo(hash string) (*DataVersion, error) {
	if d.Store == nil {
		return nil, errors.New("data module has no store")
	}
	if err := d.Store.Rollback(d.StoreName, hash, d.Taint); err != nil {
		return nil, err
	}
	return d.LoadStored()
}

// SetFile is Set with content read from path.
func (d *DataModule) SetFile(path string) (*DataVersion, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return d.Set(b)
}

// Rollback switches back to the previous version.
func (d *DataModule) Rollback() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.prev == nil {
		return errors.New("no previous version")
	}
	d.prev = d.cur.Swap(d.prev)
	return nil
}

func validateData(kind DataKind, b []byte) (*DataVersion, error) {
	sum := sha256.Sum256(b)
	v := &DataVersion{Hash: hex.EncodeToString(sum[:]), Content: append([]byte{}, b...)}
	switch kind {
	case DataPrompt, DataSkill:
		fm, err := frontMatter(b)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(fm, &v.Meta); err != nil {
			return nil, fmt.Errorf("invalid YAML front-matter: %v", err)
		}
		key := "name"
		if kind == DataSkill {
			key = "description"
		}
		if s, _ := v.Meta[key].(string); strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("front-matter: missing or empty %s", key)
		}
	case DataRetry:
		var p RetryPolicy
		t := bytes.TrimSpace(b)
		if len(t) > 0 && t[0] == '{' {
			dec := json.NewDecoder(bytes.NewReader(t))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&p); err != nil {
				return nil, fmt.Errorf("invalid retry policy JSON: %v", err)
			}
		} else {
			md, err := toml.Decode(string(b), &p)
			if err != nil {
				return nil, fmt.Errorf("invalid retry policy TOML: %v", err)
			}
			if u := md.Undecoded(); len(u) > 0 {
				return nil, fmt.Errorf("retry policy: unknown key %s", u[0])
			}
		}
		if p.MaxAttempts < 1 || p.BackoffMS < 0 {
			return nil, errors.New("retry policy: max_attempts must be >= 1 and backoff_ms >= 0")
		}
		v.Retry = &p
	default:
		return nil, fmt.Errorf("unknown data module kind %q", kind)
	}
	return v, nil
}

// frontMatter returns the YAML between a leading --- line and the next.
func frontMatter(b []byte) ([]byte, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	first, rest, ok := bytes.Cut(b, []byte("\n"))
	if !ok || strings.TrimSpace(string(first)) != "---" {
		return nil, errors.New("missing YAML front-matter (a leading --- line)")
	}
	for off := 0; off <= len(rest); {
		line, _, more := bytes.Cut(rest[off:], []byte("\n"))
		if strings.TrimSpace(string(line)) == "---" {
			return rest[:off], nil
		}
		if !more {
			break
		}
		off += len(line) + 1
	}
	return nil, errors.New("unterminated YAML front-matter")
}
