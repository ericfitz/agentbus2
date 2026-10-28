package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// The size_bytes columns and the eviction queries built on them (postgres
// budget plan, Task 1).

// countedColumns is the plan's table of the columns each size_bytes formula
// counts (32 plus their byte lengths); a column listed with a trailing ? is
// nullable and counts 0 when NULL.
var countedColumns = map[string][]string{
	"channels":          {"name", "kind", "creator", "scope?"},
	"channel_acl":       {"identity"},
	"subscriptions":     {"identity"},
	"session_cursors":   {"session_id"},
	"messages":          {"sender", "session_id", "type", "subject", "content", "metadata?", "sender_kind"},
	"message_tags":      {"tag"},
	"embeddings":        {"model", "vector"},
	"embed_failures":    {"model", "error"},
	"memory_access":     {},
	"bridge_messages":   {"slack_ts", "root_ts"},
	"enrollment_tokens": {"id", "token_hash", "identity"},
}

// memRow appends one revision of memory memID on channel ch.
func memRow(t *testing.T, s ab2store.Store, ch, memID, rev, createdAt int64, tags ...string) int64 {
	t.Helper()
	return mustAppend(t, s, ab2store.NewMessage{ChannelID: ch, Sender: "p", Content: "mem", CreatedAt: createdAt, MemoryID: &memID, Revision: &rev, Tags: tags})
}

// rawInt runs a one-integer query on db (the sums are cast so Postgres'
// numeric scans as an integer).
func rawInt(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// sumOf is the raw total size_bytes of the rows of table matching cond.
func sumOf(t *testing.T, db *sql.DB, table, cond string) int64 {
	t.Helper()
	return rawInt(t, db, "SELECT CAST(COALESCE(SUM(size_bytes), 0) AS BIGINT) FROM "+table+" WHERE "+cond)
}

func TestSizeBytesFormula(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustScope(t, s, "job1", 1)
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", []ab2store.ACLEntry{{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead}}})
		seq := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", SessionID: "s1", Type: "t", Subject: "sub", Content: "héllo ✓",
			Metadata: map[string]string{"k": "v"}, Tags: []string{"tág"}})
		mem := memRow(t, s, ch.ID, 1, 1, fixedNow)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: mem, Model: "mód", Vector: []byte{1, 2, 3}}); err != nil {
				return err
			}
			if err := tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: mem, Model: "m2", Error: "bøom", FailedAt: 1}); err != nil {
				return err
			}
			if err := tx.TouchMemoryAccess(ctx, []int64{1}, 5); err != nil {
				return err
			}
			if err := tx.Subscribe(ctx, "pat", ch.ID, 0, 1); err != nil {
				return err
			}
			if err := tx.OpenSession(ctx, ab2store.Session{ID: "sé", Identity: "pat", DisplayName: "d", ResumeTokenHash: "h", OpenedAt: 1, LeaseExpiresAt: 2}); err != nil {
				return err
			}
			return tx.PutCursor(ctx, ab2store.Cursor{SessionID: "sé", ChannelID: ch.ID, CursorSeq: 1})
		})
		b := newBridge(t, s, ch.ID, "a")
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: b.ID, Seq: seq, SlackTS: ts1, RootTS: ts1 + "é"}); err != nil {
				return err
			}
			return tx.PutEnrollmentToken(ctx, ab2store.EnrollmentToken{ID: "tök1", TokenHash: "hash1", Identity: "pat", ExpiresAt: 10, UsesLeft: 1, CreatedAt: 1})
		})

		db := rawDB(t, e, rawDSN)
		for _, table := range sqlstore.MeasuredTables {
			cols, ok := countedColumns[table]
			if !ok {
				t.Errorf("%s has no entry in countedColumns", table)
				continue
			}
			sel := []string{"size_bytes"}
			for _, c := range cols {
				sel = append(sel, strings.TrimSuffix(c, "?"))
			}
			rows, err := db.Query("SELECT " + strings.Join(sel, ", ") + " FROM " + table)
			if err != nil {
				t.Fatalf("%s: %v", table, err)
			}
			n := 0
			for rows.Next() {
				var size int64
				vals := make([][]byte, len(cols))
				dest := []any{&size}
				for i := range vals {
					dest = append(dest, &vals[i])
				}
				if err := rows.Scan(dest...); err != nil {
					t.Fatalf("%s scan: %v", table, err)
				}
				want := int64(32)
				for _, v := range vals {
					want += int64(len(v))
				}
				if size != want {
					t.Errorf("%s: size_bytes = %d, want 32 + byte lengths = %d", table, size, want)
				}
				n++
			}
			if err := rows.Close(); err != nil || n == 0 {
				t.Errorf("%s: %d rows, close %v; the test must seed every measured table", table, n, err)
			}
		}

		// The multibyte message counts bytes, not characters.
		var size int64
		var sender, session, typ, subject, content, meta, kind string
		if err := db.QueryRow(fmt.Sprintf("SELECT size_bytes, sender, session_id, type, subject, content, coalesce(metadata, ''), sender_kind FROM messages WHERE seq = %d", seq)).
			Scan(&size, &sender, &session, &typ, &subject, &content, &meta, &kind); err != nil {
			t.Fatal(err)
		}
		var bytesSum, runeSum int64 = 32, 32
		for _, c := range []string{sender, session, typ, subject, content, meta, kind} {
			bytesSum += int64(len([]byte(c)))
			runeSum += int64(utf8.RuneCountInString(c))
		}
		if size != bytesSum || size == runeSum {
			t.Errorf("messages.size_bytes = %d; want %d (bytes), not %d (characters)", size, bytesSum, runeSum)
		}
	})
}

