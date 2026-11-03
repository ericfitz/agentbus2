package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/store"
	"github.com/ericfitz/agentmesh/internal/store/sqlite"
)

// stubInspector is a scripted Inspector that records its requests.
type stubInspector struct {
	mu    sync.Mutex
	reply func(ctx context.Context, req InspectRequest) ([]byte, error)
	calls []InspectRequest
}

func (s *stubInspector) Inspect(ctx context.Context, req InspectRequest) ([]byte, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	f := s.reply
	s.mu.Unlock()
	return f(ctx, req)
}

func (s *stubInspector) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubInspector) set(f func(ctx context.Context, req InspectRequest) ([]byte, error)) {
	s.mu.Lock()
	s.reply = f
	s.mu.Unlock()
}

func replyWith(body string) func(context.Context, InspectRequest) ([]byte, error) {
	return func(context.Context, InspectRequest) ([]byte, error) { return []byte(body), nil }
}

// newInspectCore is newTestCore with an inspector and a JSON debug log kept in
// the returned buffer.
func newInspectCore(t *testing.T, mod func(*Config), in Inspector) (*Core, *testClock, *bytes.Buffer) {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "agentmesh.db"), sqlite.Options{})
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg := Defaults()
	if mod != nil {
		mod(&cfg)
	}
	clk := &testClock{t: time.UnixMilli(startMs)}
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c, err := New(s, cfg, Options{Now: clk.now, Inspector: in, Logger: log})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, clk, buf
}

const secretContent = "SECRET-CONTENT-XYZ"

func testPrep() Prepared {
	return Prepared{
		Inspect: true,
		Request: InspectRequest{
			Operation: OpSend, Source: SourceAPI, Sender: "pat", SenderKind: "agent", Session: "s1",
			Channel: InspectChannel{Name: "general", Kind: "ordinary"},
			Record:  InspectRecord{Subject: "subj", Content: secretContent},
		},
		ChannelID: 3,
		Size:      100,
	}
}

func prepareOf(p Prepared) func(context.Context, store.Tx) (Prepared, error) {
	return func(context.Context, store.Tx) (Prepared, error) { return p, nil }
}

// logLines parses the JSON log into one map per line.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if ln == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("log line %q: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

