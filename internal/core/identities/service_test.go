package identities

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
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

func newSvc(t *testing.T, mod func(*core.Config)) (Service, *clock) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentbus2.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg := core.Defaults()
	if mod != nil {
		mod(&cfg)
	}
	clk := &clock{t: time.UnixMilli(startMs)}
	c, err := core.New(s, cfg, core.Options{Now: clk.now})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	return Service{c}, clk
}

func genKey(t *testing.T) crypto.PublicKey {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	return k.Public()
}

func wantCode(t *testing.T, err error, want bus.Code) {
	t.Helper()
	if got := bus.CodeOf(err); got != want {
		t.Fatalf("code = %q (err %v), want %q", got, err, want)
	}
}

func wantDetail(t *testing.T, err error, sub string) {
	t.Helper()
	var be *bus.Error
	if !errors.As(err, &be) || !strings.Contains(be.Message, sub) {
		t.Fatalf("err = %v, want a message containing %q", err, sub)
	}
}

func tx(t *testing.T, s Service, fn func(ctx context.Context, tx store.Tx) error) {
	t.Helper()
	if err := s.Store.Tx(context.Background(), fn); err != nil {
		t.Fatalf("tx: %v", err)
	}
}

func identityRow(t *testing.T, s Service, name string) (store.Identity, bool) {
	t.Helper()
	var id store.Identity
	found := false
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		var err error
		id, err = tx.Identity(ctx, name)
		found = err == nil
		return nil
	})
	return id, found
}

func keysOf(t *testing.T, s Service, name string) []store.PublicKey {
	t.Helper()
	var ks []store.PublicKey
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		var err error
		ks, err = tx.PublicKeys(ctx, name)
		return err
	})
	return ks
}

func generalSub(t *testing.T, s Service, name string) (store.Subscription, bool) {
	t.Helper()
	var sub store.Subscription
	ok := false
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		gen, err := tx.ChannelByName(ctx, "general")
		if err != nil {
			return nil
		}
		subs, err := tx.Subscriptions(ctx, name)
		if err != nil {
			return err
		}
		for _, x := range subs {
			if x.ChannelID == gen.ID {
				sub, ok = x, true
			}
		}
		return nil
	})
	return sub, ok
}

