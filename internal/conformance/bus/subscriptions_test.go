package bus

import (
	"context"
	"reflect"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

// ackedOf is the identity's acked mark on the channel, -1 when not subscribed.
func ackedOf(h *harness, identity string, channelID int64) int64 {
	h.t.Helper()
	acked := int64(-1)
	h.tx(func(ctx context.Context, tx ab2store.Tx) error {
		subs, err := tx.Subscriptions(ctx, identity)
		if err != nil {
			return err
		}
		for _, s := range subs {
			if s.ChannelID == channelID {
				acked = s.AckedSeq
			}
		}
		return nil
	})
	return acked
}

func TestSubscriptionNeedsRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.ensureIdentity("pat")
		ch := h.channel("c", ab2store.KindOrdinary, "kim", []ab2store.ACLEntry{
			{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermWrite}},
			{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermAdmin}},
		})
		pat := h.as("pat")
		codeOf(t, pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c"}), ab2bus.CodeForbidden)
		if got := ackedOf(h, "pat", ch.ID); got != -1 {
			t.Fatalf("a refused subscribe left a row (acked %d)", got)
		}
		if err := h.as("kim").SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: []ab2bus.ACLEntry{
			{Identity: "pat", Permissions: []ab2bus.Permission{ab2bus.PermWrite, ab2bus.PermRead}},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c"}); err != nil {
			t.Fatalf("after grant: %v", err)
		}
		codeOf(t, pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "missing"}), ab2bus.CodeNotFound)
	})
}

func TestSubscriptionFromNowAndOldest(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("c", ab2store.KindOrdinary, "pat", nil)
		for i := 0; i < 5; i++ {
			h.append(ch, "pat", "", "m")
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error { return tx.SetEvictedBefore(ctx, ch.ID, 3) })

		now, oldest, def := h.as("now"), h.as("oldest"), h.as("def")
		if err := now.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c", From: "now"}); err != nil {
			t.Fatal(err)
		}
		if err := oldest.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c", From: "oldest"}); err != nil {
			t.Fatal(err)
		}
		if err := def.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c"}); err != nil {
			t.Fatal(err)
		}
		if got := ackedOf(h, "now", ch.ID); got != 5 {
			t.Errorf("now: acked = %d, want MaxSeq 5", got)
		}
		if got := ackedOf(h, "def", ch.ID); got != 5 {
			t.Errorf("default: acked = %d, want MaxSeq 5", got)
		}
		if got := ackedOf(h, "oldest", ch.ID); got != 2 {
			t.Errorf("oldest: acked = %d, want evicted-1 = 2", got)
		}
		// An existing subscription keeps its position.
		if err := oldest.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c", From: "now"}); err != nil {
			t.Fatal(err)
		}
		if got := ackedOf(h, "oldest", ch.ID); got != 2 {
			t.Errorf("resubscribe moved the mark to %d", got)
		}
		// Nothing evicted: oldest is 0, never negative.
		empty := h.channel("e", ab2store.KindOrdinary, "pat", nil)
		if err := h.as("zero").Subscribe(ctx, ab2bus.SubscribeInput{Channel: "e", From: "oldest"}); err != nil {
			t.Fatal(err)
		}
		if got := ackedOf(h, "zero", empty.ID); got != 0 {
			t.Errorf("oldest on an unevicted channel: acked = %d, want 0", got)
		}
		for _, bad := range []string{"later", "NOW", "0"} {
			codeOf(t, now.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c", From: bad}), ab2bus.CodeValidation)
		}
	})
}