func linesWithMsg(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range logLines(t, buf) {
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func inspectionNotice(t *testing.T, c *Core) (store.Notice, bool) {
	t.Helper()
	var n store.Notice
	var ok bool
	err := c.Store.Read(context.Background(), func(ctx context.Context, tx store.Tx) error {
		var e error
		n, ok, e = tx.Notice(ctx, NoticeInspection)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return n, ok
}

func TestParseVerdict(t *testing.T) {
	v, err := ParseVerdict([]byte(`{"verdict":"allow"}`))
	if err != nil || v != (Verdict{Verdict: "allow"}) {
		t.Fatalf("allow = %+v, %v", v, err)
	}
	v, err = ParseVerdict([]byte(`{"verdict":"refuse","code":"pii","reason":"contains a key","notify":true}`))
	want := Verdict{Verdict: "refuse", Code: "pii", Reason: "contains a key", Notify: true}
	if err != nil || v != want {
		t.Fatalf("refuse = %+v, %v, want %+v", v, err, want)
	}
}

func TestParseVerdictRejects(t *testing.T) {
	padded := `{"verdict":"allow"` + strings.Repeat(" ", MaxVerdictBytes) + `}`
	cases := []struct {
		name, body, class string
	}{
		{"empty", ``, FailMalformed},
		{"null", `null`, FailMalformed},
		{"array", `[]`, FailMalformed},
		{"trailing value", `{"verdict":"allow"} {}`, FailMalformed},
		{"extra member", `{"verdict":"allow","extra":1}`, FailMalformed},
		{"too long", padded, FailMalformed},
		{"wrong member case", `{"Verdict":"allow"}`, FailMalformed},
		{"null member", `{"verdict":"allow","notify":null}`, FailMalformed},
		{"wrong type", `{"verdict":"allow","notify":"yes"}`, FailMalformed},
		{"duplicate verdict", `{"verdict":"refuse","verdict":"allow"}`, FailMalformed},
		{"duplicate code", `{"verdict":"refuse","code":"a","code":"b","reason":"y"}`, FailMalformed},
		{"allow with empty code", `{"verdict":"allow","code":""}`, FailVerdict},
		{"allow with empty reason", `{"verdict":"allow","reason":""}`, FailVerdict},
		{"not an object", `"allow"`, FailMalformed},
		{"unterminated", `{"verdict":"allow"`, FailMalformed},
		{"no verdict", `{}`, FailVerdict},
		{"unknown verdict", `{"verdict":"maybe"}`, FailVerdict},
		{"redact with member", `{"verdict":"redact","redact":{}}`, FailMalformed},
		{"redact", `{"verdict":"redact"}`, FailVerdict},
		{"hold", `{"verdict":"hold","code":"x","reason":"y"}`, FailVerdict},
		{"refuse no code", `{"verdict":"refuse","reason":"y"}`, FailVerdict},
		{"refuse upper code", `{"verdict":"refuse","code":"PII","reason":"y"}`, FailVerdict},
		{"refuse long code", `{"verdict":"refuse","code":"` + strings.Repeat("a", 33) + `","reason":"y"}`, FailVerdict},
		{"refuse blank reason", `{"verdict":"refuse","code":"x","reason":"  "}`, FailVerdict},
		{"refuse no reason", `{"verdict":"refuse","code":"x"}`, FailVerdict},
		{"allow with code", `{"verdict":"allow","code":"x"}`, FailVerdict},
		{"allow with reason", `{"verdict":"allow","reason":"y"}`, FailVerdict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseVerdict([]byte(tc.body))
			var ie *InspectError
			if !errors.As(err, &ie) || ie.Class != tc.class {
				t.Fatalf("ParseVerdict = %v, want an *InspectError of class %s", err, tc.class)
			}
		})
	}
	if _, err := ParseVerdict([]byte(`{"verdict":"redact"}`)); err == nil || !strings.Contains(err.Error(), `verdict "redact" is not implemented`) {
		t.Errorf("redact error = %v", err)
	}
	if _, err := ParseVerdict([]byte(`{"verdict":"refuse","code":"` + strings.Repeat("a", 32) + `","reason":"y"}`)); err != nil {
		t.Errorf("a 32-character code rejected: %v", err)
	}
}

func TestInspectError(t *testing.T) {
	inner := errors.New("boom")
	e := &InspectError{Class: FailTransport, Err: inner}
	if e.Error() != "inspection transport: boom" || !errors.Is(e, inner) {
		t.Fatalf("InspectError = %q, unwrap ok = %v", e.Error(), errors.Is(e, inner))
	}
}

func TestDigest(t *testing.T) {
	base := testPrep()
	if base.Digest() != testPrep().Digest() || len(base.Digest()) != 64 {
		t.Fatalf("digest not stable or not 64 hex characters: %q", base.Digest())
	}
	zero := int64(0)
	changes := map[string]func(*Prepared){
		"ChannelID":    func(p *Prepared) { p.ChannelID++ },
		"Channel name": func(p *Prepared) { p.Request.Channel.Name = "other" },
		"Target":       func(p *Prepared) { p.Target = 9 },
		"Subject":      func(p *Prepared) { p.Request.Record.Subject = "x" },
		"Content":      func(p *Prepared) { p.Request.Record.Content = "x" },
		"Description":  func(p *Prepared) { p.Request.Record.Description = "x" },
		"Type":         func(p *Prepared) { p.Request.Record.Type = "x" },
		"Tags":         func(p *Prepared) { p.Request.Record.Tags = []string{"a"} },
		"Metadata":     func(p *Prepared) { p.Request.Record.Metadata = map[string]string{"k": "v"} },
		"ReplyTo 0":    func(p *Prepared) { p.Request.Record.ReplyTo = &zero },
		"BaseRevision": func(p *Prepared) { p.Request.Record.BaseRevision = 4 },
	}
	for name, mut := range changes {
		p := testPrep()
		mut(&p)
		if p.Digest() == base.Digest() {
			t.Errorf("digest unchanged by %s", name)
		}
	}
	same := map[string]func(*Prepared){
		"RequestID": func(p *Prepared) { p.Request.RequestID = "abc" },
		"Version":   func(p *Prepared) { p.Request.Version = 7 },
		"Sender":    func(p *Prepared) { p.Request.Sender = "other" },
		"Size":      func(p *Prepared) { p.Size = 5 },
	}
	for name, mut := range same {
		p := testPrep()
		mut(&p)
		if p.Digest() != base.Digest() {
			t.Errorf("digest changed by %s", name)
		}
	}
}

func TestRunInspectedNoInspector(t *testing.T) {
	c, _, _ := newInspectCore(t, nil, nil)
	if c.Inspecting() {
		t.Fatal("Inspecting() true with no inspector")
	}
	prepared, writes := 0, 0
	err := c.RunInspected(context.Background(), "pat", func(context.Context, store.Tx) (Prepared, error) {
		prepared++
		return testPrep(), nil
	}, func(want string) error {
		writes++
		if want != "" {
			t.Errorf("want = %q", want)
		}
		return nil
	})
	if err != nil || prepared != 0 || writes != 1 {
		t.Fatalf("err=%v prepared=%d writes=%d", err, prepared, writes)
	}
}

func TestRunInspectedSkip(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, _ := newInspectCore(t, nil, in)
	if !c.Inspecting() {
		t.Fatal("Inspecting() false with an inspector")
	}
	writes := 0
	err := c.RunInspected(context.Background(), "pat", prepareOf(Prepared{}), func(want string) error {
		writes++
		if want != "" {
			t.Errorf("want = %q", want)
		}
		return nil
	})
	if err != nil || in.count() != 0 || writes != 1 {
		t.Fatalf("err=%v calls=%d writes=%d", err, in.count(), writes)
	}
}

func TestRunInspectedAllow(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, buf := newInspectCore(t, nil, in)
	prep := testPrep()
	var got string
	err := c.RunInspected(context.Background(), "pat", prepareOf(prep), func(want string) error { got = want; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if in.count() != 1 {
		t.Fatalf("calls = %d", in.count())
	}
	req := in.calls[0]
	if req.Version != 1 || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(req.RequestID) {
		t.Errorf("version=%d request_id=%q", req.Version, req.RequestID)
	}
	if req.Operation != OpSend || req.Source != SourceAPI || req.Sender != "pat" || req.Record.Content != secretContent || req.Channel.Name != "general" {
		t.Errorf("request = %+v", req)
	}
	if got != prep.Digest() {
		t.Errorf("write got %q, want %q", got, prep.Digest())
	}
	if n := len(linesWithMsg(t, buf, "write inspection allowed")); n != 1 {
		t.Errorf("allowed lines = %d, want 1", n)
	}
	if strings.Contains(buf.String(), secretContent) {
		t.Error("the log carries the record's content")
	}
	if lv := linesWithMsg(t, buf, "write inspection allowed")[0]["level"]; lv != "DEBUG" {
		t.Errorf("allowed line level = %v", lv)
	}
}

func TestRunInspectedRejectsUnknownSource(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, _ := newInspectCore(t, nil, in)
	for name, mut := range map[string]func(*Prepared){
		"source":    func(p *Prepared) { p.Request.Source = "mail" },
		"operation": func(p *Prepared) { p.Request.Operation = "delete_everything" },
	} {
		p := testPrep()
		mut(&p)
		err := c.RunInspected(context.Background(), "pat", prepareOf(p), func(string) error { t.Error("write ran"); return nil })
		if bus.CodeOf(err) != bus.CodeInternal || in.count() != 0 {
			t.Errorf("%s: err=%v calls=%d, want internal and no inspector call", name, err, in.count())
		}
	}
}

func TestRunInspectedRefuse(t *testing.T) {
	long := strings.Repeat("r", 1000)
	in := &stubInspector{reply: replyWith(`{"verdict":"refuse","code":"pii","reason":"contains a key"}`)}
	c, _, buf := newInspectCore(t, func(cfg *Config) { cfg.SendMessagesPerSecond = 1 }, in)
	if !c.Limits.Check("pat", 0, c.Now()) {
		t.Fatal("limiter refuses before any charge")
	}
	err := c.RunInspected(context.Background(), "pat", prepareOf(testPrep()), func(string) error { t.Error("write ran"); return nil })
	var be *bus.Error
	if !errors.As(err, &be) || be.Code != bus.CodeInspectionRejected || be.Retryable || be.Message != "rejected by inspection (pii): contains a key" {
		t.Fatalf("err = %#v", err)
	}
	if c.Limits.Check("pat", 0, c.Now()) {
		t.Error("a refusal was not charged (Decision 13)")
	}
	lines := linesWithMsg(t, buf, "write refused by inspection")
	if len(lines) != 1 {
		t.Fatalf("refused lines = %d", len(lines))
	}
	for _, k := range []string{"request_id", "operation", "source", "sender", "channel", "code", "reason", "latency_ms"} {
		if _, ok := lines[0][k]; !ok {
			t.Errorf("refused line lacks %s: %v", k, lines[0])
		}
	}
	if lines[0]["level"] != "INFO" {
		t.Errorf("level = %v", lines[0]["level"])
	}

	in.set(replyWith(`{"verdict":"refuse","code":"pii","reason":"` + long + `"}`))
	buf.Reset()
	_ = c.RunInspected(context.Background(), "mia", prepareOf(testPrep()), func(string) error { return nil })
	lines = linesWithMsg(t, buf, "write refused by inspection")
	if len(lines) != 1 || len(lines[0]["reason"].(string)) != 256 {
		t.Fatalf("reason in log = %v", lines)
	}
	if strings.Contains(buf.String(), secretContent) {
		t.Error("the log carries the record's content")
	}
}

func TestRunInspectedNotify(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow","notify":true}`)}
	c, _, buf := newInspectCore(t, nil, in)
	for range 2 {
		if err := c.RunInspected(context.Background(), "pat", prepareOf(testPrep()), func(string) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	lines := linesWithMsg(t, buf, "write inspection asks for operator attention")
	if len(lines) != 2 || lines[0]["notify_total"] != float64(1) || lines[1]["notify_total"] != float64(2) {
		t.Fatalf("notify lines = %v", lines)
	}
	if lines[0]["level"] != "WARN" {
		t.Errorf("level = %v", lines[0]["level"])
	}
}

func TestRunInspectedPrepareError(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, _ := newInspectCore(t, func(cfg *Config) { cfg.SendMessagesPerSecond = 1 }, in)
	want := E(bus.CodeForbidden, "no")
	err := c.RunInspected(context.Background(), "pat", func(context.Context, store.Tx) (Prepared, error) { return Prepared{}, want },
		func(string) error { t.Error("write ran"); return nil })
	if err != error(want) || in.count() != 0 {
		t.Fatalf("err=%v calls=%d", err, in.count())
	}
	if !c.Limits.Check("pat", 0, c.Now()) {
		t.Error("a prepare error was charged")
	}
}

func TestRunInspectedChanged(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, _ := newInspectCore(t, nil, in)
	prepares := 0
	prepare := func(context.Context, store.Tx) (Prepared, error) { prepares++; return testPrep(), nil }
	writes := 0
	err := c.RunInspected(context.Background(), "pat", prepare, func(string) error {
		writes++
		if writes == 1 {
			return ErrInspectedChanged
		}
		return nil
	})
	if err != nil || prepares != 2 || in.count() != 2 || writes != 2 {
		t.Fatalf("err=%v prepares=%d calls=%d writes=%d", err, prepares, in.count(), writes)
	}
	if in.calls[0].RequestID == in.calls[1].RequestID {
		t.Error("a re-inspection reused the request id")
	}

	in2 := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c2, _, _ := newInspectCore(t, nil, in2)
	err = c2.RunInspected(context.Background(), "pat", prepareOf(testPrep()), func(string) error { return ErrInspectedChanged })
	var be *bus.Error
	if !errors.As(err, &be) || be.Code != bus.CodeConflict || !be.Retryable || be.Message != "the record changed while it was inspected; retry the write" {
		t.Fatalf("err = %#v", err)
	}
	if in2.count() != 2 {
		t.Errorf("calls = %d, want exactly 2", in2.count())
	}
}

type failureCase struct {
	name  string
	class string
	set   func(in *stubInspector)
}

func failureCases() []failureCase {
	return []failureCase{
		{"transport", FailTransport, func(in *stubInspector) {
			in.set(func(context.Context, InspectRequest) ([]byte, error) {
				return nil, &InspectError{Class: FailTransport, Err: errors.New("down")}
			})
		}},
		{"malformed", FailMalformed, func(in *stubInspector) { in.set(replyWith("nope")) }},
		{"verdict", FailVerdict, func(in *stubInspector) { in.set(replyWith(`{"verdict":"hold","code":"x","reason":"y"}`)) }},
		{"timeout", FailTimeout, func(in *stubInspector) {
			in.set(func(ctx context.Context, _ InspectRequest) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() })
		}},
	}
}

func TestRunInspectedFailClosed(t *testing.T) {
	for _, tc := range failureCases() {
		t.Run(tc.name, func(t *testing.T) {
			in := &stubInspector{}
			tc.set(in)
			c, _, buf := newInspectCore(t, func(cfg *Config) {
				cfg.SendMessagesPerSecond = 1
				cfg.InspectionTimeoutSeconds = 0.1
			}, in)
			err := c.RunInspected(context.Background(), "pat", prepareOf(testPrep()), func(string) error { t.Error("write ran"); return nil })
			var be *bus.Error
			if !errors.As(err, &be) || be.Code != bus.CodeUnavailable || !be.Retryable || be.Message != "write inspection is unavailable; retry later" {
				t.Fatalf("err = %#v", err)
			}
			if !c.Limits.Check("pat", 0, c.Now()) {
				t.Error("an inspector failure was charged (open note 2)")
			}
			n, ok := inspectionNotice(t, c)
			wantMsg := fmt.Sprintf("agentmesh pid %d: write inspection failing (%s); inspection_on_failure=closed", os.Getpid(), tc.class)
			if !ok || n.Message != wantMsg || n.SetAt != startMs {
				t.Errorf("notice = %+v (set %v), want message %q", n, ok, wantMsg)
			}
			lines := linesWithMsg(t, buf, "write inspection failed")
			if len(lines) != 1 || lines[0]["class"] != tc.class || lines[0]["on_failure"] != "closed" || lines[0]["level"] != "WARN" {
				t.Errorf("failure lines = %v", lines)
			}
			if strings.Contains(buf.String(), secretContent) {
				t.Error("the log carries the record's content")
			}
		})
	}
}

func TestRunInspectedFailOpen(t *testing.T) {
	for _, tc := range failureCases() {
		t.Run(tc.name, func(t *testing.T) {
			in := &stubInspector{}
			tc.set(in)
			c, _, buf := newInspectCore(t, func(cfg *Config) {
				cfg.InspectionOnFailure = InspectionOpen
				cfg.InspectionTimeoutSeconds = 0.1
			}, in)
			writes := 0
			sentinel := errors.New("write result")
			// open note: write-inspection 11 (the write runs with no digest check)
			err := c.RunInspected(context.Background(), "pat", prepareOf(testPrep()), func(want string) error {
				writes++
				if want != "" {
					t.Errorf("want = %q, a fail-open write has no digest", want)
				}
				return sentinel
			})
			if err != sentinel || writes != 1 {
				t.Fatalf("err=%v writes=%d, want write's result once", err, writes)
			}
			n, ok := inspectionNotice(t, c)
			if !ok || !strings.HasSuffix(n.Message, "inspection_on_failure=open") || !strings.Contains(n.Message, "("+tc.class+")") {
				t.Errorf("notice = %+v (set %v)", n, ok)
			}
			lines := linesWithMsg(t, buf, "write inspection failed")
			if len(lines) != 1 || lines[0]["on_failure"] != "open" {
				t.Errorf("failure lines = %v", lines)
			}
		})
	}
}

func TestRunInspectedCallerCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := &stubInspector{reply: func(ictx context.Context, _ InspectRequest) ([]byte, error) {
		cancel()
		return nil, ictx.Err()
	}}
	c, _, buf := newInspectCore(t, func(cfg *Config) { cfg.InspectionOnFailure = InspectionOpen }, in)
	err := c.RunInspected(ctx, "pat", prepareOf(testPrep()), func(string) error { t.Error("write ran for a vanished caller"); return nil })
	var be *bus.Error
	if !errors.As(err, &be) || be.Code != bus.CodeUnavailable || !be.Retryable {
		t.Fatalf("err = %#v", err)
	}
	if _, ok := inspectionNotice(t, c); ok {
		t.Error("a canceled caller set the notice")
	}
	lines := linesWithMsg(t, buf, "write inspection failed")
	if len(lines) != 1 || lines[0]["class"] != FailCanceled {
		t.Errorf("failure lines = %v", lines)
	}
}

// countingStore counts Read and Tx calls.
type countingStore struct {
	store.Store
	calls atomic.Int64
	reads atomic.Int64
	txs   atomic.Int64
}

func (s *countingStore) Read(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	s.calls.Add(1)
	s.reads.Add(1)
	return s.Store.Read(ctx, fn)
}

func (s *countingStore) Tx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	s.calls.Add(1)
	s.txs.Add(1)
	return s.Store.Tx(ctx, fn)
}

func TestInspectionNoticeRateAndClear(t *testing.T) {
	in := &stubInspector{}
	failing := func(context.Context, InspectRequest) ([]byte, error) {
		return nil, &InspectError{Class: FailTransport, Err: errors.New("down")}
	}
	in.set(failing)
	c, clk, _ := newInspectCore(t, nil, in)
	ctx := context.Background()
	run := func() { _, _ = c.inspect(ctx, "pat", testPrep()) }
	setAt := func() int64 {
		n, ok := inspectionNotice(t, c)
		if !ok {
			return -1
		}
		return n.SetAt
	}
	t0 := int64(startMs)

	run()
	if got := setAt(); got != t0 {
		t.Fatalf("SetAt after first failure = %d, want %d", got, t0)
	}
	clk.advance(30 * time.Second)
	run()
	if got := setAt(); got != t0 {
		t.Fatalf("a failure 30 s later rewrote the notice: SetAt = %d", got)
	}
	clk.advance(31 * time.Second) // t0 + 61 s
	run()
	if got := setAt(); got != t0+61_000 {
		t.Fatalf("a failure 61 s later: SetAt = %d, want %d", got, t0+61_000)
	}

	in.set(replyWith(`{"verdict":"allow"}`))
	clk.advance(29 * time.Second) // t0 + 90 s: the notice is 29 s old
	run()
	if got := setAt(); got != -1 {
		t.Fatalf("a success with a 29 s old notice left it: SetAt = %d", got)
	}

	// A second process starts unknown and its first success clears a notice
	// of any age.
	err := c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.SetNotice(ctx, store.Notice{Kind: NoticeInspection, Message: "left by a dead replica", SetAt: t0 + 100_000})
	})
	if err != nil {
		t.Fatal(err)
	}
	clk2 := &testClock{t: time.UnixMilli(startMs + 200_000)}
	cs := &countingStore{Store: c.Store}
	c2, err := New(cs, Defaults(), Options{Now: clk2.now, Inspector: in})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.inspect(ctx, "pat", testPrep()); err != nil {
		t.Fatal(err)
	}
	if got := setAt(); got != -1 {
		t.Fatalf("an unknown-state process did not clear the old notice: SetAt = %d", got)
	}
	// A notice another replica sets while this process is ok.
	err = c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.SetNotice(ctx, store.Notice{Kind: NoticeInspection, Message: "another replica", SetAt: startMs + 200_000})
	})
	if err != nil {
		t.Fatal(err)
	}
	// The earlier success set noticeCheckedAt. Five minutes on, the next success
	// reads, clears the other replica's notice whatever its age, and opens
	// exactly one Tx.
	beforeReads, beforeTxs := cs.reads.Load(), cs.txs.Load()
	clk2.advance(5 * time.Minute)
	if _, err := c2.inspect(ctx, "pat", testPrep()); err != nil {
		t.Fatal(err)
	}
	if got := cs.reads.Load() - beforeReads; got != 1 {
		t.Errorf("a success in state ok after the window made %d Reads, want 1", got)
	}
	if got := cs.txs.Load() - beforeTxs; got != 1 {
		t.Errorf("a success in state ok after the window made %d write Tx, want 1", got)
	}
	if got := setAt(); got != -1 {
		t.Errorf("a success in state ok left another replica's notice: SetAt = %d", got)
	}
}