func newToken(t *testing.T, s Service, in TokenInput) (string, store.EnrollmentToken) {
	t.Helper()
	tok, row, err := s.CreateEnrollmentToken(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	return tok, row
}

func tokenRow(t *testing.T, s Service, id string) store.EnrollmentToken {
	t.Helper()
	rows, err := s.ListEnrollmentTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("token %s not found", id)
	return store.EnrollmentToken{}
}

func TestRegisterOpenTOFU(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	k1, k2 := genKey(t), genKey(t)
	kid1, _ := keys.KID(k1)

	res, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: k1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.KID != kid1 || res.Algorithm != keys.EdDSA || res.Identity != "pat" {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := identityRow(t, s, "pat"); !ok {
		t.Fatal("no identity row")
	}
	ks := keysOf(t, s, "pat")
	if len(ks) != 1 || ks[0].KID != kid1 || ks[0].Algorithm != "EdDSA" {
		t.Fatalf("keys = %+v", ks)
	}
	var max int64
	tx(t, s, func(ctx context.Context, tx store.Tx) error { var e error; max, e = tx.MaxSeq(ctx); return e })
	if sub, ok := generalSub(t, s, "pat"); !ok || sub.AckedSeq != max {
		t.Fatalf("general subscription = %+v, %v (MaxSeq %d)", sub, ok, max)
	}

	res, err = s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: k1})
	if err != nil || res.Created {
		t.Fatalf("same key again = %+v, %v", res, err)
	}
	if len(keysOf(t, s, "pat")) != 1 {
		t.Fatal("second register stored another key")
	}
	_, err = s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: k2})
	wantCode(t, err, bus.CodeConflict)
	wantDetail(t, err, `identity "pat" already has a credential`)

	t.Run("keyless admin row", func(t *testing.T) {
		s, _ := newSvc(t, nil)
		if err := s.RegisterLocal(ctx, "root"); err != nil {
			t.Fatal(err)
		}
		if err := s.MakeAdmin(ctx, "root", true); err != nil {
			t.Fatal(err)
		}
		_, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: "root", Key: genKey(t)})
		wantCode(t, err, bus.CodeForbidden)
		wantDetail(t, err, `identity "root" requires an enrollment token`)
		if len(keysOf(t, s, "root")) != 0 {
			t.Fatal("a key was bound")
		}
		if id, _ := identityRow(t, s, "root"); !id.Admin {
			t.Fatal("admin flag lost")
		}
		// enrollment mode: the admin text, not the mode's.
		_, err = s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "root", Key: genKey(t)})
		wantDetail(t, err, `identity "root" requires an enrollment token`)
		// closed mode: the mode check runs first.
		_, err = s.Register(ctx, ModeClosed, RegisterInput{Identity: "root", Key: genKey(t)})
		wantDetail(t, err, "registration is closed")

		tok, row := newToken(t, s, TokenInput{Identity: "root"})
		res, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: "root", Key: genKey(t), EnrollmentToken: tok})
		if err != nil || res.Created {
			t.Fatalf("with token = %+v, %v", res, err)
		}
		if len(keysOf(t, s, "root")) != 1 {
			t.Fatal("key not bound")
		}
		if id, _ := identityRow(t, s, "root"); !id.Admin {
			t.Fatal("admin flag lost")
		}
		if tokenRow(t, s, row.ID).UsesLeft != 0 {
			t.Fatal("token not consumed")
		}
	})

	t.Run("keyless plain row", func(t *testing.T) {
		s, _ := newSvc(t, nil)
		if err := s.RegisterLocal(ctx, "pat"); err != nil {
			t.Fatal(err)
		}
		res, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: genKey(t)})
		if err != nil || res.Created {
			t.Fatalf("claim = %+v, %v", res, err)
		}
		if len(keysOf(t, s, "pat")) != 1 {
			t.Fatal("key not bound")
		}
	})
}

func TestRegisterClosed(t *testing.T) {
	s, _ := newSvc(t, nil)
	_, err := s.Register(context.Background(), ModeClosed, RegisterInput{Identity: "pat", Key: genKey(t)})
	wantCode(t, err, bus.CodeForbidden)
	wantDetail(t, err, "registration is closed")
	if _, ok := identityRow(t, s, "pat"); ok {
		t.Fatal("row created")
	}
}

