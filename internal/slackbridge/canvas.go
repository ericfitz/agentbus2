package slackbridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/ericfitz/agentmesh/internal/slackapi"
	"github.com/ericfitz/agentmesh/internal/store"
)

// canvasCallTimeout bounds every Slack canvas call (spec 6.4: the publisher's
// calls carry their own timeouts, so a canvas never holds up message bridging).
const canvasCallTimeout = 30 * time.Second

// The Web API method of each canvas call, as handle's method argument.
const (
	methodCanvasCreate = "conversations.canvases.create"
	methodCanvasTabs   = "conversations.info"
	methodCanvasLookup = "canvases.sections.lookup"
	methodCanvasEdit   = "canvases.edit"
	methodCanvasAccess = "canvases.access.set"
)

// canvasState is what the publisher remembers of one bridge. accessSet: the
// read-only access call succeeded for canvasID. The log fields name no
// content.
type canvasState struct {
	canvasID, hash string
	accessSet      bool
	disabled       bool
	warned         map[string]bool
	scope          string // for log lines
}

// canvasPublisher publishes the scope canvas of each live bridge. mu guards
// the state map and the warned sets only; it is never held across a Slack call
// or a Store read.
type canvasPublisher struct {
	s     *Service
	mu    sync.Mutex
	state map[int64]*canvasState
}

func newCanvasPublisher(s *Service) *canvasPublisher {
	return &canvasPublisher{s: s, state: map[int64]*canvasState{}}
}

// begin returns a working copy of the bridge's state.
func (p *canvasPublisher) begin(id int64, scope string) canvasState {
	p.mu.Lock()
	defer p.mu.Unlock()
	w := canvasState{warned: map[string]bool{}}
	if st := p.state[id]; st != nil {
		w = *st
	}
	w.scope = scope
	return w
}

// commit stores the working copy.
func (p *canvasPublisher) commit(id int64, w canvasState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state[id] = &w
}

// drop forgets the bridge.
func (p *canvasPublisher) drop(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.state, id)
}

// isDisabled reports whether the bridge's canvas is disabled.
func (p *canvasPublisher) isDisabled(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[id]
	return st != nil && st.disabled
}

// warnOnce reports true the first time key is seen for the bridge.
func (p *canvasPublisher) warnOnce(id int64, st *canvasState, key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st.warned == nil {
		st.warned = map[string]bool{}
	}
	if st.warned[key] {
		return false
	}
	st.warned[key] = true
	return true
}

// window is the next rebuild time after a transient failure.
func (p *canvasPublisher) window() time.Time { return time.Now().Add(p.s.cfg.CanvasCoalesce) }

// PublishCanvas renders the canvas of scope and publishes it to the bridge's
// Slack channel: find the bridge's canvas (by its marker), create it when the
// channel has none, replace its content when the rendering changed, and keep
// the channel's access read-only. A zero retryAt means nothing is left to
// retry; otherwise the caller marks the scope dirty again at retryAt. The
// error is the failure that caused the retry; a disabled canvas and an
// archived bridge return (zero, nil).
func (s *Service) PublishCanvas(ctx context.Context, b store.Bridge, scope string) (time.Time, error) {
	p := s.canvas
	if !s.cfg.CanvasEnabled || b.State != store.BridgeLive || b.SlackChannelID == "" || p.isDisabled(b.ID) {
		return time.Time{}, nil
	}
	snap, err := s.CanvasSnapshot(ctx, b, scope)
	if err != nil {
		return p.window(), err
	}
	r := RenderCanvas(snap)
	if r.TasksTooLarge {
		s.log.Warn("slack bridge: task lists too large for a canvas", "scope", scope)
	}
	hash := CanvasHash(r.Markdown)

	w := p.begin(b.ID, scope)
	if hash == w.hash && w.accessSet && w.canvasID != "" {
		return time.Time{}, nil
	}
	retryAt, archive, err := p.publish(ctx, b, &w, r.Markdown, hash)
	if archive {
		p.drop(b.ID)
		if aerr := s.ArchiveBridge(ctx, b, reasonSlackGone); aerr != nil {
			return p.window(), aerr
		}
		return time.Time{}, nil
	}
	p.commit(b.ID, w)
	return retryAt, err
}

// publish runs the Slack calls on the working copy w. On a failure it has
// applied the error table: a disable or an archive returns a nil error.
func (p *canvasPublisher) publish(ctx context.Context, b store.Bridge, w *canvasState, markdown, hash string) (time.Time, bool, error) {
	fail := func(method string, err error) (time.Time, bool, error) {
		at, archive := p.handle(b, w, method, err)
		if archive || w.disabled {
			return time.Time{}, archive, nil
		}
		return at, false, err
	}
	if w.canvasID == "" {
		if method, disabled, err := p.ensure(ctx, b, w, markdown, hash); err != nil {
			return fail(method, err)
		} else if disabled {
			return time.Time{}, false, nil
		}
	}
	if !w.accessSet {
		cctx, cancel := context.WithTimeout(ctx, canvasCallTimeout)
		err := p.s.sc.SetCanvasChannelAccess(cctx, w.canvasID, b.SlackChannelID, "read")
		cancel()
		if err != nil {
			return fail(methodCanvasAccess, err)
		}
		w.accessSet = true
	}
	if hash != w.hash {
		cctx, cancel := context.WithTimeout(ctx, canvasCallTimeout)
		err := p.s.sc.ReplaceCanvas(cctx, w.canvasID, markdown)
		cancel()
		if err != nil {
			return fail(methodCanvasEdit, err)
		}
		w.hash = hash
	}
	return time.Time{}, false, nil
}

