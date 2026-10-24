package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// Bridges, the message map, drops and Slack user bindings (Slack bridge spec 8).

const (
	ts1 = "1700000000.000100"
	ts2 = "1700000000.000200"
	ts3 = "1700000000.000300"
	ts4 = "1700000000.000400"
)

func newBridge(t *testing.T, s ab2store.Store, channelID int64, name string) ab2store.Bridge {
	t.Helper()
	var b ab2store.Bridge
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		b, err = tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: channelID, ChannelName: name, Scope: "job1", SlackName: "ab-" + name, CreatedAt: 5})
		return err
	})
	return b
}

func getBridge(t *testing.T, s ab2store.Store, id int64) ab2store.Bridge {
	t.Helper()
	var b ab2store.Bridge
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		b, err = tx.Bridge(ctx, id)
		return err
	})
	return b
}

func TestBridgeLifecycle(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		c1 := mustChannel(t, s, "job1-chat", ab2store.KindOrdinary, "", nil)
		c2 := mustChannel(t, s, "job1-other", ab2store.KindOrdinary, "", nil)
		b := newBridge(t, s, c1.ID, "job1-chat")
		if b.ID <= 0 || b.State != ab2store.BridgeCreating || b.ChannelID != c1.ID || b.Scope != "job1" ||
			b.SlackName != "ab-job1-chat" || b.CreatedAt != 5 || b.ArchivedAt != 0 || b.CursorTS != "" {
			t.Fatalf("CreateBridge = %+v", b)
		}
		run := func(fn txFunc) error { return txErr(s, fn) }
		wantErr := func(label string, err, want error) {
			t.Helper()
			if !errors.Is(err, want) {
				t.Errorf("%s = %v, want %v", label, err, want)
			}
		}
		wantErr("second CreateBridge", run(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: c1.ID, ChannelName: "job1-chat", Scope: "job1", SlackName: "x"})
			return err
		}), ab2store.ErrConflict)
		wantErr("SetBridgeLive without Slack id", run(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeLive(ctx, b.ID) }), ab2store.ErrInvalid)
		for i := 0; i < 2; i++ {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeSlackChannel(ctx, b.ID, "G1") })
		}
		for i := 0; i < 2; i++ {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeLive(ctx, b.ID) })
		}
		var byS, byC ab2store.Bridge
		var list, scoped []ab2store.Bridge
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			if byS, err = tx.BridgeBySlackChannel(ctx, "G1"); err != nil {
				return err
			}
			if byC, err = tx.BridgeByChannel(ctx, c1.ID); err != nil {
				return err
			}
			if list, err = tx.ListBridges(ctx); err != nil {
				return err
			}
			scoped, err = tx.ScopeBridges(ctx, "job1")
			return err
		})
		if byS.ID != b.ID || byS.State != ab2store.BridgeLive || byS.SlackChannelID != "G1" || byC.ID != b.ID {
			t.Errorf("lookups = %+v, %+v", byS, byC)
		}
		if len(list) != 1 || len(scoped) != 1 {
			t.Errorf("ListBridges %d, ScopeBridges %d, want 1 each", len(list), len(scoped))
		}
		wantErr("SetBridgeSlackChannel after live", run(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeSlackChannel(ctx, b.ID, "G1") }), ab2store.ErrConflict)
		for i := 0; i < 2; i++ {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeArchived(ctx, b.ID, 100+int64(i)) })
		}
		if got := getBridge(t, s, b.ID); got.State != ab2store.BridgeArchived || got.ArchivedAt != 100 {
			t.Errorf("archived bridge = %+v, want archived_at 100 kept by the second call", got)
		}
		wantErr("SetBridgeLive after archive", run(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeLive(ctx, b.ID) }), ab2store.ErrConflict)

		b2 := newBridge(t, s, c2.ID, "job1-other")
		wantErr("second bridge taking G1", run(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeSlackChannel(ctx, b2.ID, "G1") }), ab2store.ErrConflict)
		wantErr("create with a taken Slack id", run(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateBridge(ctx, ab2store.Bridge{ChannelID: 999, ChannelName: "z", Scope: "job1", SlackName: "z", SlackChannelID: "G1"})
			return err
		}), ab2store.ErrConflict)
	})
}

func TestBridgeListOrder(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		for i, n := range []string{"job1-b", "job1-a", "job1-c"} {
			newBridge(t, s, int64(i+1), n) // a bridge outlives its channel: no channel row needed
		}
		var names []string
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			l, err := tx.ListBridges(ctx)
			for _, b := range l {
				names = append(names, b.ChannelName)
			}
			return err
		})
		if !slices.Equal(names, []string{"job1-a", "job1-b", "job1-c"}) {
			t.Errorf("ListBridges order = %v", names)
		}
		var none []ab2store.Bridge
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			none, err = tx.ScopeBridges(ctx, "other")
			return err
		})
		if len(none) != 0 {
			t.Errorf("ScopeBridges(other) = %v", none)
		}
	})
}

func TestBridgeRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ok := ab2store.Bridge{ChannelID: 1, ChannelName: "c", Scope: "job1", SlackName: "n"}
		for name, mut := range map[string]func(*ab2store.Bridge){
			"empty SlackName": func(b *ab2store.Bridge) { b.SlackName = "" },
			"empty Scope":     func(b *ab2store.Bridge) { b.Scope = "" },
			"empty channel":   func(b *ab2store.Bridge) { b.ChannelName = "" },
			"state paused":    func(b *ab2store.Bridge) { b.State = "paused" },
			"state live":      func(b *ab2store.Bridge) { b.State = ab2store.BridgeLive },
			"NUL in name":     func(b *ab2store.Bridge) { b.SlackName = "a\x00b" },
			"bad UTF-8":       func(b *ab2store.Bridge) { b.Scope = "a\xffb" },
		} {
			b := ok
			mut(&b)
			err := txErr(s, func(ctx context.Context, tx ab2store.Tx) error { _, err := tx.CreateBridge(ctx, b); return err })
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("CreateBridge(%s) = %v, want ErrInvalid", name, err)
			}
		}
		for name, fn := range map[string]txFunc{
			"SetBridgeSlackChannel": func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeSlackChannel(ctx, 99, "G1") },
			"SetBridgeLive":         func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeLive(ctx, 99) },
			"SetBridgeArchived":     func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeArchived(ctx, 99, 1) },
			"AdvanceBridgeCursor":   func(ctx context.Context, tx ab2store.Tx) error { return tx.AdvanceBridgeCursor(ctx, 99, ts1) },
			"DeleteBridge":          func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteBridge(ctx, 99) },
			"Bridge":                func(ctx context.Context, tx ab2store.Tx) error { _, err := tx.Bridge(ctx, 99); return err },
			"BridgeByChannel":       func(ctx context.Context, tx ab2store.Tx) error { _, err := tx.BridgeByChannel(ctx, 99); return err },
			"BridgeBySlackChannel": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.BridgeBySlackChannel(ctx, "G9")
				return err
			},
			"BridgeBySlackChannel empty": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.BridgeBySlackChannel(ctx, "")
				return err
			},
			"PutBridgeMessage": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: 99, Seq: 1, SlackTS: ts1, RootTS: ts1})
			},
			"PutBridgeDrop": func(ctx context.Context, tx ab2store.Tx) error { return tx.PutBridgeDrop(ctx, 99, ts1, 1) },
		} {
			if err := txErr(s, fn); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("%s on an unknown bridge = %v, want ErrNotFound", name, err)
			}
		}
		b := newBridge(t, s, 1, "c")
		for name, fn := range map[string]txFunc{
			"empty Slack id": func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeSlackChannel(ctx, b.ID, "") },
			"NUL Slack id":   func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeSlackChannel(ctx, b.ID, "G\x00") },
			"empty ts (msg)": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: b.ID, Seq: 1, RootTS: ts1})
			},
			"empty root": func(ctx context.Context, tx ab2store.Tx) error {
				return tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: b.ID, Seq: 1, SlackTS: ts1})
			},
			"empty ts (drop)": func(ctx context.Context, tx ab2store.Tx) error { return tx.PutBridgeDrop(ctx, b.ID, "", 1) },
			"NUL ts (drop)":   func(ctx context.Context, tx ab2store.Tx) error { return tx.PutBridgeDrop(ctx, b.ID, "1\x00", 1) },
			"limit 0": func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.ThreadRoots(ctx, b.ID, "", 0)
				return err
			},
		} {
			if err := txErr(s, fn); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s = %v, want ErrInvalid", name, err)
			}
		}
		// A write inside Read fails with ErrReadOnly.
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.SetBridgeArchived(ctx, b.ID, 1) })
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Errorf("SetBridgeArchived in Read = %v, want ErrReadOnly", err)
		}
	})
}