// TestNoteSuccessOKThrottled: in state ok a second success within the window
// makes no Store call, and one at the window's end makes one Read.
func TestNoteSuccessOKThrottled(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, clk, _ := newInspectCore(t, nil, in)
	cs := &countingStore{Store: c.Store}
	c.Store = cs
	ctx := context.Background()
	c.noteSuccess(ctx) // unknown -> ok; reads once
	before := cs.calls.Load()
	clk.advance(59 * time.Second)
	c.noteSuccess(ctx)
	if got := cs.calls.Load() - before; got != 0 {
		t.Fatalf("a success 59 s on made %d Store calls, want 0", got)
	}
	clk.advance(1 * time.Second) // 60 s after the check
	c.noteSuccess(ctx)
	if got := cs.calls.Load() - before; got != 1 {
		t.Fatalf("a success 60 s on made %d Store calls, want 1 (a Read)", got)
	}
}

// TestNoticeClearedByOtherReplicaReturns: replica B's success clears the
// notice replica A set; A's next failure outside its window sets it again.
func TestNoticeClearedByOtherReplicaReturns(t *testing.T) {
	failing := &stubInspector{reply: func(context.Context, InspectRequest) ([]byte, error) {
		return nil, &InspectError{Class: FailTransport, Err: errors.New("down")}
	}}
	a, clk, _ := newInspectCore(t, nil, failing)
	ctx := context.Background()
	setAt := func() int64 {
		n, ok := inspectionNotice(t, a)
		if !ok {
			return -1
		}
		return n.SetAt
	}
	t0 := int64(startMs)
	_, _ = a.inspect(ctx, "pat", testPrep())
	if got := setAt(); got != t0 {
		t.Fatalf("SetAt after A's failure = %d, want %d", got, t0)
	}
	okIn := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	b, err := New(a.Store, Defaults(), Options{Now: clk.now, Inspector: okIn})
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(1 * time.Second)
	if _, err := b.inspect(ctx, "pat", testPrep()); err != nil {
		t.Fatal(err)
	}
	if got := setAt(); got != -1 {
		t.Fatalf("B's success left the notice: SetAt = %d", got)
	}
	clk.advance(29 * time.Second) // t0 + 30 s: inside A's window
	_, _ = a.inspect(ctx, "pat", testPrep())
	if got := setAt(); got != -1 {
		t.Fatalf("A rewrote the notice inside its window: SetAt = %d", got)
	}
	clk.advance(31 * time.Second) // t0 + 61 s
	_, _ = a.inspect(ctx, "pat", testPrep())
	if got := setAt(); got != t0+61_000 {
		t.Fatalf("A's failure at 61 s: SetAt = %d, want %d", got, t0+61_000)
	}
}

