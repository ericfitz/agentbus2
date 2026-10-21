package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/search"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// Every caller-supplied string bound to a TEXT column passes store.TextRule
// (valid UTF-8, no NUL) before the query, on both engines (Decision 54):
// SQLite stored such bytes while Postgres failed the statement raw (SQLSTATE
// 22021, not a Store error) and aborted the transaction. A write of the
// caller's own data answers ErrInvalid; a key naming another row (an
// identity, a session id) answers as for an unknown row (ErrNotFound); a
// lookup or delete by a failing key answers as for an unknown row (none,
// false, no-op). Each call runs in its own transaction because an error ends
// the transaction.

// badTexts are a non-UTF-8 string and a NUL (valid UTF-8, refused by
// Postgres TEXT the same way).
var badTexts = []string{"b\xff", "b\x00"}

// wantIs reports unless err matches want (nil: success).
func wantIs(t *testing.T, what string, err, want error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Errorf("%s err = %v, want nil", what, err)
		}
		return
	}
	if !errors.Is(err, want) {
		t.Errorf("%s err = %v, want %v", what, err, want)
	}
}

func TestMetaRejectsNUL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for _, bad := range badTexts {
			for what, fn := range map[string]txFunc{
				"TryLease(name)": func(ctx context.Context, tx ab2store.Tx) error {
					_, err := tx.TryLease(ctx, bad, "o", fixedNow, fixedNow+10)
					return err
				},
				"TryLease(owner)": func(ctx context.Context, tx ab2store.Tx) error {
					_, err := tx.TryLease(ctx, "maintenance", bad, fixedNow, fixedNow+10)
					return err
				},
				"SetNotice(kind)": func(ctx context.Context, tx ab2store.Tx) error {
					return tx.SetNotice(ctx, ab2store.Notice{Kind: bad, Message: "m", SetAt: fixedNow})
				},
				"SetNotice(message)": func(ctx context.Context, tx ab2store.Tx) error {
					return tx.SetNotice(ctx, ab2store.Notice{Kind: "k", Message: bad, SetAt: fixedNow})
				},
			} {
				wantIs(t, what+" "+bad, s.Tx(ctx, fn), ab2store.ErrInvalid)
			}
			wantIs(t, "ClearNotice "+bad, s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error { return tx.ClearNotice(ctx, bad) }), nil)
			wantIs(t, "Notice "+bad, s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				if _, found, err := tx.Notice(ctx, bad); err != nil || found {
					t.Errorf("Notice(%q) found = %v", bad, found)
					return err
				}
				return nil
			}), nil)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, found, err := tx.Notice(ctx, "k"); err != nil || found {
				t.Errorf("Notice(k) after the rejected writes: found = %v, err = %v", found, err)
			}
			return nil
		})
	})
}

func TestMessagesRejectsNUL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		good := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "p", Content: "hello", Tags: []string{"t"}})
		for _, bad := range badTexts {
			// Metadata goes through json.Marshal, which escapes NUL and
			// replaces invalid UTF-8, so it never diverged by engine; it is
			// refused by default rather than stored altered.
			for what, mut := range map[string]func(*ab2store.NewMessage){
				"sender":         func(m *ab2store.NewMessage) { m.Sender = bad },
				"session id":     func(m *ab2store.NewMessage) { m.SessionID = bad },
				"type":           func(m *ab2store.NewMessage) { m.Type = bad },
				"subject":        func(m *ab2store.NewMessage) { m.Subject = bad },
				"content":        func(m *ab2store.NewMessage) { m.Content = bad },
				"tag":            func(m *ab2store.NewMessage) { m.Tags = []string{"t", bad} },
				"metadata key":   func(m *ab2store.NewMessage) { m.Metadata = map[string]string{bad: "v"} },
				"metadata value": func(m *ab2store.NewMessage) { m.Metadata = map[string]string{"k": bad} },
			} {
				m := ab2store.NewMessage{ChannelID: ch.ID, Sender: "p", Content: "x", CreatedAt: fixedNow}
				mut(&m)
				wantIs(t, "AppendMessage("+what+" "+bad+")", s.Append(ctx, func(ctx context.Context, tx ab2store.Tx) error {
					_, err := tx.AppendMessage(ctx, m)
					return err
				}), ab2store.ErrInvalid)
			}
			for _, p := range []string{bad, bad + "*"} {
				wantIs(t, "MessagesInRange("+p+")", s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
					ms, err := tx.MessagesInRange(ctx, ch.ID, 0, 1000, []ab2store.TagPattern{ab2store.NewTagPattern(p)}, ab2store.MessageFilter{}, 10)
					if len(ms) != 0 {
						t.Errorf("MessagesInRange(%q) = %v, want none", p, messageSeqs(ms))
					}
					return err
				}), nil)
			}
			for what, q := range map[string]search.Query{
				// search.Parse turns invalid UTF-8 into U+FFFD but keeps a
				// NUL inside a word; parsing "\xff" yields a valid term.
				"parsed":      search.Parse("hello" + strings.ReplaceAll(bad, "\xff", "\x00")),
				"constructed": {Terms: []search.Term{{Text: "hello" + bad}}},
			} {
				wantIs(t, "SearchText("+what+" "+bad+")", s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
					_, err := tx.SearchText(ctx, q, []int64{ch.ID}, ab2store.SearchFilter{}, 10)
					return err
				}), ab2store.ErrInvalid)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ms, err := tx.MessagesAfter(ctx, ch.ID, 0, ab2store.MessageFilter{}, 100)
			if err != nil || !reflect.DeepEqual(messageSeqs(ms), []int64{good}) {
				t.Errorf("messages after the rejected appends = %v, %v; want [%d]", messageSeqs(ms), err, good)
			}
			return nil
		})
	})
}

