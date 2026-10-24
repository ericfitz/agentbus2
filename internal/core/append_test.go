package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// appendOne appends through Store.Append and returns the result.
func appendOne(t *testing.T, c *Core, in AppendInput) (seq int64, memID *int64, err error) {
	t.Helper()
	err = c.Store.Append(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var e error
		seq, memID, e = c.Append(ctx, tx, in)
		return e
	})
	return
}

func TestAppendMemoryKindSetsMemoryAndAccess(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	ch := mustChannel(t, c, store.ChannelCreate{Name: "mem", Kind: store.KindMemory, Creator: "pat"})
	seq, memID, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "x"})
	if err != nil || memID == nil || *memID != seq {
		t.Fatalf("Append = %d, %v, %v; want memory_id == seq", seq, memID, err)
	}
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		m, err := tx.LiveMemory(ctx, seq)
		if err != nil || m.Seq != seq || m.Revision == nil || *m.Revision != 1 || m.CreatedAt != startMs {
			t.Errorf("LiveMemory = %+v, %v", m, err)
		}
		// memory_access exists: it is what ExpiredMemories lists once old enough.
		refs, err := tx.ExpiredMemories(ctx, startMs+1, 10)
		if err != nil || len(refs) != 1 || refs[0].MemoryID != seq {
			t.Errorf("ExpiredMemories = %v, %v; want the memory's access row", refs, err)
		}
		return nil
	})
}

// ExpiredMemories filters tasks-kind channels in SQL, so a tasks-kind channel
// cannot show whether Append wrote a memory_access row. TaskRow is orthogonal
// to Kind in Append, so the flag is probed on a memory-kind channel, next to
// the same append without it.
func TestAppendTaskRowSkipsAccess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		taskRow bool
		want    int
	}{
		{"task row gets no access row", true, 0},
		{"plain memory row gets one", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestCore(t, nil)
			mustIdentity(t, c, "pat", false)
			ch := mustChannel(t, c, store.ChannelCreate{Name: "mem", Kind: store.KindMemory, Creator: "pat"})
			seq, memID, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: store.KindMemory, Sender: "pat", SenderKind: store.IdentityAgent, Content: "{}", TaskRow: tc.taskRow})
			if err != nil || memID == nil || *memID != seq {
				t.Fatalf("Append = %d, %v, %v; want memory_id == seq either way", seq, memID, err)
			}
			mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
				refs, err := tx.ExpiredMemories(ctx, startMs+1, 10)
				if err != nil || len(refs) != tc.want {
					t.Errorf("ExpiredMemories = %v, %v; want %d access row(s)", refs, err, tc.want)
				}
				return nil
			})
		})
	}
	// On a real tasks-kind channel the task row is still revision 1 of itself.
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	ch := mustChannel(t, c, store.ChannelCreate{Name: "todo", Kind: store.KindTasks, Creator: "pat"})
	seq, memID, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "{}", TaskRow: true})
	if err != nil || memID == nil || *memID != seq {
		t.Fatalf("tasks-kind Append = %d, %v, %v", seq, memID, err)
	}
}

func TestAppendOrdinaryHasNoMemory(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	ch := mustChannel(t, c, store.ChannelCreate{Name: "chat", Kind: store.KindOrdinary, Creator: "pat"})
	seq, memID, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "hi", Tags: []string{"a"}})
	if err != nil || memID != nil || seq == 0 {
		t.Fatalf("Append = %d, %v, %v", seq, memID, err)
	}
	if _, _, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: store.Kind("dm"), Sender: "pat", SenderKind: store.IdentityAgent, Content: "x"}); bus.CodeOf(err) != bus.CodeInternal {
		// An unknown kind is a programming error; it must not append silently as ordinary.
		t.Errorf("unknown kind = %v, want internal", err)
	}
}

