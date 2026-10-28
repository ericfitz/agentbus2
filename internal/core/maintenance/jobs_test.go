package maintenance

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/signingkeys"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

func jobNames(s Service) []string {
	var out []string
	for _, j := range s.Jobs() {
		out = append(out, j.Name)
	}
	return out
}

// open note: signing-keys 5
func TestSigningKeyJobsOnlyWhenWired(t *testing.T) {
	a, _, _ := twoCores(t)
	for _, n := range jobNames(a) {
		if n == "signing key rotation" || n == "signing key removal" {
			t.Fatalf("job %q present with SigningKeys nil", n)
		}
	}
	a.SigningKeys = &signingkeys.Service{Core: a.Core, Provider: keyprovider.StoreProvider{}, Settings: signingkeys.DefaultSettings()}
	names := jobNames(a)
	l := len(names)
	if names[l-1] != "assertion replay expiry" || names[l-2] != "signing key removal" || names[l-3] != "signing key rotation" {
		t.Fatalf("tail of jobs = %v", names[l-4:])
	}
}

func TestSigningKeyRotationRunJobConcurrent(t *testing.T) {
	clk := &testClock{t: time.UnixMilli(startMs)}
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentbus2.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := core.New(st, core.Defaults(), core.Options{Now: clk.now})
	if err != nil {
		t.Fatal(err)
	}
	sk := &signingkeys.Service{Core: c, Provider: keyprovider.StoreProvider{}, Settings: signingkeys.Settings{Interval: 10 * time.Hour, Cooldown: time.Hour}}
	ctx := context.Background()
	if _, _, err := sk.EnsureActive(ctx); err != nil {
		t.Fatal(err)
	}
	m := New(c)
	m.SigningKeys = sk
	clk.advance(11 * time.Hour)

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := m.RunJob(ctx, "signing key rotation")
			if err != nil {
				t.Errorf("RunJob: %v", err)
			}
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("rotations = %d, want 1", total)
	}
	var rows []store.SigningKey
	if err := st.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var e error
		rows, e = tx.ListSigningKeys(ctx)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// The removal job deletes nothing yet and the second run of rotation is 0.
	if n, err := m.RunJob(ctx, "signing key removal"); n != 0 || err != nil {
		t.Fatalf("removal: %d, %v", n, err)
	}
	if n, err := m.RunJob(ctx, "signing key rotation"); n != 0 || err != nil {
		t.Fatalf("second rotation: %d, %v", n, err)
	}
}
