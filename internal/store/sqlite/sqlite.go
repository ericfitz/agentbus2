// Package sqlite is the SQLite engine of the store: Open, the sqlstore
// dialect and the embedded migration set.
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	sqlitedrv "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/ericfitz/agentbus2/internal/search"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// ApplicationID is stamped into the file header (Decision 34): "ab2\0". It is
// distinct from v1's 0 (v1 stamps user_version, not application_id).
const ApplicationID int32 = 0x61623200

// defaultBusy is the busy timeout of Decision 69.
const defaultBusy = 5 * time.Second

// Options configures Open.
type Options struct {
	BusyTimeout time.Duration // 0: 5 s (Decision 69)
	Store       sqlstore.Options
}

// DSN builds the "file:" URI DSN for the database file at path, using
// net/url so a directory name containing '?', '#', or '%' (which change a
// naively concatenated DSN's meaning) is percent-encoded into the URI's path.
// The connection parameters are fixed and go in RawQuery verbatim:
// modernc.org/sqlite parses each _pragma value as a literal "PRAGMA ..."
// statement, so "(" and ")" must not be escaped. Ported from v1
// internal/bus/bus.go (SQLiteDSN); the file name is the caller's and
// busy_timeout comes from busy (0: 5 s).
func DSN(path string, busy time.Duration) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if busy <= 0 {
		busy = defaultBusy
	}
	u := url.URL{
		Scheme: "file",
		Path:   uriPath(filepath.ToSlash(abs)),
		RawQuery: fmt.Sprintf("_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
			busy.Milliseconds()),
	}
	return u.String(), nil
}

// uriPath turns a slash-separated absolute path into a URI path. A Windows
// path (C:/...) gains a leading slash so the DSN reads file:///C:/...; without
// it url.URL writes file://C:..., and SQLite parses C: as the URI authority
// and refuses it. Ported from v1 internal/bus/bus.go (sqliteURIPath).
func uriPath(slashed string) string {
	if strings.HasPrefix(slashed, "/") {
		return slashed
	}
	return "/" + slashed
}

// Open opens (creating when empty) the database file at path under the
// Decision 34 rules.
func Open(ctx context.Context, path string, opts Options) (store.Store, error) {
	dsn, err := DSN(path, opts.BusyTimeout)
	if err != nil {
		return nil, err
	}
	db, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	busy := opts.BusyTimeout
	if busy <= 0 {
		busy = defaultBusy
	}
	if err := pingBusy(ctx, db, busy); err != nil {
		_ = db.Close()
		return nil, err
	}
	so := opts.Store
	if so.Migrations == nil {
		so.Migrations = Migrations()
	}
	s, err := sqlstore.Open(ctx, db, dialect{}, so)
	if err != nil {
		return nil, err
	}
	if err := ensureIncrementalVacuum(ctx, db); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("sqlite: auto_vacuum: %w", err)
	}
	return s, nil
}

// pingBusy opens the first connection, retrying SQLITE_BUSY for up to busy.
// The connection's journal_mode(WAL) pragma runs while the connection is
// being opened, and on a brand-new file two openers racing to switch the mode
// get SQLITE_BUSY at once instead of waiting on the busy handler. Once the
// file is in WAL mode the pragma is a no-op, so only this first connection
// can race.
func pingBusy(ctx context.Context, db *sqlx.DB, busy time.Duration) error {
	deadline := time.Now().Add(busy)
	for {
		err := db.PingContext(ctx)
		if err == nil || !(dialect{}).IsRetryable(err) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(5+rand.IntN(15)) * time.Millisecond):
		}
	}
}

// dialect is the SQLite sqlstore.Dialect.
type dialect struct{}

var _ sqlstore.Dialect = dialect{}

func (dialect) Name() store.Engine { return store.EngineSQLite }
func (dialect) BindType() int      { return sqlx.QUESTION }

// IsRetryable: SQLITE_BUSY (5) and SQLITE_LOCKED (6) after the busy timeout.
// modernc stores whatever result code the failing call returned, which may be
// extended (SQLITE_BUSY_SNAPSHOT 517, SQLITE_LOCKED_SHAREDCACHE 262), so the
// primary code is masked out.
func (dialect) IsRetryable(err error) bool {
	var e *sqlitedrv.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code() & 0xff {
	case 5, 6:
		return true
	}
	return false
}

// IsReadOnlyViolation is always false: a deferred begin upgrades to a write
// silently, so the Go-side guard is the only one on SQLite.
func (dialect) IsReadOnlyViolation(error) bool { return false }

func (dialect) IsEmpty(ctx context.Context, db *sqlx.DB) (bool, error) {
	var n int
	if err := db.GetContext(ctx, &n, "SELECT count(*) FROM sqlite_master WHERE type = 'table'"); err != nil {
		return false, err
	}
	return n == 0, nil
}

func (dialect) TableExists(ctx context.Context, db *sqlx.DB, name string) (bool, error) {
	var n int
	if err := db.GetContext(ctx, &n, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name); err != nil {
		return false, err
	}
	return n == 1, nil
}

func (dialect) Stamp(ctx context.Context, db *sqlx.DB) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", ApplicationID))
	return err
}

func (dialect) CheckStamp(ctx context.Context, db *sqlx.DB) error {
	var id int32
	if err := db.GetContext(ctx, &id, "PRAGMA application_id"); err != nil {
		return err
	}
	if id != ApplicationID {
		return fmt.Errorf("%w: application_id %#x, want %#x", store.ErrNotAgentbus2, id, ApplicationID)
	}
	return nil
}

// LockMigrations is a no-op: the transaction is already immediate (Decision 68).
func (dialect) LockMigrations(context.Context, *sqlx.Tx) error { return nil }

// LockAppend is a no-op: the immediate transaction already queues appends.
func (dialect) LockAppend(context.Context, *sqlx.Tx) error { return nil }

// ftsPrefix is appended to a quoted prefix term; FTS5 applies it to the
// phrase's last token.
const ftsPrefix = "*"

// TextSearch translates the query to one FTS5 MATCH string: every term quoted
// with `"` doubled, a prefix term followed by ftsPrefix, joined by spaces
// (implicit AND). Ported from v1 internal/bus/search.go ftsQuery. Word and
// phrase terms translate identically.
func (dialect) TextSearch(q search.Query) (string, []any, error) {
	if q.Empty() {
		return "", nil, fmt.Errorf("%w: search query has no searchable terms", store.ErrInvalid)
	}
	parts := make([]string, 0, len(q.Terms))
	for _, t := range q.Terms {
		s := `"` + strings.ReplaceAll(t.Text, `"`, `""`) + `"`
		if t.Prefix {
			s += ftsPrefix
		}
		parts = append(parts, s)
	}
	return `m.seq IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`,
		[]any{strings.Join(parts, " ")}, nil
}

// NotifyAppend is the Milestone 7 hook; SQLite has no notification.
func (dialect) NotifyAppend(context.Context, *sqlx.Tx, int64, int64) error { return nil }
