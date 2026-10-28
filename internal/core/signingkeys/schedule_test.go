package signingkeys

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/store"
)

// keyOf is the key a provider returns for an existing row.
func keyOf(t *testing.T, row store.SigningKey) keyprovider.Key {
	t.Helper()
	pub, alg, err := keys.ParsePublic(row.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return keyprovider.Key{KID: row.KID, Algorithm: alg, Public: pub, Ref: row.ProviderRef}
}

// open note: signing-keys 4
func TestRotateDueStore(t *testing.T) {
	ctx := context.Background()
	s, clk, fp := newSvc(t)
	first, _, err := s.EnsureActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := fp.nextCalls()

	// Interval 0: never, no Next call.
	s.Settings.Interval = 0
	clk.advance(1000 * time.Hour)
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 0 || err != nil {
		t.Fatalf("interval 0: %d, %v", n, err)
	}
	if fp.nextCalls() != base {
		t.Fatal("Next called with interval 0")
	}

	// Younger than the interval: 0, the cheap read first (no Next call).
	s.Settings.Interval = 2000 * time.Hour
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 0 || err != nil {
		t.Fatalf("young: %d, %v", n, err)
	}
	if fp.nextCalls() != base {
		t.Fatal("Next called for a young key")
	}

	// At the interval: rotates.
	clk.advance(1000 * time.Hour)
	changed := 0
	s.OnChange = func() { changed++ }
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 1 || err != nil {
		t.Fatalf("due: %d, %v", n, err)
	}
	rows := rowsOf(t, s)
	if len(rows) != 2 || rows[0].KID == first.KID || rows[1].KID != first.KID || rows[1].State != store.SigningKeyRetiring {
		t.Fatalf("rows %+v", rows)
	}
	if changed != 1 {
		t.Fatalf("OnChange %d times", changed)
	}

	// A second run: the new key is young.
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 0 || err != nil {
		t.Fatalf("second: %d, %v", n, err)
	}
	if len(rowsOf(t, s)) != 2 || changed != 1 {
		t.Fatal("second run changed the set")
	}
}

func TestRotateDueStoreInsideCooldown(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	if _, _, err := s.EnsureActive(ctx); err != nil {
		t.Fatal(err)
	}
	// Interval shorter than the cooldown: the cooldown conflict is 0, not an error.
	s.Settings = Settings{Interval: time.Minute, Cooldown: time.Hour}
	clk.advance(10 * time.Minute)
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 0 || err != nil {
		t.Fatalf("%d, %v", n, err)
	}
	if len(rowsOf(t, s)) != 1 {
		t.Fatal("rotated inside the cooldown")
	}
	clk.advance(time.Hour)
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 1 || err != nil {
		t.Fatalf("after cooldown: %d, %v", n, err)
	}
}

func TestRotateDueExternal(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.UnixMilli(startMs)}
	fp := &fakeProvider{name: "awskms"}
	s := svcOver(t, openSQLite(t), clk, fp)
	s.Settings = Settings{Interval: 100 * time.Hour, Cooldown: time.Hour}
	active, _, err := s.EnsureActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	same := keyOf(t, active)
	fp.forced = &same

	// Next returns the active kid: 0, no error while the key is young.
	clk.advance(2 * time.Hour)
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 0 || err != nil {
		t.Fatalf("unchanged: %d, %v", n, err)
	}

	// Repointed: 1.
	fp.mu.Lock()
	fp.forced = nil
	fp.mu.Unlock()
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 1 || err != nil {
		t.Fatalf("repointed: %d, %v", n, err)
	}
	rows := rowsOf(t, s)
	if len(rows) != 2 || rows[0].KID == active.KID || rows[1].State != store.SigningKeyRetiring {
		t.Fatalf("rows %+v", rows)
	}

	// Repointed while the active key is younger than the cooldown: 0, adopted
	// on the first tick after it.
	clk.advance(10 * time.Minute)
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 0 || err != nil {
		t.Fatalf("inside cooldown: %d, %v", n, err)
	}
	if len(rowsOf(t, s)) != 2 {
		t.Fatal("rotated inside the cooldown")
	}
	clk.advance(time.Hour)
	if n, err := s.RotateDue(ctx, s.NowMs()); n != 1 || err != nil {
		t.Fatalf("after cooldown: %d, %v", n, err)
	}
}

