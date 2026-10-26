package slackbridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlite"
)

// TestTrustDropsAnAgentWithWrite: the inbound path rejects an agent identity
// before trust runs (a Slack account only ever binds a human), so the
// not_human mapping of access.Post is reached only by calling trust directly.
// An agent that holds write on a live bridged channel is dropped with the
// fixed text "not a human identity".
func TestTrustDropsAnAgentWithWrite(t *testing.T) {
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ab.db"), sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := core.New(st, core.Defaults(), core.Options{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	var b store.Bridge
	if err := st.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		now := time.Now().UnixMilli()
		if err := tx.CreateScope(ctx, "job1", now); err != nil {
			return err
		}
		if err := tx.CreateIdentity(ctx, "bot", now); err != nil {
			return err
		}
		seq, err := tx.MaxSeq(ctx)
		if err != nil {
			return err
		}
		ch, err := tx.CreateChannel(ctx, store.ChannelCreate{
			Name: "job1-chat", Kind: store.KindOrdinary, Scope: "job1",
			ACL:        []store.ACLEntry{{Identity: "bot", Permissions: []store.Permission{store.PermRead, store.PermWrite}}},
			CreatedSeq: seq, CreatedAt: now,
		})
		if err != nil {
			return err
		}
		b, err = tx.CreateBridge(ctx, store.Bridge{ChannelID: ch.ID, ChannelName: ch.Name, Scope: "job1", SlackName: "job1-chat", CreatedAt: now})
		if err != nil {
			return err
		}
		if err := tx.SetBridgeSlackChannel(ctx, b.ID, "C1"); err != nil {
			return err
		}
		return tx.SetBridgeLive(ctx, b.ID)
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{core: c}
	err = st.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := s.trust(ctx, tx, b, "bot", store.SlackUser{Identity: "bot", TeamID: "T1", UserID: "U1"})
		return err
	})
	if err == nil || bus.CodeOf(err) != bus.CodeForbidden || msgOf(err) != reasonNotHuman {
		t.Fatalf("trust of an agent with write = %v, want forbidden %q", err, reasonNotHuman)
	}
}
