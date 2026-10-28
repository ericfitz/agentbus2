package signingkeys

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

const startMs = 1_700_000_000_000

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeProvider counts Next calls and can name its next key. With a name other
// than "store" it behaves like an external provider over P-256 keys it holds.
type fakeProvider struct {
	name string

	mu      sync.Mutex
	nexts   int
	signers int
	forced  *keyprovider.Key
	hook    func() // runs inside Next, before it returns
	priv    map[string]crypto.Signer
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Next(ctx context.Context) (keyprovider.Key, error) {
	f.mu.Lock()
	f.nexts++
	forced, hook := f.forced, f.hook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if forced != nil {
		return *forced, nil
	}
	return f.fresh(ctx)
}

func (f *fakeProvider) fresh(ctx context.Context) (keyprovider.Key, error) {
	if f.name == keyprovider.NameStore {
		return keyprovider.StoreProvider{}.Next(ctx)
	}
	return f.external()
}

func (f *fakeProvider) external() (keyprovider.Key, error) {
	pk, err := keys.Generate(keys.ES256)
	if err != nil {
		return keyprovider.Key{}, err
	}
	k, err := keyprovider.ExternalKey(pk.Public(), "awskms://key/"+fmt.Sprint(time.Now().UnixNano()))
	if err != nil {
		return keyprovider.Key{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.priv == nil {
		f.priv = map[string]crypto.Signer{}
	}
	f.priv[k.KID] = pk
	return k, nil
}

func (f *fakeProvider) Signer(ctx context.Context, k keyprovider.Key) (keyprovider.Signer, error) {
	f.mu.Lock()
	f.signers++
	pk := f.priv[k.KID]
	f.mu.Unlock()
	if f.name == keyprovider.NameStore {
		return keyprovider.StoreProvider{}.Signer(ctx, k)
	}
	if pk == nil {
		return nil, errors.New("fake: no such key")
	}
	return extSigner{pk}, nil
}

func (f *fakeProvider) nextCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nexts
}

type extSigner struct{ crypto.Signer }

func (e extSigner) SignContext(_ context.Context, d []byte, o crypto.SignerOpts) ([]byte, error) {
	return e.Sign(nil, d, o)
}

func openSQLite(t *testing.T) store.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentbus2.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func svcOver(t *testing.T, st store.Store, clk *clock, p keyprovider.Provider) Service {
	t.Helper()
	c, err := core.New(st, core.Defaults(), core.Options{Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	return Service{Core: c, Provider: p, Settings: DefaultSettings()}
}

func newSvc(t *testing.T) (Service, *clock, *fakeProvider) {
	t.Helper()
	clk := &clock{t: time.UnixMilli(startMs)}
	fp := &fakeProvider{name: keyprovider.NameStore}
	return svcOver(t, openSQLite(t), clk, fp), clk, fp
}

func wantCode(t *testing.T, err error, want bus.Code) {
	t.Helper()
	if got := bus.CodeOf(err); got != want {
		t.Fatalf("code = %q (err %v), want %q", got, err, want)
	}
}

func wantText(t *testing.T, err error, want string) {
	t.Helper()
	var be *bus.Error
	if !errors.As(err, &be) || be.Message != want {
		t.Fatalf("err = %v, want message %q", err, want)
	}
}

func rowsOf(t *testing.T, s Service) []store.SigningKey {
	t.Helper()
	var rows []store.SigningKey
	if err := s.Store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var e error
		rows, e = tx.ListSigningKeys(ctx)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func put(t *testing.T, s Service, row store.SigningKey) {
	t.Helper()
	if err := s.Store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error { return tx.PutSigningKey(ctx, row) }); err != nil {
		t.Fatal(err)
	}
}

// storeRow is an active store row for k with the given PEM.
func storeRow(t *testing.T, k keyprovider.Key, pem string) store.SigningKey {
	t.Helper()
	pub, err := keys.MarshalPublic(k.Public)
	if err != nil {
		t.Fatal(err)
	}
	return store.SigningKey{
		KID: k.KID, State: store.SigningKeyActive, Provider: store.SigningProviderStore,
		Algorithm: string(k.Algorithm), PublicKey: pub, PrivateKeyPEM: pem, CreatedAt: startMs,
	}
}

func TestRemoveAfter(t *testing.T) {
	want := int64(1000) + (30*time.Second + 86400*time.Second + 60*time.Second).Milliseconds()
	if got := RemoveAfter(1000); got != want {
		t.Fatalf("RemoveAfter = %d, want %d", got, want)
	}
	if d := DefaultSettings(); d.Interval != 720*time.Hour || d.Cooldown != 3600*time.Second {
		t.Fatalf("defaults %+v", d)
	}
}

func TestSigningKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _, fp := newSvc(t)
	row, created, err := s.EnsureActive(ctx)
	if err != nil || !created {
		t.Fatalf("first = %v, %v", created, err)
	}
	if row.State != store.SigningKeyActive || row.Provider != store.SigningProviderStore || row.Algorithm != "EdDSA" || row.CreatedAt != startMs {
		t.Fatalf("row %+v", row)
	}
	if rows := rowsOf(t, s); len(rows) != 1 || rows[0].KID != row.KID {
		t.Fatalf("rows = %+v, want one active store row", rows)
	}
	if fp.nextCalls() != 1 {
		t.Fatalf("Next called %d times", fp.nextCalls())
	}
	row2, created, err := s.EnsureActive(ctx)
	if err != nil || created || row2.KID != row.KID {
		t.Fatalf("second = %v, %v, %v", row2.KID, created, err)
	}
	if fp.nextCalls() != 1 {
		t.Fatalf("a second EnsureActive called Next (%d)", fp.nextCalls())
	}

	// A corrupt store row is refused internal and never overwritten.
	good, _ := keyprovider.StoreProvider{}.Next(ctx)
	other, _ := keyprovider.StoreProvider{}.Next(ctx)
	es, _ := keys.Generate(keys.ES256)
	esPEM, _ := keys.EncodePrivatePEM(es)
	esKID, _ := keys.KID(es.Public())
	esPub, _ := keys.MarshalPublic(es.Public())
	cases := map[string]store.SigningKey{
		"bad PEM": storeRow(t, good, "garbage"),
		"PEM holds another key under the row's kid and public key": storeRow(t, good, string(other.PrivatePEM)),
		"kid differs": storeRow(t, other, string(good.PrivatePEM)),
		"public differs": func() store.SigningKey {
			r := storeRow(t, good, string(good.PrivatePEM))
			r.PublicKey = storeRow(t, other, "x").PublicKey
			return r
		}(),
		"public unparsable": func() store.SigningKey {
			r := storeRow(t, good, string(good.PrivatePEM))
			r.PublicKey = []byte{1}
			return r
		}(),
		"algorithm differs": {KID: esKID, State: store.SigningKeyActive, Provider: store.SigningProviderStore, Algorithm: "EdDSA", PublicKey: esPub, PrivateKeyPEM: string(esPEM), CreatedAt: startMs},
	}
	for name, bad := range cases {
		cs, _, _ := newSvc(t)
		put(t, cs, bad)
		_, created, err := cs.EnsureActive(ctx)
		if err == nil || created {
			t.Errorf("%s: EnsureActive = %v, %v, want an error", name, created, err)
			continue
		}
		wantCode(t, err, bus.CodeInternal)
		wantText(t, err, "signing key row "+bad.KID+" is corrupt")
		if rows := rowsOf(t, cs); len(rows) != 1 || rows[0].PrivateKeyPEM != bad.PrivateKeyPEM {
			t.Errorf("%s: row changed: %+v", name, rows)
		}
	}
}

// A set with only retiring rows gets a new active key; the retiring rows are
// left alone.
func TestEnsureActiveRetiringOnlyCreatesActive(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSvc(t)
	old, _ := keyprovider.StoreProvider{}.Next(ctx)
	r := storeRow(t, old, string(old.PrivatePEM))
	r.State, r.RetiredAt, r.RemoveAfter = store.SigningKeyRetiring, startMs, startMs+10
	put(t, s, r)
	if _, created, err := s.EnsureActive(ctx); err != nil || !created {
		t.Fatalf("created = %v, %v", created, err)
	}
	rows := rowsOf(t, s)
	if len(rows) != 2 || rows[0].State != store.SigningKeyActive || rows[1].KID != old.KID {
		t.Fatalf("rows = %+v, want the new active row then the old", rows)
	}
}

type conflictStore struct {
	store.Store

	mu    sync.Mutex
	armed bool
	limit int
	calls int
	after func()
}

func (c *conflictStore) arm(limit int, after func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed, c.limit, c.calls, c.after = true, limit, 0, after
}

func (c *conflictStore) txCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *conflictStore) Tx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	c.mu.Lock()
	fault, first := false, false
	var after func()
	if c.armed {
		c.calls++
		fault = c.limit < 0 || c.calls <= c.limit
		first = c.calls == 1
		after = c.after
	}
	c.mu.Unlock()
	if !fault {
		return c.Store.Tx(ctx, fn)
	}
	err := c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return fmt.Errorf("%w: injected by the test", store.ErrConflict)
	})
	if first && after != nil {
		after()
	}
	return err
}

