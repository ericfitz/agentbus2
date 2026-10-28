package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Inspector judges a content write before it commits (write inspection, issue
// #5). Inspect returns the inspector's raw reply body; ParseVerdict, the one
// strict parser, judges it for every adapter. An adapter classifies its own
// errors as *InspectError; any other error counts as a transport failure.
type Inspector interface {
	Inspect(ctx context.Context, req InspectRequest) ([]byte, error)
}

// Source is where a write came from.
type Source string

const (
	SourceAPI   Source = "api"
	SourceSlack Source = "slack"
)

// The inspected operations.
const (
	OpSend       = "send"
	OpEditMemory = "edit_memory"
	OpTaskCreate = "task_create"
	OpTaskUpdate = "task_update"
)

const (
	// InspectRequestVersion is the version member of an InspectRequest.
	InspectRequestVersion = 1
	// MaxVerdictBytes caps an inspector reply; a longer one is malformed.
	MaxVerdictBytes = 64 << 10
)

// The verdicts.
const (
	VerdictAllow  = "allow"
	VerdictRefuse = "refuse"
)

// The values of Config.InspectionOnFailure.
const (
	InspectionClosed = "closed"
	InspectionOpen   = "open"
)

// The failure classes. FailCanceled is for the log only: the caller went away.
const (
	FailTimeout   = "timeout"
	FailTransport = "transport"
	FailStatus    = "status"
	FailMalformed = "malformed"
	FailVerdict   = "verdict"
	FailCanceled  = "canceled"
)

// maxReasonLog is the longest inspector reason a log line carries.
const maxReasonLog = 256

// noticeResetAge is how often the inspection notice is rewritten while
// failing, and how old it must be before a success clears it.
const noticeResetAge = 60 * time.Second

// noticeTxTimeout bounds a notice transaction, which runs without the
// caller's cancellation.
const noticeTxTimeout = 5 * time.Second

// InspectError is an inspection failure with its class.
type InspectError struct {
	Class string
	Err   error
}

func (e *InspectError) Error() string { return fmt.Sprintf("inspection %s: %v", e.Class, e.Err) }
func (e *InspectError) Unwrap() error { return e.Err }

// InspectRequest is what the inspector is handed. It carries the record, the
// sender and its kind, the session, the channel's name and kind and the source;
// never an ACL, the administrator flag or a scope (ADR 0011).
type InspectRequest struct {
	Version    int            `json:"version"`
	RequestID  string         `json:"request_id"`
	Operation  string         `json:"operation"`
	Source     Source         `json:"source"`
	Sender     string         `json:"sender"`
	SenderKind string         `json:"sender_kind"`
	Session    string         `json:"session,omitempty"`
	Channel    InspectChannel `json:"channel"`
	Record     InspectRecord  `json:"record"`
}

// InspectChannel names the channel the write lands in.
type InspectChannel struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// InspectRecord is the content under inspection.
type InspectRecord struct {
	Subject      string            `json:"subject,omitempty"`
	Content      string            `json:"content,omitempty"`
	Description  string            `json:"description,omitempty"`
	Type         string            `json:"type,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	ReplyTo      *int64            `json:"reply_to,omitempty"`
	BaseRevision int64             `json:"base_revision,omitempty"`
}

// Verdict is a parsed inspector reply.
type Verdict struct {
	Verdict string `json:"verdict"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Notify  bool   `json:"notify"`
}

var refuseCode = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

func malformed(format string, args ...any) error {
	return &InspectError{Class: FailMalformed, Err: fmt.Errorf(format, args...)}
}

func badVerdict(format string, args ...any) error {
	return &InspectError{Class: FailVerdict, Err: fmt.Errorf(format, args...)}
}

// ParseVerdict is the one judge of an inspector reply, strict by design: a
// reply that is almost right (an unknown or miscased member, a trailing value,
// null, over MaxVerdictBytes, an unknown verdict, a bad code or reason) is an
// *InspectError, never an allow (Review Focus 3). The classes are malformed
// (not one JSON object of known members) and verdict (a well-formed object
// that does not say allow or a valid refuse).
func ParseVerdict(raw []byte) (Verdict, error) {
	if len(raw) > MaxVerdictBytes {
		return Verdict{}, malformed("reply is %d bytes, over the %d byte limit", len(raw), MaxVerdictBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil {
		return Verdict{}, malformed("reply is not a JSON object: %v", err)
	} else if d, ok := t.(json.Delim); !ok || d != '{' {
		return Verdict{}, malformed("reply is not a JSON object")
	}
	var v Verdict
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return Verdict{}, malformed("reply is not a JSON object: %v", err)
		}
		name, _ := t.(string)
		if seen[name] {
			return Verdict{}, malformed("member %q is repeated", name)
		}
		seen[name] = true
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return Verdict{}, malformed("member %q: %v", name, err)
		}
		if string(bytes.TrimSpace(val)) == "null" {
			return Verdict{}, malformed("member %q is null", name)
		}
		switch name {
		case "verdict":
			err = json.Unmarshal(val, &v.Verdict)
		case "code":
			err = json.Unmarshal(val, &v.Code)
		case "reason":
			err = json.Unmarshal(val, &v.Reason)
		case "notify":
			err = json.Unmarshal(val, &v.Notify)
		default:
			return Verdict{}, malformed("unknown member %q", name)
		}
		if err != nil {
			return Verdict{}, malformed("member %q: %v", name, err)
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return Verdict{}, malformed("reply is not a JSON object: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Verdict{}, malformed("reply has content after the JSON object")
	}
	// open note: write-inspection 7 (an allow that names code or reason, even empty, is a failure)
	if v.Verdict == VerdictAllow && (seen["code"] || seen["reason"]) {
		return Verdict{}, badVerdict("an allow verdict carries a code or a reason")
	}
	if err := v.validate(); err != nil {
		return Verdict{}, err
	}
	return v, nil
}

func (v Verdict) validate() error {
	switch v.Verdict {
	case VerdictAllow:
		if v.Code != "" || v.Reason != "" {
			return badVerdict("an allow verdict carries a code or a reason")
		}
	case VerdictRefuse:
		if !refuseCode.MatchString(v.Code) {
			return badVerdict("a refuse verdict needs a code of 1 to 32 characters from [a-z0-9_]")
		}
		if strings.TrimSpace(v.Reason) == "" {
			return badVerdict("a refuse verdict needs a reason")
		}
	case "redact":
		return badVerdict("verdict %q is not implemented", v.Verdict)
	case "":
		return badVerdict("the reply has no verdict")
	default:
		return badVerdict("unknown verdict %q", v.Verdict)
	}
	return nil
}

// Prepared is what a write path's read-only pre-pass hands RunInspected.
// Inspect false means the write needs no inspection. Request.Version and
// Request.RequestID are filled by the runner.
type Prepared struct {
	Inspect   bool
	Request   InspectRequest
	ChannelID int64
	// Target is the id of the record being changed (a memory's or task's
	// message id), 0 for a new one.
	Target int64
	// Size is the envelope size charged when the inspector refuses.
	Size int
}

// Digest identifies the inspected content: hex SHA-256 of the channel id and
// name, the target and the record. The authoritative write recomputes it and
// answers ErrInspectedChanged when it differs. It ignores the request id,
// version, sender and size.
func (p Prepared) Digest() string {
	b, err := json.Marshal(struct {
		ChannelID int64         `json:"channel_id"`
		Channel   string        `json:"channel"`
		Target    int64         `json:"target"`
		Record    InspectRecord `json:"record"`
	}{p.ChannelID, p.Request.Channel.Name, p.Target, p.Request.Record})
	if err != nil {
		// Strings, numbers and maps of strings always marshal.
		panic("core: digest: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ErrInspectedChanged is what the authoritative write answers when the record
// it would write differs from the one the inspector judged.
var ErrInspectedChanged = errors.New("core: the inspected record changed before the write")

// Inspecting reports whether an inspector is configured.
func (c *Core) Inspecting() bool { return c.inspector != nil }

type inspectState int

const (
	inspectUnknown inspectState = iota
	inspectOK
	inspectFailing
)

// RunInspected drives one content write. With no inspector it is write("").
// Otherwise prepare runs in a Store.Read snapshot (the write path's own
// side-effect-free pre-pass); when it asks for inspection the inspector runs
// outside any transaction, then write(want) runs the authoritative write with
// the digest the inspector judged (want == "" means no digest check) and
// answers ErrInspectedChanged on a mismatch. One mismatch is inspected again;
// a second is a retryable conflict.
func (c *Core) RunInspected(ctx context.Context, identity string,
	prepare func(ctx context.Context, tx store.Tx) (Prepared, error), write func(want string) error) error {
	if c.inspector == nil {
		return write("")
	}
	for attempt := 0; attempt < 2; attempt++ {
		var prep Prepared
		err := c.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
			var e error
			prep, e = prepare(ctx, tx)
			return e
		})
		if err != nil {
			return FromStore(err)
		}
		if !prep.Inspect {
			return write("")
		}
		// open note: write-inspection 11 (a fail-open write has want == "": nothing was inspected)
		want, err := c.inspect(ctx, identity, prep)
		if err != nil {
			return err
		}
		if err = write(want); !errors.Is(err, ErrInspectedChanged) {
			return err
		}
	}
	// open note: write-inspection 1 (retryable conflict, so the bridge retries instead of dropping)
	return bus.Errorf(bus.CodeConflict, true, "the record changed while it was inspected; retry the write")
}

// inspect asks the inspector about prep and applies inspection_on_failure. It
// returns the digest to check ("" after a fail-open), or the refusal or
// unavailable error. It runs outside any transaction (Decision 26).
func (c *Core) inspect(ctx context.Context, identity string, prep Prepared) (string, error) {
	switch prep.Request.Source {
	case SourceAPI, SourceSlack:
	default:
		return "", bus.Internal(fmt.Errorf("core: inspection: unknown source %q", prep.Request.Source))
	}
	switch prep.Request.Operation {
	case OpSend, OpEditMemory, OpTaskCreate, OpTaskUpdate:
	default:
		return "", bus.Internal(fmt.Errorf("core: inspection: unknown operation %q", prep.Request.Operation))
	}
	// open note: write-inspection 14 (16 hex characters, new per inspector call)
	id, err := RandomHex(8)
	if err != nil {
		return "", bus.Internal(err)
	}
	req := prep.Request
	req.Version, req.RequestID = InspectRequestVersion, id

	start := time.Now()
	ictx, cancel := context.WithTimeout(ctx, time.Duration(c.Cfg.InspectionTimeoutSeconds*float64(time.Second)))
	raw, err := c.inspector.Inspect(ictx, req)
	var v Verdict
	if err == nil {
		v, err = ParseVerdict(raw)
	}
	ictxErr := ictx.Err()
	cancel()
	latency := time.Since(start).Milliseconds()
	log := c.Log.With("request_id", id, "operation", req.Operation, "source", string(req.Source),
		"sender", req.Sender, "channel", req.Channel.Name)

	// open note: write-inspection 3 (a caller that is gone gets nothing written, in either mode)
	if ctx.Err() != nil {
		log.Warn("write inspection failed", "class", FailCanceled, "on_failure", c.Cfg.InspectionOnFailure, "latency_ms", latency)
		return "", E(bus.CodeUnavailable, "write inspection is unavailable; retry later")
	}
	if err != nil {
		class := FailTransport
		var ie *InspectError
		switch {
		case errors.Is(ictxErr, context.DeadlineExceeded):
			class = FailTimeout
		case errors.As(err, &ie):
			class = normalizeClass(ie.Class)
		}
		c.noteFailure(ctx, class)
		log.Warn("write inspection failed", "class", class, "on_failure", c.Cfg.InspectionOnFailure,
			"latency_ms", latency, "error", cutBytes(err.Error(), maxReasonLog))
		if c.Cfg.InspectionOnFailure == InspectionOpen {
			return "", nil
		}
		return "", E(bus.CodeUnavailable, "write inspection is unavailable; retry later")
	}

	c.noteSuccess(ctx)
	if v.Notify {
		log.Warn("write inspection asks for operator attention", "notify_total", c.notifyTotal.Add(1),
			"verdict", v.Verdict, "code", v.Code, "reason", cutBytes(v.Reason, maxReasonLog), "latency_ms", latency)
	}
	if v.Verdict == VerdictAllow {
		log.Debug("write inspection allowed", "latency_ms", latency)
		return prep.Digest(), nil
	}
	// open note: write-inspection 2 (only a refusal charges the limiter; a failure charges nothing)
	c.Limits.Charge(identity, prep.Size, c.Now())
	log.Info("write refused by inspection", "code", v.Code, "reason", cutBytes(v.Reason, maxReasonLog), "latency_ms", latency)
	return "", E(bus.CodeInspectionRejected, "rejected by inspection (%s): %s", v.Code, v.Reason)
}

// normalizeClass maps a class an adapter made up to transport, so a log line
// or the notice text only ever carries a known class.
func normalizeClass(class string) string {
	switch class {
	case FailTimeout, FailTransport, FailStatus, FailMalformed, FailVerdict:
		return class
	}
	return FailTransport
}

// cutBytes cuts s to at most n bytes without splitting a character.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// noteFailure marks the inspector failing and writes the inspection notice,
// at most once a minute per process. A notice write error is a log line.
func (c *Core) noteFailure(ctx context.Context, class string) {
	now := c.Now()
	c.inspectMu.Lock()
	c.inspectState = inspectFailing
	c.failGen++
	if !c.noticeSetAt.IsZero() && now.Sub(c.noticeSetAt) < noticeResetAge {
		c.inspectMu.Unlock()
		return
	}
	prev := c.noticeSetAt
	c.noticeSetAt = now
	c.inspectMu.Unlock()

	msg := fmt.Sprintf("agentbus2 pid %d: write inspection failing (%s); inspection_on_failure=%s", os.Getpid(), class, c.Cfg.InspectionOnFailure)
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), noticeTxTimeout)
	defer cancel()
	err := c.Store.Tx(nctx, func(ctx context.Context, tx store.Tx) error {
		return tx.SetNotice(ctx, store.Notice{Kind: NoticeInspection, Message: msg, SetAt: now.UnixMilli()})
	})
	if err != nil {
		c.inspectMu.Lock()
		c.noticeSetAt = prev
		c.inspectMu.Unlock()
		c.Log.Warn("write inspection notice not written", "error", err.Error())
	}
}

// noteSuccess clears the inspection notice once the inspector works again. A
// process in state ok returns at once. Otherwise it reads the notice: absent,
// or at least a minute old, it is cleared and the state becomes ok; a younger
// one stays (another replica may still be failing). A process starts unknown,
// so its first success also clears the notice a dead replica left behind.
func (c *Core) noteSuccess(ctx context.Context) {
	c.inspectMu.Lock()
	ok := c.inspectState == inspectOK
	gen := c.failGen
	c.inspectMu.Unlock()
	if ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), noticeTxTimeout)
	defer cancel()
	// open note: write-inspection 4 (the unknown state is cleared by the first success)
	var cleared bool
	err := c.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		cleared = false
		n, found, err := tx.Notice(ctx, NoticeInspection)
		if err != nil {
			return err
		}
		if !found {
			cleared = true
			return nil
		}
		if c.Now().Sub(time.UnixMilli(n.SetAt)) < noticeResetAge {
			return nil
		}
		cleared = true
		return tx.ClearNotice(ctx, NoticeInspection)
	})
	if err != nil {
		c.Log.Warn("write inspection notice not cleared", "error", err.Error())
		return
	}
	if cleared {
		c.inspectMu.Lock()
		// A failure that ran meanwhile (failGen moved) may have set the notice
		// after this read: stay failing.
		if c.failGen == gen {
			c.inspectState = inspectOK
			c.noticeSetAt = time.Time{}
		}
		c.inspectMu.Unlock()
	}
}