// The kind is checked before the row is written: a caller that swallows the
// error and commits must not leave a message behind.
func TestAppendUnknownKindWritesNothing(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	ch := mustChannel(t, c, store.ChannelCreate{Name: "chat", Kind: store.KindOrdinary, Creator: "pat"})
	err := c.Store.Append(context.Background(), func(ctx context.Context, tx store.Tx) error {
		if _, _, e := c.Append(ctx, tx, AppendInput{ChannelID: ch.ID, Kind: store.Kind("dm"), Sender: "pat", SenderKind: store.IdentityAgent, Content: "x"}); bus.CodeOf(e) != bus.CodeInternal {
			t.Errorf("unknown kind = %v, want internal", e)
		}
		return nil // commit anyway
	})
	if err != nil {
		t.Fatal(err)
	}
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		ms, err := tx.MessagesAfter(ctx, ch.ID, 0, store.MessageFilter{}, 10)
		if err != nil || len(ms) != 0 {
			t.Errorf("MessagesAfter = %v, %v; want nothing written for an unknown kind", ms, err)
		}
		return nil
	})
}

func TestAppendRejectsOversizedEnvelope(t *testing.T) {
	c, _ := newTestCore(t, func(cfg *Config) { cfg.MaxMessageKiB = 1 })
	in := AppendInput{Channel: "chat", Kind: store.KindOrdinary, Sender: "pat", SenderKind: store.IdentityAgent, Content: strings.Repeat("x", 2048)}
	_, err := c.EnvelopeSize(in, "pat")
	if bus.CodeOf(err) != bus.CodeValidation || !strings.Contains(err.Error(), "envelope exceeds max_message_kib (1 KiB)") {
		t.Fatalf("EnvelopeSize = %v, want validation envelope exceeds max_message_kib (1 KiB)", err)
	}
	in.Content = "small"
	n, err := c.EnvelopeSize(in, "pat")
	if err != nil || n <= len("small") || n > 1024 {
		t.Fatalf("EnvelopeSize small = %d, %v", n, err)
	}
	// Memory kind is sized larger (memory_id and revision members).
	in.Kind = store.KindMemory
	m, _ := c.EnvelopeSize(in, "pat")
	if m <= n {
		t.Errorf("memory-kind size %d not above ordinary %d", m, n)
	}
}

// A display name is <identity>/<hint>-N, longer than the sender: the bound
// sizes the real Display, and falls back to the sender when none is given
// (an unresolved display falls back to the sender on the wire).
func TestEnvelopeSizeCountsDisplay(t *testing.T) {
	c, _ := newTestCore(t, func(cfg *Config) { cfg.MaxMessageKiB = 1 })
	in := AppendInput{Channel: "chat", Kind: store.KindOrdinary, Sender: "pat", SenderKind: store.IdentityAgent, Content: strings.Repeat("x", 600)}
	base, err := c.EnvelopeSize(in, "pat")
	if err != nil {
		t.Fatal(err)
	}
	in.Display = "pat"
	if n, _ := c.EnvelopeSize(in, "pat"); n != base {
		t.Errorf("Display == sender: size %d, want the fallback size %d", n, base)
	}
	in.Display = "pat/" + strings.Repeat("h", 128) + "-12"
	n, err := c.EnvelopeSize(in, "pat")
	if err != nil {
		t.Fatal(err)
	}
	if want := base + len(in.Display) - len("pat"); n != want {
		t.Errorf("size with a long display = %d, want %d", n, want)
	}
	in.Content = strings.Repeat("x", 900)
	if _, err := c.EnvelopeSize(in, "pat"); bus.CodeOf(err) != bus.CodeValidation {
		t.Errorf("a long display pushing the envelope over max_message_kib = %v, want validation", err)
	}
}

func TestAppendRejectsUnknownReplyTo(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	ch := mustChannel(t, c, store.ChannelCreate{Name: "chat", Kind: store.KindOrdinary, Creator: "pat"})
	missing := int64(999)
	_, _, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "x", ReplyTo: &missing})
	if bus.CodeOf(err) != bus.CodeNotFound || !strings.Contains(err.Error(), "reply_to 999 does not exist") {
		t.Fatalf("err = %v, want not_found reply_to 999 does not exist", err)
	}
	first, _, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "y", ReplyTo: &first}); err != nil {
		t.Fatalf("reply to an existing message: %v", err)
	}
}