func TestRegisterEnrollment(t *testing.T) {
	ctx := context.Background()
	s, clk := newSvc(t, nil)
	k := genKey(t)

	_, err := s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "pat", Key: k})
	wantCode(t, err, bus.CodeForbidden)
	wantDetail(t, err, "registration requires an enrollment token")
	_, err = s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "pat", Key: k, EnrollmentToken: "abe_nope"})
	wantCode(t, err, bus.CodeForbidden)
	wantDetail(t, err, "enrollment token is invalid")

	tok, row := newToken(t, s, TokenInput{Uses: 1})
	res, err := s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "pat", Key: k, EnrollmentToken: tok})
	if err != nil || !res.Created {
		t.Fatalf("register = %+v, %v", res, err)
	}
	if tokenRow(t, s, row.ID).UsesLeft != 0 {
		t.Fatal("UsesLeft != 0")
	}
	// the lost-response retry
	res, err = s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "pat", Key: k, EnrollmentToken: tok})
	if err != nil || res.Created {
		t.Fatalf("retry = %+v, %v", res, err)
	}
	if len(keysOf(t, s, "pat")) != 1 || tokenRow(t, s, row.ID).UsesLeft != 0 {
		t.Fatal("retry changed state")
	}
	_, err = s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "kim", Key: genKey(t), EnrollmentToken: tok})
	wantCode(t, err, bus.CodeForbidden)
	wantDetail(t, err, "enrollment token is invalid")

	// a different key with a fresh token: conflict, token not consumed
	tok2, row2 := newToken(t, s, TokenInput{})
	_, err = s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "pat", Key: genKey(t), EnrollmentToken: tok2})
	wantCode(t, err, bus.CodeConflict)
	if tokenRow(t, s, row2.ID).UsesLeft != 1 {
		t.Fatal("conflict consumed the token")
	}

	// bound to kim, used for lee
	tok3, _ := newToken(t, s, TokenInput{Identity: "kim"})
	_, err = s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "lee", Key: genKey(t), EnrollmentToken: tok3})
	wantCode(t, err, bus.CodeForbidden)
	wantDetail(t, err, "enrollment token is invalid")
	if _, ok := identityRow(t, s, "lee"); ok {
		t.Fatal("lee created")
	}

	// expired
	tok4, _ := newToken(t, s, TokenInput{Expires: time.Hour})
	clk.advance(2 * time.Hour)
	_, err = s.Register(ctx, ModeEnrollment, RegisterInput{Identity: "lee", Key: genKey(t), EnrollmentToken: tok4})
	wantCode(t, err, bus.CodeForbidden)
}

func TestRegisterOpenValidatesSuppliedToken(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	k := genKey(t)
	_, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: k, EnrollmentToken: "abe_bogus"})
	wantCode(t, err, bus.CodeForbidden)
	tok, row := newToken(t, s, TokenInput{})
	if _, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: k, EnrollmentToken: tok}); err != nil {
		t.Fatal(err)
	}
	if tokenRow(t, s, row.ID).UsesLeft != 0 {
		t.Fatal("token not consumed")
	}
	res, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: k, EnrollmentToken: "abe_bogus"})
	if err != nil || res.Created {
		t.Fatalf("same-key retry with a bogus token = %+v, %v", res, err)
	}
}

func TestRegisterRejects(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	k := genKey(t)
	for _, name := range []string{"a/b", "everyone", "admins", "", strings.Repeat("x", 129)} {
		_, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: name, Key: k})
		wantCode(t, err, bus.CodeValidation)
	}
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: rk.Public()})
	wantCode(t, err, bus.CodeValidation)
	_, err = s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat"})
	wantCode(t, err, bus.CodeValidation)
	wantDetail(t, err, "public key is required")
	if _, ok := identityRow(t, s, "pat"); ok {
		t.Fatal("row created by a rejected register")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal(`Mode("") did not panic`)
			}
		}()
		_, _ = s.Register(ctx, Mode(""), RegisterInput{Identity: "pat", Key: k})
	}()
}

func TestParseMode(t *testing.T) {
	for _, m := range []string{"open", "enrollment", "closed"} {
		if got, err := ParseMode(m); err != nil || string(got) != m {
			t.Fatalf("ParseMode(%q) = %q, %v", m, got, err)
		}
	}
	for _, m := range []string{"", "Open", "all"} {
		_, err := ParseMode(m)
		wantCode(t, err, bus.CodeValidation)
		wantDetail(t, err, "enrollment")
	}
}

func TestRegisterLocalAndCreateIdentity(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	if err := s.RegisterLocal(ctx, "pat"); err != nil {
		t.Fatal(err)
	}
	if _, ok := generalSub(t, s, "pat"); !ok {
		t.Fatal("no general subscription")
	}
	if len(keysOf(t, s, "pat")) != 0 {
		t.Fatal("a key was created")
	}
	if err := s.RegisterLocal(ctx, "pat"); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateIdentity(ctx, "kim")
	if err != nil || !created {
		t.Fatalf("CreateIdentity = %v, %v", created, err)
	}
	created, err = s.CreateIdentity(ctx, "kim")
	if err != nil || created {
		t.Fatalf("CreateIdentity again = %v, %v", created, err)
	}
	if id, _ := identityRow(t, s, "kim"); id.Admin {
		t.Fatal("admin")
	}
	wantCode(t, s.RegisterLocal(ctx, "a/b"), bus.CodeValidation)
	wantCode(t, s.RegisterLocal(ctx, "everyone"), bus.CodeValidation)
}

