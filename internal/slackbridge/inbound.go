package slackbridge

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/messaging"
	"github.com/ericfitz/agentbus2/internal/core/sessions"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/store"
)

// The outcomes of handling one Slack message (spec 6.3).
const (
	OutcomeSent      = "sent"      // appended on the bus
	OutcomeDuplicate = "duplicate" // already mapped: nothing to do
	OutcomeIgnored   = "ignored"   // not for the bus, and final
	OutcomeDropped   = "dropped"   // refused as unauthorized, recorded, final
	OutcomeRetry     = "retry"     // a transient failure: try again
)

const (
	// slackHint is the session hint of every Slack human (one session per
	// human per server; display "alice@corp.example/slack").
	slackHint = "slack"

	userCacheTTL = 10 * time.Minute

	// requeueAttempts is how many times the runner's worker requeues a retry
	// outcome before leaving the message to the next catch-up; the delay
	// starts at Config.RetryBase and doubles to requeueMax.
	requeueAttempts = 5
	requeueMax      = 30 * time.Second
)

type cachedUser struct {
	u  slackapi.User
	at time.Time
}

// userCache caches users.info answers for userCacheTTL, by the Service's
// clock.
type userCache struct {
	mu sync.Mutex
	m  map[string]cachedUser
}

// user answers the Slack user, from the cache when it is fresh.
func (s *Service) user(ctx context.Context, id string) (slackapi.User, error) {
	now := s.core.Now()
	s.users.mu.Lock()
	c, ok := s.users.m[id]
	s.users.mu.Unlock()
	if ok && now.Sub(c.at) < userCacheTTL {
		return c.u, nil
	}
	u, err := s.sc.UserInfo(ctx, id)
	if err != nil {
		return slackapi.User{}, err
	}
	s.users.mu.Lock()
	if s.users.m == nil {
		s.users.m = map[string]cachedUser{}
	}
	s.users.m[id] = cachedUser{u: u, at: now}
	s.users.mu.Unlock()
	return u, nil
}

// HandleEvent handles one Socket Mode event once, without a runner: a message
// event is processed (a retry outcome is left to the next catch-up) and a
// member_left_channel event runs the membership check of a done scope. Every
// other kind is ignored here. The runner does not call it: its worker owns
// the retries of a retry outcome (runner.go).
func (s *Service) HandleEvent(ctx context.Context, ev slackapi.Event) {
	switch ev.Kind {
	case slackapi.EventMessage:
		if s.ProcessEvent(ctx, ev) == OutcomeRetry {
			s.log.Warn("slack message left to the next catch-up", "slack_user", ev.Message.User, "slack_channel", ev.Channel)
		}
	case slackapi.EventMemberLeft:
		if err := s.memberLeft(ctx, ev.Channel); err != nil && ctx.Err() == nil {
			s.log.Warn("slack member check failed", "slack_channel", ev.Channel, "code", bus.CodeOf(err))
		}
	}
}

// ProcessEvent handles one message event and answers the outcome.
// Only EventMessage is handled; every other kind (a closed set: an unknown
// one too) is ignored.
func (s *Service) ProcessEvent(ctx context.Context, ev slackapi.Event) string {
	if ev.Kind != slackapi.EventMessage {
		return OutcomeIgnored
	}
	out, _ := s.handleMessage(ctx, ev.Channel, ev.Message, false)
	return out
}

// refusal names why a Slack user may not post, or "" when it may.
func (s *Service) refusal(u slackapi.User, self slackapi.Self) string {
	switch {
	case u.IsBot:
		return "bot user"
	case u.Deleted:
		return "deleted user"
	case u.Restricted || u.UltraRestricted:
		return "guest user"
	case u.Stranger:
		return "external user"
	case u.TeamID != self.TeamID:
		return "user of another workspace"
	case u.Email == "":
		return "user has no email"
	}
	return ""
}

// The fixed texts of the trust check; only these reach the drop log.
const (
	reasonNotHuman = "not a human identity"
	reasonNoWrite  = "no explicit write permission"
	reasonBinding  = "Slack account does not match the bound account"
	reasonConflict = "Slack user binding conflict"
)

// dropReason is the log reason of a forbidden answer: a trust text, else a
// fixed "refused" (a core authorization text names the identity, an email).
func dropReason(err error) string {
	if m := msgOf(err); m == reasonNotHuman || m == reasonNoWrite || m == reasonBinding || m == reasonConflict {
		return m
	}
	return "refused"
}

// errBridgeGone marks a bridge that was archived, removed or retired between
// the lookup and the write; the message is ignored.
var errBridgeGone = core.E(bus.CodeNotFound, "the bridge is no longer live")

