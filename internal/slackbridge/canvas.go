package slackbridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/ericfitz/agentmesh/internal/core/notify"
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
// the maps, the dirty set and the flags only; it is never held across a Slack
// call or a Store read, so the marks (called from the socket, lifecycle and
// hint paths) never wait on Slack.
type canvasPublisher struct {
	s     *Service
	mu    sync.Mutex
	state map[int64]*canvasState

	dirty        canvasDirty
	pendingAll   bool
	pendingAt    time.Time                 // the deadline markAll recorded
	known        map[int64]string          // every channel refresh listed: its scope name or ""
	chanScope    map[int64]string          // task and memory channels of scopes with a live bridge
	unknown      map[int64]bool            // channels hinted since the last refresh that known lacks
	scopeBridges map[string][]store.Bridge // live bridges with a Slack channel, by scope name
	wake         chan struct{}
}

func newCanvasPublisher(s *Service) *canvasPublisher {
	return &canvasPublisher{
		s: s, state: map[int64]*canvasState{},
		known: map[int64]string{}, chanScope: map[int64]string{}, unknown: map[int64]bool{},
		scopeBridges: map[string][]store.Bridge{}, wake: make(chan struct{}, 1),
	}
}

// reset forgets everything: a lock tenure starts with no remembered canvas ID
// or hash, so another replica's edits during the gap are never masked by an
// equal hash.
func (p *canvasPublisher) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = map[int64]*canvasState{}
	p.known = map[int64]string{}
	p.chanScope = map[int64]string{}
	p.unknown = map[int64]bool{}
	p.scopeBridges = map[string][]store.Bridge{}
	p.dirty = canvasDirty{}
	p.pendingAll = false
	select {
	case <-p.wake:
	default:
	}
}

// markAll asks for a refresh and a rebuild of every live scope, one window from
// now. It never blocks.
func (p *canvasPublisher) markAll() {
	if !p.s.cfg.CanvasEnabled {
		return
	}
	at := p.window()
	p.mu.Lock()
	p.pendingAll = true
	p.pendingAt = at
	p.dirty.mark("", at)
	p.mu.Unlock()
	p.poke()
}

func (p *canvasPublisher) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// markHint turns a bus hint into a mark: a channel of a scope with a live
// bridge marks its scope; chat traffic marks nothing; a channel the last
// refresh did not know marks a refresh.
func (p *canvasPublisher) markHint(h notify.Hint) {
	if !p.s.cfg.CanvasEnabled {
		return
	}
	if h.ChannelID == 0 {
		p.markAll()
		return
	}
	at := p.window()
	p.mu.Lock()
	if scope, ok := p.chanScope[h.ChannelID]; ok {
		p.dirty.mark(scope, at)
	} else if _, ok := p.known[h.ChannelID]; !ok {
		p.unknown[h.ChannelID] = true
		p.dirty.mark("", at)
	}
	p.mu.Unlock()
	p.poke()
}