func TestFromStoreMapping(t *testing.T) {
	passthrough := bus.Errorf(bus.CodeForbidden, false, "no")
	cases := []struct {
		name      string
		in        error
		code      bus.Code
		retryable bool
		text      string
	}{
		{"not found", store.ErrNotFound, bus.CodeNotFound, false, ""},
		{"conflict", fmt.Errorf("%w: channel exists", store.ErrConflict), bus.CodeConflict, false, "channel exists"},
		{"invalid keeps reason", fmt.Errorf("%w: channel name must not contain '/'", store.ErrInvalid), bus.CodeValidation, false, "channel name must not contain '/'"},
		{"retryable", store.ErrRetryable, bus.CodeUnavailable, true, ""},
		{"not append", store.ErrNotAppend, bus.CodeInternal, true, ""},
		{"read only", store.ErrReadOnly, bus.CodeInternal, true, ""},
		{"other", errors.New("boom"), bus.CodeInternal, true, "boom"},
		{"wrapped bus error", fmt.Errorf("ctx: %w", passthrough), bus.CodeForbidden, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := FromStore(tc.in)
			var be *bus.Error
			if !errors.As(err, &be) {
				t.Fatalf("FromStore(%v) = %v, not a *bus.Error", tc.in, err)
			}
			if be.Code != tc.code || be.Retryable != tc.retryable || !strings.Contains(be.Message, tc.text) {
				t.Errorf("FromStore(%v) = %+v, want code %s retryable %v text %q", tc.in, be, tc.code, tc.retryable, tc.text)
			}
		})
	}
	if FromStore(nil) != nil {
		t.Error("FromStore(nil) != nil")
	}
	if err := FromStore(passthrough); err != error(passthrough) {
		t.Error("a *bus.Error was not passed through unchanged")
	}
	if be := FromStore(fmt.Errorf("%w: channel name must not contain '/'", store.ErrInvalid)).(*bus.Error); strings.Contains(be.Message, "store:") {
		t.Errorf("validation message %q still carries the store prefix", be.Message)
	}
}

func TestEFixedRetryable(t *testing.T) {
	for code, want := range map[bus.Code]bool{
		bus.CodeRateLimited: true, bus.CodeUnavailable: true, bus.CodeInternal: true,
		bus.CodeValidation: false, bus.CodeNotFound: false, bus.CodeConflict: false, bus.CodeForbidden: false,
		bus.CodeNotRegistered: false, bus.CodeReplaced: false, bus.CodeSessionLapsed: false, bus.CodeSessionClosed: false,
	} {
		if got := E(code, "x").Retryable; got != want {
			t.Errorf("E(%s).Retryable = %v, want %v", code, got, want)
		}
	}
}

func TestRowRejected(t *testing.T) {
	err := fmt.Errorf("embed: %w", &RowRejected{Index: 1, Reason: "too long"})
	if !IsRowRejected(err) {
		t.Error("IsRowRejected false for a wrapped RowRejected")
	}
	if IsRowRejected(errors.New("500")) || IsRowRejected(nil) {
		t.Error("IsRowRejected true for a non-RowRejected")
	}
}

