// Package repoconfig reads and writes the per-repository file
// .local/agentbus2.json. Its one job is to select a profile (Decision 33): the
// file lives in a cloned repository, so it may never carry anything that runs
// or identifies, and every key but "profile" is rejected.
package repoconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ericfitz/agentbus2/internal/store"
)

// File is a parsed .local/agentbus2.json.
type File struct {
	Path    string
	Profile string
}

// FileName is the file's path relative to a repository root, as slashes.
const FileName = ".local/agentbus2.json"

func pathIn(dir string) string { return filepath.Join(dir, ".local", "agentbus2.json") }

// ValidProfileName reports why name cannot name a profile: profile names
// become directory names under credentials/ and tokens/, so besides
// store.NameRule they exclude "." and "..", backslashes and a leading dash.
func ValidProfileName(name string) error {
	if err := store.NameRule(name); err != nil {
		return err
	}
	if name == "." || name == ".." {
		return errors.New("must not be a dot name")
	}
	if strings.ContainsAny(name, `\:`) {
		return errors.New(`must not contain '\' or ':'`)
	}
	return nil
}

// Find walks up from dir to the nearest .local/agentbus2.json, stopping at the
// directory holding the nearest .git (a directory or, in a worktree, a file;
// that directory's own file is still read). It returns (nil, nil) when no file
// is found. A file that is unreadable or malformed, names a bad profile, or
// holds any key but "profile" returns an error naming the path.
func Find(dir string) (*File, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for {
		f, err := load(dir)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return nil, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

func load(dir string) (*File, error) {
	p := pathIn(dir)
	body, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("%s: file is empty, expected a JSON object", p)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%s: must contain exactly one JSON object", p)
	}
	if raw == nil {
		return nil, fmt.Errorf("%s: must be a JSON object, not null", p)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != "profile" {
			return nil, fmt.Errorf("%s: %s may only select a profile: unexpected key %q", p, FileName, k)
		}
	}
	rawProfile, ok := raw["profile"]
	if !ok {
		return nil, fmt.Errorf("%s: missing \"profile\"", p)
	}
	var name string
	if err := json.Unmarshal(rawProfile, &name); err != nil || bytes.Equal(bytes.TrimSpace(rawProfile), []byte("null")) {
		return nil, fmt.Errorf("%s: \"profile\" must be a string", p)
	}
	if name == "" {
		return nil, fmt.Errorf("%s: \"profile\" must not be empty", p)
	}
	if err := ValidProfileName(name); err != nil {
		return nil, fmt.Errorf("%s: \"profile\" %q: %w", p, name, err)
	}
	return &File{Path: p, Profile: name}, nil
}

// Write replaces dir/.local/agentbus2.json with {"profile": profile}, creating
// .local/ if needed. The write is a temp file in the same directory renamed
// into place, so a reader sees the old file or the new one. The file holds no
// secret, so it is 0644.
func Write(dir, profile string) (*File, error) {
	if profile == "" {
		return nil, errors.New("profile must not be empty")
	}
	if err := ValidProfileName(profile); err != nil {
		return nil, fmt.Errorf("profile %q: %w", profile, err)
	}
	p := pathIn(dir)
	d := filepath.Dir(p)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return nil, err
	}
	body, err := json.MarshalIndent(map[string]string{"profile": profile}, "", "  ")
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(d, ".agentbus2-*.tmp")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(0o644); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return nil, err
	}
	return &File{Path: p, Profile: profile}, nil
}
