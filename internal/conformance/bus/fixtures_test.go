package bus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgconn"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/local"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// fakeClock is the harness's injected clock (Decision 49).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

// fakeVocab is the fixed vocabulary of fakeEmbedder vectors.
var fakeVocab = []string{
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel",
	"india", "juliet", "kilo", "lima", "mike", "november", "oscar", "papa",
}

// fakeEmbedder is a deterministic core.Embedder. A text's vector is its
// lowercase-word histogram over the 16-word vocabulary, normalized; words
// outside it are ignored. Fail makes every Embed return a plain error (an
// endpoint failure); Reject maps a text to a reason: an Embed whose texts
// contain a rejected one returns a *core.RowRejected for the first such
// index and no vectors (the whole call fails, as an HTTP 422 does), so the
// row-by-row retry is what isolates it.
type fakeEmbedder struct {
	Fail   bool
	Reject map[string]string
}

func (f *fakeEmbedder) Model() string { return "fake" }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.Fail {
		return nil, errors.New("fake embedder: endpoint failure")
	}
	for i, tx := range texts {
		if reason, ok := f.Reject[tx]; ok {
			return nil, &core.RowRejected{Index: i, Reason: reason}
		}
	}
	idx := map[string]int{}
	for i, w := range fakeVocab {
		idx[w] = i
	}
	out := make([][]float32, len(texts))
	for i, tx := range texts {
		v := make([]float32, len(fakeVocab))
		for _, w := range strings.FieldsFunc(strings.ToLower(tx), func(r rune) bool { return !unicode.IsLetter(r) }) {
			if j, ok := idx[w]; ok {
				v[j]++
			}
		}
		rules.Normalize(v)
		out[i] = v
	}
	return out, nil
}

// faultStore wraps a Store to inject one transaction fault. Tx and Append
// call the inner Store with a wrapper around fn that runs fn and, on the
// first call only (fired flips under the mutex), discards fn's nil and
// returns failFirst instead. The inner store's own retry loop classifies it:
// on Postgres 40001 is retryable, so fn runs a second time and commits once;
// on SQLite the dialect does not classify it, so it surfaces unwrapped and
// nothing committed. Returning failFirst after the inner Append had
// committed would leave the message committed and break both halves of the
// test, which is why the wrapper is inside fn.
type faultStore struct {
	ab2store.Store
	failFirst *pgconn.PgError

	mu    sync.Mutex
	fired bool
}

func (f *faultStore) wrap(fn func(context.Context, ab2store.Tx) error) func(context.Context, ab2store.Tx) error {
	return func(ctx context.Context, tx ab2store.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failFirst != nil && !f.fired {
			f.fired = true
			return f.failFirst
		}
		return nil
	}
}

func (f *faultStore) Tx(ctx context.Context, fn func(context.Context, ab2store.Tx) error) error {
	return f.Store.Tx(ctx, f.wrap(fn))
}

func (f *faultStore) Append(ctx context.Context, fn func(context.Context, ab2store.Tx) error) error {
	return f.Store.Append(ctx, f.wrap(fn))
}

// pgconnError40001 is a serialization failure, the fault faultStore injects.
var pgconnError40001 = pgconn.PgError{Code: "40001", Message: "could not serialize access"}

// harness is one Core over one isolated database, with every collaborator the
// tests steer.
type harness struct {
	t        *testing.T
	e        dbtest.Engine
	store    ab2store.Store // what the Core uses; a faultStore when harnessOptions.fault is set
	raw      ab2store.Store // the unwrapped Store: the setup helpers use it, so a fault is not spent on fixtures
	core     *core.Core
	clock    *fakeClock
	notifier notify.Notifier // *notify.Memory by default, *notify.Polling when harnessOptions.polling > 0; tests use Subscribe() only
	embedder *fakeEmbedder
	cfg      core.Config

	reopen func() (ab2store.Store, error)
}

// harnessOptions selects a harness's variants.
type harnessOptions struct {
	cfg      *core.Config  // nil: core.Defaults()
	polling  time.Duration // >0: notify.NewPolling(polling)
	embedder bool          // a fakeEmbedder, with cfg.EmbeddingModel "fake"
	fault    *pgconn.PgError
}

// newHarness opens a fresh database and builds a Core over it, with the
// clock at fixedNowMs.
func newHarness(t *testing.T, e dbtest.Engine, o harnessOptions) *harness {
	t.Helper()
	s, reopen := openStore(t, e)
	h := &harness{t: t, e: e, reopen: reopen}
	h.build(s, o)
	return h
}

// build wires a Core over s into h.
func (h *harness) build(s ab2store.Store, o harnessOptions) {
	h.t.Helper()
	h.cfg = core.Defaults()
	if o.cfg != nil {
		h.cfg = *o.cfg
	}
	h.raw = s
	if o.fault != nil {
		s = &faultStore{Store: s, failFirst: o.fault}
	}
	h.store = s
	h.clock = &fakeClock{t: time.UnixMilli(fixedNowMs)}
	if o.polling > 0 {
		h.notifier = notify.NewPolling(o.polling)
	} else {
		h.notifier = notify.NewMemory()
	}
	opts := core.Options{Now: h.clock.Now, Notifier: h.notifier}
	if o.embedder {
		h.embedder = &fakeEmbedder{}
		h.cfg.EmbeddingModel = h.embedder.Model()
		opts.Embedder = h.embedder
	}
	c, err := core.New(h.store, h.cfg, opts)
	if err != nil {
		h.t.Fatalf("core.New: %v", err)
	}
	h.core = c
}

