package slackbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

func TestPostedMap(t *testing.T) {
	s := &Service{}
	s.setPosted(postedKey{1, 10}, "t1")
	s.setPosted(postedKey{1, 11}, "t2")
	s.setPosted(postedKey{2, 10}, "t3")
	if ts, ok := s.getPosted(postedKey{1, 10}); !ok || ts != "t1" {
		t.Fatalf("got %q, %v", ts, ok)
	}
	if _, ok := s.getPosted(postedKey{1, 10}); !ok {
		t.Fatal("getPosted removed the entry")
	}
	s.clearBridgePosted(1)
	if _, ok := s.getPosted(postedKey{1, 10}); ok {
		t.Fatal("bridge 1 entry survived clearBridgePosted(1)")
	}
	if _, ok := s.getPosted(postedKey{1, 11}); ok {
		t.Fatal("bridge 1 entry survived clearBridgePosted(1)")
	}
	if _, ok := s.getPosted(postedKey{2, 10}); !ok {
		t.Fatal("another bridge's entry was cleared")
	}
	s.clearBridgePosted(0)
	if _, ok := s.getPosted(postedKey{2, 10}); ok {
		t.Fatal("clearBridgePosted(0) left an entry")
	}
	s.clearBridgePosted(0) // a nil-safe second run
}

func TestOtherTeam(t *testing.T) {
	self := slackapi.Self{TeamID: "T1"}
	for _, tc := range []struct {
		u, s string
		want bool
	}{{"T1", "T1", false}, {"T9", "T1", true}, {"", "T1", true}, {"T1", "", true}, {"", "", true}} {
		if got := otherTeam(slackapi.User{TeamID: tc.u}, slackapi.Self{TeamID: tc.s}); got != tc.want {
			t.Errorf("otherTeam(%q, %q) = %v, want %v", tc.u, tc.s, got, tc.want)
		}
	}
	// The inbound check agrees: an empty team id is dropped.
	s := &Service{}
	if got := s.refusal(slackapi.User{Email: "a@b.example"}, self); got != "user of another workspace" {
		t.Errorf("refusal(no team) = %q", got)
	}
}

func TestDropRecorded(t *testing.T) {
	s := &Service{}
	s.dropRecorded(1, 5) // nil-safe
	at := time.Unix(1700000000, 0)
	for _, k := range []postedKey{{1, 4}, {1, 5}, {1, 6}, {2, 4}} {
		s.setPosted(k, "t")
		s.setUncertain(k, at)
	}
	s.dropRecorded(1, 5)
	for k, want := range map[postedKey]bool{{1, 4}: false, {1, 5}: false, {1, 6}: true, {2, 4}: true} {
		if _, ok := s.getPosted(k); ok != want {
			t.Errorf("posted %v present = %v, want %v", k, ok, want)
		}
		if _, ok := s.getUncertain(k); ok != want {
			t.Errorf("uncertain %v present = %v, want %v", k, ok, want)
		}
	}
	s.setUncertain(postedKey{1, 6}, at.Add(time.Hour)) // the first attempt time stays
	if got, _ := s.getUncertain(postedKey{1, 6}); !got.Equal(at) {
		t.Errorf("uncertain time = %v, want the first attempt %v", got, at)
	}
	s.clearBridgePosted(1)
	if n := s.PendingPosts(); n != 2 {
		t.Errorf("PendingPosts after clearBridgePosted(1) = %d, want 2 (bridge 2)", n)
	}
	s.clearBridgePosted(0)
	if n := s.PendingPosts(); n != 0 {
		t.Errorf("PendingPosts = %d", n)
	}
}

func TestPostUncertain(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("read tcp: connection reset by peer"), true},
		{fmt.Errorf("slack: chat.postMessage: %w", errors.New("EOF")), true},
		{context.DeadlineExceeded, true},
		{context.Canceled, true},
		{&slackapi.APIError{Method: "chat.postMessage", Code: "http_502"}, true},
		{&slackapi.APIError{Method: "chat.postMessage", Code: "http_504"}, true},
		{&slackapi.APIError{Method: "chat.postMessage", Code: "http_408"}, true},
		{&slackapi.APIError{Method: "chat.postMessage", Code: "http_403"}, false},
		{&slackapi.APIError{Method: "chat.postMessage", Code: "invalid_blocks"}, false},
		{&slackapi.APIError{Method: "chat.postMessage", Code: "internal_error"}, true},
		{&slackapi.APIError{Method: "chat.postMessage", Code: "service_unavailable"}, true},
		{&slackapi.RateLimitedError{Method: "chat.postMessage"}, false},
		{fmt.Errorf("wrapped: %w", &slackapi.RateLimitedError{Method: "chat.postMessage"}), false},
		{slackapi.ErrChannelGone, false},
	} {
		if got := postUncertain(tc.err); got != tc.want {
			t.Errorf("postUncertain(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRetiredBridgeRemembersNothing(t *testing.T) {
	s := &Service{}
	s.setPosted(postedKey{1, 1}, "t")
	s.clearBridgePosted(1)
	// A drain still in flight finishes its post after the archive.
	s.setPosted(postedKey{1, 2}, "t")
	s.setUncertain(postedKey{1, 3}, time.Now())
	if n := s.PendingPosts(); n != 0 {
		t.Errorf("PendingPosts = %d for an archived bridge", n)
	}
	s.setPosted(postedKey{2, 1}, "t")
	if n := s.PendingPosts(); n != 1 {
		t.Errorf("another bridge's post was refused: %d", n)
	}
}