func TestInspectClassNormalized(t *testing.T) {
	in := &stubInspector{reply: func(context.Context, InspectRequest) ([]byte, error) {
		return nil, &InspectError{Class: "weird\nclass", Err: errors.New("x")}
	}}
	c, _, buf := newInspectCore(t, nil, in)
	_, _ = c.inspect(context.Background(), "pat", testPrep())
	lines := linesWithMsg(t, buf, "write inspection failed")
	if len(lines) != 1 || lines[0]["class"] != FailTransport {
		t.Fatalf("lines = %v, want class transport", lines)
	}
	if n, ok := inspectionNotice(t, c); !ok || !strings.Contains(n.Message, "(transport)") {
		t.Fatalf("notice = %+v", n)
	}
}

// hookStore runs after once, after the next Tx returns.
type hookStore struct {
	store.Store
	mu     sync.Mutex
	before func()
	after  func()
	// readAfter runs once, after the next Read returns.
	readAfter func()
	txs       int
}

func (s *hookStore) Read(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	err := s.Store.Read(ctx, fn)
	s.mu.Lock()
	h := s.readAfter
	s.readAfter = nil
	s.mu.Unlock()
	if h != nil {
		h()
	}
	return err
}

func (s *hookStore) Tx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	s.mu.Lock()
	s.txs++
	b := s.before
	s.before = nil
	s.mu.Unlock()
	if b != nil {
		b()
	}
	err := s.Store.Tx(ctx, fn)
	s.mu.Lock()
	h := s.after
	s.after = nil
	s.mu.Unlock()
	if h != nil {
		h()
	}
	return err
}