func newConflictSvc(t *testing.T) (loser, winner Service, cs *conflictStore, clk *clock) {
	t.Helper()
	raw := openSQLite(t)
	clk = &clock{t: time.UnixMilli(startMs)}
	cs = &conflictStore{Store: raw}
	return svcOver(t, cs, clk, &fakeProvider{name: keyprovider.NameStore}), svcOver(t, raw, clk, &fakeProvider{name: keyprovider.NameStore}), cs, clk
}

func TestEnsureActiveRerunsAfterConflict(t *testing.T) {
	ctx := context.Background()

	t.Run("a second replica loads the winner's key", func(t *testing.T) {
		loser, winner, cs, _ := newConflictSvc(t)
		var winnerKID string
		cs.arm(1, func() {
			row, created, err := winner.EnsureActive(ctx)
			if err != nil || !created {
				t.Errorf("the winner's EnsureActive = %v, %v", created, err)
				return
			}
			winnerKID = row.KID
		})
		row, created, err := loser.EnsureActive(ctx)
		if err != nil || created {
			t.Fatalf("EnsureActive = %v, %v, want a load of the winner's key", created, err)
		}
		if row.KID != winnerKID || winnerKID == "" {
			t.Fatalf("kid = %q, want the winner's %q", row.KID, winnerKID)
		}
		if got := cs.txCalls(); got != 2 {
			t.Fatalf("%d transactions, want 2", got)
		}
	})

	t.Run("the rerun is bounded at one", func(t *testing.T) {
		loser, winner, cs, _ := newConflictSvc(t)
		cs.arm(-1, nil)
		_, created, err := loser.EnsureActive(ctx)
		wantCode(t, err, bus.CodeConflict)
		if created {
			t.Fatal("created with an error")
		}
		if got := cs.txCalls(); got != 2 {
			t.Fatalf("%d transactions, want 2", got)
		}
		if rows := rowsOf(t, winner); len(rows) != 0 {
			t.Fatalf("a faulted attempt left rows: %+v", rows)
		}
	})
}

