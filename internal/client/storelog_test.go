package client

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/storetest"
)

// captureStderr points os.Stderr and the log package's output (the sink of
// slog.Default) at a file until the returned func runs, which restores both
// and returns what was written.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	saved, savedLog := os.Stderr, log.Writer()
	os.Stderr = f
	log.SetOutput(f)
	return func() string {
		os.Stderr = saved
		log.SetOutput(savedLog)
		_ = f.Close()
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

// TestLocalStoreLogger (issue #20): the Store's read-normalization error goes
// to Options.Logger; with no Logger (the TUI's path: only an OpenLogger on the
// terminal) nothing reaches stderr, slog.Default or the OpenLogger.
func TestLocalStoreLogger(t *testing.T) {
	ctx := context.Background()
	// pat is an administrator holding a scope: every call loads the principal.
	scopedAdmin := func(t *testing.T) *env {
		e := localEnv(t)
		storetest.ScopedAdminRaw(t, profile.SQLitePath(profile.Profile{Name: "default", Mode: profile.ModeLocal}, e.data), "pat", "acme")
		return e
	}

	t.Run("logger", func(t *testing.T) {
		e := scopedAdmin(t)
		var logs lockedBuf
		c := e.openAs(t, "pat", func(o *Options) { o.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
		if _, err := c.Bus.ListChannels(ctx); err != nil {
			t.Fatal(err)
		}
		if s := logs.String(); !strings.Contains(s, storetest.NormalizationError) || !strings.Contains(s, "identity=pat") {
			t.Fatalf("Options.Logger has %q, want the normalization error naming pat", s)
		}
	})

	t.Run("no logger", func(t *testing.T) {
		e := scopedAdmin(t)
		var term lockedBuf
		done := captureStderr(t)
		c, err := Open(ctx, Options{Environ: e.environ, Cwd: t.TempDir(), Identity: "pat", Config: noMerge(),
			OpenLogger: slog.New(slog.NewTextHandler(&term, &slog.HandlerOptions{Level: slog.LevelWarn}))})
		if err == nil {
			_, err = c.Bus.ListChannels(ctx)
			_ = c.Close()
		}
		stderr := done()
		if err != nil {
			t.Fatal(err)
		}
		if stderr != "" || term.String() != "" {
			t.Fatalf("stderr %q, OpenLogger %q: want both empty", stderr, term.String())
		}
	})
}
