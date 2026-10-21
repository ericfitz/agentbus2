package bus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

func historySeqs(ms []ab2bus.Message) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.Seq
	}
	return out
}

func TestHistoryNeedsRead(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		ch := h.channel("priv", ab2store.KindOrdinary, "pat", []ab2store.ACLEntry{{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}})
		h.append(ch, "pat", "", "secret")
		kim := h.as("kim")
		_, err := kim.History(ctx, ab2bus.HistoryInput{Channel: "priv"})
		codeOf(t, err, ab2bus.CodeForbidden)
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			return tx.SetACL(ctx, ch.ID, []ab2store.ACLEntry{{Identity: "kim", Permissions: []ab2store.Permission{ab2store.PermRead}}})
		})
		got, err := kim.History(ctx, ab2bus.HistoryInput{Channel: "priv"})
		if err != nil || len(got) != 1 {
			t.Fatalf("History after grant = %v, %v", got, err)
		}
		_, err = kim.History(ctx, ab2bus.HistoryInput{Channel: "nope"})
		codeOf(t, err, ab2bus.CodeNotFound)
		_, err = newBus(h, "ghost").History(ctx, ab2bus.HistoryInput{Channel: "priv"})
		codeOf(t, err, ab2bus.CodeNotRegistered)
	})
}

func TestHistoryAuthBeforeInputValidation(t *testing.T) {
	// Registration, the channel and the read permission are checked before the
	// count and tag patterns (v1 ran auth first): a caller who may not read
	// learns nothing from a validation error, and gets the auth code.
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.channel("priv", ab2store.KindOrdinary, "pat", []ab2store.ACLEntry{{Identity: "pat", Permissions: []ab2store.Permission{ab2store.PermRead, ab2store.PermWrite}}})
		h.as("kim")
		bad := []struct {
			name string
			in   ab2bus.HistoryInput
		}{
			{"negative count", ab2bus.HistoryInput{Channel: "priv", Count: -1}},
			{"bad tag pattern", ab2bus.HistoryInput{Channel: "priv", Tags: []string{"bad tag"}}},
		}
		for _, c := range bad {
			_, err := newBus(h, "ghost").History(ctx, c.in)
			if got := ab2bus.CodeOf(err); got != ab2bus.CodeNotRegistered {
				t.Errorf("unregistered, %s: code = %q (%v), want not_registered", c.name, got, err)
			}
			_, err = h.as("kim").History(ctx, c.in)
			if got := ab2bus.CodeOf(err); got != ab2bus.CodeForbidden {
				t.Errorf("forbidden, %s: code = %q (%v), want forbidden", c.name, got, err)
			}
			unknown := c.in
			unknown.Channel = "nope"
			_, err = h.as("pat").History(ctx, unknown)
			if got := ab2bus.CodeOf(err); got != ab2bus.CodeNotFound {
				t.Errorf("unknown channel, %s: code = %q (%v), want not_found", c.name, got, err)
			}
			_, err = h.as("pat").History(ctx, c.in)
			if got := ab2bus.CodeOf(err); got != ab2bus.CodeValidation {
				t.Errorf("authorized, %s: code = %q (%v), want validation", c.name, got, err)
			}
		}
	})
}

func TestHistoryValidationMessagesArePlain(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		_, err := h.as("pat").History(context.Background(), ab2bus.HistoryInput{Channel: "dev", Tags: []string{"bad tag"}})
		var be *ab2bus.Error
		if !errors.As(err, &be) || be.Code != ab2bus.CodeValidation {
			t.Fatalf("err = %v, want a validation error", err)
		}
		if strings.HasPrefix(be.Message, "{") || strings.Contains(be.Message, `\"code\"`) || !strings.Contains(be.Message, `"bad tag"`) {
			t.Errorf("message = %q, want plain text naming the pattern", be.Message)
		}
	})
}

