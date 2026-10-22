//go:build unix

package sqlite

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ericfitz/agentbus2/internal/store"
)

// withUmask sets the process umask for the test. Not parallel: the umask is
// process-wide.
func withUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func bufLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// TestOpenCreatesPrivateFile: the database holds the server signing key in
// clear, so a fresh file is 0600 under umask 022, and the -wal and -shm files
// SQLite creates next to it follow its mode.
func TestOpenCreatesPrivateFile(t *testing.T) {
	withUmask(t, 0o022)
	path := newPath(t)
	log, buf := bufLogger()
	s := mustOpen(t, path, Options{Logger: log})
	if got := mode(t, path); got != 0o600 {
		t.Fatalf("database mode = %#o, want 0600", got)
	}
	if err := s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.CreateIdentity(ctx, "pat", fixedNow)
	}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if got := mode(t, path+suffix); got != 0o600 {
			t.Errorf("%s mode = %#o, want 0600", suffix, got)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected log output on a fresh file: %s", buf.String())
	}
}

// TestOpenWarnsOnReadableFile: without RequirePrivate (local mode) an existing
// group- or world-readable file is opened with a warning naming the path and
// mode; the mode is left as it is. A local database has no signing key, so the
// warning does not mention one.
func TestOpenWarnsOnReadableFile(t *testing.T) {
	withUmask(t, 0o022)
	for _, perm := range []os.FileMode{0o644, 0o640, 0o604} {
		path := newPath(t)
		_ = mustOpen(t, path, Options{}).Close()
		if err := os.Chmod(path, perm); err != nil {
			t.Fatal(err)
		}
		log, buf := bufLogger()
		mustOpen(t, path, Options{Logger: log})
		out := buf.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, path) || !strings.Contains(out, fmt.Sprintf("%#o", perm)) {
			t.Errorf("perm %#o: warning lacks level, path or mode: %q", perm, out)
		}
		if strings.Contains(out, "signing key") {
			t.Errorf("perm %#o: local-mode warning mentions a signing key: %q", perm, out)
		}
		if got := mode(t, path); got != perm {
			t.Errorf("perm %#o: mode changed to %#o", perm, got)
		}
	}
}

// TestOpenRequirePrivate: with RequirePrivate an existing file readable by
// group or others is refused with an error naming the path, the mode and the
// fix; a 0600 file opens and a missing file is created 0600.
func TestOpenRequirePrivate(t *testing.T) {
	withUmask(t, 0o022)
	for _, perm := range []os.FileMode{0o644, 0o640, 0o604, 0o620, 0o602} {
		path := newPath(t)
		_ = mustOpen(t, path, Options{}).Close()
		if err := os.Chmod(path, perm); err != nil {
			t.Fatal(err)
		}
		_, err := openAt(t, path, Options{RequirePrivate: true})
		if err == nil {
			t.Fatalf("perm %#o: Open succeeded, want a refusal", perm)
		}
		for _, want := range []string{path, fmt.Sprintf("%#o", perm), "chmod 600", "signing key"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("perm %#o: error %q lacks %q", perm, err, want)
			}
		}
	}
	path := newPath(t)
	_ = mustOpen(t, path, Options{RequirePrivate: true}).Close() // missing: created 0600
	if got := mode(t, path); got != 0o600 {
		t.Fatalf("created mode = %#o, want 0600", got)
	}
	mustOpen(t, path, Options{RequirePrivate: true}) // 0600: opens
}

// TestOpenQuietOnPrivateFile: a second open of a 0600 file logs nothing.
func TestOpenQuietOnPrivateFile(t *testing.T) {
	withUmask(t, 0o022)
	path := newPath(t)
	_ = mustOpen(t, path, Options{}).Close()
	log, buf := bufLogger()
	mustOpen(t, path, Options{Logger: log})
	if buf.Len() != 0 {
		t.Errorf("unexpected log output: %s", buf.String())
	}
	if got := mode(t, path); got != 0o600 {
		t.Errorf("mode = %#o, want 0600", got)
	}
}

// TestOpenMissingDirectory: a missing parent directory is an error naming the
// path, and nothing is created.
func TestOpenMissingDirectory(t *testing.T) {
	path := newPath(t) + "/no/such/dir/x.db"
	if _, err := openAt(t, path, Options{}); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Open = %v, want an error naming %s", err, path)
	}
}

// TestOpenRacingCreatesPrivateFile: openers racing on a missing file all
// succeed (the losers of O_EXCL take the existing-file path) and the file is
// 0600 without a warning.
func TestOpenRacingCreatesPrivateFile(t *testing.T) {
	withUmask(t, 0o022)
	path := newPath(t)
	log, buf := bufLogger()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := openAt(t, path, Options{Logger: log})
			if err == nil {
				err = s.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	if got := mode(t, path); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected log output: %s", buf.String())
	}
}