// TestInspectionFailureDuringSuccessClear: a failure that runs between a
// success's notice read and its state update must leave the state failing.
func TestInspectionFailureDuringSuccessClear(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, _ := newInspectCore(t, nil, in)
	hs := &hookStore{Store: c.Store}
	c.Store = hs
	ctx := context.Background()
	hs.readAfter = func() { c.noteFailure(ctx, FailTransport) }
	c.noteSuccess(ctx) // state unknown, no notice: would become ok
	if _, ok := inspectionNotice(t, c); !ok {
		t.Fatal("the interleaved failure did not set the notice")
	}
	c.inspectMu.Lock()
	st := c.inspectState
	c.inspectMu.Unlock()
	if st == inspectOK {
		t.Fatal("state is ok while the notice is set")
	}
}

// TestInspectionSuccessDuringFailureWrite: a success that runs entirely
// between a failure's state change and its SetNotice commit must not leave the
// state ok with the notice set.
func TestInspectionSuccessDuringFailureWrite(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, _ := newInspectCore(t, nil, in)
	hs := &hookStore{Store: c.Store}
	c.Store = hs
	ctx := context.Background()
	hs.before = func() { c.noteSuccess(ctx) } // unknown state, no notice yet: sets ok
	c.noteFailure(ctx, FailTransport)
	if _, ok := inspectionNotice(t, c); !ok {
		t.Fatal("the failure did not set the notice")
	}
	c.inspectMu.Lock()
	st := c.inspectState
	c.inspectMu.Unlock()
	if st != inspectFailing {
		t.Fatalf("state = %v with the notice set, want failing", st)
	}
	// The next success clears it, whatever its age.
	c.noteSuccess(ctx)
	if _, ok := inspectionNotice(t, c); ok {
		t.Fatal("the next success did not clear the notice")
	}
}