func TestSubscriptionUnsubscribeNeedsNothing(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("c", ab2store.KindOrdinary, "kim", nil)
		pat := h.as("pat")
		if err := pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c"}); err != nil {
			t.Fatal(err)
		}
		// Decision 95: revoke read, unsubscribe still works.
		if err := h.as("kim").SetACL(ctx, ab2bus.SetACLInput{Channel: "c", Entries: []ab2bus.ACLEntry{}}); err != nil {
			t.Fatal(err)
		}
		codeOf(t, pat.Subscribe(ctx, ab2bus.SubscribeInput{Channel: "c"}), ab2bus.CodeForbidden)
		if err := pat.Unsubscribe(ctx, ab2bus.UnsubscribeInput{Channel: "c"}); err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
		if got := ackedOf(h, "pat", ch.ID); got != -1 {
			t.Fatalf("still subscribed (acked %d)", got)
		}
		if err := pat.Unsubscribe(ctx, ab2bus.UnsubscribeInput{Channel: "c"}); err != nil {
			t.Fatalf("second Unsubscribe: %v", err)
		}
		codeOf(t, pat.Unsubscribe(ctx, ab2bus.UnsubscribeInput{Channel: "missing"}), ab2bus.CodeNotFound)
	})
}

func TestSubscriptionTagsRoundTrip(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("c", ab2store.KindOrdinary, "pat", nil)
		h.append(ch, "pat", "", "m")
		h.append(ch, "pat", "", "m")
		pat := h.as("pat")

		if err := pat.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"Env:*", "beta", "beta"}}); err != nil {
			t.Fatal(err)
		}
		if err := pat.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"beta", "env:*"}}); err != nil {
			t.Fatalf("the same set again: %v", err)
		}
		if err := pat.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"urgent"}}); err != nil {
			t.Fatal(err)
		}
		got, err := pat.TagSubscriptions(ctx)
		want := [][]string{{"beta", "env:*"}, {"urgent"}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("TagSubscriptions = %v, %v; want %v", got, err, want)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			subs, err := tx.TagSubscriptions(ctx, "pat")
			if err != nil || len(subs) != 2 {
				t.Fatalf("store subs = %+v, %v", subs, err)
			}
			for _, s := range subs {
				if s.CreatedSeq != 2 {
					t.Errorf("%s: CreatedSeq = %d, want MaxSeq 2", s.TagsKey, s.CreatedSeq)
				}
			}
			return nil
		})

		eleven := make([]string, 11)
		for i := range eleven {
			eleven[i] = "t" + strings.Repeat("a", i)
		}
		codeOf(t, pat.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: eleven}), ab2bus.CodeValidation)
		codeOf(t, pat.SubscribeTags(ctx, ab2bus.TagSubscribeInput{}), ab2bus.CodeValidation)
		codeOf(t, pat.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"has space"}}), ab2bus.CodeValidation)
		codeOf(t, pat.UnsubscribeTags(ctx, ab2bus.TagSubscribeInput{}), ab2bus.CodeValidation)
		// The set travels as one path segment on the wire, so an empty pattern
		// alone is the empty segment (a router 404 unless the remote Bus
		// answers for the core); beside another pattern it is a bad pattern.
		codeOf(t, pat.UnsubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{""}}), ab2bus.CodeValidation)
		codeOf(t, pat.UnsubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"beta", ""}}), ab2bus.CodeValidation)
		codeOf(t, pat.SubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{""}}), ab2bus.CodeValidation)

		if err := pat.UnsubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"ENV:*", "beta"}}); err != nil {
			t.Fatal(err)
		}
		if err := pat.UnsubscribeTags(ctx, ab2bus.TagSubscribeInput{Patterns: []string{"never-added"}}); err != nil {
			t.Fatalf("removing an absent set: %v", err)
		}
		got, err = pat.TagSubscriptions(ctx)
		if err != nil || !reflect.DeepEqual(got, [][]string{{"urgent"}}) {
			t.Fatalf("after unsubscribe = %v, %v", got, err)
		}
		// Another identity's sets are its own.
		if got, err := h.as("kim").TagSubscriptions(ctx); err != nil || len(got) != 0 {
			t.Fatalf("kim = %v, %v", got, err)
		}
	})
}