func TestSessionsRejectsNUL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "pat", nil)
		want := sessionX("s1", "pat", "h1", 1, 1000)
		sessionOpen(t, s, want)
		for _, bad := range badTexts {
			for what, c := range map[string]struct {
				mut  func(*ab2store.Session)
				want error
			}{
				"id":                {func(x *ab2store.Session) { x.ID = bad }, ab2store.ErrInvalid},
				"identity":          {func(x *ab2store.Session) { x.Identity = bad }, ab2store.ErrNotFound},
				"display name":      {func(x *ab2store.Session) { x.DisplayName = bad }, ab2store.ErrInvalid},
				"hint":              {func(x *ab2store.Session) { x.Hint = bad }, ab2store.ErrInvalid},
				"resume token hash": {func(x *ab2store.Session) { x.ResumeTokenHash = bad }, ab2store.ErrInvalid},
				"pending token":     {func(x *ab2store.Session) { x.PendingToken = bad }, ab2store.ErrInvalid},
				"watch id":          {func(x *ab2store.Session) { x.WatchID = bad }, ab2store.ErrInvalid},
			} {
				x := sessionX("s2", "pat", "h2", 1, 1000)
				c.mut(&x)
				wantIs(t, "OpenSession("+what+" "+bad+")", s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error { return tx.OpenSession(ctx, x) }), c.want)
			}
			for what, c := range map[string]struct {
				fn   txFunc
				want error
			}{
				"Session": {func(ctx context.Context, tx ab2store.Tx) error { _, err := tx.Session(ctx, bad); return err }, ab2store.ErrNotFound},
				"SessionByResumeTokenHash": {func(ctx context.Context, tx ab2store.Tx) error {
					_, err := tx.SessionByResumeTokenHash(ctx, bad)
					return err
				}, ab2store.ErrNotFound},
				"RenewLease":           {func(ctx context.Context, tx ab2store.Tx) error { return tx.RenewLease(ctx, bad, 5) }, ab2store.ErrNotFound},
				"CloseSession":         {func(ctx context.Context, tx ab2store.Tx) error { return tx.CloseSession(ctx, bad, 5) }, ab2store.ErrNotFound},
				"RegisterWatch(id)":    {func(ctx context.Context, tx ab2store.Tx) error { return tx.RegisterWatch(ctx, bad, "w", 5) }, ab2store.ErrNotFound},
				"RegisterWatch(watch)": {func(ctx context.Context, tx ab2store.Tx) error { return tx.RegisterWatch(ctx, "s1", bad, 5) }, ab2store.ErrInvalid},
				"SetPendingToken(id)":  {func(ctx context.Context, tx ab2store.Tx) error { return tx.SetPendingToken(ctx, bad, "t") }, ab2store.ErrNotFound},
				"SetPendingToken(tok)": {func(ctx context.Context, tx ab2store.Tx) error { return tx.SetPendingToken(ctx, "s1", bad) }, ab2store.ErrInvalid},
				"SetTagsCursor":        {func(ctx context.Context, tx ab2store.Tx) error { return tx.SetTagsCursor(ctx, bad, 1, 2) }, ab2store.ErrNotFound},
				"PutCursor": {func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutCursor(ctx, ab2store.Cursor{SessionID: bad, ChannelID: ch.ID})
				}, ab2store.ErrNotFound},
				"DeleteCursor": {func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteCursor(ctx, bad, ch.ID) }, nil},
				"ListSessions(identity)": {func(ctx context.Context, tx ab2store.Tx) error {
					ss, err := tx.ListSessions(ctx, bad)
					if len(ss) != 0 {
						t.Errorf("ListSessions(%q) = %v, want none", bad, sessionIDs(ss))
					}
					return err
				}, nil},
				"Cursors": {func(ctx context.Context, tx ab2store.Tx) error {
					cs, err := tx.Cursors(ctx, bad)
					if len(cs) != 0 {
						t.Errorf("Cursors(%q) = %v, want none", bad, cs)
					}
					return err
				}, nil},
			} {
				wantIs(t, what+" "+bad, s.Tx(ctx, c.fn), c.want)
			}
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			ss, err := tx.ListSessions(ctx, "")
			if err != nil || len(ss) != 1 || !reflect.DeepEqual(ss[0], want) {
				t.Errorf("sessions after the rejected writes = %+v, %v; want only %+v", ss, err, want)
			}
			return nil
		})
	})
}