// errCanvasLookup is a lookup that found no canvas right after a create said
// one exists; it is retried at the next window.
var errCanvasLookup = errors.New("slackbridge: the canvas the channel has was not found")

// ensure finds the bridge's canvas, or creates it (spec 6.3). On a create the
// markdown counts as published (w.hash). A foreign canvas tab disables the
// bridge (disabled true, nil error). On an error, method names the failed
// call.
func (p *canvasPublisher) ensure(ctx context.Context, b store.Bridge, w *canvasState, markdown, hash string) (method string, disabled bool, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		id, foreign, method, err := p.find(ctx, b)
		if err != nil {
			return method, false, err
		}
		if id != "" {
			w.canvasID, w.hash, w.accessSet = id, "", false
			return "", false, nil
		}
		if foreign {
			w.disabled = true
			if p.warnOnce(b.ID, w, "foreign") {
				p.s.log.Warn("slack bridge: canvas belongs to someone else; canvas disabled",
					"scope", w.scope, "channel", b.ChannelName, "slack_channel", b.SlackChannelID)
			}
			return "", true, nil
		}
		cctx, cancel := context.WithTimeout(ctx, canvasCallTimeout)
		id, err = p.s.sc.CreateChannelCanvas(cctx, b.SlackChannelID, CanvasTitle, markdown)
		cancel()
		if err == nil {
			w.canvasID, w.hash, w.accessSet = id, hash, false
			return "", false, nil
		}
		var ae *slackapi.APIError
		if errors.As(err, &ae) && (ae.Code == "channel_canvas_already_exists" || ae.Code == "free_team_canvas_tab_already_exists") {
			continue
		}
		return methodCanvasCreate, false, err
	}
	return methodCanvasTabs, false, errCanvasLookup
}

// find looks for the bridge's canvas among the channel's canvas tabs: the
// first tab that holds the marker. foreign is true when there are tabs and
// none does.
func (p *canvasPublisher) find(ctx context.Context, b store.Bridge) (id string, foreign bool, method string, err error) {
	cctx, cancel := context.WithTimeout(ctx, canvasCallTimeout)
	tabs, err := p.s.sc.ChannelCanvases(cctx, b.SlackChannelID)
	cancel()
	if err != nil {
		return "", false, methodCanvasTabs, err
	}
	for _, tab := range tabs {
		cctx, cancel := context.WithTimeout(ctx, canvasCallTimeout)
		ok, err := p.s.sc.CanvasContains(cctx, tab.ID, CanvasMarker)
		cancel()
		if err != nil {
			return "", false, methodCanvasLookup, err
		}
		if ok {
			return tab.ID, false, "", nil
		}
	}
	return "", len(tabs) > 0, "", nil
}

// handle is the error table of spec 6.4 for a failed call of method. It
// updates st (disable, forget the canvas) and logs the warnings (once per
// bridge); retryAt is when to rebuild, archive asks the caller to archive the
// bridge as a channel archived by hand in Slack. A disable returns a zero
// retryAt.
func (p *canvasPublisher) handle(b store.Bridge, st *canvasState, method string, err error) (retryAt time.Time, archive bool) {
	next := p.window()
	var rl *slackapi.RateLimitedError
	if errors.As(err, &rl) {
		return time.Now().Add(min(rl.RetryAfter, rateLimitWaitMax)), false
	}
	var ae *slackapi.APIError
	if !errors.As(err, &ae) {
		return next, false // transport, context
	}
	code := ae.Code
	attrs := []any{"scope", st.scope, "channel", b.ChannelName, "slack_channel", b.SlackChannelID, "canvas", st.canvasID}
	disable := func(key, msg string, extra ...any) (time.Time, bool) {
		st.disabled = true
		if p.warnOnce(b.ID, st, key) {
			p.s.log.Warn(msg, append(attrs, extra...)...)
		}
		return time.Time{}, false
	}
	switch code {
	case "team_tier_cannot_create_channel_canvases", "canvas_disabled_user_team":
		return disable("plan", "slack bridge: canvas unavailable on this Slack plan; canvas disabled", "code", code)
	case "missing_scope":
		return disable("scope", "slack bridge: the Slack app needs the canvases:read and canvases:write scopes; canvas disabled", "code", code)
	case "canvas_not_found":
		st.canvasID, st.hash, st.accessSet = "", "", false
		return next, false
	case "channel_not_found":
		if method == methodCanvasEdit {
			st.canvasID, st.hash, st.accessSet = "", "", false
			return next, false
		}
		return time.Time{}, true
	case "is_archived", "not_in_channel":
		return time.Time{}, true
	case "canvas_editing_locked", "fatal_error", "internal_error", "service_unavailable", "request_timeout":
		return next, false
	case "invalid_auth", "not_authed", "account_inactive", "token_revoked", "token_expired", "access_denied":
		if p.warnOnce(b.ID, st, "cred:"+code) {
			p.s.log.Warn("slack bridge: Slack refused the canvas call with a credential error", append(attrs, "code", code)...)
		}
		return next, false
	}
	if strings.HasPrefix(code, "http_") {
		return next, false
	}
	return disable("code:"+code, "slack bridge: canvas call refused with an unexpected code; canvas disabled", "code", code)
}