func TestEnsureActiveProviderMismatch(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	row, _, err := s.EnsureActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ext := svcOver(t, s.Store, clk, &fakeProvider{name: "awskms"})
	_, created, err := ext.EnsureActive(ctx)
	wantCode(t, err, bus.CodeConflict)
	wantText(t, err, "active signing key "+row.KID+" is provider store, configured provider is awskms: run agentbus2 server admin signing-key rotate with the new configuration")
	if created {
		t.Fatal("created")
	}
	if ext.Provider.(*fakeProvider).nextCalls() != 0 {
		t.Fatal("Next was called")
	}
	if rows := rowsOf(t, s); len(rows) != 1 || rows[0].KID != row.KID {
		t.Fatalf("rows %+v", rows)
	}
}

func TestEnsureActiveExternalProvider(t *testing.T) {
	ctx := context.Background()
	clk := &clock{t: time.UnixMilli(startMs)}
	fp := &fakeProvider{name: "awskms"}
	s := svcOver(t, openSQLite(t), clk, fp)
	row, created, err := s.EnsureActive(ctx)
	if err != nil || !created {
		t.Fatalf("%v, %v", created, err)
	}
	if row.Provider != "awskms" || row.ProviderRef == "" || row.PrivateKeyPEM != "" || row.Algorithm != "ES256" {
		t.Fatalf("row %+v", row)
	}
	if again, created, err := s.EnsureActive(ctx); err != nil || created || again.KID != row.KID {
		t.Fatalf("second %v, %v", created, err)
	}
	// An external row whose public key is EdDSA is corrupt.
	bad := svcOver(t, openSQLite(t), clk, fp)
	ed, _ := keyprovider.StoreProvider{}.Next(ctx)
	edPub, _ := keys.MarshalPublic(ed.Public)
	put(t, bad, store.SigningKey{KID: ed.KID, State: store.SigningKeyActive, Provider: "awskms", ProviderRef: "awskms://x", Algorithm: "EdDSA", PublicKey: edPub, CreatedAt: startMs})
	_, _, err = bad.EnsureActive(ctx)
	wantCode(t, err, bus.CodeInternal)
}