func TestBridgeMessages(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		b := newBridge(t, s, 1, "c")
		other := newBridge(t, s, 2, "d")
		put := func(m ab2store.BridgeMessage) error {
			return txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutBridgeMessage(ctx, m) })
		}
		for _, m := range []ab2store.BridgeMessage{
			{BridgeID: b.ID, Seq: 10, SlackTS: ts1, RootTS: ts1, CreatedAt: 1},
			{BridgeID: b.ID, Seq: 11, SlackTS: ts2, RootTS: ts1, CreatedAt: 2},
			{BridgeID: b.ID, Seq: 12, SlackTS: ts3, RootTS: ts1, CreatedAt: 3},
			{BridgeID: other.ID, Seq: 10, SlackTS: ts1, RootTS: ts1, CreatedAt: 4}, // same keys on another bridge are fine
		} {
			if err := put(m); err != nil {
				t.Fatalf("PutBridgeMessage(%+v) = %v", m, err)
			}
		}
		if err := put(ab2store.BridgeMessage{BridgeID: b.ID, Seq: 10, SlackTS: ts4, RootTS: ts4}); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("same seq = %v, want ErrConflict", err)
		}
		if err := put(ab2store.BridgeMessage{BridgeID: b.ID, Seq: 13, SlackTS: ts1, RootTS: ts1}); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("same ts = %v, want ErrConflict", err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if seq, err := tx.NewestInThread(ctx, b.ID, ts1, "1700000000.000250"); err != nil || seq != 11 {
				t.Errorf("NewestInThread(.000250) = %d, %v, want 11", seq, err)
			}
			if _, err := tx.NewestInThread(ctx, b.ID, ts1, ts1); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("NewestInThread(.000100) = %v, want ErrNotFound", err)
			}
			if _, err := tx.NewestInThread(ctx, b.ID, ts4, ts4); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("NewestInThread(unknown root) = %v, want ErrNotFound", err)
			}
			if r, err := tx.ThreadRoots(ctx, b.ID, "", 10); err != nil || !slices.Equal(r, []string{ts1}) {
				t.Errorf("ThreadRoots(all) = %v, %v", r, err)
			}
			if r, err := tx.ThreadRoots(ctx, b.ID, ts4, 10); err != nil || len(r) != 0 {
				t.Errorf("ThreadRoots(.000400) = %v, %v, want empty", r, err)
			}
			m, err := tx.BridgeMessageBySeq(ctx, b.ID, 11)
			if want := (ab2store.BridgeMessage{BridgeID: b.ID, Seq: 11, SlackTS: ts2, RootTS: ts1, CreatedAt: 2}); err != nil || m != want {
				t.Errorf("BridgeMessageBySeq = %+v, %v", m, err)
			}
			if m, err = tx.BridgeMessageByTS(ctx, b.ID, ts3); err != nil || m.Seq != 12 {
				t.Errorf("BridgeMessageByTS = %+v, %v", m, err)
			}
			if _, err = tx.BridgeMessageBySeq(ctx, b.ID, 99); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("BridgeMessageBySeq(99) = %v", err)
			}
			if _, err = tx.BridgeMessageByTS(ctx, b.ID, ts4); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("BridgeMessageByTS(.000400) = %v", err)
			}
			return nil
		})
	})
}

func TestBridgeThreadRootsOrderAndLimit(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		b := newBridge(t, s, 1, "c")
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for i, m := range []ab2store.BridgeMessage{
				{Seq: 1, SlackTS: ts1, RootTS: ts1},
				{Seq: 2, SlackTS: ts2, RootTS: ts2},
				{Seq: 3, SlackTS: ts3, RootTS: ts1}, // the older thread was active last
			} {
				m.BridgeID, m.CreatedAt = b.ID, int64(i)
				if err := tx.PutBridgeMessage(ctx, m); err != nil {
					return err
				}
			}
			return nil
		})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if r, err := tx.ThreadRoots(ctx, b.ID, "", 10); err != nil || !slices.Equal(r, []string{ts1, ts2}) {
				t.Errorf("ThreadRoots = %v, %v, want most recently active first", r, err)
			}
			if r, err := tx.ThreadRoots(ctx, b.ID, "", 1); err != nil || !slices.Equal(r, []string{ts1}) {
				t.Errorf("ThreadRoots(limit 1) = %v, %v", r, err)
			}
			if r, err := tx.ThreadRoots(ctx, b.ID, ts3, 10); err != nil || !slices.Equal(r, []string{ts1}) {
				t.Errorf("ThreadRoots(since .000300) = %v, %v", r, err)
			}
			return nil
		})
	})
}

func TestBridgeDrops(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		b := newBridge(t, s, 1, "c")
		for i := 0; i < 2; i++ {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutBridgeDrop(ctx, b.ID, ts1, 7) })
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if d, err := tx.BridgeDropped(ctx, b.ID, ts1); err != nil || !d {
				t.Errorf("BridgeDropped = %v, %v, want true", d, err)
			}
			if d, err := tx.BridgeDropped(ctx, b.ID, ts2); err != nil || d {
				t.Errorf("BridgeDropped(other ts) = %v, %v, want false", d, err)
			}
			return nil
		})
		if err := txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutBridgeDrop(ctx, 99, ts1, 1) }); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("PutBridgeDrop unknown bridge = %v, want ErrNotFound", err)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteBridge(ctx, b.ID) })
		if n := scalar[int](t, rawDB(t, e, rawDSN), "SELECT count(*) FROM bridge_dropped"); n != 0 {
			t.Errorf("bridge_dropped rows after DeleteBridge = %d, want 0", n)
		}
	})
}