// TestInspectionSuccessClearsYoungNotice: a success in state failing clears a
// notice of any age, with one Tx; a success that finds none opens no Tx.
func TestInspectionSuccessClearsYoungNotice(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, _ := newInspectCore(t, nil, in)
	ctx := context.Background()
	c.noteFailure(ctx, FailTransport)
	hs := &hookStore{Store: c.Store}
	c.Store = hs
	c.noteSuccess(ctx)
	if hs.txs != 1 {
		t.Fatalf("noteSuccess opened %d write Tx, want 1", hs.txs)
	}
	if _, ok := inspectionNotice(t, c); ok {
		t.Fatal("a young notice was not cleared")
	}
	c.inspectMu.Lock()
	st := c.inspectState
	c.inspectMu.Unlock()
	if st != inspectOK {
		t.Fatalf("state = %v, want ok", st)
	}
}

// TestInspectionSuccessNoNoticeOpensNoTx: a success that finds no notice only reads.
func TestInspectionSuccessNoNoticeOpensNoTx(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, _, _ := newInspectCore(t, nil, in)
	hs := &hookStore{Store: c.Store}
	c.Store = hs
	c.noteSuccess(context.Background())
	if hs.txs != 0 {
		t.Fatalf("noteSuccess opened %d write Tx, want 0", hs.txs)
	}
}

