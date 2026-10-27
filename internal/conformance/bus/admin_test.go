package bus

import (
	"context"
	"reflect"
	"testing"
	"time"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func TestAdminStatusNeedsAdmin(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		root := h.admin("root").(ab2bus.Admin)

		// A lapsed session first, then the clock moves past its lease.
		h.session("kim", "kim")
		h.clock.Advance(time.Duration(h.cfg.SessionLeaseSeconds+h.cfg.LeaseSkewSeconds+1) * time.Second)
		rootS := h.session("root", "root")
		patS := h.session("pat", "pat/review")
		closed := h.session("pat", "pat-2")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.CloseSession(ctx, closed.ID, h.core.NowMs()); err != nil {
				return err
			}
			return tx.SetNotice(ctx, ab2store.Notice{Kind: "capacity", Message: "disk nearly full", SetAt: 42})
		})
		h.channel("beta", ab2store.KindTasks, "pat", nil)
		h.channel("alpha", ab2store.KindOrdinary, "root", nil)

		_, err := h.as("kim").(ab2bus.Admin).Status(ctx)
		codeOf(t, err, ab2bus.CodeForbidden)

		st, err := root.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Sessions) != 2 {
			t.Fatalf("sessions = %+v, want the two live ones", st.Sessions)
		}
		byID := map[string]ab2bus.SessionInfo{}
		for _, s := range st.Sessions {
			byID[s.ID] = s
		}
		if s := byID[patS.ID]; s.Identity != "pat" || s.DisplayName != "pat/review" || s.OpenedAt != patS.OpenedAt {
			t.Errorf("pat session = %+v", s)
		}
		if _, ok := byID[rootS.ID]; !ok {
			t.Errorf("root session missing")
		}
		wantCh := []ab2bus.ChannelInfo{
			{Channel: ab2bus.Channel{Name: "alpha", Kind: ab2bus.KindOrdinary, Capabilities: []ab2bus.ChannelCapability{}}, Creator: "root", CreatedAt: h.core.NowMs()},
			{Channel: ab2bus.Channel{Name: "beta", Kind: ab2bus.KindTasks, Capabilities: []ab2bus.ChannelCapability{}}, Creator: "pat", CreatedAt: h.core.NowMs()},
		}
		if len(st.Channels) != 2 || !reflect.DeepEqual(st.Channels, wantCh) {
			t.Errorf("channels = %+v, want %+v", st.Channels, wantCh)
		}
		if len(st.Notices) != 1 || st.Notices[0] != (ab2bus.Notice{Kind: "capacity", Message: "disk nearly full", SetAt: 42}) {
			t.Errorf("notices = %+v", st.Notices)
		}
		if st.EmbeddingBacklog != 0 || st.EmbeddingRejected != 0 || st.EmbeddingError != "" {
			t.Errorf("embedding fields without an embedder: %+v", st)
		}
		// An unregistered caller is not_registered, not forbidden.
		_, err = newBus(h, "nobody").(ab2bus.Admin).Status(ctx)
		codeOf(t, err, ab2bus.CodeNotRegistered)
	})
}

func TestAdminStatusEmbeddingCounters(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		ctx := context.Background()
		h := newHarness(t, e, harnessOptions{embedder: true})
		root := h.admin("root").(ab2bus.Admin)
		mem := h.channel("mem", ab2store.KindMemory, "pat", nil)
		h.append(mem, "pat", "", "alpha")
		h.append(mem, "pat", "", "bravo")
		bad := h.append(mem, "pat", "", "charlie")
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			if err := tx.PutEmbedFailure(ctx, ab2store.EmbedFailure{Seq: bad, Model: "fake", Error: "too long", FailedAt: 7}); err != nil {
				return err
			}
			return tx.SetNotice(ctx, ab2store.Notice{Kind: "embedding", Message: "endpoint down", SetAt: 9})
		})
		st, err := root.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.EmbeddingBacklog != 2 || st.EmbeddingRejected != 1 || st.EmbeddingRejectedLast != "too long" ||
			st.EmbeddingError != "endpoint down" || st.EmbeddingErrorAt != 9 {
			t.Fatalf("status = %+v", st)
		}

		// Without an embedder every embedding field is zero, whatever the rows say.
		plain := newHarness(t, e, harnessOptions{})
		pmem := plain.channel("mem", ab2store.KindMemory, "pat", nil)
		plain.append(pmem, "pat", "", "alpha")
		plain.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SetNotice(ctx, ab2store.Notice{Kind: "embedding", Message: "stale", SetAt: 1})
		})
		st, err = plain.admin("root").(ab2bus.Admin).Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if st.EmbeddingBacklog != 0 || st.EmbeddingRejected != 0 || st.EmbeddingRejectedLast != "" || st.EmbeddingError != "" || st.EmbeddingErrorAt != 0 {
			t.Fatalf("status without an embedder = %+v, want zero embedding fields", st)
		}
	})
}

func TestAdminDeleteAndRenameAreTheChannelOperations(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("c", ab2store.KindOrdinary, "pat", nil)
		pat := h.as("pat").(ab2bus.Admin) // a channel admin, not a server admin
		kim := h.as("kim").(ab2bus.Admin)
		codeOf(t, kim.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "c", NewName: "d"}), ab2bus.CodeForbidden)
		codeOf(t, kim.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "c"}), ab2bus.CodeForbidden)
		if err := pat.RenameChannel(ctx, ab2bus.RenameChannelInput{Name: "c", NewName: "d"}); err != nil {
			t.Fatal(err)
		}
		if err := pat.DeleteChannel(ctx, ab2bus.DeleteChannelInput{Name: "d"}); err != nil {
			t.Fatal(err)
		}
		if chs, err := h.as("kim").ListChannels(ctx); err != nil || len(chs) != 0 {
			t.Fatalf("channels = %+v, %v", chs, err)
		}
	})
}