// cascadeClosure returns the tables reachable from roots by ON DELETE CASCADE
// foreign keys, read from the catalog.
func cascadeClosure(t *testing.T, e dbtest.Engine, db *sql.DB, roots ...string) map[string]bool {
	t.Helper()
	q := `SELECT m.name AS child, f."table" AS parent FROM sqlite_master m, pragma_foreign_key_list(m.name) f WHERE m.type = 'table' AND f.on_delete = 'CASCADE'`
	if e.Name == "postgres" {
		q = `SELECT cl.relname, pl.relname FROM pg_constraint c
			JOIN pg_class cl ON cl.oid = c.conrelid JOIN pg_class pl ON pl.oid = c.confrelid
			JOIN pg_namespace n ON n.oid = cl.relnamespace
			WHERE c.contype = 'f' AND c.confdeltype = 'c' AND n.nspname = current_schema()`
	}
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("cascade catalog: %v", err)
	}
	defer func() { _ = rows.Close() }()
	children := map[string][]string{}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			t.Fatal(err)
		}
		children[parent] = append(children[parent], child)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	var walk func(string)
	walk = func(table string) {
		if out[table] {
			return
		}
		out[table] = true
		for _, c := range children[table] {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out
}

func TestMeasuredTablesCoverCascades(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		_, _, rawDSN := openStore(t, e, sqlstore.Options{})
		db := rawDB(t, e, rawDSN)
		closure := cascadeClosure(t, e, db, "messages", "channels", "bridge_messages", "enrollment_tokens", "memory_access")
		measured := map[string]bool{}
		for _, m := range sqlstore.MeasuredTables {
			measured[m] = true
		}
		for table := range closure {
			if !measured[table] {
				t.Errorf("%s is deleted by an eviction delete or its cascade but is not in sqlstore.MeasuredTables", table)
			}
		}
		for table := range measured {
			if !closure[table] {
				t.Errorf("%s is in sqlstore.MeasuredTables but no eviction delete reaches it", table)
			}
			q := "SELECT count(*) FROM pragma_table_xinfo(?) WHERE name = 'size_bytes'"
			if e.Name == "postgres" {
				q = "SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'size_bytes'"
			}
			var n int
			if err := db.QueryRow(q, table).Scan(&n); err != nil || n != 1 {
				t.Errorf("%s: size_bytes columns = %d, %v; want 1", table, n, err)
			}
		}
	})
}

