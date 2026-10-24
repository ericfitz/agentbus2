package sessions

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

type hintClock struct{ t time.Time }

func (c *hintClock) now() time.Time { return c.t }

func newHintedSvc(t *testing.T) (Service, *hintClock) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentbus2.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	clk := &hintClock{t: time.UnixMilli(1_700_000_000_000)}
	c, err := core.New(s, core.Defaults(), core.Options{Now: clk.now})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	err = s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.CreateHuman(ctx, store.HumanBinding{Identity: "alice@corp.example", ClaimType: store.ClaimEmail, Value: "alice@corp.example"}, c.NowMs())
	})
	if err != nil {
		t.Fatalf("CreateHuman: %v", err)
	}
	return Service{c}, clk
}

func session(t *testing.T, s Service, id string) store.Session {
	t.Helper()
	var row store.Session
	err := s.Store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		row, err = tx.Session(ctx, id)
		return err
	})
	if err != nil {
		t.Fatalf("Session(%s): %v", id, err)
	}
	return row
}

func TestEnsureHinted(t *testing.T) {
	ctx := context.Background()
	s, clk := newHintedSvc(t)
	id1, err := s.EnsureHinted(ctx, "alice@corp.example", "slack")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	row := session(t, s, id1)
	if row.DisplayName != "alice@corp.example/slack" || row.Hint != "slack" || row.ClosedAt != 0 {
		t.Fatalf("session = %+v", row)
	}

	clk.t = clk.t.Add(time.Minute)
	id2, err := s.EnsureHinted(ctx, "alice@corp.example", "slack")
	if err != nil || id2 != id1 {
		t.Fatalf("second call = %q, %v; want %q", id2, err, id1)
	}
	if got := session(t, s, id1); got.LeaseExpiresAt <= row.LeaseExpiresAt {
		t.Errorf("lease not renewed: %d then %d", row.LeaseExpiresAt, got.LeaseExpiresAt)
	}

	// Another hint is another session.
	other, err := s.EnsureHinted(ctx, "alice@corp.example", "tui")
	if err != nil || other == id1 {
		t.Fatalf("other hint = %q, %v", other, err)
	}

	// A closed session is replaced.
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.CloseSession(ctx, id1, s.NowMs())
	})
	if err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	id3, err := s.EnsureHinted(ctx, "alice@corp.example", "slack")
	if err != nil || id3 == id1 {
		t.Fatalf("after close = %q, %v", id3, err)
	}
	if got := session(t, s, id3); got.DisplayName != "alice@corp.example/slack" {
		t.Errorf("replacement display = %q", got.DisplayName)
	}

	// Closed enumerations: a bad hint, no hint and an unknown identity.
	for _, hint := range []string{"bad/hint", ""} {
		if _, err := s.EnsureHinted(ctx, "alice@corp.example", hint); bus.CodeOf(err) != bus.CodeValidation {
			t.Errorf("hint %q: %v, want validation", hint, err)
		}
	}
	if _, err := s.EnsureHinted(ctx, "nobody@corp.example", "slack"); bus.CodeOf(err) != bus.CodeNotRegistered {
		t.Errorf("unknown identity: %v, want not_registered", err)
	}
}
