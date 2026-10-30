package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// Agent statuses (issue #10): the append-only per-identity history.

func statusRow(identity string, n int) ab2store.AgentStatus {
	return ab2store.AgentStatus{
		Identity: identity, Tag: "implementing", Text: fmt.Sprintf("step %d", n),
		SessionID: "sess-" + identity, DisplayName: "Display " + identity, SetAt: fixedNow + int64(n),
	}
}

func appendStatus(t *testing.T, s ab2store.Store, st ab2store.AgentStatus) int64 {
	t.Helper()
	var rev int64
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		rev, err = tx.AppendAgentStatus(ctx, st)
		return err
	})
	return rev
}

func statusHistory(t *testing.T, s ab2store.Store, identity string, limit int) []ab2store.AgentStatus {
	t.Helper()
	var out []ab2store.AgentStatus
	mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		out, err = tx.AgentStatusHistory(ctx, identity, limit)
		return err
	})
	return out
}

func statusRevs(rows []ab2store.AgentStatus) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Revision)
	}
	return out
}

func prune(t *testing.T, s ab2store.Store, identity string, keep int) int {
	t.Helper()
	var n int
	mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
		n, err = tx.PruneAgentStatuses(ctx, identity, keep)
		return err
	})
	return n
}

func TestAgentStatusAppendAndRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		for _, n := range []string{"pat", "kim", "alpha_1", "alpha-2"} {
			mustIdentity(t, s, n)
		}
		with := statusRow("pat", 1)
		with.Problem = "build is red"
		with.Tag = "blocked"
		for i, st := range []ab2store.AgentStatus{statusRow("pat", 0), with, statusRow("pat", 2)} {
			if got := appendStatus(t, s, st); got != int64(i+1) {
				t.Errorf("pat append %d = revision %d, want %d", i, got, i+1)
			}
		}
		if got := appendStatus(t, s, statusRow("kim", 0)); got != 1 {
			t.Errorf("kim revision = %d, want 1", got)
		}
		appendStatus(t, s, statusRow("alpha_1", 0))
		appendStatus(t, s, statusRow("alpha-2", 0))

		hist := statusHistory(t, s, "pat", 10)
		if !slices.Equal(statusRevs(hist), []int64{1, 2, 3}) {
			t.Fatalf("history revisions = %v, want [1 2 3]", statusRevs(hist))
		}
		want := statusRow("pat", 0)
		want.Revision = 1
		if hist[0] != want {
			t.Errorf("history[0] = %+v, want %+v", hist[0], want)
		}
		with.Revision = 2
		if hist[1] != with {
			t.Errorf("history[1] = %+v, want %+v", hist[1], with)
		}

		var latest []ab2store.AgentStatus
		mustRead(t, s, func(ctx context.Context, tx ab2store.Tx) (err error) {
			latest, err = tx.LatestAgentStatuses(ctx)
			return err
		})
		var names []string
		for _, l := range latest {
			names = append(names, l.Identity)
		}
		if !slices.Equal(names, []string{"alpha-2", "alpha_1", "kim", "pat"}) {
			t.Fatalf("latest identities = %v, want byte order alpha-2, alpha_1, kim, pat", names)
		}
		if latest[3].Revision != 3 || latest[3].Text != "step 2" {
			t.Errorf("latest pat = %+v, want revision 3 text %q", latest[3], "step 2")
		}
	})
}

func TestAgentStatusHistoryLimit(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		for i := 0; i < 5; i++ {
			appendStatus(t, s, statusRow("pat", i))
		}
		if got := statusRevs(statusHistory(t, s, "pat", 2)); !slices.Equal(got, []int64{4, 5}) {
			t.Errorf("limit 2 = %v, want [4 5]", got)
		}
		for _, limit := range []int{0, -1} {
			err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.AgentStatusHistory(ctx, "pat", limit)
				return err
			})
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("limit %d err = %v, want ErrInvalid", limit, err)
			}
		}
		got := statusHistory(t, s, "nobody", 5)
		if got == nil || len(got) != 0 {
			t.Errorf("unknown identity = %#v, want empty non-nil", got)
		}
	})
}