// refresh reloads the channel, scope and bridge facts in one Store.Read and
// installs them. The scope of a bridge is the scope whose members include its
// channel.
func (p *canvasPublisher) refresh(ctx context.Context) error {
	known := map[int64]string{}
	kinds := map[int64]store.Kind{}
	live := map[string][]store.Bridge{}
	err := p.s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		chans, err := tx.ListChannels(ctx)
		if err != nil {
			return err
		}
		for _, ch := range chans {
			known[ch.ID] = ""
			kinds[ch.ID] = ch.Kind
		}
		scs, err := tx.ListScopes(ctx)
		if err != nil {
			return err
		}
		for _, sc := range scs {
			if sc.State == store.ScopeRetired {
				continue
			}
			members, _, err := tx.ScopeMembers(ctx, sc.Name)
			if err != nil {
				return err
			}
			for _, ch := range members {
				known[ch.ID] = sc.Name
				kinds[ch.ID] = ch.Kind
			}
		}
		bridges, err := tx.ListBridges(ctx)
		if err != nil {
			return err
		}
		for _, b := range bridges {
			if b.State != store.BridgeLive || b.SlackChannelID == "" {
				continue
			}
			if scope := known[b.ChannelID]; scope != "" {
				live[scope] = append(live[scope], b)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	chanScope := map[int64]string{}
	for id, scope := range known {
		if scope != "" && kinds[id] != store.KindOrdinary && len(live[scope]) > 0 {
			chanScope[id] = scope
		}
	}
	liveIDs := map[int64]bool{}
	for _, bs := range live {
		for _, b := range bs {
			liveIDs[b.ID] = true
		}
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.known, p.chanScope, p.scopeBridges = known, chanScope, live
	for id := range p.state {
		if !liveIDs[id] {
			delete(p.state, id)
		}
	}
	if p.pendingAll {
		for scope := range live {
			p.dirty.mark(scope, p.pendingAt)
		}
		p.pendingAll = false
	}
	for id := range p.unknown {
		if _, ok := known[id]; !ok {
			continue // created after the read: its own refresh mark follows
		}
		if scope, ok := chanScope[id]; ok {
			p.dirty.mark(scope, now)
		}
		delete(p.unknown, id)
	}
	return nil
}

// liveBridge re-reads the bridge and its scope: false when the bridge is gone
// or no longer live, or the scope is gone or retired (a removal, an archive or
// a retirement between the refresh and the publish).
func (p *canvasPublisher) liveBridge(ctx context.Context, id int64, scope string) (store.Bridge, bool, error) {
	var b store.Bridge
	live := false
	err := p.s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		if b, err = tx.Bridge(ctx, id); err != nil {
			return err
		}
		sc, err := tx.Scope(ctx, scope)
		if err != nil {
			return err
		}
		live = b.State == store.BridgeLive && b.SlackChannelID != "" && sc.State != store.ScopeRetired
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return b, false, nil
	}
	return b, live, err
}

// run is the publisher goroutine of one lock tenure: it waits for the earliest
// deadline, refreshes when asked, and publishes each due scope's bridges. Its
// Slack calls carry their own timeouts and never hold up the other goroutines.
func (p *canvasPublisher) run(ctx context.Context, hints <-chan notify.Hint) {
	// The hint pump only reads hints and marks (a lock and a non-blocking
	// wake, no I/O): the subscription holds one hint, so a reader busy in a
	// Slack call would lose the hints that arrive meanwhile.
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		for {
			select {
			case <-ctx.Done():
				return
			case h, ok := <-hints:
				if !ok {
					return
				}
				p.markHint(h)
			}
		}
	}()
	defer func() { <-pumped }()
	for {
		p.mu.Lock()
		at, due := p.dirty.next()
		p.mu.Unlock()
		var timer *time.Timer
		var fire <-chan time.Time
		if due {
			timer = time.NewTimer(max(0, time.Until(at)))
			fire = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-p.wake:
		case <-fire:
			p.pass(ctx)
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// pass publishes the scopes that are due now.
func (p *canvasPublisher) pass(ctx context.Context) {
	p.mu.Lock()
	taken := p.dirty.take(time.Now())
	p.mu.Unlock()
	if len(taken) > 0 && taken[0] == "" {
		taken = taken[1:]
		if err := p.refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			p.s.log.Warn("slack bridge: canvas refresh failed", "err", err)
			p.mu.Lock()
			p.dirty.mark("", time.Now().Add(p.s.cfg.CanvasCoalesce))
			p.mu.Unlock()
		}
	}
	for _, scope := range taken {
		p.mu.Lock()
		bridges := append([]store.Bridge(nil), p.scopeBridges[scope]...)
		p.mu.Unlock()
		for _, b := range bridges {
			if ctx.Err() != nil {
				return
			}
			cur, ok, err := p.liveBridge(ctx, b.ID, scope)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				p.s.log.Warn("slack bridge: canvas bridge check failed", "scope", scope, "channel", b.ChannelName)
				p.remark(scope, p.window())
				continue
			}
			if !ok {
				p.drop(b.ID)
				continue
			}
			retryAt, err := p.s.PublishCanvas(ctx, cur, scope)
			if err != nil && ctx.Err() == nil {
				code := ""
				var ae *slackapi.APIError
				if errors.As(err, &ae) {
					code = ae.Code
				}
				// handle warned once for a credential code on this bridge.
				if code == "" || !p.warnedCred(cur.ID, code) {
					p.s.log.Warn("slack bridge: canvas publish failed",
						"scope", scope, "channel", cur.ChannelName, "slack_channel", cur.SlackChannelID, "code", code)
				}
			}
			if !retryAt.IsZero() {
				p.remark(scope, retryAt)
			}
		}
	}
}

// warnedCred reports whether handle has already warned about the credential
// code for the bridge.
func (p *canvasPublisher) warnedCred(id int64, code string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[id]
	return st != nil && st.warned["cred:"+code]
}

func (p *canvasPublisher) remark(scope string, at time.Time) {
	p.mu.Lock()
	p.dirty.mark(scope, at)
	p.mu.Unlock()
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
// archived bridge return (zero, nil). The publisher loop (run) is the only
// caller for a bridge, so the calls for one bridge never overlap.
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
	hash := CanvasHash(r.Markdown)

	w := p.begin(b.ID, scope)
	if hash == w.hash && w.accessSet && w.canvasID != "" {
		return time.Time{}, nil
	}
	if r.TasksTooLarge {
		s.log.Warn("slack bridge: task lists too large for a canvas", "scope", scope)
	}
	retryAt, archive, err := p.publish(ctx, b, &w, r.Markdown, hash)
	if archive {
		p.drop(b.ID)
		actx, cancel := context.WithTimeout(ctx, canvasCallTimeout)
		aerr := s.ArchiveBridge(actx, b, reasonSlackGone)
		cancel()
		if aerr != nil {
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
		if attempt > 0 {
			// The create said a canvas exists but the lookup after it found
			// none: look again next window, never create twice.
			return methodCanvasTabs, false, errCanvasLookup
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
