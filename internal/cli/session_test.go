package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestSessionOpenClose(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	run := func(args ...string) (int, string, string) {
		var o, er bytes.Buffer
		code := RunSession(ctx, args, e.Environ, strings.NewReader(""), &o, &er)
		return code, o.String(), er.String()
	}
	type opened struct {
		Session         string `json:"session"`
		DisplayName     string `json:"display_name"`
		ResumeToken     string `json:"resume_token"`
		Resumed         bool   `json:"resumed"`
		Pending         []struct{ Channel string }
		RejectedCursors []struct{ Channel, Reason string } `json:"rejected_cursors"`
	}

	code, out, er := run("open")
	if code != 0 {
		t.Fatalf("open: %d %s", code, er)
	}
	var s opened
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("open output %q: %v", out, err)
	}
	if s.Session == "" || s.DisplayName == "" || len(s.ResumeToken) != 43 || s.Resumed {
		t.Fatalf("open: %+v", s)
	}
	for _, k := range []string{`"pending"`, `"rejected_cursors"`} {
		if !strings.Contains(out, k) {
			t.Errorf("open output lacks %s: %s", k, out)
		}
	}

	if code, _, er := run("close", s.Session); code != 0 {
		t.Fatalf("close: %d %s", code, er)
	}
	if code, _, er := run("close", s.Session); code != 0 {
		t.Fatalf("second close (idempotent): %d %s", code, er)
	}

	// Resuming a closed session by its token is the core's call; a hint with a
	// slash is rejected there.
	if code, _, er := run("open", "-hint", "a/b"); code != 1 || !strings.Contains(er, "validation") {
		t.Fatalf("bad hint: %d %q", code, er)
	}

	// An offered cursor ahead of the channel is rejected with a reason, once
	// the identity is subscribed.
	pat := e.conn(t, "pat")
	if err := pat.Bus.Subscribe(ctx, bus.SubscribeInput{Channel: "general"}); err != nil {
		t.Fatal(err)
	}
	code, out, er = run("open", "-cursor", "general=999999")
	if code != 0 {
		t.Fatalf("cursor open: %d %s", code, er)
	}
	var c opened
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.RejectedCursors) != 1 || c.RejectedCursors[0].Channel != "general" || c.RejectedCursors[0].Reason != "ahead" {
		t.Fatalf("rejected cursors: %+v", c.RejectedCursors)
	}

	// Resume by token.
	code, out, er = run("open", "-resume-token", c.ResumeToken)
	if code != 0 {
		t.Fatalf("resume: %d %s", code, er)
	}
	var r opened
	if err := json.Unmarshal([]byte(out), &r); err != nil || !r.Resumed || r.Session != c.Session {
		t.Fatalf("resume: %+v %v", r, err)
	}
}

func TestSessionGrammarExits2(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"open", "-cursor", "general"},
		{"open", "-cursor", "general=x"},
		{"open", "-cursor", "=5"},
		{"open", "-filter", "x"},
		{"open", "extra"},
		{"close"},
		{"close", "a", "b"},
		{"close", "-nope", "a"},
	} {
		var o, er bytes.Buffer
		if code := RunSession(ctx, args, e.Environ, strings.NewReader(""), &o, &er); code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, er.String())
		}
	}
	for _, args := range [][]string{{"-h"}, {"open", "-h"}, {"close", "-h"}} {
		var o, er bytes.Buffer
		if code := RunSession(ctx, args, e.Environ, strings.NewReader(""), &o, &er); code != 0 {
			t.Errorf("%v: exit %d, want 0", args, code)
		}
	}
}