func TestAgentStatusPrune(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		mustIdentity(t, s, "kim")
		appendStatus(t, s, statusRow("kim", 0))
		for i := 0; i < 5; i++ {
			appendStatus(t, s, statusRow("pat", i))
		}
		steps := []struct {
			keep, deleted int
			left          []int64
		}{
			{5, 0, []int64{1, 2, 3, 4, 5}},
			{4, 1, []int64{2, 3, 4, 5}},
			{1, 3, []int64{5}},
			{1, 0, []int64{5}}, // a second run deletes nothing
		}
		for _, st := range steps {
			if n := prune(t, s, "pat", st.keep); n != st.deleted {
				t.Errorf("keep %d deleted %d, want %d", st.keep, n, st.deleted)
			}
			if got := statusRevs(statusHistory(t, s, "pat", 10)); !slices.Equal(got, st.left) {
				t.Errorf("after keep %d: %v, want %v", st.keep, got, st.left)
			}
		}
		if got := statusRevs(statusHistory(t, s, "kim", 10)); !slices.Equal(got, []int64{1}) {
			t.Errorf("kim touched by pat prune: %v", got)
		}
		err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.PruneAgentStatuses(ctx, "pat", 0)
			return err
		})
		if !errors.Is(err, ab2store.ErrInvalid) {
			t.Errorf("keep 0 err = %v, want ErrInvalid", err)
		}
		if got := appendStatus(t, s, statusRow("pat", 9)); got != 6 {
			t.Errorf("append after prune = revision %d, want 6", got)
		}
	})
}

func TestAgentStatusAppendRejects(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		appendErr := func(st ab2store.AgentStatus) error {
			return s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
				_, err := tx.AppendAgentStatus(ctx, st)
				return err
			})
		}
		if err := appendErr(statusRow("ghost", 0)); !errors.Is(err, ab2store.ErrNotFound) {
			t.Errorf("unknown identity err = %v, want ErrNotFound", err)
		}
		fields := map[string]func(*ab2store.AgentStatus){
			"text":         func(s *ab2store.AgentStatus) { s.Text = "a\x00b" },
			"problem":      func(s *ab2store.AgentStatus) { s.Problem = "bad \xff" },
			"tag":          func(s *ab2store.AgentStatus) { s.Tag = "a\x00" },
			"session id":   func(s *ab2store.AgentStatus) { s.SessionID = "\xc3" },
			"display name": func(s *ab2store.AgentStatus) { s.DisplayName = "x\x00" },
		}
		for name, mut := range fields {
			st := statusRow("pat", 0)
			mut(&st)
			err := appendErr(st)
			if !errors.Is(err, ab2store.ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			} else if !strings.Contains(err.Error(), name) {
				t.Errorf("%s: error %q does not name the field", name, err)
			}
		}
		if got := statusHistory(t, s, "pat", 10); len(got) != 0 {
			t.Errorf("rejected appends left rows: %+v", got)
		}
		err := s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.AppendAgentStatus(ctx, statusRow("pat", 0))
			return err
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Errorf("append in Read = %v, want ErrReadOnly", err)
		}
		err = s.Read(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.PruneAgentStatuses(ctx, "pat", 1)
			return err
		})
		if !errors.Is(err, ab2store.ErrReadOnly) {
			t.Errorf("prune in Read = %v, want ErrReadOnly", err)
		}
	})
}

func TestAgentStatusCascade(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		db := rawDB(t, e, rawDSN)
		count := func() int { return scalar[int](t, db, "SELECT count(*) FROM agent_statuses") }

		mustScope(t, s, "job1", 1)
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			return tx.CreateScopedIdentity(ctx, "job1-coder", "job1", 2)
		})
		mustIdentity(t, s, "pat")
		appendStatus(t, s, statusRow("job1-coder", 0))
		appendStatus(t, s, statusRow("job1-coder", 1))
		appendStatus(t, s, statusRow("pat", 0))
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.RetireIdentity(ctx, "job1-coder", 50)
			return err
		})
		if n := scalar[int](t, db, "SELECT count(*) FROM agent_statuses WHERE identity = 'job1-coder'"); n != 0 {
			t.Errorf("job1-coder rows after RetireIdentity = %d, want 0", n)
		}
		if n := count(); n != 1 {
			t.Errorf("rows after retire = %d, want pat's 1", n)
		}

		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.ResetData(ctx) })
		if n := count(); n != 0 {
			t.Errorf("rows after ResetData = %d, want 0", n)
		}
		requireCounts(t, db, 2, "identities") // pat and the reserved slack-bridge

		appendStatus(t, s, statusRow("pat", 0))
		mustTx(t, s, func(ctx context.Context, tx ab2store.Tx) error { return tx.ResetRegistry(ctx, "") })
		if n := count(); n != 0 {
			t.Errorf("rows after ResetRegistry = %d, want 0", n)
		}
	})
}

func TestAgentStatusConcurrentAppends(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, _ := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 10; i++ {
					err := s.Tx(context.Background(), func(ctx context.Context, tx ab2store.Tx) error {
						_, err := tx.AppendAgentStatus(ctx, statusRow("pat", i))
						return err
					})
					if err != nil {
						errs <- err
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("concurrent append: %v", err)
		}
		want := make([]int64, 20)
		for i := range want {
			want[i] = int64(i + 1)
		}
		if got := statusRevs(statusHistory(t, s, "pat", 100)); !slices.Equal(got, want) {
			t.Errorf("revisions = %v, want 1..20", got)
		}
	})
}
