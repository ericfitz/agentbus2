package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/filelock"
	"github.com/ericfitz/agentbus2/internal/procs"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// The session registry and the state file (Decisions 41, 78, 79; Milestone 6
// choice 10).
//
// State (Definition of Done D):
//   - Second run: a restart under the same harness finds the primary state
//     file whose MCP server is dead and resumes that session; the file is
//     rewritten with the new pid, and the session id and token are unchanged.
//   - Overlapping runs: two servers under one harness each mark their own
//     first hintless session primary; the harness's hooks take the earliest
//     opened_at. register runs under an exclusive lock on the sessions
//     directory, so two restarting servers cannot resume the same file: the
//     second finds the file already carrying the first's live pid.
//   - After a failed run: an exit, clean or not, leaves the files. A file whose
//     session was closed elsewhere, or swept past retention, is replaced by a
//     fresh session on the next register. Only close_session removes a file
//     on purpose; Run's Sweep removes the files of dead servers and harnesses.

// findResumable returns the primary state file a restarted server should
// resume: this harness's, on this profile and identity, whose MCP server is
// dead, the earliest opened. The caller holds s.smu and the directory lock.
func (s *server) findResumable(dataDir string, r client.Resolved) *sessionstate.File {
	if s.harness.Pid <= 0 {
		return nil
	}
	all, err := sessionstate.List(dataDir)
	if err != nil {
		s.log.Warn("listing session state files failed", "err", err)
	}
	for i := range all {
		f := all[i]
		if !f.Primary || f.Profile != r.Profile.Name || f.Identity != r.Identity {
			continue
		}
		if f.HarnessPID != s.harness.Pid || (s.harness.Start != 0 && f.HarnessStart != 0 && f.HarnessStart != s.harness.Start) {
			continue
		}
		if f.MCPPID == s.self || procs.Alive(s.tbl, procs.Ref{Pid: f.MCPPID}) {
			continue
		}
		if _, mine := s.files[f.Session]; mine {
			continue
		}
		return &f
	}
	return nil
}

// lockDir takes the exclusive cross-process lock register runs under.
func lockDir(dataDir string) (func(), error) {
	dir := sessionstate.Dir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".register.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// fileFor returns the state of session id: this server's registry first, then
// the file on disk when it belongs to this bus (a session opened by an
// earlier server under the same harness, whose id the model still holds).
func (s *server) fileFor(dataDir string, r client.Resolved, id string) (sessionstate.File, bool) {
	s.smu.Lock()
	f, ok := s.files[id]
	s.smu.Unlock()
	if ok {
		return f, true
	}
	f, err := sessionstate.Read(dataDir, id)
	if err != nil || f.Profile != r.Profile.Name || f.Identity != r.Identity {
		return sessionstate.File{}, false
	}
	s.smu.Lock()
	s.files[id] = f
	s.smu.Unlock()
	return f, true
}

// resumeLapsed opens the session's stored resume token once. It returns the
// session id to retry with (ok false: no token is known, the caller keeps its
// original error). A token past retention yields a fresh session under a new
// id; the state file is replaced and the caller reports it.
func (s *server) resumeLapsed(ctx context.Context, c *client.Conn, id string) (newID string, ok bool, err error) {
	dataDir, err := s.dataDir()
	if err != nil {
		return "", false, err
	}
	f, found := s.fileFor(dataDir, c.Resolved, id)
	if !found || f.ResumeToken == "" {
		return "", false, nil
	}
	sess, err := c.Bus.Open(ctx, bus.OpenSessionInput{ResumeToken: f.ResumeToken})
	if err != nil {
		return "", false, err
	}
	s.smu.Lock()
	defer s.smu.Unlock()
	if sess.ID == id {
		if f.DisplayName != sess.DisplayName || f.ResumeToken != sess.ResumeToken {
			f.DisplayName, f.ResumeToken = sess.DisplayName, sess.ResumeToken
			if err := s.putLocked(dataDir, f); err != nil {
				return "", false, bus.Internal(err)
			}
		}
		return id, true, nil
	}
	_ = sessionstate.Remove(dataDir, id)
	delete(s.files, id)
	nf := f
	nf.Session, nf.DisplayName, nf.ResumeToken = sess.ID, sess.DisplayName, sess.ResumeToken
	nf.OpenedAt = s.nowMs()
	if err := s.putLocked(dataDir, nf); err != nil {
		return "", false, bus.Internal(err)
	}
	return sess.ID, true, nil
}

// putLocked writes f and records it in the registry; s.smu is held.
func (s *server) putLocked(dataDir string, f sessionstate.File) error {
	if err := sessionstate.Write(dataDir, f); err != nil {
		return fmt.Errorf("writing the session state file: %w", err)
	}
	s.files[f.Session] = f
	return nil
}

// dropFile removes the state file of id and forgets it.
func (s *server) dropFile(dataDir, id string) error {
	s.smu.Lock()
	defer s.smu.Unlock()
	delete(s.files, id)
	return sessionstate.Remove(dataDir, id)
}

// withSession runs f against the session and, when the bus answers
// session_lapsed, resumes it once with the stored token and runs f again
// (choice 10). Any other error, and a second lapse, are returned as they are.
func withSession[T any](ctx context.Context, s *server, session string, f func(b bus.Bus, id string) (T, error)) (T, error) {
	var zero T
	c, err := s.getConn()
	if err != nil {
		return zero, err
	}
	out, err := f(c.Bus, session)
	if bus.CodeOf(err) != bus.CodeSessionLapsed {
		return out, err
	}
	id, ok, rerr := s.resumeLapsed(ctx, c, session)
	if rerr != nil {
		return zero, rerr
	}
	if !ok {
		return out, err
	}
	if id != session {
		return zero, bus.Errorf(bus.CodeSessionLapsed, false,
			"session %s lapsed past retention and cannot resume; a new session %s was opened, pass it instead", session, id)
	}
	return f(c.Bus, id)
}