func TestMemoryHelpers(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	ch := mustChannel(t, c, store.ChannelCreate{Name: "mem", Kind: store.KindMemory, Creator: "pat"})
	seq, _, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		m, gotCh, err := c.LiveRevision(ctx, tx, seq)
		if err != nil || m.Seq != seq || gotCh.ID != ch.ID {
			t.Errorf("LiveRevision = %+v, %+v, %v", m, gotCh, err)
		}
		if _, _, err := c.LiveRevision(ctx, tx, 12345); bus.CodeOf(err) != bus.CodeNotFound || !strings.Contains(err.Error(), "memory 12345 has no live revision") {
			t.Errorf("LiveRevision(unknown) = %v", err)
		}
		return nil
	})
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		ok, err := c.TombstoneMemory(ctx, tx, seq, seq, startMs)
		if err != nil || !ok {
			t.Errorf("TombstoneMemory = %v, %v", ok, err)
		}
		return nil
	})
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		if ok, err := c.TombstoneMemory(ctx, tx, seq, seq, startMs); err != nil || ok {
			t.Errorf("second TombstoneMemory = %v, %v; want false", ok, err)
		}
		if _, _, err := c.LiveRevision(ctx, tx, seq); bus.CodeOf(err) != bus.CodeNotFound {
			t.Errorf("LiveRevision after tombstone = %v", err)
		}
		return nil
	})
}

// TombstoneMemory drops the memory's access row. The row is only observable
// through ExpiredMemories, which lists live revisions: a live revision 2
// sharing the memory id shows whether the access row survived tombstoning
// revision 1.
func TestTombstoneMemoryDropsAccessRow(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	ch := mustChannel(t, c, store.ChannelCreate{Name: "mem", Kind: store.KindMemory, Creator: "pat"})
	seq1, _, err := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var seq2 int64
	err = c.Store.Append(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var e error
		if seq2, e = tx.AppendMessage(ctx, store.NewMessage{ChannelID: ch.ID, Sender: "pat", SenderKind: store.IdentityAgent, CreatedAt: c.NowMs(), Content: "y"}); e != nil {
			return e
		}
		return tx.SetMemory(ctx, seq2, seq1, 2)
	})
	if err != nil {
		t.Fatal(err)
	}
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		refs, err := tx.ExpiredMemories(ctx, startMs+1, 10)
		if err != nil || len(refs) == 0 {
			t.Fatalf("ExpiredMemories before = %v, %v; want the memory listed through its access row", refs, err)
		}
		if ok, err := c.TombstoneMemory(ctx, tx, seq1, seq1, startMs); err != nil || !ok {
			t.Fatalf("TombstoneMemory = %v, %v", ok, err)
		}
		if refs, err := tx.ExpiredMemories(ctx, startMs+1, 10); err != nil || len(refs) != 0 {
			t.Errorf("access row survived the tombstone: %v, %v", refs, err)
		}
		return nil
	})
}

func TestSessionLiveness(t *testing.T) {
	c, _ := newTestCore(t, nil)
	lease := c.LeaseUntil(startMs)
	if lease != startMs+90_000 {
		t.Fatalf("LeaseUntil = %d", lease)
	}
	s := store.Session{LeaseExpiresAt: lease}
	skew := int64(c.Cfg.LeaseSkewSeconds) * 1000
	if !c.IsLive(s, lease+skew) {
		t.Error("not live exactly at lease + skew")
	}
	if c.IsLive(s, lease+skew+1) {
		t.Error("live past lease + skew")
	}
	s.ClosedAt = 1
	if c.IsLive(s, startMs) {
		t.Error("a closed session is live")
	}
}