func TestPrepareRotationRefusesBadProviderKey(t *testing.T) {
	ctx := context.Background()
	s, _, fp := newSvc(t)
	good, _ := keyprovider.StoreProvider{}.Next(ctx)
	wrong := good
	wrong.KID = "not-the-thumbprint"
	fp.forced = &wrong
	if _, err := s.PrepareRotation(ctx); bus.CodeOf(err) != bus.CodeInternal {
		t.Fatalf("kid mismatch: %v", err)
	}
	wrong = good
	wrong.Algorithm = keys.ES256
	if _, err := s.PrepareRotation(ctx); bus.CodeOf(err) != bus.CodeInternal {
		t.Fatalf("algorithm mismatch: %v", err)
	}
	if _, _, err := s.EnsureActive(ctx); bus.CodeOf(err) != bus.CodeInternal {
		t.Fatalf("EnsureActive: %v", err)
	}
	if rows := rowsOf(t, s); len(rows) != 0 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestRotateRetiresAndInserts(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	first, _, err := s.EnsureActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changes := 0
	s.OnChange = func() { changes++ }
	clk.advance(2 * time.Hour)
	res, err := s.Rotate(ctx, RotateInput{})
	if err != nil {
		t.Fatal(err)
	}
	now := clk.now().UnixMilli()
	rows := rowsOf(t, s)
	if len(rows) != 2 || rows[0].KID != res.Active.KID || rows[0].State != store.SigningKeyActive || rows[0].CreatedAt != now {
		t.Fatalf("rows %+v", rows)
	}
	if rows[1].KID != first.KID || rows[1].State != store.SigningKeyRetiring || rows[1].RetiredAt != now || rows[1].RemoveAfter != RemoveAfter(now) {
		t.Fatalf("retiring row %+v", rows[1])
	}
	if res.Previous.KID != first.KID || res.Revoked || res.Active.KID == first.KID {
		t.Fatalf("result %+v", res)
	}
	if changes != 1 {
		t.Fatalf("OnChange ran %d times, want 1", changes)
	}
}

func TestRotateWithNoActiveRowInserts(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSvc(t)
	res, err := s.Rotate(ctx, RotateInput{RevokePrevious: true})
	if err != nil || res.Revoked || res.Previous.KID != "" {
		t.Fatalf("%+v, %v", res, err)
	}
	if rows := rowsOf(t, s); len(rows) != 1 || rows[0].KID != res.Active.KID {
		t.Fatalf("rows %+v", rows)
	}
}

func TestRotateCooldown(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	first, _, _ := s.EnsureActive(ctx)
	changes := 0
	s.OnChange = func() { changes++ }
	clk.advance(1800 * time.Second)
	before := rowsOf(t, s)
	_, err := s.Rotate(ctx, RotateInput{})
	wantCode(t, err, bus.CodeConflict)
	if !errors.Is(err, ErrCooldown) || errors.Is(err, ErrRotateNoChange) {
		t.Fatalf("sentinels: %v", err)
	}
	wantText(t, err, fmt.Sprintf("the active signing key %s was created at %s; the next rotation is allowed at %s (signing_key_rotation_cooldown_seconds 3600; -revoke-previous is exempt)",
		first.KID, time.UnixMilli(startMs).UTC().Format(time.RFC3339), time.UnixMilli(startMs+3_600_000).UTC().Format(time.RFC3339)))
	after := rowsOf(t, s)
	if fmt.Sprint(before) != fmt.Sprint(after) || changes != 0 {
		t.Fatalf("rows changed or OnChange ran: %v -> %v (%d)", before, after, changes)
	}
	clk.advance(1800*time.Second - time.Millisecond)
	if _, err := s.Rotate(ctx, RotateInput{}); !errors.Is(err, ErrCooldown) {
		t.Fatalf("1 ms before the cooldown: %v", err)
	}
	clk.advance(time.Millisecond)
	if _, err := s.Rotate(ctx, RotateInput{}); err != nil {
		t.Fatalf("at exactly the cooldown: %v", err)
	}
	if changes != 1 {
		t.Fatalf("OnChange ran %d times", changes)
	}
}

func TestRotateRevokePrevious(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	oldest, _, _ := s.EnsureActive(ctx)
	clk.advance(2 * time.Hour)
	mid, err := s.Rotate(ctx, RotateInput{})
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Minute) // inside the cooldown
	res, err := s.Rotate(ctx, RotateInput{RevokePrevious: true})
	if err != nil {
		t.Fatalf("revoke inside the cooldown: %v", err)
	}
	if !res.Revoked || res.Previous.KID != mid.Active.KID {
		t.Fatalf("result %+v", res)
	}
	rows := rowsOf(t, s)
	if len(rows) != 2 || rows[0].KID != res.Active.KID || rows[1].KID != oldest.KID || rows[1].State != store.SigningKeyRetiring {
		t.Fatalf("rows %+v: want the new active and the older retiring key only", rows)
	}
}

