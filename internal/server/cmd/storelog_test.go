package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/server/config"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/storetest"
)

// TestStoreLogsToConfiguredLogger (issue #20): the Store's read-normalization
// error reaches the server's configured logger, both as Run composes it
// (newLogger, then openStore) and through the host commands' openFromConfig.
func TestStoreLogsToConfiguredLogger(t *testing.T) {
	ctx := context.Background()

	t.Run("serve", func(t *testing.T) {
		db := filepath.Join(t.TempDir(), "ab2.db")
		storetest.ScopedAdminRaw(t, db, "rogue", "acme")
		cfg := config.Config{SQLite: db}
		var logs syncBuf
		st, err := openStore(ctx, cfg, newLogger(cfg, &logs))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		var got store.Identity
		if err := st.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			got, err = tx.Identity(ctx, "rogue")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got.Scope != "" || !got.Admin {
			t.Fatalf("read %+v, want an unscoped administrator", got)
		}
		if s := logs.String(); !strings.Contains(s, storetest.NormalizationError) || !strings.Contains(s, "identity=rogue") {
			t.Fatalf("the configured logger has %q, want the normalization error naming rogue", s)
		}
	})

	t.Run("admin", func(t *testing.T) {
		db := filepath.Join(t.TempDir(), "ab2.db")
		storetest.ScopedAdminRaw(t, db, "rogue", "acme")
		var o, e syncBuf
		RunAdmin(ctx, []string{"-sqlite", db, "reset-credential", "rogue"}, nil, &o, &e)
		if s := e.String(); !strings.Contains(s, storetest.NormalizationError) || !strings.Contains(s, "identity=rogue") {
			t.Fatalf("stderr %q (stdout %q), want the normalization error naming rogue", s, o.String())
		}
	})
}