func TestSweepBytesIncludeCascades(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		db := rawDB(t, e, rawDSN)
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		ord := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "p", Content: "m", Tags: []string{"x", "yy"}})
		mem := memRow(t, s, ch.ID, 1, 1, fixedNow)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: mem, Model: "m", Error: "boom", FailedAt: 1}); err != nil {
				return err
			}
			if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: mem, Model: "m", Vector: []byte{1, 2}}); err != nil {
				return err
			}
			_, err := tx.TombstoneMessage(ctx, mem, 100)
			return err
		})
		raw := func(seq int64) int64 {
			c := fmt.Sprintf("seq = %d", seq)
			return sumOf(t, db, "messages", c) + sumOf(t, db, "message_tags", c) + sumOf(t, db, "embeddings", c) + sumOf(t, db, "embed_failures", c)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			got, err := tx.OrdinaryMessagesBefore(ctx, 1<<62, 10)
			want := []ab2store.SeqChannel{{Seq: ord, ChannelID: ch.ID, Bytes: raw(ord)}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("OrdinaryMessagesBefore = %v, %v; want %v", got, err, want)
			}
			if tags := sumOf(t, db, "message_tags", fmt.Sprintf("seq = %d", ord)); len(got) != 1 || got[0].Bytes <= sumOf(t, db, "messages", fmt.Sprintf("seq = %d", ord)) || tags == 0 {
				t.Errorf("Bytes %v does not include the tag rows (%d)", got, tags)
			}
			got, err = tx.ExpiredTombstones(ctx, 1000, 10)
			want = []ab2store.SeqChannel{{Seq: mem, ChannelID: ch.ID, Bytes: raw(mem)}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("ExpiredTombstones = %v, %v; want %v", got, err, want)
			}
			return nil
		})
	})
}

func TestOrphanedBridgeMessages(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		db := rawDB(t, e, rawDSN)
		mustScope(t, s, "job1", 1)
		ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
		b := newBridge(t, s, ch.ID, "a")
		gone := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "p", Content: "m"})
		kept := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "p", Content: "m"})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: b.ID, Seq: gone, SlackTS: ts1, RootTS: ts1}); err != nil {
				return err
			}
			if err := tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: b.ID, Seq: kept, SlackTS: ts2, RootTS: ts1}); err != nil {
				return err
			}
			return tx.DeleteMessages(ctx, []int64{gone})
		})
		want := []ab2store.BridgeMessageRef{{BridgeID: b.ID, Seq: gone, Bytes: sumOf(t, db, "bridge_messages", fmt.Sprintf("seq = %d", gone))}}
		var got []ab2store.BridgeMessageRef
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			got, err = tx.OrphanedBridgeMessages(ctx, 10)
			return err
		})
		if !reflect.DeepEqual(got, want) || want[0].Bytes == 0 {
			t.Fatalf("OrphanedBridgeMessages = %v; want %v", got, want)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.DeleteBridgeMessages(ctx, got) })
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if got, err := tx.OrphanedBridgeMessages(ctx, 10); err != nil || len(got) != 0 {
				t.Errorf("after delete: %v, %v; want none", got, err)
			}
			if _, err := tx.BridgeMessageBySeq(ctx, b.ID, kept); err != nil {
				t.Errorf("the mapped row went with the orphan: %v", err)
			}
			if _, err := tx.BridgeMessageBySeq(ctx, b.ID, gone); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("the orphan is still there: %v", err)
			}
			return nil
		})
		// A row whose message still exists is never deleted, even when named.
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.DeleteBridgeMessages(ctx, []ab2store.BridgeMessageRef{{BridgeID: b.ID, Seq: kept}})
		})
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.BridgeMessageBySeq(ctx, b.ID, kept); err != nil {
				t.Errorf("DeleteBridgeMessages deleted a row whose message exists: %v", err)
			}
			if _, err := tx.OrphanedBridgeMessages(ctx, 0); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("limit 0 = %v, want ErrInvalid", err)
			}
			return nil
		})
	})
}

