package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

const appName = "agentbus2"

// validateMigrations requires versions exactly 1..n in order (Decision 53).
func validateMigrations(ms []Migration) error {
	if len(ms) == 0 {
		return fmt.Errorf("%w: no migrations", store.ErrSchemaInvalid)
	}
	for i, m := range ms {
		if m.Version != i+1 {
			return fmt.Errorf("%w: migration %d has version %d, want %d", store.ErrSchemaInvalid, i, m.Version, i+1)
		}
		if m.Up == nil {
			return fmt.Errorf("%w: migration version %d has no Up", store.ErrSchemaInvalid, m.Version)
		}
	}
	return nil
}

// Open applies the Decision 34 rules to db, migrates it under the Decision 68
// lock and returns the Store. It closes db on error.
func Open(ctx context.Context, db *sqlx.DB, d Dialect, opts Options) (*Store, error) {
	s, err := open(ctx, db, d, opts)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func open(ctx context.Context, db *sqlx.DB, d Dialect, opts Options) (*Store, error) {
	if err := validateMigrations(opts.Migrations); err != nil {
		return nil, err
	}
	latest := len(opts.Migrations)
	now := opts.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	attempts := opts.TxAttempts
	if attempts <= 0 {
		attempts = 1
	}

	empty, err := d.IsEmpty(ctx, db)
	if err != nil {
		return nil, err
	}
	if empty {
		if err := d.Stamp(ctx, db); err != nil {
			return nil, err
		}
		if err := bootstrap(ctx, db, d, now()); err != nil {
			return nil, err
		}
	} else {
		if err := d.CheckStamp(ctx, db); err != nil {
			return nil, err
		}
		ok, err := d.TableExists(ctx, db, "schema_meta")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: no schema_meta table", store.ErrNotAgentbus2)
		}
	}

	cur, err := readMeta(ctx, db, d, latest)
	if err != nil {
		return nil, err
	}
	for v := cur; v < latest; v++ {
		if err := runStep(ctx, db, d, v, opts.Migrations[v]); err != nil {
			return nil, fmt.Errorf("migrate schema version %d to %d: %w", v, v+1, err)
		}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, d: d, attempts: attempts, log: logger, held: map[string]string{}, conns: map[string]*sql.Conn{}}, nil
}

// bootstrap creates schema_meta at version 0 under the migration lock. It
// begins with the engine's default isolation, never Store.Tx (Review Focus 4).
func bootstrap(ctx context.Context, db *sqlx.DB, d Dialect, now int64) error {
	t, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = t.Rollback() }()
	if err := d.LockMigrations(ctx, t); err != nil {
		return err
	}
	if _, err := t.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  app TEXT NOT NULL,
  schema_version BIGINT NOT NULL,
  engine TEXT NOT NULL,
  created_at BIGINT NOT NULL)`); err != nil {
		return err
	}
	q := sqlx.Rebind(d.BindType(), "INSERT INTO schema_meta (id, app, schema_version, engine, created_at) VALUES (1, ?, 0, ?, ?) ON CONFLICT (id) DO NOTHING")
	if _, err := t.ExecContext(ctx, q, appName, string(d.Name()), now); err != nil {
		return err
	}
	return t.Commit()
}

// readMeta applies the row checks of Decision 34 and returns schema_version.
func readMeta(ctx context.Context, db *sqlx.DB, d Dialect, latest int) (int, error) {
	var app, engine string
	var version int64
	err := db.QueryRowxContext(ctx, "SELECT app, schema_version, engine FROM schema_meta WHERE id = 1").Scan(&app, &version, &engine)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: schema_meta has no row", store.ErrNotAgentbus2)
	}
	if err != nil {
		return 0, err
	}
	switch {
	case app != appName:
		return 0, fmt.Errorf("%w: app %q", store.ErrNotAgentbus2, app)
	case engine != string(d.Name()):
		return 0, fmt.Errorf("%w: engine %q, want %q", store.ErrSchemaInvalid, engine, d.Name())
	case version < 0:
		return 0, fmt.Errorf("%w: schema_version %d", store.ErrSchemaInvalid, version)
	case version > int64(latest):
		return 0, fmt.Errorf("%w (database has version %d, this binary %d)", store.ErrUpgradeNeeded, version, latest)
	}
	return int(version), nil
}

// runStep moves schema_version from v to v+1 in one transaction: lock,
// re-read the version, skip if another process got there first, run, stamp,
// commit (v1 internal/bus/migrate.go runMigration, re-keyed to schema_meta).
func runStep(ctx context.Context, db *sqlx.DB, d Dialect, v int, m Migration) error {
	t, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = t.Rollback() }()
	if err := d.LockMigrations(ctx, t); err != nil {
		return err
	}
	var cur int
	if err := t.GetContext(ctx, &cur, "SELECT schema_version FROM schema_meta WHERE id = 1"); err != nil {
		return err
	}
	if cur != v {
		return nil // another process got here first
	}
	if m.Version != v+1 {
		return fmt.Errorf("%w: no migration to version %d", store.ErrSchemaInvalid, v+1)
	}
	if err := m.Up(ctx, t); err != nil {
		return err
	}
	if _, err := t.ExecContext(ctx, sqlx.Rebind(d.BindType(), "UPDATE schema_meta SET schema_version = ? WHERE id = 1"), v+1); err != nil {
		return err
	}
	return t.Commit()
}

// SchemaVersion reads schema_meta.schema_version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.GetContext(ctx, &v, "SELECT schema_version FROM schema_meta WHERE id = 1"); err != nil {
		return 0, err
	}
	return v, nil
}