func TestMakeAdmin(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	err := s.MakeAdmin(ctx, "ghost", true)
	wantCode(t, err, bus.CodeNotRegistered)
	wantDetail(t, err, "register it first")
	if err := s.RegisterLocal(ctx, "pat"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []bool{true, true, false} {
		if err := s.MakeAdmin(ctx, "pat", want); err != nil {
			t.Fatal(err)
		}
		if id, _ := identityRow(t, s, "pat"); id.Admin != want {
			t.Fatalf("admin = %v, want %v", id.Admin, want)
		}
	}
}

func TestResetCredential(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	if err := s.RegisterLocal(ctx, "pat"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterLocal(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	if err := s.MakeAdmin(ctx, "root", true); err != nil {
		t.Fatal(err)
	}
	put := func() {
		for i := 0; i < 2; i++ {
			pub := genKey(t)
			kid, _ := keys.KID(pub)
			der, _ := keys.MarshalPublic(pub)
			tx(t, s, func(ctx context.Context, tx store.Tx) error {
				return tx.PutPublicKey(ctx, store.PublicKey{Identity: "pat", KID: kid, Algorithm: "EdDSA", Key: der, CreatedAt: startMs})
			})
		}
	}
	put()
	if n, err := s.ResetCredential(ctx, "pat"); err != nil || n != 2 {
		t.Fatalf("n = %d, %v", n, err)
	}
	if n, err := s.ResetCredential(ctx, "pat"); err != nil || n != 0 {
		t.Fatalf("again n = %d, %v", n, err)
	}
	_, err := s.ResetCredential(ctx, "ghost")
	wantCode(t, err, bus.CodeNotRegistered)

	put()
	_, err = s.ResetCredentialAs(ctx, "pat", "pat", "")
	wantCode(t, err, bus.CodeForbidden)
	if len(keysOf(t, s, "pat")) != 2 {
		t.Fatal("non-admin removed keys")
	}
	if n, err := s.ResetCredentialAs(ctx, "root", "pat", "k1"); err != nil || n != 2 {
		t.Fatalf("n = %d, %v", n, err)
	}
	put()
	if n, err := s.ResetCredentialAs(ctx, "root", "pat", "k1"); err != nil || n != 2 {
		t.Fatalf("replay n = %d, %v", n, err)
	}
	if len(keysOf(t, s, "pat")) != 2 {
		t.Fatal("the replay ran again")
	}
	_, err = s.ResetCredentialAs(ctx, "root", "kim", "k1")
	wantCode(t, err, bus.CodeConflict)
}

// fixture builds the Reset fixtures: pat and kim with keys, admin root with a
// key, a token, the signing key, a channel with messages, a session and a
// receipt.
func fixture(t *testing.T, s Service) {
	t.Helper()
	ctx := context.Background()
	for _, n := range []string{"pat", "kim", "root"} {
		if _, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: n, Key: genKey(t)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MakeAdmin(ctx, "root", true); err != nil {
		t.Fatal(err)
	}
	newToken(t, s, TokenInput{})
	if _, _, _, err := s.LoadOrCreateSigningKey(ctx); err != nil {
		t.Fatal(err)
	}
	err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
		ch, err := tx.CreateChannel(ctx, store.ChannelCreate{Name: "dev", Kind: store.KindOrdinary, Creator: "pat", CreatedAt: startMs})
		if err != nil {
			return err
		}
		if err := tx.Subscribe(ctx, "kim", ch.ID, 0, startMs); err != nil {
			return err
		}
		_, _, err = s.Append(ctx, tx, core.AppendInput{ChannelID: ch.ID, Channel: "dev", Kind: ch.Kind, Sender: "pat", Content: "hi"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		return tx.OpenSession(ctx, store.Session{
			ID: "s1", Identity: "pat", ResumeTokenHash: "h", OpenedAt: startMs, LeaseExpiresAt: startMs + 60_000,
		})
	})
}

type snapshot struct {
	Identities []store.Identity
	Keys       map[string][]store.PublicKey
	Tokens     []store.EnrollmentToken
	Signing    store.SigningKey
	MaxSeq     int64
}

func snap(t *testing.T, s Service) snapshot {
	t.Helper()
	sn := snapshot{Keys: map[string][]store.PublicKey{}}
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		var err error
		if sn.Identities, err = tx.ListIdentities(ctx); err != nil {
			return err
		}
		for _, id := range sn.Identities {
			if sn.Keys[id.Name], err = tx.PublicKeys(ctx, id.Name); err != nil {
				return err
			}
		}
		if sn.Tokens, err = tx.ListEnrollmentTokens(ctx); err != nil {
			return err
		}
		if sn.Signing, err = tx.SigningKey(ctx); err != nil {
			return err
		}
		sn.MaxSeq, err = tx.MaxSeq(ctx)
		return err
	})
	return sn
}

func channelNames(t *testing.T, s Service) ([]store.Channel, map[int64][]store.ACLEntry) {
	t.Helper()
	var chs []store.Channel
	acls := map[int64][]store.ACLEntry{}
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		var err error
		if chs, err = tx.ListChannels(ctx); err != nil {
			return err
		}
		for _, c := range chs {
			if acls[c.ID], err = tx.ACL(ctx, c.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return chs, acls
}

func TestResetKeepsIdentitiesAndKeys(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	fixture(t, s)
	before := snap(t, s)

	// a non-admin changes nothing
	wantCode(t, s.Reset(ctx, "pat", "", bus.ResetData), bus.CodeForbidden)
	if chs, _ := channelNames(t, s); len(chs) != 2 {
		t.Fatalf("a non-admin reset changed the channels: %+v", chs)
	}
	// an unknown scope changes nothing
	err := s.Reset(ctx, "root", "", bus.ResetScope("all"))
	wantCode(t, err, bus.CodeValidation)
	wantDetail(t, err, `"all"`)
	if chs, _ := channelNames(t, s); len(chs) != 2 {
		t.Fatal("an invalid scope changed the channels")
	}

	for i, scope := range []bus.ResetScope{bus.ResetData, ""} {
		if i == 1 {
			fixtureAgain(t, s)
		}
		if err := s.Reset(ctx, "root", fmt.Sprintf("run%d", i), scope); err != nil {
			t.Fatal(err)
		}
		chs, acls := channelNames(t, s)
		if len(chs) != 1 || chs[0].Name != "general" || chs[0].Creator != "" {
			t.Fatalf("channels = %+v", chs)
		}
		if !reflect.DeepEqual(acls[chs[0].ID], store.DefaultACL("")) {
			t.Fatalf("acl = %+v", acls[chs[0].ID])
		}
		for _, n := range []string{"pat", "kim", "root"} {
			if _, ok := generalSub(t, s, n); !ok {
				t.Fatalf("%s is not subscribed to general", n)
			}
		}
		if got := snap(t, s); !reflect.DeepEqual(got, before) {
			t.Fatalf("registry changed:\n got %+v\nwant %+v", got, before)
		}
		tx(t, s, func(ctx context.Context, tx store.Tx) error {
			ss, err := tx.ListSessions(ctx, "")
			if len(ss) != 0 {
				t.Fatalf("sessions survived: %+v", ss)
			}
			return err
		})
	}

	// the same key twice runs once
	if err := s.Reset(ctx, "root", "once", bus.ResetData); err != nil {
		t.Fatal(err)
	}
	fixtureAgain(t, s)
	if err := s.Reset(ctx, "root", "once", bus.ResetData); err != nil {
		t.Fatal(err)
	}
	if chs, _ := channelNames(t, s); len(chs) != 2 {
		t.Fatalf("the replay ran again: %+v", chs)
	}
}

// fixtureAgain adds a channel and a session after a reset so that a second
// Reset has something to wipe.
func fixtureAgain(t *testing.T, s Service) {
	t.Helper()
	tx(t, s, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.CreateChannel(ctx, store.ChannelCreate{Name: "dev", Kind: store.KindOrdinary, Creator: "pat", CreatedAt: startMs}); err != nil {
			return err
		}
		return tx.OpenSession(ctx, store.Session{ID: "s2", Identity: "pat", ResumeTokenHash: "h2", OpenedAt: startMs, LeaseExpiresAt: startMs + 60_000})
	})
}

func TestResetEverythingKeepsCaller(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	fixture(t, s)
	before := snap(t, s)
	wantCode(t, s.Reset(ctx, "pat", "", bus.ResetEverything), bus.CodeForbidden)
	if got := snap(t, s); !reflect.DeepEqual(got, before) {
		t.Fatal("a refused reset changed the registry")
	}
	for i := 0; i < 2; i++ {
		if err := s.Reset(ctx, "root", "", bus.ResetEverything); err != nil {
			t.Fatal(err)
		}
		got := snap(t, s)
		if len(got.Identities) != 1 || got.Identities[0].Name != "root" || !got.Identities[0].Admin {
			t.Fatalf("identities = %+v", got.Identities)
		}
		if !reflect.DeepEqual(got.Keys["root"], before.Keys["root"]) {
			t.Fatal("root's keys changed")
		}
		if len(got.Tokens) != 0 {
			t.Fatalf("tokens = %+v", got.Tokens)
		}
		if got.Signing != before.Signing || got.MaxSeq != before.MaxSeq {
			t.Fatal("signing key or MaxSeq changed")
		}
		chs, _ := channelNames(t, s)
		if len(chs) != 1 || chs[0].Name != "general" {
			t.Fatalf("channels = %+v", chs)
		}
		if _, ok := generalSub(t, s, "root"); !ok {
			t.Fatal("root not subscribed")
		}
		if _, ok := generalSub(t, s, "pat"); ok {
			t.Fatal("pat survived")
		}
	}
}

func TestResetUsesConfiguredDefaultACL(t *testing.T) {
	ctx := context.Background()
	want := []store.ACLEntry{{Identity: store.Everyone, Permissions: []store.Permission{store.PermRead}}}
	s, _ := newSvc(t, func(c *core.Config) { c.DefaultACL = func(string) []store.ACLEntry { return want } })
	fixture(t, s)
	if err := s.Reset(ctx, "root", "", bus.ResetData); err != nil {
		t.Fatal(err)
	}
	chs, acls := channelNames(t, s)
	if len(chs) != 1 || !reflect.DeepEqual(acls[chs[0].ID], want) {
		t.Fatalf("acl = %+v", acls[chs[0].ID])
	}
}

func TestPublicKey(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	k := genKey(t)
	res, err := s.Register(ctx, ModeOpen, RegisterInput{Identity: "pat", Key: k})
	if err != nil {
		t.Fatal(err)
	}
	pub, alg, err := s.PublicKey(ctx, "pat", res.KID)
	if err != nil || alg != keys.EdDSA {
		t.Fatalf("PublicKey = %v, %v, %v", pub, alg, err)
	}
	if kid, _ := keys.KID(pub); kid != res.KID {
		t.Fatal("wrong key")
	}
	_, _, err = s.PublicKey(ctx, "pat", "nope")
	wantCode(t, err, bus.CodeNotFound)
	_, _, err = s.PublicKey(ctx, "ghost", res.KID)
	wantCode(t, err, bus.CodeNotFound)
}

func TestEnrollmentTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	tok, row := newToken(t, s, TokenInput{})
	if !strings.HasPrefix(tok, "abe_") || len(tok) != 47 {
		t.Fatalf("token %q", tok)
	}
	if row.TokenHash != HashToken(tok) || len(row.ID) != 16 {
		t.Fatalf("row = %+v", row)
	}
	if row.UsesLeft != 1 || row.ExpiresAt != startMs+24*3600*1000 || row.CreatedAt != startMs {
		t.Fatalf("defaults = %+v", row)
	}
	rows, err := s.ListEnrollmentTokens(ctx)
	if err != nil || len(rows) != 1 || rows[0] != row {
		t.Fatalf("list = %+v, %v", rows, err)
	}
	if err := s.RevokeEnrollmentToken(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	wantCode(t, s.RevokeEnrollmentToken(ctx, row.ID), bus.CodeNotFound)
	_, _, err = s.CreateEnrollmentToken(ctx, TokenInput{Uses: -1})
	wantCode(t, err, bus.CodeValidation)
	_, _, err = s.CreateEnrollmentToken(ctx, TokenInput{Expires: -time.Hour})
	wantCode(t, err, bus.CodeValidation)
	_, _, err = s.CreateEnrollmentToken(ctx, TokenInput{Identity: "a/b"})
	wantCode(t, err, bus.CodeValidation)
}

func TestSigningKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := newSvc(t, nil)
	k1, alg, created, err := s.LoadOrCreateSigningKey(ctx)
	if err != nil || !created || alg != keys.EdDSA {
		t.Fatalf("first = %v, %v, %v", alg, created, err)
	}
	var row store.SigningKey
	tx(t, s, func(ctx context.Context, tx store.Tx) error { var e error; row, e = tx.SigningKey(ctx); return e })
	if kid, _ := keys.KID(k1.Public()); row.KID != kid {
		t.Fatal("row KID differs")
	}
	if _, _, err := keys.ParsePrivatePEM([]byte(row.PrivateKeyPEM)); err != nil {
		t.Fatal(err)
	}
	k2, alg, created, err := s.LoadOrCreateSigningKey(ctx)
	if err != nil || created || alg != keys.EdDSA {
		t.Fatalf("second = %v, %v, %v", alg, created, err)
	}
	if kid1, _ := keys.KID(k1.Public()); true {
		if kid2, _ := keys.KID(k2.Public()); kid1 != kid2 {
			t.Fatal("a different key on the second call")
		}
	}

	// a corrupt row is reported and left alone
	s2, _ := newSvc(t, nil)
	if err := s2.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutSigningKey(ctx, store.SigningKey{KID: "kid1", Algorithm: "EdDSA", PrivateKeyPEM: "garbage", CreatedAt: startMs})
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = s2.LoadOrCreateSigningKey(ctx)
	wantCode(t, err, bus.CodeInternal)
	wantDetail(t, err, `"kid1"`)
	tx(t, s2, func(ctx context.Context, tx store.Tx) error {
		r, err := tx.SigningKey(ctx)
		if err != nil || r.PrivateKeyPEM != "garbage" {
			t.Fatalf("row = %+v, %v", r, err)
		}
		return nil
	})

	// a PEM that parses but whose KID differs
	s3, _ := newSvc(t, nil)
	other, _ := keys.Generate(keys.EdDSA)
	pem, _ := keys.EncodePrivatePEM(other)
	tx(t, s3, func(ctx context.Context, tx store.Tx) error {
		return tx.PutSigningKey(ctx, store.SigningKey{KID: "wrong", Algorithm: "EdDSA", PrivateKeyPEM: string(pem), CreatedAt: startMs})
	})
	_, _, _, err = s3.LoadOrCreateSigningKey(ctx)
	wantCode(t, err, bus.CodeInternal)
}