// TestNoticeWriteThrottleSurvivesClear: on one core an inspector that
// alternates failure and success inside the window writes the notice once; a
// clear does not reset the per-process write throttle.
func TestNoticeWriteThrottleSurvivesClear(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, clk, _ := newInspectCore(t, nil, in)
	hs := &hookStore{Store: c.Store}
	c.Store = hs
	ctx := context.Background()
	const n = 6
	for i := 0; i < n; i++ {
		c.noteFailure(ctx, FailTransport)
		c.noteSuccess(ctx)
		clk.advance(5 * time.Second)
	}
	// One SetNotice and the one clear that removed it; every later success
	// only reads (no notice) and every later failure is throttled.
	if hs.txs != 2 {
		t.Fatalf("%d failure/success pairs in %v opened %d write Tx, want 2", n, time.Duration(n)*5*time.Second, hs.txs)
	}
	clk.advance(noticeResetAge) // well past the last write
	c.noteFailure(ctx, FailTransport)
	if hs.txs != 3 {
		t.Fatalf("a failure after the window opened %d write Tx in all, want 3", hs.txs)
	}
	if _, ok := inspectionNotice(t, c); !ok {
		t.Fatal("a failure after the window did not write the notice")
	}
}

// TestNoteSuccessReadFailureDoesNotThrottle: a failed Read in state ok must
// not delay the next look by the throttle window.
func TestNoteSuccessReadFailureDoesNotThrottle(t *testing.T) {
	in := &stubInspector{reply: replyWith(`{"verdict":"allow"}`)}
	c, clk, _ := newInspectCore(t, nil, in)
	ctx := context.Background()
	c.noteSuccess(ctx) // unknown -> ok
	clk.advance(noticeResetAge)
	fs := &failReadStore{Store: c.Store}
	c.Store = fs
	c.noteSuccess(ctx) // Read fails
	if fs.reads != 1 {
		t.Fatalf("reads = %d, want 1", fs.reads)
	}
	clk.advance(time.Second)
	c.noteSuccess(ctx)
	if fs.reads != 2 {
		t.Fatalf("the next success after a failed read made %d reads in all, want 2", fs.reads)
	}
}

type failReadStore struct {
	store.Store
	reads int
}

func (s *failReadStore) Read(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	s.reads++
	if s.reads == 1 {
		return errors.New("read failed")
	}
	return s.Store.Read(ctx, fn)
}

// TestInspectionNoticeTTLMinimum: the TTL floor must leave room for a failing
// process to rewrite the notice (every noticeResetAge) before it expires.
func TestInspectionNoticeTTLMinimum(t *testing.T) {
	c := Defaults()
	c.InspectionNoticeTTLSeconds = int(2 * noticeResetAge / time.Second)
	if err := c.Validate(); err != nil {
		t.Fatalf("2 x noticeResetAge is refused: %v", err)
	}
	c.InspectionNoticeTTLSeconds--
	if c.Validate() == nil {
		t.Fatal("a TTL under 2 x noticeResetAge is accepted")
	}
}