func TestExpiredEnrollmentTokens(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		db := rawDB(t, e, rawDSN)
		const now = 1000
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			for _, tok := range []ab2store.EnrollmentToken{
				{ID: "past", TokenHash: "h1", ExpiresAt: now - 1, UsesLeft: 1, CreatedAt: 1},
				{ID: "now", TokenHash: "h2", ExpiresAt: now, UsesLeft: 1, CreatedAt: 1},
				{ID: "future", TokenHash: "h3", ExpiresAt: now + 1, UsesLeft: 1, CreatedAt: 1},
			} {
				if err := tx.PutEnrollmentToken(ctx, tok); err != nil {
					return err
				}
			}
			return nil
		})
		want := []ab2store.TokenRef{{ID: "past", Bytes: sumOf(t, db, "enrollment_tokens", "id = 'past'")}}
		var got []ab2store.TokenRef
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			got, err = tx.ExpiredEnrollmentTokens(ctx, now, 10)
			return err
		})
		if !reflect.DeepEqual(got, want) || want[0].Bytes == 0 {
			t.Fatalf("ExpiredEnrollmentTokens = %v; want %v", got, want)
		}
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.DeleteEnrollmentTokens(ctx, []string{"past"})
		})
		if n := rawInt(t, db, "SELECT count(*) FROM enrollment_tokens"); n != 2 {
			t.Errorf("tokens left = %d, want 2", n)
		}
	})
}