// trust is the Decision 11 and 18 check, run before a session is opened and
// again inside the send transaction: the bridge is still live, the identity
// is a human, holds an explicit write entry, and is not bound to another
// Slack account (nor the Slack account to another identity). bind reports
// that neither side is bound yet. Lookups come first: on Postgres a failed
// statement aborts the transaction.
func trust(ctx context.Context, tx store.Tx, b store.Bridge, identity string, su store.SlackUser) (bind bool, err error) {
	cur, err := tx.Bridge(ctx, b.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && cur.State != store.BridgeLive) {
		return false, errBridgeGone
	}
	if err != nil {
		return false, err
	}
	ident, err := tx.Identity(ctx, identity)
	if err != nil {
		return false, err
	}
	if ident.Kind != store.IdentityHuman {
		return false, core.E(bus.CodeForbidden, reasonNotHuman)
	}
	acl, err := tx.ACL(ctx, b.ChannelID)
	if err != nil {
		return false, err
	}
	if !slices.ContainsFunc(acl, func(e store.ACLEntry) bool {
		return e.Identity == identity && slices.Contains(e.Permissions, store.PermWrite)
	}) {
		return false, core.E(bus.CodeForbidden, reasonNoWrite)
	}
	bound, errUser := tx.SlackUserOf(ctx, identity)
	if errUser != nil && !errors.Is(errUser, store.ErrNotFound) {
		return false, errUser
	}
	owner, errOwner := tx.IdentityOfSlackUser(ctx, su.TeamID, su.UserID)
	if errOwner != nil && !errors.Is(errOwner, store.ErrNotFound) {
		return false, errOwner
	}
	switch {
	case errUser == nil && errOwner == nil:
		if bound.TeamID != su.TeamID || bound.UserID != su.UserID || owner != identity {
			return false, core.E(bus.CodeForbidden, reasonBinding)
		}
		return false, nil
	case errUser == nil || errOwner == nil:
		return false, core.E(bus.CodeForbidden, reasonBinding)
	}
	return true, nil
}