func TestRotateNextIsActiveKey(t *testing.T) {
	ctx := context.Background()
	for _, revoke := range []bool{false, true} {
		s, clk, fp := newSvc(t)
		active, _, _ := s.EnsureActive(ctx)
		clk.advance(2 * time.Hour)
		same := keyprovider.Key{KID: active.KID, Algorithm: keys.EdDSA}
		pub, _, _ := keys.ParsePublic(active.PublicKey)
		same.Public = pub
		same.PrivatePEM = []byte(active.PrivateKeyPEM)
		fp.forced = &same
		before := fmt.Sprint(rowsOf(t, s))
		_, err := s.Rotate(ctx, RotateInput{RevokePrevious: revoke})
		wantCode(t, err, bus.CodeConflict)
		if !errors.Is(err, ErrRotateNoChange) || errors.Is(err, ErrCooldown) {
			t.Fatalf("sentinels: %v", err)
		}
		wantText(t, err, "the key URI resolves to the active signing key "+active.KID+": point it at a new key, then rotate")
		if fmt.Sprint(rowsOf(t, s)) != before {
			t.Fatalf("revoke=%v: rows changed", revoke)
		}
	}
}

func TestRotateRefusesRetiringKID(t *testing.T) {
	ctx := context.Background()
	s, clk, fp := newSvc(t)
	first, _, _ := s.EnsureActive(ctx)
	clk.advance(2 * time.Hour)
	if _, err := s.Rotate(ctx, RotateInput{}); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Hour)
	pub, _, _ := keys.ParsePublic(first.PublicKey)
	fp.forced = &keyprovider.Key{KID: first.KID, Algorithm: keys.EdDSA, Public: pub, PrivatePEM: []byte(first.PrivateKeyPEM)}
	before := fmt.Sprint(rowsOf(t, s))
	_, err := s.Rotate(ctx, RotateInput{})
	wantCode(t, err, bus.CodeConflict)
	if errors.Is(err, ErrRotateNoChange) || errors.Is(err, ErrCooldown) || fmt.Sprint(rowsOf(t, s)) != before {
		t.Fatalf("err %v or rows changed", err)
	}
}

