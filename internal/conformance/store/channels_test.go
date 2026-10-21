package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func channelNames(cs []ab2store.Channel) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestChannelCreate(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "work", ab2store.KindOrdinary, "pat", nil)
		if ch.ID <= 0 {
			t.Errorf("ID = %d, want > 0", ch.ID)
		}
		want := ab2store.Channel{ID: ch.ID, Name: "work", Kind: ab2store.KindOrdinary, Creator: "pat", CreatedSeq: 0, EvictedBeforeSeq: 0, CreatedAt: fixedNow}
		if ch != want {
			t.Errorf("created = %+v, want %+v", ch, want)
		}
		tasks := mustChannel(t, s, "jobs", ab2store.KindTasks, "", nil)
		if tasks.Creator != "" || tasks.Kind != ab2store.KindTasks {
			t.Errorf("tasks channel = %+v", tasks)
		}
		mustChannel(t, s, "alpha", ab2store.KindMemory, "pat", nil)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.ChannelByName(ctx, "work")
			if err != nil || got != ch {
				t.Errorf("ChannelByName = %+v, %v; want %+v", got, err, ch)
			}
			got, err = tx.Channel(ctx, ch.ID)
			if err != nil || got != ch {
				t.Errorf("Channel = %+v, %v; want %+v", got, err, ch)
			}
			got, err = tx.ChannelByName(ctx, "jobs")
			if err != nil || got.Creator != "" {
				t.Errorf("jobs = %+v, %v; want Creator \"\"", got, err)
			}
			list, err := tx.ListChannels(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n := channelNames(list); !equalStrings(n, []string{"alpha", "jobs", "work"}) {
				t.Errorf("ListChannels = %v, want [alpha jobs work]", n)
			}
			bk, err := tx.ListChannelsByKind(ctx, ab2store.KindTasks)
			if err != nil {
				t.Fatal(err)
			}
			if n := channelNames(bk); !equalStrings(n, []string{"jobs"}) {
				t.Errorf("ListChannelsByKind(tasks) = %v, want [jobs]", n)
			}
			if _, err := tx.ChannelByName(ctx, "nope"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("ChannelByName(nope) err = %v, want ErrNotFound", err)
			}
			if _, err := tx.Channel(ctx, 9999); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("Channel(9999) err = %v, want ErrNotFound", err)
			}
			// Listing by an unrecognized kind is rejected, not an empty result.
			for _, k := range []ab2store.Kind{"dm", "", "Ordinary"} {
				if _, err := tx.ListChannelsByKind(ctx, k); !errors.Is(err, ab2store.ErrInvalid) {
					t.Errorf("ListChannelsByKind(%q) err = %v, want ErrInvalid", k, err)
				}
			}
			return nil
		})
	})
}

func TestChannelCreateRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustChannel(t, s, "taken", ab2store.KindOrdinary, "pat", nil)
		create := func(c ab2store.ChannelCreate) error {
			c.CreatedAt = fixedNow
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.CreateChannel(ctx, c)
				return err
			})
		}
		for _, name := range []string{"a/b", "", strings.Repeat("x", 129), "c\xff"} {
			if err := create(ab2store.ChannelCreate{Name: name, Kind: ab2store.KindOrdinary}); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("name %q err = %v, want ErrInvalid", name, err)
			}
		}
		for _, k := range []ab2store.Kind{"dm", ""} {
			if err := create(ab2store.ChannelCreate{Name: "k", Kind: k}); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("kind %q err = %v, want ErrInvalid", k, err)
			}
		}
		if err := create(ab2store.ChannelCreate{Name: "taken", Kind: ab2store.KindOrdinary}); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("duplicate err = %v, want ErrConflict", err)
		}
		if err := create(ab2store.ChannelCreate{Name: "ghosted", Kind: ab2store.KindOrdinary, Creator: "ghost"}); !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("unregistered creator err = %v, want ErrInvalid", err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.ChannelByName(ctx, "ghosted"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("rejected channel exists: %v", err)
			}
			return nil
		})
	})
}

