package slackgo

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

// socketServer serves apps.connections.open and a Socket Mode websocket. Each
// connection gets script(connIndex, conn) after the hello; every client frame
// is a JSON ack whose envelope_id lands in acks.
type socketServer struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	conns  int
	acked  map[string]chan struct{}
	script func(n int, send func(any))
}

func newSocketServer(t *testing.T, script func(n int, send func(any))) *socketServer {
	t.Helper()
	s := &socketServer{t: t, acked: map[string]chan struct{}{}, script: script}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"url":"ws` + strings.TrimPrefix(s.srv.URL, "http") + `/ws"}`))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		s.mu.Lock()
		n := s.conns
		s.conns++
		s.mu.Unlock()
		var wmu sync.Mutex
		send := func(v any) {
			wmu.Lock()
			defer wmu.Unlock()
			_ = conn.WriteJSON(v)
		}
		send(map[string]any{"type": "hello", "num_connections": 1})
		go s.script(n, send)
		for {
			var ack struct {
				EnvelopeID string `json:"envelope_id"`
			}
			if err := conn.ReadJSON(&ack); err != nil {
				return
			}
			close(s.ackChan(ack.EnvelopeID))
		}
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *socketServer) ackChan(id string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.acked[id]
	if !ok {
		ch = make(chan struct{})
		s.acked[id] = ch
	}
	return ch
}

func (s *socketServer) ackSeen(id string) bool {
	select {
	case <-s.ackChan(id):
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

func eventsEnvelope(id string, event map[string]any) map[string]any {
	return map[string]any{
		"type": "events_api", "envelope_id": id, "accepts_response_payload": false,
		"payload": map[string]any{"token": "x", "team_id": "T1", "api_app_id": "A1", "type": "event_callback", "event": event},
	}
}

func socketClient(t *testing.T, s *socketServer) *Client {
	t.Helper()
	c, err := New(testApp, testBot, Options{APIURL: s.srv.URL + "/api/"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunSocketAcksFirst(t *testing.T) {
	s := newSocketServer(t, func(n int, send func(any)) {
		if n > 0 {
			return
		}
		send(eventsEnvelope("e1", map[string]any{"type": "message", "channel": "C1", "user": "U1", "text": "hi", "ts": "1700000000.000100", "thread_ts": "1700000000.000001"}))
		send(eventsEnvelope("e2", map[string]any{"type": "zzz_unknown_event"}))
		send(eventsEnvelope("e3", map[string]any{"type": "member_left_channel", "channel": "C1", "user": "U2"}))
		send(eventsEnvelope("e4", map[string]any{"type": "message", "subtype": "thread_broadcast", "channel": "C1", "user": "U1", "text": "bc", "ts": "1700000000.000200"}))
		send(eventsEnvelope("e5", map[string]any{"type": "reaction_added", "user": "U1", "reaction": "x", "item": map[string]any{"type": "message"}}))
		send(map[string]any{"type": "slash_commands", "envelope_id": "e6", "payload": map[string]any{"command": "/x"}})
		send(eventsEnvelope("e7", map[string]any{"type": "message", "channel": "C1", "user": "U1", "text": "last", "ts": "1700000000.000300"}))
	})

	var mu sync.Mutex
	var got []slackapi.Event
	ackedBeforeHandler := map[string]bool{}
	connected := make(chan int, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	c := socketClient(t, s)
	go func() {
		done <- c.RunSocket(ctx, func(_ context.Context, ev slackapi.Event) {
			mu.Lock()
			got = append(got, ev)
			n := len(got)
			mu.Unlock()
			if ev.Kind == slackapi.EventConnected {
				connected <- n
				return
			}
			seen := s.ackSeen(ev.EnvelopeID)
			mu.Lock()
			ackedBeforeHandler[ev.EnvelopeID] = seen
			mu.Unlock()
		})
	}()

	// first connect
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("no EventConnected")
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); _, ok := ackedBeforeHandler["e7"]; return ok })
	for _, id := range []string{"e2", "e5", "e6"} {
		if !s.ackSeen(id) {
			t.Errorf("envelope %s not acked", id)
		}
	}

	mu.Lock()
	snapshot := append([]slackapi.Event(nil), got...)
	acked := maps.Clone(ackedBeforeHandler)
	mu.Unlock()
	kinds := make([]string, 0, len(snapshot))
	for _, e := range snapshot {
		kinds = append(kinds, string(e.Kind)+":"+e.EnvelopeID)
	}
	if want := "connected:,message:e1,member_left_channel:e3,message:e4,message:e7"; strings.Join(kinds, ",") != want {
		t.Fatalf("handled events %q, want %q", strings.Join(kinds, ","), want)
	}
	m := snapshot[1]
	if m.Channel != "C1" || m.User != "U1" || m.Message.Text != "hi" || m.Message.TS != "1700000000.000100" || m.Message.ThreadTS != "1700000000.000001" {
		t.Errorf("message event %+v", m)
	}
	if snapshot[2].Channel != "C1" || snapshot[2].User != "U2" {
		t.Errorf("member-left event %+v", snapshot[2])
	}
	if snapshot[3].Message.Subtype != "thread_broadcast" {
		t.Errorf("subtype %+v", snapshot[3])
	}
	for _, id := range []string{"e1", "e3", "e4", "e7"} {
		if !acked[id] {
			t.Errorf("handler for %s ran before its ack reached the server", id)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunSocket on context end: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunSocket did not return after cancel")
	}
}

func TestRunSocketReconnectsOnDisconnect(t *testing.T) {
	s := newSocketServer(t, func(n int, send func(any)) {
		if n == 0 {
			send(map[string]any{"type": "disconnect", "reason": "refresh_requested"})
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 4)
	done := make(chan error, 1)
	c := socketClient(t, s)
	go func() {
		done <- c.RunSocket(ctx, func(_ context.Context, ev slackapi.Event) {
			if ev.Kind == slackapi.EventConnected {
				connected <- struct{}{}
			}
		})
	}()
	for i := range 2 {
		select {
		case <-connected:
		case <-time.After(10 * time.Second):
			t.Fatalf("EventConnected %d never came", i+1)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunSocketInvalidAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer srv.Close()
	c, err := New(testApp, testBot, Options{APIURL: srv.URL + "/api/"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = c.RunSocket(ctx, func(context.Context, slackapi.Event) { t.Error("handler called") })
	if err == nil {
		t.Fatal("RunSocket returned nil on invalid_auth")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("token in error: %v", err)
	}
	b, _ := json.Marshal(err.Error())
	t.Logf("error: %s", b)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestRunSocketAuthErrorsNameTheSetting(t *testing.T) {
	for _, code := range []string{"invalid_auth", "token_revoked", "account_inactive"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":false,"error":"` + code + `"}`))
		}))
		c, _ := New(testApp, testBot, Options{APIURL: srv.URL + "/api/"})
		err := c.RunSocket(context.Background(), func(context.Context, slackapi.Event) {})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "slack_app_token_file") || !strings.Contains(err.Error(), code) || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: got %v", code, err)
		}
	}
}

func TestRunSocketNotFoundIsInvalidAuth(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c, _ := New(testApp, testBot, Options{APIURL: srv.URL + "/api/"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := c.RunSocket(ctx, func(context.Context, slackapi.Event) { t.Error("handler called") })
	if err == nil || !strings.Contains(err.Error(), "slack_app_token_file") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("got %v", err)
	}
}

func TestRunSocketWarnsOnUnparsableEnvelope(t *testing.T) {
	s := newSocketServer(t, func(n int, send func(any)) {
		if n == 0 {
			send(eventsEnvelope("bad1", map[string]any{"type": "zzz_unknown_event", "text": "PAYLOADSECRET"}))
		}
	})
	var buf syncBuf
	c, err := New(testApp, testBot, Options{APIURL: s.srv.URL + "/api/", Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.RunSocket(ctx, func(context.Context, slackapi.Event) {}) }()
	if !s.ackSeen("bad1") {
		t.Fatal("not acked")
	}
	waitFor(t, func() bool { return strings.Contains(buf.String(), "dropped an unparsable envelope") })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "bad1") || strings.Contains(out, "PAYLOADSECRET") {
		t.Fatalf("log %q", out)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