func TestIdleTaskChannels(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		db := rawDB(t, e, rawDSN)
		const T = 1000
		mustScope(t, s, "job1", 1)
		mustIdentity(t, s, "pat")
		task := func(name string, createdAt int64) ab2store.Channel {
			return channelAt(t, s, name, ab2store.KindTasks, createdAt)
		}
		arch := task("arch", 90)
		idle := task("idle", 100)
		oldmsg := task("oldmsg", 100)
		withMsg := task("withmsg", 100)
		tomb := task("tomb", 100)
		read := task("read", 100)
		live := task("live", 100)
		creating := task("creating", 100)
		ordinary := channelAt(t, s, "ordinary", ab2store.KindOrdinary, 100)
		memChan := channelAt(t, s, "memories", ab2store.KindMemory, 100)

		// oldmsg: activity at 500 only, with every kind of child row.
		mustAppend(t, s, ab2store.NewMessage{ChannelID: oldmsg.ID, Sender: "p", Content: "m", CreatedAt: 500, Tags: []string{"t1", "t2"}})
		mem := memRow(t, s, oldmsg.ID, 7, 1, 400)
		mustAppend(t, s, ab2store.NewMessage{ChannelID: withMsg.ID, Sender: "p", Content: "m", CreatedAt: T})
		old := mustAppend(t, s, ab2store.NewMessage{ChannelID: tomb.ID, Sender: "p", Content: "m", CreatedAt: 50})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.TombstoneMessage(ctx, old, T); err != nil {
				return err
			}
			if err := tx.TouchChannelRead(ctx, read.ID, T); err != nil {
				return err
			}
			if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: mem, Model: "m", Vector: []byte{1, 2, 3}}); err != nil {
				return err
			}
			if err := tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: mem, Model: "m2", Error: "boom", FailedAt: 1}); err != nil {
				return err
			}
			if err := tx.SetACL(ctx, oldmsg.ID, []ab2store.ACLEntry{{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead}}}); err != nil {
				return err
			}
			if err := tx.Subscribe(ctx, "pat", oldmsg.ID, 0, 1); err != nil {
				return err
			}
			if err := tx.OpenSession(ctx, ab2store.Session{ID: "s1", Identity: "pat", DisplayName: "d", ResumeTokenHash: "h", OpenedAt: 1, LeaseExpiresAt: 2}); err != nil {
				return err
			}
			return tx.PutCursor(ctx, ab2store.Cursor{SessionID: "s1", ChannelID: oldmsg.ID, CursorSeq: 1})
		})
		for _, c := range []struct {
			ch    ab2store.Channel
			state ab2store.BridgeState
		}{{live, ab2store.BridgeLive}, {creating, ab2store.BridgeCreating}, {arch, ab2store.BridgeArchived}, {ordinary, ab2store.BridgeCreating}, {memChan, ab2store.BridgeCreating}} {
			b := newBridge(t, s, c.ch.ID, c.ch.Name)
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
				switch c.state {
				case ab2store.BridgeLive:
					if err := tx.SetBridgeSlackChannel(ctx, b.ID, "G-"+c.ch.Name); err != nil {
						return err
					}
					return tx.SetBridgeLive(ctx, b.ID)
				case ab2store.BridgeArchived:
					return tx.SetBridgeArchived(ctx, b.ID, 10)
				}
				return nil
			})
		}

		names := func(chs []ab2store.Channel) []string {
			var out []string
			for _, c := range chs {
				out = append(out, c.Name)
			}
			return out
		}
		var got []ab2store.Channel
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			got, err = tx.IdleTaskChannels(ctx, T, 20)
			return err
		})
		if want := []string{"arch", "idle", "oldmsg"}; !slices.Equal(names(got), want) {
			t.Fatalf("IdleTaskChannels(%d) = %v; want %v (archived bridge listed; live, creating, message, tombstone and read at the cutoff, ordinary and memory channels not)", T, names(got), want)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			one, err := tx.IdleTaskChannels(ctx, T, 1)
			if err != nil || len(one) != 1 || one[0].Name != "arch" {
				t.Errorf("limit 1 = %v, %v; want arch", names(one), err)
			}
			if _, err := tx.IdleTaskChannels(ctx, T, 0); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("limit 0 = %v, want ErrInvalid", err)
			}
			return nil
		})

		// ChannelBytes: the channel and every row its delete cascades to.
		id := fmt.Sprintf("%d", oldmsg.ID)
		inMsgs := "seq IN (SELECT seq FROM messages WHERE channel_id = " + id + ")"
		wantBytes := sumOf(t, db, "channels", "id = "+id)
		for _, table := range []string{"channel_acl", "subscriptions", "session_cursors", "messages"} {
			wantBytes += sumOf(t, db, table, "channel_id = "+id)
		}
		for _, table := range []string{"message_tags", "embeddings", "embed_failures"} {
			wantBytes += sumOf(t, db, table, inMsgs)
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			n, err := tx.ChannelBytes(ctx, oldmsg.ID)
			if err != nil || n != wantBytes {
				t.Errorf("ChannelBytes = %d, %v; want %d", n, err, wantBytes)
			}
			if _, err := tx.ChannelBytes(ctx, 9999); !errors.Is(err, ab2store.ErrNotFound) {
				t.Errorf("ChannelBytes(unknown) = %v, want ErrNotFound", err)
			}
			return nil
		})

		// DeleteIdleTaskChannel re-checks the condition.
		mustAppend(t, s, ab2store.NewMessage{ChannelID: idle.ID, Sender: "p", Content: "m", CreatedAt: T})
		var didIdle, didOld, didUnknown, didLive bool
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			if didIdle, err = tx.DeleteIdleTaskChannel(ctx, idle.ID, T); err != nil {
				return err
			}
			if didLive, err = tx.DeleteIdleTaskChannel(ctx, live.ID, T); err != nil {
				return err
			}
			if didUnknown, err = tx.DeleteIdleTaskChannel(ctx, 9999, T); err != nil {
				return err
			}
			didOld, err = tx.DeleteIdleTaskChannel(ctx, oldmsg.ID, T)
			return err
		})
		if didIdle || didLive || didUnknown || !didOld {
			t.Errorf("DeleteIdleTaskChannel: message at the cutoff %v, live bridge %v, unknown %v, idle %v; want false, false, false, true", didIdle, didLive, didUnknown, didOld)
		}
		if n := rawInt(t, db, "SELECT count(*) FROM messages WHERE channel_id = "+id) + sumOf(t, db, "subscriptions", "channel_id = "+id); n != 0 {
			t.Errorf("deleted channel left %d child rows", n)
		}
	})
}

