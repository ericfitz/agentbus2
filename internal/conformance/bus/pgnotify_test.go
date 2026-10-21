package bus

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/notify/pgnotify"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/postgres"
)

// postgresEngine is the postgres entry of dbtest.Engines. The tests built on it
// cover the LISTEN connection, which the polling form has no analogue of; they
// fail, never skip, when Postgres is unreachable.
func postgresEngine(t *testing.T) dbtest.Engine {
	t.Helper()
	for _, e := range dbtest.Engines() {
		if e.Name == "postgres" {
			return e
		}
	}
	t.Fatal("dbtest.Engines has no postgres engine")
	return dbtest.Engine{}
}

// expectHint fails unless the next hint within d equals want.
func expectHint(t *testing.T, hints <-chan notify.Hint, d time.Duration, want notify.Hint) {
	t.Helper()
	select {
	case got := <-hints:
		if got != want {
			t.Fatalf("got hint %v, want %v", got, want)
		}
	case <-time.After(d):
		t.Fatalf("no hint %v within %v", want, d)
	}
}

// expectNoSeqHint fails when a hint with Seq > 0 arrives within d.
func expectNoSeqHint(t *testing.T, hints <-chan notify.Hint, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case got := <-hints:
			if got.Seq > 0 {
				t.Fatalf("unexpected hint %v", got)
			}
		case <-deadline:
			return
		}
	}
}

func TestPgnotifyHintOnAppend(t *testing.T) {
	h, n, _ := harnessOverDSN(t, postgresEngine(t))
	hints, cancel := n.Subscribe()
	defer cancel()
	drain(hints, 300*time.Millisecond)
	ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
	seq := h.sibling(harnessOptions{}).append(ch, "pat", "", "hello")
	expectHint(t, hints, time.Second, notify.Hint{ChannelID: ch.ID, Seq: seq})
}

// TestPgnotifyHintOnWatchRegistration: Gate 0 item 1 (Eric, 2026-10-21).
func TestPgnotifyHintOnWatchRegistration(t *testing.T) {
	h, n, _ := harnessOverDSN(t, postgresEngine(t))
	s := h.session("pat", "pat")
	hints, cancel := n.Subscribe()
	defer cancel()
	drain(hints, 300*time.Millisecond)
	sib := h.sibling(harnessOptions{})
	sib.tx(func(ctx context.Context, tx ab2store.Tx) error {
		return tx.RegisterWatch(ctx, s.ID, "w1", sib.core.LeaseUntil(sib.core.NowMs()))
	})
	got, ok := nextHint(hints, time.Second)
	if !ok || got != (notify.Hint{}) {
		t.Fatalf("got %v, %v; want the control hint {0 0}", got, ok)
	}
	expectNoSeqHint(t, hints, 300*time.Millisecond)
}

func TestPgnotifyNoHintFromAnotherSchema(t *testing.T) {
	e := postgresEngine(t)
	h1, n1, _ := harnessOverDSN(t, e)
	s2, reopen2, _ := openStoreDSN(t, e)
	h2 := &harness{t: t, e: e, reopen: reopen2}
	h2.build(s2, harnessOptions{})
	hints, cancel := n1.Subscribe()
	defer cancel()
	drain(hints, 300*time.Millisecond)
	ch2 := h2.channel("dev", ab2store.KindOrdinary, "pat", nil)
	h2.append(ch2, "pat", "", "other schema")
	if got, ok := nextHint(hints, 500*time.Millisecond); ok {
		t.Fatalf("hint %v crossed schemas", got)
	}
	ch1 := h1.channel("dev", ab2store.KindOrdinary, "pat", nil)
	seq := h1.append(ch1, "pat", "", "this schema")
	expectHint(t, hints, time.Second, notify.Hint{ChannelID: ch1.ID, Seq: seq})
}

func TestPgnotifyReconnects(t *testing.T) {
	e := postgresEngine(t)
	h, _, dsn := harnessOverDSN(t, e)
	name := "agentbus2-listen-test-" + randHex8(t)
	l := pgnotify.New(t.Context(), dsn, pgnotify.Options{MinBackoff: 50 * time.Millisecond, ApplicationName: name})
	t.Cleanup(l.Close)
	waitConnected(t, l, 3*time.Second)
	hints, cancel := l.Subscribe()
	defer cancel()
	drain(hints, 300*time.Millisecond)

	db := e.Open(t)
	if _, err := db.ExecContext(context.Background(),
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1 AND datname = current_database() AND pid <> pg_backend_pid()", name); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	waitFor(t, time.Second, "listener disconnected", func() bool { return !l.Connected() })
	ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
	seq := h.sibling(harnessOptions{}).append(ch, "pat", "", "sent during the outage")
	waitConnected(t, l, 3*time.Second)
	// The catch-up hint wakes an armed watch to find the message.
	got, ok := nextHint(hints, 3*time.Second)
	if !ok || got != (notify.Hint{}) {
		t.Fatalf("got %v, %v; want the catch-up Hint{}", got, ok)
	}
	seq2 := h.sibling(harnessOptions{}).append(ch, "pat", "", "after")
	if seq2 <= seq {
		t.Fatalf("seq %d not after %d", seq2, seq)
	}
	// The outage append may commit after the new session's LISTEN (the
	// backoff is 50 ms), and then its own hint follows the catch-up one; it
	// is the only hint allowed before seq2's.
	deadline := time.After(time.Second)
	for {
		select {
		case got := <-hints:
			switch got {
			case notify.Hint{ChannelID: ch.ID, Seq: seq2}:
				return
			case notify.Hint{ChannelID: ch.ID, Seq: seq}, notify.Hint{}:
				continue
			}
			t.Fatalf("got hint %v, want %v", got, notify.Hint{ChannelID: ch.ID, Seq: seq2})
		case <-deadline:
			t.Fatalf("no hint %v within 1s", notify.Hint{ChannelID: ch.ID, Seq: seq2})
		}
	}
}

func TestPgnotifyChannelMatchesDialect(t *testing.T) {
	e := postgresEngine(t)
	h, n, dsn := harnessOverDSN(t, e)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var want string
	if err := db.QueryRowContext(context.Background(), postgres.ChannelQuery).Scan(&want); err != nil {
		t.Fatal(err)
	}
	l := n.(*pgnotify.Listener)
	if got := l.Channel(); got != want {
		t.Fatalf("listener channel %q, dialect channel %q", got, want)
	}
	hints, cancel := n.Subscribe()
	defer cancel()
	drain(hints, 300*time.Millisecond)
	ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
	seq := h.sibling(harnessOptions{}).append(ch, "pat", "", "x")
	expectHint(t, hints, time.Second, notify.Hint{ChannelID: ch.ID, Seq: seq})
}

func TestPgnotifyRolledBackAppendDoesNotNotify(t *testing.T) {
	h, n, _ := harnessOverDSN(t, postgresEngine(t))
	ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
	hints, cancel := n.Subscribe()
	defer cancel()
	drain(hints, 300*time.Millisecond)
	boom := errors.New("boom")
	err := h.raw.Append(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
		if _, err := tx.AppendMessage(ctx, ab2store.NewMessage{ChannelID: ch.ID, Sender: "pat", CreatedAt: h.core.NowMs(), Content: "x"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Append err = %v, want boom", err)
	}
	expectNoSeqHint(t, hints, 500*time.Millisecond)
	// The same append committed does notify, so the silent window above was
	// not for want of a working listener.
	seq := h.append(ch, "pat", "", "committed")
	expectHint(t, hints, time.Second, notify.Hint{ChannelID: ch.ID, Seq: seq})
}
