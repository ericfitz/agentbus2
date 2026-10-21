package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestSendReceiveAck(t *testing.T) {
	type opCall = func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) int
	e := newTestEnv(t)
	ctx := context.Background()
	call := func(f opCall, stdin string, args ...string) (int, string, string) {
		var o, er bytes.Buffer
		code := f(ctx, args, e.Environ, strings.NewReader(stdin), &o, &er)
		return code, o.String(), er.String()
	}
	type msg struct {
		Seq     int64    `json:"seq"`
		Channel string   `json:"channel"`
		Subject string   `json:"subject"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
	}
	type recv struct {
		Messages    []msg  `json:"messages"`
		Batch       string `json:"batch"`
		Redelivered bool   `json:"redelivered"`
		AckIgnored  bool   `json:"ack_ignored"`
	}
	receive := func(args ...string) recv {
		t.Helper()
		code, out, er := call(RunReceive, "", args...)
		if code != 0 {
			t.Fatalf("receive %v: %d %s", args, code, er)
		}
		var r recv
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("receive output %q: %v", out, err)
		}
		return r
	}
	send := func(args ...string) int64 {
		t.Helper()
		code, out, er := call(RunSend, "", args...)
		if code != 0 {
			t.Fatalf("send %v: %d %s", args, code, er)
		}
		var r struct{ Seq int64 }
		if err := json.Unmarshal([]byte(out), &r); err != nil || r.Seq == 0 {
			t.Fatalf("send output %q: %v", out, err)
		}
		return r.Seq
	}

	sam := e.conn(t, "sam")
	pat := e.conn(t, "pat")
	if _, err := sam.Bus.CreateChannel(ctx, bus.CreateChannelInput{Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"general", "dev"} {
		if err := pat.Bus.Subscribe(ctx, bus.SubscribeInput{Channel: c}); err != nil {
			t.Fatal(err)
		}
	}

	s1 := send("-identity", "sam", "-subject", "s", "-tag", "deploy", "-key", "k1", "general", "hello")
	if s2 := send("-identity", "sam", "-subject", "s", "-tag", "deploy", "-key", "k1", "general", "hello"); s2 != s1 {
		t.Fatalf("same key sent twice: seq %d then %d", s1, s2)
	}

	r := receive("-no-ack")
	if len(r.Messages) != 1 || r.Messages[0].Seq != s1 || r.Messages[0].Subject != "s" || !reflect.DeepEqual(r.Messages[0].Tags, []string{"deploy"}) {
		t.Fatalf("first receive: %+v", r)
	}
	ids, err := pat.Bus.Identities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		for _, s := range id.Sessions {
			if id.Name == "pat" {
				t.Errorf("the throwaway session survived: %+v", s)
			}
		}
	}
	// Nothing advanced the acked mark, so another throwaway sees it again.
	if r := receive("-no-ack"); len(r.Messages) != 1 || r.Messages[0].Seq != s1 {
		t.Fatalf("second receive: %+v", r)
	}
	// The acking receive returns it once more and moves the mark.
	if r := receive(); len(r.Messages) != 1 || r.Messages[0].Seq != s1 {
		t.Fatalf("acking receive: %+v", r)
	}
	if r := receive("-no-ack"); len(r.Messages) != 0 {
		t.Fatalf("after the ack: %+v", r)
	}

	dev := send("-identity", "sam", "dev", "on dev")
	gen := send("-identity", "sam", "general", "on general")
	if r := receive("-channel", "dev", "-no-ack"); len(r.Messages) != 1 || r.Messages[0].Seq != dev {
		t.Fatalf("-channel dev: %+v", r)
	}
	if r := receive("-no-ack"); len(r.Messages) != 2 {
		t.Fatalf("both channels: %+v", r)
	}
	_ = gen

	// Redelivery proper, on one session.
	s, err := pat.Bus.Open(ctx, bus.OpenSessionInput{Hint: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	first := receive("-session", s.ID, "-no-ack")
	if len(first.Messages) != 2 || first.Batch == "" || first.Redelivered {
		t.Fatalf("first batch: %+v", first)
	}
	again := receive("-session", s.ID, "-no-ack")
	if !again.Redelivered || again.Batch != first.Batch || len(again.Messages) != 2 {
		t.Fatalf("redelivery: %+v", again)
	}
	done := receive("-session", s.ID, "-ack", first.Batch)
	if len(done.Messages) != 0 || done.AckIgnored {
		t.Fatalf("ack: %+v", done)
	}
	// Without -no-ack, -session touches nothing but that session: a batch it
	// reads stays pending until acked.
	send("-identity", "sam", "general", "one more")
	if r := receive("-session", s.ID); len(r.Messages) != 1 || r.Batch == "" {
		t.Fatalf("session receive: %+v", r)
	}
	if r := receive("-session", s.ID); !r.Redelivered {
		t.Fatalf("a -session receive acked on its own: %+v", r)
	}
	if r := receive("-session", s.ID, "-ack", "wrong-token"); !r.AckIgnored {
		t.Fatalf("wrong token: %+v", r)
	}
}

func TestSendFromStdin(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	var o, er bytes.Buffer
	if code := RunSend(ctx, []string{"general", "-"}, e.Environ, strings.NewReader("from stdin\nline 2"), &o, &er); code != 0 {
		t.Fatalf("send -: %d %s", code, er.String())
	}
	o.Reset()
	if code := RunSend(ctx, []string{"general"}, e.Environ, strings.NewReader("no argument"), &o, &er); code != 0 {
		t.Fatalf("send with no content: %d %s", code, er.String())
	}
	o.Reset()
	if code := RunHistory(ctx, []string{"general"}, e.Environ, strings.NewReader(""), &o, &er); code != 0 {
		t.Fatalf("history: %d %s", code, er.String())
	}
	var ms []struct{ Content string }
	if err := json.Unmarshal(o.Bytes(), &ms); err != nil || len(ms) != 2 || ms[0].Content != "from stdin\nline 2" || ms[1].Content != "no argument" {
		t.Fatalf("history: %q %v", o.String(), err)
	}
}

func TestHistoryAndSearch(t *testing.T) {
	type opCall = func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) int
	e := newTestEnv(t)
	ctx := context.Background()
	call := func(f opCall, args ...string) (int, string, string) {
		var o, er bytes.Buffer
		code := f(ctx, args, e.Environ, strings.NewReader(""), &o, &er)
		return code, o.String(), er.String()
	}
	for _, a := range [][]string{
		{"-tag", "deploy", "general", "hello world"},
		{"general", "second note"},
		{"-tag", "deploy", "general", "third note"},
	} {
		if code, _, er := call(RunSend, a...); code != 0 {
			t.Fatalf("send %v: %d %s", a, code, er)
		}
	}
	contents := func(args ...string) []string {
		t.Helper()
		code, out, er := call(RunHistory, args...)
		if code != 0 {
			t.Fatalf("history %v: %d %s", args, code, er)
		}
		var ms []struct{ Content string }
		if err := json.Unmarshal([]byte(out), &ms); err != nil {
			t.Fatal(err)
		}
		var cs []string
		for _, m := range ms {
			cs = append(cs, m.Content)
		}
		return cs
	}
	// With no bound the window starts at the oldest; with -before alone it is
	// the newest messages below it (core history rules).
	if got := contents("-count", "2", "general"); !reflect.DeepEqual(got, []string{"hello world", "second note"}) {
		t.Fatalf("-count 2: %v", got)
	}
	if got := contents("-before", "4", "-count", "2", "general"); !reflect.DeepEqual(got, []string{"second note", "third note"}) {
		t.Fatalf("-before 4 -count 2: %v", got)
	}
	if got := contents("-tag", "deploy", "general"); !reflect.DeepEqual(got, []string{"hello world", "third note"}) {
		t.Fatalf("-tag deploy: %v", got)
	}
	if got := contents("-before", "3", "general"); len(got) != 2 {
		t.Fatalf("-before 3: %v", got)
	}
	if got := contents("-after", "2", "general"); !reflect.DeepEqual(got, []string{"third note"}) {
		t.Fatalf("-after 2: %v", got)
	}

	code, out, er := call(RunSearch, "hello")
	if code != 0 {
		t.Fatalf("search: %d %s", code, er)
	}
	var res struct {
		Hits []struct {
			Content string
			Score   float64
		}
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Hits) != 1 || res.Hits[0].Content != "hello world" {
		t.Fatalf("search output %q: %v", out, err)
	}
	if code, out, _ := call(RunSearch, "-channel", "general", "-mode", "text", "-tag", "deploy", "-count", "5", "note"); code != 0 || !strings.Contains(out, "third note") {
		t.Fatalf("search with filters: %d %q", code, out)
	}
	// An unknown mode is rejected, by the core (Decision 80: the CLI never
	// filters values), not accepted.
	if code, _, er := call(RunSearch, "-mode", "bogus", "hello"); code != 1 || !strings.Contains(er, "validation") {
		t.Fatalf("-mode bogus: %d %q", code, er)
	}
}

func TestSendAttribution(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	pat := e.conn(t, "pat")
	if err := pat.Bus.Subscribe(ctx, bus.SubscribeInput{Channel: "general"}); err != nil {
		t.Fatal(err)
	}
	s, err := pat.Bus.Open(ctx, bus.OpenSessionInput{Hint: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	var o, er bytes.Buffer
	if code := RunSend(ctx, []string{"-session", s.ID, "general", "mine"}, e.Environ, strings.NewReader(""), &o, &er); code != 0 {
		t.Fatalf("send: %d %s", code, er.String())
	}
	count := func(args ...string) int {
		t.Helper()
		var o, er bytes.Buffer
		if code := RunReceive(ctx, append(args, "-session", s.ID, "-no-ack"), e.Environ, strings.NewReader(""), &o, &er); code != 0 {
			t.Fatalf("receive: %d %s", code, er.String())
		}
		var r struct{ Messages []json.RawMessage }
		if err := json.Unmarshal(o.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return len(r.Messages)
	}
	if n := count(); n != 0 {
		t.Fatalf("own message delivered without -include-own: %d", n)
	}
	if n := count("-include-own"); n != 1 {
		t.Fatalf("-include-own: %d messages, want 1", n)
	}
}

func TestOperatorMessageGrammarExits2(t *testing.T) {
	type opCall = func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) int
	e := newTestEnv(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		f    opCall
		args [][]string
	}{
		"send": {RunSend, [][]string{
			nil, {"general", "a", "b"}, {"-nope", "general", "x"}, {"-metadata", "novalue", "general", "x"},
			{"-metadata", "=v", "general", "x"}, {"-metadata", "a=1", "-metadata", "a=2", "general", "x"},
			{"-reply-to", "x", "general", "x"}, {"-filter", "x", "general", "x"},
		}},
		"receive": {RunReceive, [][]string{
			{"extra"}, {"-nope"}, {"-count", "x"}, {"-ack", "T"}, {"-session", "s", "-no-ack", "-filter", "x"}, {"-include-own=maybe"},
		}},
		"history": {RunHistory, [][]string{
			nil, {"a", "b"}, {"-count", "x", "general"}, {"-before", "x", "general"}, {"-after", "1.5", "general"}, {"-nope", "general"},
		}},
		"search": {RunSearch, [][]string{
			nil, {"a", "b"}, {"-count", "x", "q"}, {"-since", "x", "q"}, {"-until", "x", "q"}, {"-thread", "x", "q"}, {"-nope", "q"},
		}},
	} {
		for _, args := range c.args {
			var o, er bytes.Buffer
			if code := c.f(ctx, args, e.Environ, strings.NewReader(""), &o, &er); code != 2 {
				t.Errorf("%s %v: exit %d, want 2 (stderr %q)", name, args, code, er.String())
			}
		}
		var o, er bytes.Buffer
		if code := c.f(ctx, []string{"-h"}, e.Environ, strings.NewReader(""), &o, &er); code != 0 {
			t.Errorf("%s -h: exit %d, want 0", name, code)
		}
	}
	// A rejected grammar sent nothing.
	c := e.conn(t, "pat")
	ms, err := c.Bus.History(ctx, bus.HistoryInput{Channel: "general", Count: 10})
	if err != nil || len(ms) != 0 {
		t.Fatalf("history after usage errors: %v %v", ms, err)
	}
}
