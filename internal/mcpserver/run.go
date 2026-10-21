package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/procs"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
	"github.com/ericfitz/agentbus2/internal/version"
)

// Run serves stdio until the harness closes the connection or a signal
// arrives. It never writes stdout (the protocol owns it) or stderr; every
// diagnostic goes to the per-process log file.
//
// Order: the log file, the harness lookup (done by NewServer: ErrGone or an
// error is logged and the harness reference stays zero), Sweep of the state
// files whose server and harness are both dead, the server, then the Conn
// close. The state files are left in place (choice 10).
func Run(ctx context.Context, o Options) error {
	self := o.Self
	if self == 0 {
		self = os.Getpid()
		o.Self = self
	}
	dataDir := o.DataDir
	if dataDir == "" {
		d, err := profile.DataDir(o.Client.Environ)
		if err != nil {
			return err
		}
		dataDir = d
	}
	if o.Log == nil {
		log, closeLog, err := OpenLog(dataDir, self)
		if err != nil {
			return err
		}
		defer closeLog()
		o.Log = log
	}
	log := o.Log
	if o.Table == nil {
		o.Table = procs.System
	}
	o.Client.Maintenance = true // the local-mode maintenance tick; server mode ignores it

	if n, err := sessionstate.Sweep(dataDir, o.Table); err != nil {
		log.Warn("sweeping the session state files failed", "err", err)
	} else if n > 0 {
		log.Info("swept session state files", "removed", n)
	}

	// SIGTERM, SIGINT and SIGHUP end the run through ctx so the Conn closes;
	// a harness that only closes stdin gets there too.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()

	srv, closeServer, err := NewServer(ctx, o)
	if err != nil {
		return err
	}
	log.Info("agentbus2 mcp started", "version", version.Version, "data", dataDir)
	err = srv.Run(ctx, &mcp.StdioTransport{})
	if cerr := closeServer(); cerr != nil {
		log.Warn("closing the bus failed", "err", cerr)
	}
	if ctx.Err() != nil {
		return nil // a signal is an orderly end
	}
	if err != nil {
		log.Error("server run failed", "err", err)
	}
	return err
}

// OpenLog opens dataDir/logs/mcp-<pid>.log (O_APPEND, 0600), one file per
// process, and returns a JSON-lines logger on it. The returned func closes
// and removes the file; a second call does nothing. A crash leaves the file
// behind for diagnosis.
func OpenLog(dataDir string, pid int) (*slog.Logger, func(), error) {
	if pid <= 0 {
		return nil, nil, fmt.Errorf("log file: invalid pid %d", pid)
	}
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("mcp-%d.log", pid))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	h := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: utcMillis})
	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			_ = f.Close()
			_ = os.Remove(path)
		})
	}
	return slog.New(h).With("pid", pid), closeFn, nil
}

// utcMillis rewrites the top-level time attribute as UTC RFC3339 with
// millisecond precision, e.g. 2026-09-09T17:04:05.123Z (v1 logfile.go).
func utcMillis(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		a.Value = slog.StringValue(a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	}
	return a
}