// handleMessage handles one message of a bridged Slack channel (spec 6.3),
// from an event or, with catchUp, from a history read. It answers the
// outcome; the error is set only with OutcomeRetry, for the log. State
// (Definition of Done D): every path is idempotent. The ts map is the dedupe,
// so a second run, a redelivered envelope and an overlapping catch-up give
// duplicate; a drop is recorded once and final; a run that failed leaves no
// row (the send, the map row and the binding are one transaction).
func (s *Service) handleMessage(ctx context.Context, channelID string, m slackapi.Message, catchUp bool) (string, error) {
	self, err := s.requireReady()
	if err != nil {
		return OutcomeRetry, err
	}
	// 1. The bridge, live.
	var b store.Bridge
	err = s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		b, err = tx.BridgeBySlackChannel(ctx, channelID)
		return err
	})
	if errors.Is(err, store.ErrNotFound) || (err == nil && b.State != store.BridgeLive) {
		return OutcomeIgnored, nil
	}
	if err != nil {
		return OutcomeRetry, err
	}
	// 2. The app's own posts.
	if (m.BotID != "" && m.BotID == self.BotID) || (m.User != "" && m.User == self.UserID) {
		if catchUp && m.BotID != "" && m.BotID == self.BotID && m.MetadataSeq > 0 && slackapi.ValidTS(m.TS) {
			if err := s.repairOwn(ctx, b, m); err != nil {
				return OutcomeRetry, err
			}
		}
		return OutcomeIgnored, nil
	}
	// 3. Plain messages and thread broadcasts of a user, with a valid ts.
	if (m.Subtype != "" && m.Subtype != "thread_broadcast") || m.User == "" ||
		!slackapi.ValidTS(m.TS) || (m.ThreadTS != "" && !slackapi.ValidTS(m.ThreadTS)) ||
		strings.TrimSpace(m.Text) == "" {
		return OutcomeIgnored, nil
	}
	// 4. Mapped: done. Dropped before: final.
	var mapped, dropped bool
	err = s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		mapped, dropped = false, false
		_, err := tx.BridgeMessageByTS(ctx, b.ID, m.TS)
		if err == nil {
			mapped = true
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		dropped, err = tx.BridgeDropped(ctx, b.ID, m.TS)
		return err
	})
	if err != nil {
		return OutcomeRetry, err
	}
	if mapped {
		return OutcomeDuplicate, nil
	}
	if dropped {
		return OutcomeIgnored, nil
	}
	// 5. The Slack user.
	u, err := s.user(ctx, m.User)
	if errors.Is(err, slackapi.ErrUserNotFound) {
		return s.drop(ctx, b, m, "user not found in Slack")
	}
	if err != nil {
		return OutcomeRetry, err
	}
	if why := s.refusal(u, self); why != "" {
		return s.drop(ctx, b, m, why)
	}
	// 6. The human identity.
	identity, err := (identities.Service{Core: s.core}).LookupHumanByEmail(ctx, u.Email)
	switch bus.CodeOf(err) {
	case "":
	case bus.CodeNotFound:
		return s.drop(ctx, b, m, "no human identity for the Slack user")
	case bus.CodeValidation:
		return s.drop(ctx, b, m, "the Slack user's email is not valid")
	default:
		return OutcomeRetry, err
	}
	su := store.SlackUser{Identity: identity, TeamID: u.TeamID, UserID: u.ID}
	// Decisions 11 and 18 before any session or Slack call is made.
	err = s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := trust(ctx, tx, b, identity, su)
		return err
	})
	switch {
	case err == nil:
	case errors.Is(err, errBridgeGone):
		return OutcomeIgnored, nil
	case bus.CodeOf(err) == bus.CodeForbidden:
		return s.drop(ctx, b, m, dropReason(err))
	default:
		return OutcomeRetry, err
	}
	// 7 and 8. Where the message sits in its thread.
	var replyTo, after int64
	var quote string
	if m.ThreadTS != "" && m.ThreadTS != m.TS {
		var out string
		replyTo, after, quote, out, err = s.threadOf(ctx, b, channelID, self, m)
		if out != "" {
			return out, err
		}
	}
	if seq, ok, err := s.permalinkParent(ctx, b, channelID, m.Text); err != nil {
		return OutcomeRetry, err
	} else if ok {
		replyTo, after, quote = seq, seq, ""
	}
	// 9 and 10. The session, then the send with the map row and the binding.
	session, err := (sessions.Service{Core: s.core}).EnsureHinted(ctx, identity, slackHint)
	if err != nil {
		return OutcomeRetry, err
	}
	name := func(id string) string {
		if nu, err := s.user(ctx, id); err == nil {
			return nu.DisplayName
		}
		return ""
	}
	content := SlackToMarkdown(m.Text, name)
	if quote != "" {
		content = quoteLines(SlackToMarkdown(quote, name)) + "\n\n" + content
	}
	meta := map[string]string{}
	if u.DisplayName != "" {
		meta["slack_display_name"] = u.DisplayName
	}
	send := func(replyTo, after int64) error {
		in := bus.SendInput{Channel: b.ChannelName, Content: content, Session: session, Metadata: withAfter(meta, after)}
		if replyTo != 0 {
			in.ReplyTo = &replyTo
		}
		var gone bool
		_, err := (messaging.Service{Core: s.core}).SendWith(ctx, identity, in, func(ctx context.Context, tx store.Tx, seq int64) error {
			gone = false
			bind, err := trust(ctx, tx, b, identity, su)
			if errors.Is(err, errBridgeGone) {
				gone = true
				return err
			}
			if err != nil {
				return err
			}
			if bind {
				su.CreatedAt = s.core.NowMs()
				if err := tx.PutSlackUser(ctx, su); err != nil {
					if errors.Is(err, store.ErrConflict) {
						return core.E(bus.CodeForbidden, reasonConflict)
					}
					return err
				}
			}
			_, err = tx.BridgeMessageByTS(ctx, b.ID, m.TS)
			if err == nil {
				return core.E(bus.CodeConflict, "Slack message %s is already mapped", m.TS)
			}
			if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			root := m.ThreadTS
			if root == "" {
				root = m.TS
			}
			return tx.PutBridgeMessage(ctx, store.BridgeMessage{BridgeID: b.ID, Seq: seq, SlackTS: m.TS, RootTS: root, CreatedAt: s.core.NowMs()})
		})
		if gone {
			return errBridgeGone
		}
		return err
	}
	err = send(replyTo, after)
	if bus.CodeOf(err) == bus.CodeNotFound && !errors.Is(err, errBridgeGone) && replyTo != 0 {
		// The parent aged out of retention: top-level, without the pointer.
		err = send(0, 0)
	}
	switch {
	case err == nil:
		return OutcomeSent, nil
	case errors.Is(err, errBridgeGone):
		return OutcomeIgnored, nil
	}
	switch bus.CodeOf(err) {
	case bus.CodeConflict:
		return OutcomeDuplicate, nil
	case bus.CodeValidation:
		s.log.Info("slack message ignored", "slack_user", m.User, "slack_channel", b.SlackChannelID, "code", bus.CodeValidation)
		return OutcomeIgnored, nil
	case bus.CodeForbidden:
		return s.drop(ctx, b, m, dropReason(err))
	}
	return OutcomeRetry, err
}