func TestChannelRename(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ch := mustChannel(t, s, "old", ab2store.KindOrdinary, "", nil)
		mustChannel(t, s, "other", ab2store.KindOrdinary, "", nil)
		rename := func(id int64, name string) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				return tx.RenameChannel(ctx, id, name)
			})
		}
		if err := rename(ch.ID, "new"); err != nil {
			t.Fatal(err)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.ChannelByName(ctx, "old"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("old name err = %v, want ErrNotFound", err)
			}
			got, err := tx.ChannelByName(ctx, "new")
			if err != nil || got.ID != ch.ID {
				t.Errorf("new = %+v, %v; want id %d", got, err, ch.ID)
			}
			return nil
		})
		if err := rename(ch.ID, "other"); !errors.Is(err, ab2store.ErrConflict) {
			t.Errorf("taken err = %v, want ErrConflict", err)
		}
		for _, name := range []string{"a/b", "n\xff"} {
			if err := rename(ch.ID, name); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("rename to %q err = %v, want ErrInvalid", name, err)
			}
		}
		if err := rename(9999, "fresh"); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown id err = %v, want ErrNotFound", err)
		}
	})
}

func TestChannelDeleteRemovesACL(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "doomed", ab2store.KindOrdinary, "pat", nil)
		keep := mustChannel(t, s, "keep", ab2store.KindOrdinary, "pat", nil)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteChannel(ctx, ch.ID) })
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.Channel(ctx, ch.ID); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("Channel err = %v, want ErrNotFound", err)
			}
			ids, err := tx.ReadableChannels(ctx, "pat")
			if err != nil || len(ids) != 1 || ids[0] != keep.ID {
				t.Errorf("ReadableChannels = %v, %v; want [%d]", ids, err, keep.ID)
			}
			return nil
		})
		db := rawDB(t, e, rawDSN)
		if n := scalar[int](t, db, "SELECT count(*) FROM channel_acl WHERE channel_id = "+itoa(ch.ID)); n != 0 {
			t.Errorf("channel_acl rows left = %d", n)
		}
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteChannel(ctx, ch.ID) })
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("second delete err = %v, want ErrNotFound", err)
		}
	})
}

func TestChannelSetEvictedBefore(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ch := mustChannel(t, s, "c", ab2store.KindOrdinary, "", nil)
		set := func(seq int64) {
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.SetEvictedBefore(ctx, ch.ID, seq) })
		}
		get := func() int64 {
			var v int64
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				c, err := tx.Channel(ctx, ch.ID)
				v = c.EvictedBeforeSeq
				return err
			})
			return v
		}
		set(10)
		set(5)
		if v := get(); v != 10 {
			t.Errorf("after 10, 5: %d, want 10", v)
		}
		set(12)
		if v := get(); v != 12 {
			t.Errorf("after 12: %d, want 12", v)
		}
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error { return tx.SetEvictedBefore(ctx, 9999, 1) })
		if !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown channel err = %v, want ErrNotFound", err)
		}
	})
}

func TestChannelCreateInsideReadIsReadOnly(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.CreateChannel(ctx, ab2store.ChannelCreate{Name: "ro", Kind: ab2store.KindOrdinary, CreatedAt: fixedNow})
			return err
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Fatalf("err = %v, want ErrReadOnly", err)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.ChannelByName(ctx, "ro"); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("channel exists after read-only create: %v", err)
			}
			return nil
		})
	})
}

// ListChannels and ListChannelsByKind are in byte order of the name on both
// engines (the names are the ones that make SQLite's BINARY collation and
// Postgres's database collation disagree).
func TestChannelListIsByteOrder(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for _, n := range byteOrderNames {
			mustChannel(t, s, n, ab2store.KindTasks, "", nil)
		}
		mustChannel(t, s, "plain", ab2store.KindOrdinary, "", nil)
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			all, err := tx.ListChannels(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := channelNames(all), []string{"Pat", "alpha-2", "alpha_1", "kim", "plain", "zed", "\u00c9mile"}; !equalStrings(got, want) {
				t.Errorf("ListChannels = %q, want byte order %q", got, want)
			}
			tasks, err := tx.ListChannelsByKind(ctx, ab2store.KindTasks)
			if err != nil {
				t.Fatal(err)
			}
			if got := channelNames(tasks); !equalStrings(got, byteOrderWant) {
				t.Errorf("ListChannelsByKind(tasks) = %q, want byte order %q", got, byteOrderWant)
			}
			return nil
		})
	})
}