func TestIdleMemories(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		db := rawDB(t, e, rawDSN)
		const T = 1000
		mc := channelAt(t, s, "memories", ab2store.KindMemory, 100)
		oc := channelAt(t, s, "ordinary", ab2store.KindOrdinary, 100)
		jobs := channelAt(t, s, "jobs", ab2store.KindTasks, 100)

		// Memory 1: two revisions (the first tombstoned), tags, an embedding,
		// accessed at 300. Memory 2: accessed at the cutoff. Memory 3: a task
		// row. Memory 4: no access row. Memory 5: created at the cutoff.
		rev1 := memRow(t, s, mc.ID, 1, 1, 100)
		rev2 := memRow(t, s, oc.ID, 1, 2, 200, "a", "bb")
		memRow(t, s, mc.ID, 2, 1, 100)
		memRow(t, s, jobs.ID, 3, 1, 100)
		memRow(t, s, mc.ID, 4, 1, 100)
		memRow(t, s, mc.ID, 5, 1, T)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			if _, err := tx.TombstoneMessage(ctx, rev1, 150); err != nil {
				return err
			}
			if err := tx.PutEmbedding(ctx, ab2store.Embedding{Seq: rev2, Model: "m", Vector: []byte{1, 2, 3}}); err != nil {
				return err
			}
			if err := tx.TouchMemoryAccess(ctx, []int64{1}, 300); err != nil {
				return err
			}
			if err := tx.TouchMemoryAccess(ctx, []int64{2}, T); err != nil {
				return err
			}
			return tx.TouchMemoryAccess(ctx, []int64{3}, 1)
		})
		bytesOf := func(id int64) int64 {
			in := fmt.Sprintf("seq IN (SELECT seq FROM messages WHERE memory_id = %d)", id)
			return sumOf(t, db, "messages", fmt.Sprintf("memory_id = %d", id)) + sumOf(t, db, "message_tags", in) +
				sumOf(t, db, "embeddings", in) + sumOf(t, db, "embed_failures", in) + sumOf(t, db, "memory_access", fmt.Sprintf("memory_id = %d", id))
		}
		want := []ab2store.SizedMemory{{MemoryID: 4, Bytes: bytesOf(4)}, {MemoryID: 1, Bytes: bytesOf(1)}}
		var got []ab2store.SizedMemory
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			got, err = tx.IdleMemories(ctx, T, 10)
			return err
		})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("IdleMemories = %v; want %v (memory 4 first: activity 100 against 300; 2 accessed at the cutoff, 3 on a task list and 5 created at the cutoff are not idle)", got, want)
		}
		if bytesOf(1) <= sumOf(t, db, "messages", "memory_id = 1")+sumOf(t, db, "memory_access", "memory_id = 1") {
			t.Errorf("test setup: memory 1 has no tag or embedding rows to count")
		}
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			one, err := tx.IdleMemories(ctx, T, 1)
			if err != nil || !reflect.DeepEqual(one, want[:1]) {
				t.Errorf("limit 1 = %v, %v; want %v", one, err, want[:1])
			}
			if _, err := tx.IdleMemories(ctx, T, 0); !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("limit 0 = %v, want ErrInvalid", err)
			}
			return nil
		})
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.PurgeMemory(ctx, 1) })
		if n := rawInt(t, db, "SELECT count(*) FROM messages WHERE memory_id = 1") + sumOf(t, db, "memory_access", "memory_id = 1") +
			sumOf(t, db, "message_tags", fmt.Sprintf("seq IN (%d, %d)", rev1, rev2)) + sumOf(t, db, "embeddings", fmt.Sprintf("seq = %d", rev2)); n != 0 {
			t.Errorf("PurgeMemory left rows (sum %d)", n)
		}
		if n := rawInt(t, db, "SELECT count(*) FROM messages WHERE memory_id <> 1"); n != 4 {
			t.Errorf("PurgeMemory touched other memories: %d rows left, want 4", n)
		}
	})
}