func TestConversions(t *testing.T) {
	if k, err := KindFromBus(""); err != nil || k != store.KindOrdinary {
		t.Errorf(`KindFromBus("") = %v, %v`, k, err)
	}
	if _, err := KindFromBus("dm"); bus.CodeOf(err) != bus.CodeValidation {
		t.Errorf("KindFromBus(dm) = %v", err)
	}
	if KindToBus("") != bus.KindOrdinary || KindToBus(store.KindTasks) != bus.KindTasks {
		t.Error("KindToBus")
	}
	if _, err := ACLFromBus([]bus.ACLEntry{{Identity: "pat", Permissions: []bus.Permission{"owner"}}}); bus.CodeOf(err) != bus.CodeValidation {
		t.Errorf("ACLFromBus unknown permission = %v", err)
	}
	if got, err := ACLFromBus(nil); err != nil || got != nil {
		t.Errorf("ACLFromBus(nil) = %v, %v; nil must stay nil (configured default)", got, err)
	}
	if got, err := ACLFromBus([]bus.ACLEntry{}); err != nil || got == nil || len(got) != 0 {
		t.Errorf("ACLFromBus([]) = %#v, %v; empty must stay empty", got, err)
	}
	back := ACLToBus([]store.ACLEntry{{Identity: "pat", Permissions: []store.Permission{store.PermRead}}})
	if len(back) != 1 || back[0].Permissions[0] != bus.PermRead {
		t.Errorf("ACLToBus = %+v", back)
	}
	ch := store.Channel{Name: "c", Kind: store.KindMemory, Creator: "pat", CreatedAt: 5}
	if got := ChannelToBus(ch); got != (bus.Channel{Name: "c", Kind: bus.KindMemory}) {
		t.Errorf("ChannelToBus = %+v", got)
	}
	if got := ChannelInfoToBus(ch); got.Creator != "pat" || got.CreatedAt != 5 || got.Name != "c" {
		t.Errorf("ChannelInfoToBus = %+v", got)
	}
}

func TestToBusMessagesResolvesNames(t *testing.T) {
	c, _ := newTestCore(t, nil)
	mustIdentity(t, c, "pat", false)
	ch := mustChannel(t, c, store.ChannelCreate{Name: "chat", Kind: store.KindOrdinary, Creator: "pat"})
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		return tx.OpenSession(ctx, store.Session{ID: "s1", Identity: "pat", DisplayName: "pat-display", ResumeTokenHash: "h", OpenedAt: startMs, LeaseExpiresAt: startMs + 1000})
	})
	s1, _, _ := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, SessionID: "s1", Content: "a"})
	s2, _, _ := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, SessionID: "gone", Content: "b"})
	s3, _, _ := appendOne(t, c, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: store.IdentityAgent, Content: "c"})
	mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
		ms, err := tx.MessagesBySeqs(ctx, []int64{s1, s2, s3})
		if err != nil {
			return err
		}
		out, err := c.ToBusMessages(ctx, tx, ms)
		if err != nil || len(out) != 3 {
			t.Fatalf("ToBusMessages = %v, %v", out, err)
		}
		for i, want := range []string{"pat-display", "pat", ""} {
			if out[i].Channel != "chat" || out[i].Display != want {
				t.Errorf("message %d = channel %q display %q, want chat/%q", i, out[i].Channel, out[i].Display, want)
			}
		}
		return nil
	})
}

// The sender kind is checked before the row is written: "" and an unknown
// kind answer internal and leave nothing behind, even when the caller commits.
func TestAppendRefusesEmptySenderKind(t *testing.T) {
	for _, kind := range []store.IdentityKind{"", "robot"} {
		c, _ := newTestCore(t, nil)
		mustIdentity(t, c, "pat", false)
		ch := mustChannel(t, c, store.ChannelCreate{Name: "chat", Kind: store.KindOrdinary, Creator: "pat"})
		err := c.Store.Append(context.Background(), func(ctx context.Context, tx store.Tx) error {
			if _, _, e := c.Append(ctx, tx, AppendInput{ChannelID: ch.ID, Kind: ch.Kind, Sender: "pat", SenderKind: kind, Content: "x"}); bus.CodeOf(e) != bus.CodeInternal {
				t.Errorf("SenderKind %q = %v, want internal", kind, e)
			}
			return nil // commit anyway
		})
		if err != nil {
			t.Fatal(err)
		}
		mustTx(t, c, func(ctx context.Context, tx store.Tx) error {
			ms, err := tx.MessagesAfter(ctx, ch.ID, 0, store.MessageFilter{}, 10)
			if err != nil || len(ms) != 0 {
				t.Errorf("SenderKind %q: MessagesAfter = %v, %v; want nothing written", kind, ms, err)
			}
			return nil
		})
	}
}