func TestRotateDueExternalStale(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.UnixMilli(startMs)}
	fp := &fakeProvider{name: "awskms"}
	s := svcOver(t, openSQLite(t), clk, fp)
	s.Settings = Settings{Interval: 100 * time.Hour, Cooldown: time.Hour}
	active, _, err := s.EnsureActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	same := keyOf(t, active)
	fp.forced = &same
	clk.advance(101 * time.Hour)
	n, err := s.RotateDue(ctx, s.NowMs())
	if n != 0 || err == nil {
		t.Fatalf("%d, %v", n, err)
	}
	want := fmt.Sprintf("signing key %s is older than signing_key_rotation_hours (100): point the key URI at a new key, then run agentbus2 server admin signing-key rotate", active.KID)
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
	if len(rowsOf(t, s)) != 1 {
		t.Fatal("rotated")
	}
}

func TestRotateDueProviderMismatch(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	row, _, err := s.EnsureActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(1000 * time.Hour)
	ext := svcOver(t, s.Store, clk, &fakeProvider{name: "awskms"})
	n, err := ext.RotateDue(ctx, ext.NowMs())
	if n != 0 {
		t.Fatalf("n = %d", n)
	}
	wantCode(t, err, bus.CodeConflict)
	wantText(t, err, "active signing key "+row.KID+" is provider store, configured provider is awskms: run agentbus2 server admin signing-key rotate with the new configuration")
	if ext.Provider.(*fakeProvider).nextCalls() != 0 {
		t.Fatal("Next called")
	}
	if rows := rowsOf(t, s); len(rows) != 1 || rows[0].KID != row.KID {
		t.Fatalf("rows %+v", rows)
	}
}

func TestRotateDueNoActiveRow(t *testing.T) {
	s, _, fp := newSvc(t)
	if n, err := s.RotateDue(context.Background(), s.NowMs()); n != 0 || err != nil {
		t.Fatalf("%d, %v", n, err)
	}
	if fp.nextCalls() != 0 || len(rowsOf(t, s)) != 0 {
		t.Fatal("RotateDue created a key")
	}
}

func TestRemoveExpired(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	if _, _, err := s.EnsureActive(ctx); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Hour)
	if _, err := s.Rotate(ctx, RotateInput{}); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Hour)
	if _, err := s.Rotate(ctx, RotateInput{}); err != nil {
		t.Fatal(err)
	}
	if len(rowsOf(t, s)) != 3 {
		t.Fatal("want 3 rows")
	}
	changed := 0
	s.OnChange = func() { changed++ }

	// Not yet due.
	if n, err := s.RemoveExpired(ctx, s.NowMs()); n != 0 || err != nil || changed != 0 {
		t.Fatalf("early: %d, %v, %d", n, err, changed)
	}
	// The older retiring key is past remove_after, the newer is not.
	rows := rowsOf(t, s)
	older, newer := rows[2], rows[1]
	if older.RemoveAfter >= newer.RemoveAfter {
		t.Fatalf("order: %+v", rows)
	}
	if n, err := s.RemoveExpired(ctx, older.RemoveAfter+1); n != 1 || err != nil || changed != 1 {
		t.Fatalf("older: %d, %v, %d", n, err, changed)
	}
	// A second run deletes nothing.
	if n, err := s.RemoveExpired(ctx, older.RemoveAfter+1); n != 0 || err != nil || changed != 1 {
		t.Fatalf("second: %d, %v, %d", n, err, changed)
	}
	// Far in the future: the newer retiring key goes, the active key never.
	if n, err := s.RemoveExpired(ctx, newer.RemoveAfter+10*365*24*3600*1000); n != 1 || err != nil || changed != 2 {
		t.Fatalf("all: %d, %v, %d", n, err, changed)
	}
	left := rowsOf(t, s)
	if len(left) != 1 || left[0].State != store.SigningKeyActive {
		t.Fatalf("left %+v", left)
	}
	if n, err := s.RemoveExpired(ctx, newer.RemoveAfter+100*365*24*3600*1000); n != 0 || err != nil {
		t.Fatalf("active key: %d, %v", n, err)
	}
}
