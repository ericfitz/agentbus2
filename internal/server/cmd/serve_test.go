package cmd

import (
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

func openSQLite(t *testing.T, path string) store.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), path, sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// startServe runs Serve on 127.0.0.1:0 and returns once it is listening. stop
// cancels it and returns Serve's error, waiting at most 10 s.
func startServe(t *testing.T, c Config) (addr string, stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ready := make(chan net.Addr, 1)
	c.Listen = "127.0.0.1:0"
	c.Ready = func(a net.Addr) { ready <- a }
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, c) }()
	select {
	case a := <-ready:
		addr = a.String()
	case err := <-errc:
		t.Fatalf("Serve returned before listening: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not become ready")
	}
	return addr, func() error {
		cancel()
		select {
		case err := <-errc:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return after cancel")
			return nil
		}
	}
}

func oneSecond() *core.Config {
	cfg := core.Defaults()
	cfg.MaintenanceIntervalSeconds = 1
	return &cfg
}

func channelNames(t *testing.T, s store.Store) []string {
	t.Helper()
	var names []string
	err := s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		chs, err := tx.ListChannels(ctx)
		for _, c := range chs {
			names = append(names, c.Name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestServeStartsGeneralAndMaintenance(t *testing.T) {
	s := openSQLite(t, filepath.Join(t.TempDir(), "ab2.db"))
	err := s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.PutReceipt(ctx, store.Receipt{Identity: "pat", Key: "k", Fingerprint: "f", Result: []byte("{}"), ExpiresAt: 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	_, stop := startServe(t, Config{Store: s, Cfg: oneSecond()})

	var general store.Channel
	err = s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var err error
		general, err = tx.ChannelByName(ctx, "general")
		return err
	})
	if err != nil {
		t.Fatalf("general: %v", err)
	}
	if general.Creator != "" {
		t.Fatalf("general creator %q, want none (Decision 92)", general.Creator)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		var found bool
		err := s.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
			var err error
			_, found, err = tx.Receipt(ctx, "pat", "k", 0)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the expired receipt survived 3 s of maintenance")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

type lease struct {
	owner     string
	expiresAt int64
}

func readLease(t *testing.T, db *sql.DB) lease {
	t.Helper()
	var l lease
	if err := db.QueryRow("SELECT owner, expires_at FROM leases WHERE name = 'maintenance'").Scan(&l.owner, &l.expiresAt); err != nil {
		t.Fatal(err)
	}
	return l
}

func schemaVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow("SELECT schema_version FROM schema_meta WHERE id = 1").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

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

// Definition of Done D: a second run and a start after a crash. Two clocks:
// the maintenance goroutine ticks on wall time while the lease row is judged
// on the injected Now.
func TestServeSecondRunAndCrashLeftovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ab2.db")
	clock := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}

	// Second run: both start, one general.
	for i := 0; i < 2; i++ {
		s := openSQLite(t, path)
		_, stop := startServe(t, Config{Store: s, Cfg: oneSecond(), Now: clock.Now})
		if err := stop(); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if got := channelNames(t, s); len(got) != 1 || got[0] != "general" {
			t.Fatalf("run %d: channels %v, want [general]", i+1, got)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	version := schemaVersion(t, db)

	// Crash case: A takes the lease, is canceled, B starts after the fake
	// clock passes twice the interval.
	sA := openSQLite(t, path)
	_, stopA := startServe(t, Config{Store: sA, Cfg: oneSecond(), Now: clock.Now})
	var a lease
	deadline := time.Now().Add(2500 * time.Millisecond)
	for a = readLease(t, db); a.owner == ""; a = readLease(t, db) {
		if time.Now().After(deadline) {
			t.Fatal("server A never took the lease")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := stopA(); err != nil {
		t.Fatal(err)
	}

	sB := openSQLite(t, path)
	_, stopB := startServe(t, Config{Store: sB, Cfg: oneSecond(), Now: clock.Now})
	clock.Advance(3 * time.Second) // past 2 * interval
	deadline = time.Now().Add(2500 * time.Millisecond)
	var b lease
	for b = readLease(t, db); b.owner == a.owner; b = readLease(t, db) {
		if time.Now().After(deadline) {
			t.Fatalf("server B never took over the lease from %q", a.owner)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if b.owner == "" || b.expiresAt <= a.expiresAt {
		t.Fatalf("lease after take-over %+v, was %+v", b, a)
	}
	if got := schemaVersion(t, db); got != version {
		t.Fatalf("schema version %d after the crash, was %d", got, version)
	}
	if err := stopB(); err != nil {
		t.Fatal(err)
	}
}

func TestServeTimeoutsAllowLongPoll(t *testing.T) {
	srv := httpServer(Config{}, nil)
	if srv.WriteTimeout != 0 || srv.ReadHeaderTimeout != 10*time.Second ||
		srv.ReadTimeout != 30*time.Second || srv.IdleTimeout != 120*time.Second {
		t.Fatalf("timeouts: write %v header %v read %v idle %v", srv.WriteTimeout, srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout)
	}
	if got := httpServer(Config{ReadTimeout: 2 * time.Second}, nil).ReadTimeout; got != 2*time.Second {
		t.Fatalf("ReadTimeout %v, want the configured 2s", got)
	}
}

func TestServeRequiresStore(t *testing.T) {
	if err := Serve(context.Background(), Config{Listen: "127.0.0.1:0"}); err == nil {
		t.Fatal("Serve with no Store succeeded")
	}
}
