// Package plugins loads Agent Plugins 1.0.0 packages (https://agent-plugins.org).
//
// This file validates a plugin's root plugin.json manifest under the spec's
// containment rules. It never makes a network request: the $schema value is
// only compared against the one recognized URL.
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaV1 is the only plugin.json $schema value recognized.
const SchemaV1 = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"

// ManifestFile is the manifest's name at the plugin root.
const ManifestFile = "plugin.json"

// MaxManifestBytes caps how much of plugin.json is read.
const MaxManifestBytes = 1 << 20

// knownFields are the manifest's top-level fields in the 1.0.0 schema.
var knownFields = map[string]bool{
	"$schema": true, "name": true, "version": true, "description": true,
	"author": true, "homepage": true, "repository": true, "license": true,
	"keywords": true, "extensions": true,
}

// Manifest is a validated plugin.json.
type Manifest struct {
	Name        string
	Version     string
	Description string
	Root        string // filesystem-resolved plugin root
	Extensions  map[string]json.RawMessage
	Warnings    []string // non-fatal problems (unknown fields, bad extensions)
}

// ValidName reports whether s is a valid plugin name: 1-64 characters of
// [a-z0-9.-], starting and ending with [a-z0-9], with no "--" or "..".
func ValidName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	if strings.Contains(s, "--") || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !alnum && c != '.' && c != '-' {
			return false
		}
		if (i == 0 || i == len(s)-1) && !alnum {
			return false
		}
	}
	return true
}

// ResolveInside resolves path through symlinks and returns it only when the
// result stays within the already-resolved root.
func ResolveInside(root, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s resolves outside the plugin root", path)
	}
	return resolved, nil
}

// LoadManifest reads and validates <dir>/plugin.json. An error means the
// plugin is rejected; non-fatal problems are returned in Manifest.Warnings.
func LoadManifest(dir string) (*Manifest, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("plugin path %q must be absolute", dir)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("plugin path %s: %w", dir, err)
	}
	if fi, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("plugin path %s: %w", dir, err)
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("plugin path %s is not a directory", dir)
	}
	path, err := ResolveInside(root, filepath.Join(root, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", ManifestFile)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", ManifestFile, MaxManifestBytes)
	}
	return parseManifest(root, data)
}

func parseManifest(root string, data []byte) (*Manifest, error) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON object: %w", ManifestFile, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%s: must be a JSON object", ManifestFile)
	}
	if dec.More() {
		return nil, errors.New(ManifestFile + ": trailing data after JSON object")
	}
	var schema string
	if v, ok := raw["$schema"]; !ok {
		return nil, errors.New(ManifestFile + ": missing $schema")
	} else if json.Unmarshal(v, &schema) != nil || schema != SchemaV1 {
		return nil, fmt.Errorf("%s: unsupported $schema (want %s)", ManifestFile, SchemaV1)
	}
	m := &Manifest{Root: root}
	v, ok := raw["name"]
	if !ok {
		return nil, errors.New(ManifestFile + ": missing name")
	}
	if json.Unmarshal(v, &m.Name) != nil || !ValidName(m.Name) {
		return nil, fmt.Errorf("%s: invalid name %s", ManifestFile, string(v))
	}
	optString := func(key string, dst *string) {
		if v, ok := raw[key]; ok && json.Unmarshal(v, dst) != nil {
			m.Warnings = append(m.Warnings, fmt.Sprintf("%s: %s is not a string; ignored", ManifestFile, key))
			*dst = ""
		}
	}
	optString("version", &m.Version)
	optString("description", &m.Description)
	if v, ok := raw["extensions"]; ok {
		var obj map[string]json.RawMessage
		if json.Unmarshal(v, &obj) != nil || obj == nil {
			m.Warnings = append(m.Warnings, ManifestFile+": extensions is not an object; ignored")
		} else {
			m.Extensions = obj
		}
	}
	var unknown []string
	for k := range raw {
		if !knownFields[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		m.Warnings = append(m.Warnings, fmt.Sprintf("%s: unknown field %q ignored", ManifestFile, k))
	}
	return m, nil
}
