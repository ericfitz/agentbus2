package slackbridge

import (
	"testing"

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
