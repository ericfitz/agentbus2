// Package sessionstate keeps the per-session state file of Decision 78: one
// JSON file per session under <dataDir>/sessions, holding the harness
// reference, the session id, the profile and the resume token, so a restarted
// MCP server or a CLI invocation under the same harness finds its session
// (Decision 41). The files are mode 0600 and replaced atomically.
//
// It imports internal/procs and the standard library only.
package sessionstate

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

	"github.com/ericfitz/agentbus2/internal/procs"
)

// File is the state of one session.
type File struct {
	Session              string `json:"session"`
	Identity             string `json:"identity"`
	Profile              string `json:"profile"`
	DisplayName          string `json:"display_name"`
	ResumeToken          string `json:"resume_token"`
	HarnessPID           int    `json:"harness_pid"`
	HarnessStart         int64  `json:"harness_start"`
	HarnessClient        string `json:"harness_client"`
	HarnessClientVersion string `json:"harness_client_version,omitempty"`
	MCPPID               int    `json:"mcp_pid"`
	Primary              bool   `json:"primary"`
	OpenedAt             int64  `json:"opened_at"`
}

const ext = ".json"

// Dir is the directory that holds the state files.
func Dir(dataDir string) string { return filepath.Join(dataDir, "sessions") }

// checkSession refuses an empty session id and any that is not a plain file
// name stem, so a session id can never address a path outside Dir.
func checkSession(session string) error {
	if session == "" {
		return errors.New("sessionstate: session is required")
	}
	if session == "." || session == ".." || strings.ContainsAny(session, "/\\\x00") {
		return fmt.Errorf("sessionstate: invalid session %q", session)
	}
	return nil
}

func path(dataDir, session string) string { return filepath.Join(Dir(dataDir), session+ext) }

// Write stores f atomically as Dir/<session>.json (mode 0600), creating Dir
// (mode 0700) when needed. A second Write of the same session replaces the
// file whole.
func Write(dataDir string, f File) error {
	if err := checkSession(f.Session); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(dataDir), 0o700); err != nil {
		return err
	}
	return writeAtomic(path(dataDir, f.Session), append(data, '\n'))
}

// writeAtomic writes data to path through a temp file created O_EXCL with mode
// 0600 in path's directory, fsynced, closed and renamed over path. The temp
// file is removed on any error.
func writeAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp") // O_EXCL, mode 0600
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func decode(b []byte) (File, error) {
	var f File
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return File{}, errors.New("trailing data after the JSON document")
	}
	if err := checkSession(f.Session); err != nil {
		return File{}, err
	}
	return f, nil
}

// Read returns the state of session. The JSON is strict: an unknown member, a
// trailing document or a session member that differs from the file name is an
// error.
func Read(dataDir, session string) (File, error) {
	if err := checkSession(session); err != nil {
		return File{}, err
	}
	p := path(dataDir, session)
	b, err := os.ReadFile(p)
	if err != nil {
		return File{}, err
	}
	f, err := decode(b)
	if err != nil {
		return File{}, fmt.Errorf("sessionstate: %s: %w", p, err)
	}
	if f.Session != session {
		return File{}, fmt.Errorf("sessionstate: %s: session %q does not match the file name", p, f.Session)
	}
	return f, nil
}

// Remove deletes the state of session; nil when there is none.
func Remove(dataDir, session string) error {
	if err := checkSession(session); err != nil {
		return err
	}
	if err := os.Remove(path(dataDir, session)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List returns every parsable state file ordered by OpenedAt, then Session. An
// unparsable file is skipped; the first such error is returned only when
// nothing parsed. A missing directory is an empty list.
func List(dataDir string) ([]File, error) {
	ents, err := os.ReadDir(Dir(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []File
	var firstErr error
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ext) {
			continue
		}
		f, err := Read(dataDir, strings.TrimSuffix(name, ext))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OpenedAt != out[j].OpenedAt {
			return out[i].OpenedAt < out[j].OpenedAt
		}
		return out[i].Session < out[j].Session
	})
	if len(out) == 0 {
		return nil, firstErr
	}
	return out, nil
}

// matches reports whether f belongs to harness h: the same pid and, when both
// starts are known, the same start time (as procs.Alive).
func matches(f File, h procs.Ref) bool {
	return f.HarnessPID == h.Pid && (h.Start == 0 || f.HarnessStart == 0 || f.HarnessStart == h.Start)
}

// ForHarness returns the session of harness h: among the files for h the
// earliest-opened Primary one (then by Session, for a total order); with no
// Primary, the earliest-opened. False when there is none.
func ForHarness(dataDir string, h procs.Ref) (File, bool, error) {
	if h.Pid <= 0 {
		return File{}, false, nil
	}
	all, err := List(dataDir)
	if err != nil {
		return File{}, false, err
	}
	var best, firstAny *File
	for i := range all { // List order is OpenedAt, Session
		f := &all[i]
		if !matches(*f, h) {
			continue
		}
		if firstAny == nil {
			firstAny = f
		}
		if f.Primary && best == nil {
			best = f
		}
	}
	if best == nil {
		best = firstAny
	}
	if best == nil {
		return File{}, false, nil
	}
	return *best, true, nil
}

// Sweep removes each state file whose MCP server process is not alive and
// whose harness is not alive either. A file with a live harness and a dead MCP
// server stays: it is what a restarted MCP server resumes from. It returns the
// number removed.
func Sweep(dataDir string, t procs.Table) (removed int, err error) {
	all, err := List(dataDir)
	if err != nil {
		return 0, err
	}
	for _, f := range all {
		if procs.Alive(t, procs.Ref{Pid: f.MCPPID}) || procs.Alive(t, procs.Ref{Pid: f.HarnessPID, Start: f.HarnessStart}) {
			continue
		}
		if err := Remove(dataDir, f.Session); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