func TestBridgeCursorMonotonic(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		b := newBridge(t, s, 1, "c")
		for _, ts := range []string{ts3, ts2, ts3} {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.AdvanceBridgeCursor(ctx, b.ID, ts) })
			if got := getBridge(t, s, b.ID).CursorTS; got != ts3 {
				t.Fatalf("cursor after advancing to %s = %s, want %s", ts, got, ts3)
			}
		}
	})
}

func TestDeleteBridgeCascades(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		b := newBridge(t, s, 1, "c")
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: b.ID, Seq: 1, SlackTS: ts1, RootTS: ts1}); err != nil {
				return err
			}
			return tx.PutBridgeDrop(ctx, b.ID, ts2, 1)
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteBridge(ctx, b.ID) })
		db := rawDB(t, e, rawDSN)
		for _, tbl := range []string{"bridges", "bridge_messages", "bridge_dropped"} {
			if n := scalar[int](t, db, "SELECT count(*) FROM "+tbl); n != 0 {
				t.Errorf("%s rows after DeleteBridge = %d, want 0", tbl, n)
			}
		}
		if err := txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteBridge(ctx, b.ID) }); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("second DeleteBridge = %v, want ErrNotFound", err)
		}
	})
}

func TestSlackUsers(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		mustIdentity(t, s, "eric")
		mustIdentity(t, s, "kim")
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CreateScopedIdentity(ctx, "job1-coder", "job1", 2)
		})
		put := func(u ab2store.SlackUser) error {
			return txErr(s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PutSlackUser(ctx, u) })
		}
		eric := ab2store.SlackUser{Identity: "eric", TeamID: "T1", UserID: "U1", CreatedAt: 9}
		if err := put(eric); err != nil {
			t.Fatalf("bind eric: %v", err)
		}
		if err := put(eric); err != nil {
			t.Errorf("bind eric again = %v, want nil", err)
		}
		for name, c := range map[string]struct {
			u    ab2store.SlackUser
			want error
		}{
			"eric to U2":       {ab2store.SlackUser{Identity: "eric", TeamID: "T1", UserID: "U2"}, ab2store.ErrConflict},
			"eric to T2/U1":    {ab2store.SlackUser{Identity: "eric", TeamID: "T2", UserID: "U1"}, ab2store.ErrConflict},
			"kim to U1":        {ab2store.SlackUser{Identity: "kim", TeamID: "T1", UserID: "U1"}, ab2store.ErrConflict},
			"unknown identity": {ab2store.SlackUser{Identity: "nobody", TeamID: "T1", UserID: "U3"}, ab2store.ErrNotFound},
			"empty team":       {ab2store.SlackUser{Identity: "kim", UserID: "U3"}, ab2store.ErrInvalid},
			"empty user":       {ab2store.SlackUser{Identity: "kim", TeamID: "T1"}, ab2store.ErrInvalid},
			"NUL user":         {ab2store.SlackUser{Identity: "kim", TeamID: "T1", UserID: "U\x00"}, ab2store.ErrInvalid},
		} {
			if err := put(c.u); !errors.Is(err, c.want) {
				t.Errorf("bind %s = %v, want %v", name, err, c.want)
			}
		}
		// kim may take the same user id in another team.
		if err := put(ab2store.SlackUser{Identity: "kim", TeamID: "T2", UserID: "U1"}); err != nil {
			t.Errorf("kim to T2/U1 = %v, want nil", err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if got, err := tx.SlackUserOf(ctx, "eric"); err != nil || got != eric {
				t.Errorf("SlackUserOf(eric) = %+v, %v", got, err)
			}
			if id, err := tx.IdentityOfSlackUser(ctx, "T1", "U1"); err != nil || id != "eric" {
				t.Errorf("IdentityOfSlackUser = %q, %v", id, err)
			}
			if _, err := tx.IdentityOfSlackUser(ctx, "T1", "U9"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("IdentityOfSlackUser(unbound) = %v", err)
			}
			if _, err := tx.SlackUserOf(ctx, "nobody"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("SlackUserOf(unbound) = %v", err)
			}
			return nil
		})
		for _, want := range []bool{true, false} {
			var did bool
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				did, err = tx.DeleteSlackUser(ctx, "eric")
				return err
			})
			if did != want {
				t.Errorf("DeleteSlackUser = %v, want %v", did, want)
			}
		}
		// Retiring the identity removes its binding.
		if err := put(ab2store.SlackUser{Identity: "job1-coder", TeamID: "T1", UserID: "U5"}); err != nil {
			t.Fatal(err)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.RetireIdentity(ctx, "job1-coder", 50)
			return err
		})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.IdentityOfSlackUser(ctx, "T1", "U5"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("binding after RetireIdentity = %v, want ErrNotFound", err)
			}
			return nil
		})
	})
}
