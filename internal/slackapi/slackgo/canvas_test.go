package slackgo

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentmesh/internal/slackapi"
)

func TestCreateChannelCanvasParams(t *testing.T) {
	f, c := newFake(t)
	f.on("conversations.canvases.create", jsonReply(`{"ok":true,"canvas_id":"F1"}`))
	id, err := c.CreateChannelCanvas(context.Background(), "C1", "Status", "# x")
	if err != nil || id != "F1" {
		t.Fatalf("%q %v", id, err)
	}
	form := f.callsOf("conversations.canvases.create")[0].Form
	if form.Get("channel_id") != "C1" || form.Get("title") != "Status" ||
		form.Get("document_content") != `{"type":"markdown","markdown":"# x"}` {
		t.Fatalf("form %v", form)
	}
}

func TestChannelCanvasesReadsTabData(t *testing.T) {
	f, c := newFake(t)
	f.on("conversations.info", jsonReply(`{"ok":true,"channel":{"id":"C1","properties":{"canvas":null,"tabs":[{"id":"T1","label":"Status","type":"canvas","data":{"file_id":"F1"}},{"id":"T2","label":"Files","type":"files"}]}}}`))
	tabs, err := c.ChannelCanvases(context.Background(), "C1")
	if err != nil || len(tabs) != 1 || tabs[0] != (slackapi.CanvasTab{ID: "F1", Label: "Status"}) {
		t.Fatalf("%v %v", tabs, err)
	}
	if cs := f.callsOf("conversations.info"); len(cs) != 1 || cs[0].Form.Get("channel") != "C1" {
		t.Fatalf("calls %v", cs)
	}
}

func TestCanvasContains(t *testing.T) {
	f, c := newFake(t)
	f.on("canvases.sections.lookup", jsonReply(`{"ok":true,"sections":[{"id":"S1"}]}`))
	ok, err := c.CanvasContains(context.Background(), "F1", "changes go through agentmesh")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if got := f.callsOf("canvases.sections.lookup")[0].Form.Get("criteria"); got != `{"contains_text":"changes go through agentmesh"}` {
		t.Fatalf("criteria %s", got)
	}
	f.on("canvases.sections.lookup", jsonReply(`{"ok":true,"sections":[]}`))
	if ok, err := c.CanvasContains(context.Background(), "F1", "x"); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestReplaceCanvasParams(t *testing.T) {
	f, c := newFake(t)
	if err := c.ReplaceCanvas(context.Background(), "F1", "# y"); err != nil {
		t.Fatal(err)
	}
	form := f.callsOf("canvases.edit")[0].Form
	if form.Get("canvas_id") != "F1" ||
		form.Get("changes") != `[{"operation":"replace","document_content":{"type":"markdown","markdown":"# y"}}]` {
		t.Fatalf("form %v", form)
	}
}

func TestSetCanvasAccessParams(t *testing.T) {
	f, c := newFake(t)
	if err := c.SetCanvasChannelAccess(context.Background(), "F1", "C1", "read"); err != nil {
		t.Fatal(err)
	}
	form := f.callsOf("canvases.access.set")[0].Form
	if form.Get("canvas_id") != "F1" || form.Get("access_level") != "read" || form.Get("channel_ids") != `["C1"]` {
		t.Fatalf("form %v", form)
	}
	err := c.SetCanvasChannelAccess(context.Background(), "F1", "C1", "admin")
	if !errors.Is(err, slackapi.ErrCanvasAccessLevel) || len(f.callsOf("canvases.access.set")) != 1 {
		t.Fatalf("admin: %v", err)
	}
}

func TestCanvasErrorsKeepCodes(t *testing.T) {
	ctx := context.Background()
	ops := map[string]func(c *Client) error{
		"conversations.canvases.create": func(c *Client) error { _, e := c.CreateChannelCanvas(ctx, "C1", "t", "m"); return e },
		"conversations.info":            func(c *Client) error { _, e := c.ChannelCanvases(ctx, "C1"); return e },
		"canvases.sections.lookup":      func(c *Client) error { _, e := c.CanvasContains(ctx, "F1", "x"); return e },
		"canvases.edit":                 func(c *Client) error { return c.ReplaceCanvas(ctx, "F1", "m") },
		"canvases.access.set":           func(c *Client) error { return c.SetCanvasChannelAccess(ctx, "F1", "C1", "read") },
	}
	for method, op := range ops {
		for _, code := range []string{"is_archived", "channel_not_found"} {
			f, c := newFake(t)
			f.on(method, slackErr(code))
			err := op(c)
			var ae *slackapi.APIError
			if !errors.As(err, &ae) || ae.Code != code || ae.Method != method || errors.Is(err, slackapi.ErrChannelGone) {
				t.Errorf("%s %s: %#v", method, code, err)
			}
		}
		f, c := newFake(t)
		f.on(method, func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
		})
		var rl *slackapi.RateLimitedError
		if err := op(c); !errors.As(err, &rl) || rl.RetryAfter != 3*time.Second {
			t.Errorf("%s 429: %#v", method, err)
		}
		f, c = newFake(t)
		f.on(method, func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
		err := op(c)
		var ae *slackapi.APIError
		if !errors.As(err, &ae) || ae.Code != "http_500" {
			t.Errorf("%s 500: %#v", method, err)
		}
		if strings.Contains(err.Error(), "xoxb") {
			t.Errorf("%s: token in error", method)
		}
	}
}
