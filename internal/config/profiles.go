package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// profileName is the accepted character set for profile names.
var profileName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// yamlLine strips yaml.v3's "line N: " prefix, whose line numbers would
// refer to a re-encoded profile rather than the config file.
var yamlLine = regexp.MustCompile(`line \d+: `)

// applyProfiles parses the config file data, validates its optional
// profiles: section and returns the file with that section removed and, if
// profile is not "", the named profile merged over the base settings.
// Without a profiles: key data is returned unchanged. Every error is an
// *InvalidError naming the file.
func applyProfiles(path string, data []byte, profile string) ([]byte, error) {
	invalid := func(format string, args ...any) error {
		return &InvalidError{fmt.Errorf("config %s: "+format, append([]any{path}, args...)...)}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// Leave syntax errors to the regular decode, which reports them.
		if profile != "" {
			return nil, invalid("%v", err)
		}
		return data, nil
	}
	var base *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
		base = doc.Content[0]
	}
	idx := -1
	if base != nil {
		for i := 0; i+1 < len(base.Content); i += 2 {
			if base.Content[i].Value == "profiles" {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		if profile != "" {
			return nil, invalid("--profile %s: this file defines no profiles", profile)
		}
		return data, nil
	}
	section := base.Content[idx+1]
	if section.Kind == yaml.AliasNode {
		section = section.Alias
	}
	if section.Kind != yaml.MappingNode {
		return nil, invalid("profiles must be a mapping of profile names to settings")
	}
	profiles := map[string]*yaml.Node{}
	var names []string
	for i := 0; i+1 < len(section.Content); i += 2 {
		name, body := section.Content[i].Value, section.Content[i+1]
		if !profileName.MatchString(name) {
			return nil, invalid("profile name %q is invalid; use only letters, digits, _ and -", name)
		}
		if _, dup := profiles[name]; dup {
			return nil, invalid("profile %s is defined more than once", name)
		}
		if err := checkProfile(name, body); err != nil {
			return nil, invalid("%v", err)
		}
		if body.Kind == yaml.AliasNode {
			body = body.Alias
		}
		profiles[name] = body
		names = append(names, name)
	}
	// Remove the section, so the regular decode sees only base settings.
	base.Content = append(base.Content[:idx:idx], base.Content[idx+2:]...)
	if profile != "" {
		p, ok := profiles[profile]
		if !ok {
			sort.Strings(names)
			defined := "none"
			if len(names) > 0 {
				defined = strings.Join(names, ", ")
			}
			return nil, invalid("unknown profile %q (defined profiles: %s)", profile, defined)
		}
		mergeNode(base, p)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, invalid("profiles: %v", err)
	}
	return out, nil
}

// checkProfile validates one profile body: a mapping with no nested
// profiles, no null values and only known config keys.
func checkProfile(name string, body *yaml.Node) error {
	if body.Kind == yaml.AliasNode {
		body = body.Alias
	}
	if body.Kind != yaml.MappingNode {
		return fmt.Errorf("profile %s must be a mapping of settings", name)
	}
	for i := 0; i+1 < len(body.Content); i += 2 {
		if body.Content[i].Value == "profiles" {
			return fmt.Errorf("profile %s: profiles can't be nested", name)
		}
	}
	if key := nullKey(body, ""); key != "" {
		return fmt.Errorf("profile %s: %s is null; a profile can only override a setting, not remove it", name, key)
	}
	b, err := yaml.Marshal(body)
	if err != nil {
		return fmt.Errorf("profile %s: %w", name, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	cfg := Default()
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("profile %s: %s", name, yamlLine.ReplaceAllString(err.Error(), ""))
	}
	return nil
}

// nullKey returns the dotted key of the first explicit null value in a
// mapping, or "".
func nullKey(n *yaml.Node, prefix string) string {
	if n.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := prefix+n.Content[i].Value, n.Content[i+1]
		if v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null" {
			return k
		}
		if s := nullKey(v, k+"."); s != "" {
			return s
		}
	}
	return ""
}

// mergeNode merges the mapping over into base: mappings merge key by key,
// recursively; scalars, lists and values of a different kind replace the
// base value.
func mergeNode(base, over *yaml.Node) {
	for i := 0; i+1 < len(over.Content); i += 2 {
		k, v := over.Content[i], over.Content[i+1]
		if v.Kind == yaml.AliasNode {
			v = v.Alias
		}
		found := false
		for j := 0; j+1 < len(base.Content); j += 2 {
			if base.Content[j].Value != k.Value {
				continue
			}
			found = true
			b := base.Content[j+1]
			if b.Kind == yaml.AliasNode {
				b = b.Alias
			}
			if b.Kind == yaml.MappingNode && v.Kind == yaml.MappingNode {
				merged := &yaml.Node{Kind: yaml.MappingNode, Tag: b.Tag, Content: append([]*yaml.Node(nil), b.Content...)}
				mergeNode(merged, v)
				base.Content[j+1] = merged
			} else {
				base.Content[j+1] = v
			}
			break
		}
		if !found {
			base.Content = append(base.Content, k, v)
		}
	}
}