func TestSubscriptionsRejectsNUL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "pat", nil)
		for _, bad := range badTexts {
			valid := func() ab2store.TagSubscription { return tagSet("pat", "k", 0, "t") }
			for what, c := range map[string]struct {
				fn   txFunc
				want error
			}{
				"Subscribe":        {func(ctx context.Context, tx ab2store.Tx) error { return tx.Subscribe(ctx, bad, ch.ID, 0, fixedNow) }, ab2store.ErrNotFound},
				"Unsubscribe":      {func(ctx context.Context, tx ab2store.Tx) error { return tx.Unsubscribe(ctx, bad, ch.ID) }, nil},
				"AdvanceAckedMark": {func(ctx context.Context, tx ab2store.Tx) error { return tx.AdvanceAckedMark(ctx, bad, ch.ID, 5) }, ab2store.ErrNotFound},
				"SubscribeTags(identity)": {func(ctx context.Context, tx ab2store.Tx) error {
					x := valid()
					x.Identity = bad
					return tx.SubscribeTags(ctx, x)
				}, ab2store.ErrNotFound},
				"SubscribeTags(key)": {func(ctx context.Context, tx ab2store.Tx) error {
					x := valid()
					x.TagsKey = bad
					return tx.SubscribeTags(ctx, x)
				}, ab2store.ErrInvalid},
				"SubscribeTags(pattern)": {func(ctx context.Context, tx ab2store.Tx) error {
					return tx.SubscribeTags(ctx, tagSet("pat", "k", 0, "t", bad+"*"))
				}, ab2store.ErrInvalid},
				"SubscribeTags(lo)": {func(ctx context.Context, tx ab2store.Tx) error {
					x := valid()
					x.Patterns[0].Lo = bad
					return tx.SubscribeTags(ctx, x)
				}, ab2store.ErrInvalid},
				"SubscribeTags(hi)": {func(ctx context.Context, tx ab2store.Tx) error {
					x := valid()
					x.Patterns[0].Hi = bad
					return tx.SubscribeTags(ctx, x)
				}, ab2store.ErrInvalid},
				"UnsubscribeTags(identity)": {func(ctx context.Context, tx ab2store.Tx) error { return tx.UnsubscribeTags(ctx, bad, "k") }, nil},
				"UnsubscribeTags(key)":      {func(ctx context.Context, tx ab2store.Tx) error { return tx.UnsubscribeTags(ctx, "pat", bad) }, nil},
				"Subscriptions": {func(ctx context.Context, tx ab2store.Tx) error {
					subs, err := tx.Subscriptions(ctx, bad)
					if len(subs) != 0 {
						t.Errorf("Subscriptions(%q) = %v, want none", bad, subs)
					}
					return err
				}, nil},
				"TagSubscriptions": {func(ctx context.Context, tx ab2store.Tx) error {
					subs, err := tx.TagSubscriptions(ctx, bad)
					if len(subs) != 0 {
						t.Errorf("TagSubscriptions(%q) = %v, want none", bad, subs)
					}
					return err
				}, nil},
				"TagMatchesAfter": {func(ctx context.Context, tx ab2store.Tx) error {
					ms, err := tx.TagMatchesAfter(ctx, bad, 0, 0, []int64{ch.ID}, ab2store.MessageFilter{}, 10)
					if len(ms) != 0 {
						t.Errorf("TagMatchesAfter(%q) = %v, want none", bad, messageSeqs(ms))
					}
					return err
				}, nil},
			} {
				wantIs(t, what+" "+bad, s.Tx(ctx, c.fn), c.want)
			}
		}
		if subs := tagSubs(t, s, "pat"); len(subs) != 0 {
			t.Errorf("tag subscriptions after the rejected writes = %+v, want none", subs)
		}
	})
}