func TestEvictionLeft(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		cut := ab2store.EvictionCutoffs{TombstoneBefore: 1000, TokenExpiredBefore: 1000, IdleBefore: 1000, MessageBefore: 1000}
		left := func(s ab2store.Store) ab2store.EvictionLeft {
			t.Helper()
			var l ab2store.EvictionLeft
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				l, err = tx.EvictionLeft(ctx, cut)
				return err
			})
			return l
		}
		s, _, _ := openStore(t, e, sqlstore.Options{})
		if l := left(s); l != (ab2store.EvictionLeft{}) {
			t.Fatalf("empty bus: %+v, want all false", l)
		}
		for _, c := range []struct {
			name string
			add  func(t *testing.T, s ab2store.Store)
			want ab2store.EvictionLeft
		}{
			{"orphaned bridge message", func(t *testing.T, s ab2store.Store) {
				mustScope(t, s, "job1", 1)
				ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
				b := newBridge(t, s, ch.ID, "a")
				mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutBridgeMessage(ctx, ab2store.BridgeMessage{BridgeID: b.ID, Seq: 77, SlackTS: ts1, RootTS: ts1})
				})
			}, ab2store.EvictionLeft{Useless: true}},
			{"expired tombstone", func(t *testing.T, s ab2store.Store) {
				ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
				seq := mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "p", Content: "m"})
				mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
					_, err := tx.TombstoneMessage(ctx, seq, 500)
					return err
				})
			}, ab2store.EvictionLeft{Expired: true}},
			{"expired token", func(t *testing.T, s ab2store.Store) {
				mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
					return tx.PutEnrollmentToken(ctx, ab2store.EnrollmentToken{ID: "t", TokenHash: "h", ExpiresAt: 500, UsesLeft: 1, CreatedAt: 1})
				})
			}, ab2store.EvictionLeft{Expired: true}},
			{"idle task list", func(t *testing.T, s ab2store.Store) { channelAt(t, s, "jobs", ab2store.KindTasks, 100) }, ab2store.EvictionLeft{Idle: true}},
			{"idle memory", func(t *testing.T, s ab2store.Store) {
				ch := channelAt(t, s, "mem", ab2store.KindMemory, 5000)
				memRow(t, s, ch.ID, 1, 1, 100)
			}, ab2store.EvictionLeft{Idle: true}},
			{"old ordinary message", func(t *testing.T, s ab2store.Store) {
				ch := mustChannel(t, s, "a", ab2store.KindOrdinary, "", nil)
				mustAppend(t, s, ab2store.NewMessage{ChannelID: ch.ID, Sender: "p", Content: "m", CreatedAt: 100})
			}, ab2store.EvictionLeft{Ordinary: true}},
		} {
			t.Run(c.name, func(t *testing.T) {
				s, _, _ := openStore(t, e, sqlstore.Options{})
				c.add(t, s)
				if l := left(s); l != c.want {
					t.Errorf("EvictionLeft = %+v, want %+v", l, c.want)
				}
			})
		}
		l := ab2store.EvictionLeft{Useless: true}
		if !l.Early() || l.Late() || (ab2store.EvictionLeft{Ordinary: true}).Early() || !(ab2store.EvictionLeft{Idle: true}).Late() || !(ab2store.EvictionLeft{Expired: true}).Early() {
			t.Errorf("Early/Late wrong")
		}
	})
}

func TestTouchChannelRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		ch := mustChannel(t, s, "a", ab2store.KindTasks, "", nil)
		touch := func(id, at int64) {
			t.Helper()
			mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.TouchChannelRead(ctx, id, at) })
		}
		last := func() int64 {
			t.Helper()
			var c ab2store.Channel
			mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
				c, err = tx.ChannelByName(ctx, "a")
				return err
			})
			return c.LastReadAt
		}
		if n := last(); n != 0 {
			t.Fatalf("new channel LastReadAt = %d, want 0", n)
		}
		touch(ch.ID, 100)
		if n := last(); n != 100 {
			t.Errorf("after 100: %d", n)
		}
		touch(ch.ID, 50)
		if n := last(); n != 100 {
			t.Errorf("after 50: %d, want 100", n)
		}
		touch(9999, 5) // an unknown channel is not an error
	})
}
