package slackbridge

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestConfigWithDefaults(t *testing.T) {
	ok := Config{ArchiveAfterDone: time.Hour, MaxMessageChars: 3000}
	got, err := ok.withDefaults()
	if err != nil || got.PostInterval != time.Second || got.LockRenew != 10*time.Second || got.LifecycleInterval != time.Minute || got.Logger == nil {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	for name, c := range map[string]Config{
		"no archive wait": {MaxMessageChars: 3000},
		"chars 199":       {ArchiveAfterDone: time.Hour, MaxMessageChars: 199},
		"chars 3001":      {ArchiveAfterDone: time.Hour, MaxMessageChars: 3001},
		"negative post":   {ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, PostInterval: -1},
		"negative renew":  {ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, LockRenew: -1},
		"negative life":   {ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, LifecycleInterval: -1},
		"negative retry":  {ArchiveAfterDone: time.Hour, MaxMessageChars: 3000, RetryBase: -1},
	} {
		if _, err := c.withDefaults(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, n := range []int{200, 3000} {
		if _, err := (Config{ArchiveAfterDone: time.Hour, MaxMessageChars: n}).withDefaults(); err != nil {
			t.Errorf("chars %d: %v", n, err)
		}
	}
}

func TestWithBridgeRead(t *testing.T) {
	R, W := store.PermRead, store.PermWrite
	in := []store.ACLEntry{{Identity: "a", Permissions: []store.Permission{R, W}}}
	got := withBridgeRead(in)
	if len(got) != 2 || got[1].Identity != store.SlackBridge || !slices.Equal(got[1].Permissions, []store.Permission{R}) {
		t.Errorf("added = %+v", got)
	}
	if len(in) != 1 {
		t.Error("input modified")
	}
	// Already there: unchanged. Present without read: read is added.
	again := withBridgeRead(got)
	if len(again) != 2 || !slices.Equal(again[1].Permissions, []store.Permission{R}) {
		t.Errorf("idempotent = %+v", again)
	}
	odd := withBridgeRead([]store.ACLEntry{{Identity: store.SlackBridge, Permissions: []store.Permission{W}}})
	if len(odd) != 1 || !slices.Contains(odd[0].Permissions, R) {
		t.Errorf("without read = %+v", odd)
	}
}

func TestSlackErr(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code bus.Code
		sub  string
	}{
		{&slackapi.RateLimitedError{Method: "Invite", RetryAfter: 3 * time.Second}, bus.CodeRateLimited, "3s"},
		{&slackapi.APIError{Method: "Invite", Code: "missing_scope"}, bus.CodeUnavailable, "missing_scope"},
		{slackapi.ErrChannelGone, bus.CodeUnavailable, "gone"},
		{errors.New("dial tcp 10.0.0.1: connection refused"), bus.CodeUnavailable, "could not be reached"},
	} {
		got := slackErr(tc.err)
		if bus.CodeOf(got) != tc.code || !strings.Contains(got.Error(), tc.sub) {
			t.Errorf("%v -> %v", tc.err, got)
		}
		var be *bus.Error
		if !errors.As(got, &be) || !be.Retryable {
			t.Errorf("%v: not retryable", tc.err)
		}
	}
	if got := slackErr(nil); got != nil {
		t.Errorf("nil -> %v", got)
	}
	if got := slackErr(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("canceled -> %v", got)
	}
	// The transport error text (which may carry hosts) is not passed on.
	if got := slackErr(errors.New("dial tcp 10.0.0.1: connection refused")); strings.Contains(got.Error(), "10.0.0.1") {
		t.Errorf("transport text leaked: %v", got)
	}
}

func TestToBus(t *testing.T) {
	b := toBus(store.Bridge{ChannelName: "c", Scope: "s", SlackChannelID: "C1", SlackName: "n", State: store.BridgeLive, CreatedAt: 5, ArchivedAt: 9})
	want := bus.Bridge{Channel: "c", Scope: "s", SlackChannelID: "C1", SlackName: "n", State: bus.BridgeLive, CreatedAt: 5, ArchivedAt: 9}
	if b != want {
		t.Errorf("toBus = %+v", b)
	}
}