func withAfter(meta map[string]string, after int64) map[string]string {
	out := make(map[string]string, len(meta)+1)
	for k, v := range meta {
		out[k] = v
	}
	if after != 0 {
		out["slack_after"] = strconv.FormatInt(after, 10)
	}
	return out
}

// quoteLines prefixes every line of s with "> ".
func quoteLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}

// msgOf is the message of a *bus.Error (never content: the texts the trust
// check and the hook raise are fixed).
func msgOf(err error) string {
	var be *bus.Error
	if errors.As(err, &be) {
		return be.Message
	}
	return "refused"
}

// drop records the message as dropped and logs it: the Slack user, the Slack
// channel and the reason, never the text or the email.
func (s *Service) drop(ctx context.Context, b store.Bridge, m slackapi.Message, reason string) (string, error) {
	err := s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutBridgeDrop(ctx, b.ID, m.TS, s.core.NowMs())
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) { // not found: the bridge was removed meanwhile
		return OutcomeRetry, err
	}
	s.log.Info("slack message dropped", "slack_user", m.User, "slack_channel", b.SlackChannelID, "reason", reason)
	return OutcomeDropped, nil
}

// repairOwn maps an own post a crashed worker did not map, and advances the
// slack-bridge acked mark to its seq (catch-up only; plan choice). The
// lookups come first because a failed statement ends the transaction.
func (s *Service) repairOwn(ctx context.Context, b store.Bridge, m slackapi.Message) error {
	return s.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		cur, err := tx.Bridge(ctx, b.ID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && cur.State != store.BridgeLive) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.BridgeMessageBySeq(ctx, b.ID, m.MetadataSeq); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err := tx.BridgeMessageByTS(ctx, b.ID, m.TS); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		msg, err := tx.Message(ctx, m.MetadataSeq)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (msg.ChannelID != b.ChannelID || msg.Sender == "")) {
			return nil // evicted, or not a message of this channel: nothing to map
		}
		if err != nil {
			return err
		}
		root := m.ThreadTS
		if root == "" || !slackapi.ValidTS(root) {
			root = m.TS
		}
		if err := tx.PutBridgeMessage(ctx, store.BridgeMessage{BridgeID: b.ID, Seq: m.MetadataSeq, SlackTS: m.TS, RootTS: root, CreatedAt: s.core.NowMs()}); err != nil {
			return err
		}
		return tx.AdvanceAckedMark(ctx, store.SlackBridge, b.ChannelID, m.MetadataSeq)
	})
}

// threadOf resolves a thread reply (step 7): the root's bus seq and the
// newest bridged message in the thread before the reply. A root with no map
// row posted by this app is quoted; any other unmapped root (its author may
// be unauthorized) is never quoted. out is non-empty when the message is
// finished (retry).
func (s *Service) threadOf(ctx context.Context, b store.Bridge, channelID string, self slackapi.Self, m slackapi.Message) (replyTo, after int64, quote, out string, err error) {
	err = s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		root, err := tx.BridgeMessageByTS(ctx, b.ID, m.ThreadTS)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		replyTo = root.Seq
		after, err = tx.NewestInThread(ctx, b.ID, m.ThreadTS, m.TS)
		if errors.Is(err, store.ErrNotFound) {
			after, err = 0, nil
		}
		return err
	})
	if err != nil {
		return 0, 0, "", OutcomeRetry, err
	}
	if replyTo != 0 {
		return replyTo, after, "", "", nil
	}
	page, err := s.sc.Replies(ctx, channelID, m.ThreadTS, "", "")
	var ae *slackapi.APIError
	switch {
	case err == nil:
	case errors.As(err, &ae) && ae.Code == "thread_not_found", errors.Is(err, slackapi.ErrChannelGone):
		return 0, 0, "", "", nil
	default:
		return 0, 0, "", OutcomeRetry, err
	}
	for _, r := range page.Messages {
		if r.TS == m.ThreadTS {
			if r.BotID != "" && r.BotID == self.BotID {
				quote = r.Text
			}
			break
		}
	}
	return 0, 0, quote, "", nil
}

// permalinkParent is step 8: the first permalink to this Slack channel whose
// ts is mapped names the parent.
func (s *Service) permalinkParent(ctx context.Context, b store.Bridge, channelID, text string) (int64, bool, error) {
	var seq int64
	var ok bool
	err := s.core.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		for _, pl := range FindPermalinks(text) {
			if pl.Channel != channelID {
				continue
			}
			row, err := tx.BridgeMessageByTS(ctx, b.ID, pl.TS)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			seq, ok = row.Seq, true
			return nil
		}
		return nil
	})
	return seq, ok, err
}