// newBus is how the suite builds a Bus for an identity; Milestone 4
// reassigns it to run the same suite through the client.
var newBus = func(h *harness, identity string) ab2bus.Bus { return local.New(h.core, identity) }

// sibling is a second Core over the same database (the same SQLite path or
// Postgres schema), with its own Store handle, notifier, clock and limiter:
// the local multi-process cases.
func (h *harness) sibling(o harnessOptions) *harness {
	h.t.Helper()
	s, err := h.reopen()
	if err != nil {
		h.t.Fatalf("sibling Open: %v", err)
	}
	sib := &harness{t: h.t, e: h.e, reopen: h.reopen}
	sib.build(s, o)
	return sib
}

// tx runs fn in a Store.Tx and fails the test on error.
func (h *harness) tx(fn func(ctx context.Context, tx ab2store.Tx) error) {
	h.t.Helper()
	if err := h.raw.Tx(context.Background(), fn); err != nil {
		h.t.Fatalf("store tx: %v", err)
	}
}

// ensureIdentity creates the identity when missing.
func (h *harness) ensureIdentity(name string) {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		if _, err := tx.Identity(ctx, name); err == nil {
			return nil
		} else if !errors.Is(err, ab2store.ErrNotFound) {
			return err
		}
		return tx.CreateIdentity(ctx, name, h.core.NowMs())
	})
}

// as returns a Bus for identity, creating the identity when missing.
func (h *harness) as(identity string) ab2bus.Bus {
	h.t.Helper()
	h.ensureIdentity(identity)
	return newBus(h, identity)
}

// human returns a Bus for the human identity named email (created through
// Tx.CreateHuman with an email binding when missing). The identity name is
// the email.
func (h *harness) human(email string) ab2bus.Bus {
	h.t.Helper()
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		if _, err := tx.Identity(ctx, email); err == nil {
			return nil
		} else if !errors.Is(err, ab2store.ErrNotFound) {
			return err
		}
		return tx.CreateHuman(ctx, ab2store.HumanBinding{Identity: email, ClaimType: ab2store.ClaimEmail, Value: email}, h.core.NowMs())
	})
	return newBus(h, email)
}

// admin is as plus the administrator flag.
func (h *harness) admin(identity string) ab2bus.Bus {
	h.t.Helper()
	h.ensureIdentity(identity)
	h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetAdmin(ctx, identity, true) })
	return newBus(h, identity)
}

// channel creates a channel through the Store, with CreatedSeq = MaxSeq. A
// nil acl stores the Decision 88 default for creator.
func (h *harness) channel(name string, kind ab2store.Kind, creator string, acl []ab2store.ACLEntry) ab2store.Channel {
	h.t.Helper()
	if creator != "" {
		h.ensureIdentity(creator) // the Store requires a registered creator
	}
	var ch ab2store.Channel
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err = tx.CreateChannel(ctx, ab2store.ChannelCreate{
			Name: name, Kind: kind, Creator: creator, ACL: acl, CreatedSeq: seq, CreatedAt: h.core.NowMs(),
		})
		return err
	})
	return ch
}

// append writes a message through Store.Append around core.Append, so a
// memory-kind channel gets its memory_id, revision 1 and memory_access row
// and a tasks-kind channel its TaskRow; CreatedAt is the harness clock.
func (h *harness) append(ch ab2store.Channel, sender, session, content string, tags ...string) int64 {
	h.t.Helper()
	var seq int64
	err := h.raw.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		var err error
		seq, _, err = h.core.Append(ctx, tx, core.AppendInput{
			ChannelID: ch.ID, Channel: ch.Name, Kind: ch.Kind, Sender: sender, SenderKind: ab2store.IdentityAgent, SessionID: session,
			Content: content, Tags: tags, TaskRow: ch.Kind == ab2store.KindTasks,
		})
		return err
	})
	if err != nil {
		h.t.Fatalf("append: %v", err)
	}
	return seq
}

// session writes a live session row through Store.OpenSession, never through
// Open: the identity is created when missing, the ID is random, OpenedAt is
// the clock, the lease is core.LeaseUntil(clock) and TagsCursorSeq is the
// identity's TagsAckedSeq; it has no cursors (a test that needs them uses
// PutCursor). It is lapsed by advancing the clock and closed by
// Store.CloseSession.
func (h *harness) session(identity, display string) ab2store.Session {
	h.t.Helper()
	h.ensureIdentity(identity)
	id, err := core.RandomHex(16)
	if err != nil {
		h.t.Fatal(err)
	}
	hash, err := core.RandomHex(16)
	if err != nil {
		h.t.Fatal(err)
	}
	now := h.core.NowMs()
	var s ab2store.Session
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		ident, err := tx.Identity(ctx, identity)
		if err != nil {
			return err
		}
		s = ab2store.Session{
			ID: id, Identity: identity, DisplayName: display, ResumeTokenHash: hash,
			OpenedAt: now, LeaseExpiresAt: h.core.LeaseUntil(now), TagsCursorSeq: ident.TagsAckedSeq,
		}
		return tx.OpenSession(ctx, s)
	})
	return s
}

// codeOf fails the test unless err carries the wanted bus code.
func codeOf(t *testing.T, err error, want ab2bus.Code) {
	t.Helper()
	if got := ab2bus.CodeOf(err); got != want {
		t.Fatalf("error code = %q (err %v), want %q", got, err, want)
	}
}