func TestChannelsRejectsNUL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "pat", nil)
		var strangerPerms ab2store.Permissions
		var strangerReadable []int64
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			if strangerPerms, err = tx.EffectivePermissions(ctx, ch.ID, "stranger"); err != nil {
				return err
			}
			strangerReadable, err = tx.ReadableChannels(ctx, "stranger")
			return err
		})
		for _, bad := range badTexts {
			wantIs(t, "ChannelByName "+bad, s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.ChannelByName(ctx, bad)
				return err
			}), ab2store.ErrNotFound)
			wantIs(t, "CreateChannel(creator "+bad+")", s.Tx(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "b", Kind: ab2store.KindOrdinary, Creator: bad, CreatedAt: fixedNow})
				return err
			}), ab2store.ErrInvalid)
			// Such an identity cannot exist, so it gets what any unregistered
			// identity gets: the everyone rows.
			wantIs(t, "EffectivePermissions/ReadableChannels "+bad, s.Read(ctx, func(ctx context.Context, tx ab2store.Tx) error {
				p, err := tx.EffectivePermissions(ctx, ch.ID, bad)
				if err != nil {
					return err
				}
				if p != strangerPerms {
					t.Errorf("EffectivePermissions(%q) = %+v, want an unregistered identity's %+v", bad, p, strangerPerms)
				}
				ids, err := tx.ReadableChannels(ctx, bad)
				if !reflect.DeepEqual(ids, strangerReadable) {
					t.Errorf("ReadableChannels(%q) = %v, want an unregistered identity's %v", bad, ids, strangerReadable)
				}
				return err
			}), nil)
		}
	})
}

func TestEmbeddingsRejectsNUL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		ch := mustChannel(t, s, "a", ab2store.KindMemory, "", nil)
		mem := embeddingMemory(t, s, ch.ID, 1)
		for _, bad := range badTexts {
			for what, c := range map[string]struct {
				fn   txFunc
				want error
			}{
				"PutEmbedding(model)": {func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutEmbedding(ctx, ab2store.Embedding{Seq: mem, Model: bad, Vector: []byte{1, 2, 3, 4}})
				}, ab2store.ErrInvalid},
				"PutEmbedFailure(model)": {func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: mem, Model: bad, Error: "e", FailedAt: fixedNow})
				}, ab2store.ErrInvalid},
				"PutEmbedFailure(error)": {func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: mem, Model: "m", Error: bad, FailedAt: fixedNow})
				}, ab2store.ErrInvalid},
				"UnembeddedMemories": {func(ctx context.Context, tx ab2store.Tx) error {
					_, err := tx.UnembeddedMemories(ctx, bad, 10)
					return err
				}, ab2store.ErrInvalid},
				"DeleteStaleModel": {func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteStaleModel(ctx, bad) }, ab2store.ErrInvalid},
				"Embeddings": {func(ctx context.Context, tx ab2store.Tx) error {
					es, err := tx.Embeddings(ctx, bad, []int64{ch.ID}, ab2store.SearchFilter{})
					if len(es) != 0 {
						t.Errorf("Embeddings(%q) = %v, want none", bad, es)
					}
					return err
				}, nil},
			} {
				wantIs(t, what+" "+bad, s.Tx(ctx, c.fn), c.want)
			}
		}
		for _, tbl := range []string{"embeddings", "embed_failures"} {
			if n := embeddingCount(t, e, rawDSN, tbl); n != 0 {
				t.Errorf("%s rows after the rejected writes = %d, want 0", tbl, n)
			}
		}
	})
}