func TestHistoryWindowsAndTags(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{cfg: sendCfgWith(func(c *core.Config) {
			c.ReceiveDefaultCount = 3
			c.ReceiveMaxCount = 5
		})})
		ctx := context.Background()
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		for i := 1; i <= 8; i++ {
			switch i {
			case 2, 4:
				h.append(ch, "pat", "", "m", "env:prod")
			case 6:
				h.append(ch, "pat", "", "m", "environment")
			default:
				h.append(ch, "pat", "", "m")
			}
		}
		b := h.as("pat")
		hist := func(in ab2bus.HistoryInput) []int64 {
			t.Helper()
			in.Channel = "dev"
			got, err := b.History(ctx, in)
			if err != nil {
				t.Fatalf("History(%+v): %v", in, err)
			}
			return historySeqs(got)
		}
		eq := func(name string, got []int64, want ...int64) {
			t.Helper()
			if !slices.Equal(got, want) {
				t.Errorf("%s = %v, want %v", name, got, want)
			}
		}
		eq("default count, oldest first", hist(ab2bus.HistoryInput{}), 1, 2, 3)
		eq("count 0 is the default", hist(ab2bus.HistoryInput{Count: 0}), 1, 2, 3)
		eq("count capped at max", hist(ab2bus.HistoryInput{Count: 100}), 1, 2, 3, 4, 5)
		eq("after", hist(ab2bus.HistoryInput{After: sendInt64p(5), Count: 5}), 6, 7, 8)
		eq("before only is the newest window, ascending", hist(ab2bus.HistoryInput{Before: sendInt64p(7), Count: 3}), 4, 5, 6)
		eq("before and after", hist(ab2bus.HistoryInput{After: sendInt64p(2), Before: sendInt64p(6), Count: 5}), 3, 4, 5)
		eq("before and after, limited from the after end", hist(ab2bus.HistoryInput{After: sendInt64p(2), Before: sendInt64p(8), Count: 2}), 3, 4)
		eq("pattern env*", hist(ab2bus.HistoryInput{Tags: []string{"env*"}, Count: 5}), 2, 4, 6)
		eq("pattern env:*", hist(ab2bus.HistoryInput{Tags: []string{"ENV:*"}, Count: 5}), 2, 4)
		eq("pattern with before", hist(ab2bus.HistoryInput{Tags: []string{"env*"}, Before: sendInt64p(6), Count: 5}), 2, 4)

		_, err := b.History(ctx, ab2bus.HistoryInput{Channel: "dev", Count: -1})
		codeOf(t, err, ab2bus.CodeValidation)
		_, err = b.History(ctx, ab2bus.HistoryInput{Channel: "dev", Tags: []string{"bad tag"}})
		codeOf(t, err, ab2bus.CodeValidation)

		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			_, err := tx.TombstoneMessage(ctx, 4, h.core.NowMs())
			return err
		})
		eq("tombstoned excluded", hist(ab2bus.HistoryInput{Tags: []string{"env*"}, Count: 5}), 2, 6)
	})
}

func TestHistoryTrims(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{cfg: sendCfgWith(func(c *core.Config) { c.ResultDefaultKiB = 1 })})
		ctx := context.Background()
		ch := h.channel("dev", ab2store.KindOrdinary, "pat", nil)
		big := strings.Repeat("x", 800)
		for range 4 {
			h.append(ch, "pat", "", big)
		}
		b := h.as("pat")
		got, err := b.History(ctx, ab2bus.HistoryInput{Channel: "dev"})
		if err != nil || len(got) == 0 || len(got) >= 4 {
			t.Fatalf("oldest-first = %v, %v; want trimmed to at least one and fewer than four", historySeqs(got), err)
		}
		if got[0].Seq != 1 {
			t.Errorf("trim kept %v, want the rows nearest the cursor from seq 1", historySeqs(got))
		}
		got, err = b.History(ctx, ab2bus.HistoryInput{Channel: "dev", Before: sendInt64p(5)})
		if err != nil || len(got) == 0 || len(got) >= 4 {
			t.Fatalf("newest-first = %v, %v", historySeqs(got), err)
		}
		if last := got[len(got)-1].Seq; last != 4 || !slices.IsSorted(historySeqs(got)) {
			t.Errorf("newest window = %v, want ascending and ending at 4", historySeqs(got))
		}
	})
}