func TestRotateSwitchesProvider(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	old, _, _ := s.EnsureActive(ctx)
	clk.advance(2 * time.Hour)
	ext := svcOver(t, s.Store, clk, &fakeProvider{name: "awskms"})
	res, err := ext.Rotate(ctx, RotateInput{})
	if err != nil {
		t.Fatal(err)
	}
	rows := rowsOf(t, s)
	if len(rows) != 2 || rows[0].Provider != "awskms" || rows[0].ProviderRef == "" || rows[0].PrivateKeyPEM != "" || rows[0].Algorithm != "ES256" {
		t.Fatalf("rows %+v", rows)
	}
	if rows[1].KID != old.KID || rows[1].State != store.SigningKeyRetiring || res.Previous.Provider != "store" {
		t.Fatalf("rows %+v result %+v", rows, res)
	}
}

func TestRotateFailureAfterNextLeavesRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, clk, fp := newSvc(t)
	if _, _, err := s.EnsureActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Hour)
	before := fmt.Sprint(rowsOf(t, s))
	changes := 0
	s.OnChange = func() { changes++ }
	fp.hook = cancel
	if _, err := s.Rotate(ctx, RotateInput{}); err == nil {
		t.Fatal("Rotate succeeded with a canceled context")
	}
	fp.hook = nil
	if fmt.Sprint(rowsOf(t, s)) != before || changes != 0 {
		t.Fatalf("rows changed or OnChange ran")
	}
}

func TestRotateProviderFailure(t *testing.T) {
	s, _, fp := newSvc(t)
	fp.forced = nil
	bad := &failingProvider{fakeProvider: fp}
	s.Provider = bad
	_, err := s.Rotate(context.Background(), RotateInput{})
	wantCode(t, err, bus.CodeInternal)
	if _, _, err := s.EnsureActive(context.Background()); bus.CodeOf(err) != bus.CodeInternal {
		t.Fatalf("EnsureActive: %v", err)
	}
	if rows := rowsOf(t, s); len(rows) != 0 {
		t.Fatalf("rows %+v", rows)
	}
}

type failingProvider struct{ *fakeProvider }

func (f *failingProvider) Next(context.Context) (keyprovider.Key, error) {
	return keyprovider.Key{}, errors.New("kms is down")
}

func TestRotateRerunsAfterConflictThenCooldown(t *testing.T) {
	ctx := context.Background()
	loser, winner, cs, clk := newConflictSvc(t)
	if _, _, err := loser.EnsureActive(ctx); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Hour)
	cs.arm(1, func() {
		if _, err := winner.Rotate(ctx, RotateInput{}); err != nil {
			t.Errorf("winner: %v", err)
		}
	})
	_, err := loser.Rotate(ctx, RotateInput{})
	if !errors.Is(err, ErrCooldown) {
		t.Fatalf("loser = %v, want the cooldown conflict after the rerun", err)
	}
	if cs.txCalls() != 2 {
		t.Fatalf("%d transactions, want 2", cs.txCalls())
	}
	if rows := rowsOf(t, winner); len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestListOmitsPrivateKey(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	if _, _, err := s.EnsureActive(ctx); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Hour)
	if _, err := s.Rotate(ctx, RotateInput{}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx)
	if err != nil || len(rows) != 2 || rows[0].State != store.SigningKeyActive {
		t.Fatalf("%+v, %v", rows, err)
	}
	for _, r := range rows {
		if r.PrivateKeyPEM != "" || strings.Contains(fmt.Sprint(r), "PRIVATE") {
			t.Fatalf("List leaked a private key: %+v", r)
		}
	}
	if stored := rowsOf(t, s); stored[0].PrivateKeyPEM == "" {
		t.Fatal("List cleared the stored row")
	}
}
