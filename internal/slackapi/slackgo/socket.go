package slackgo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

// RunSocket implements slackapi.Client over Socket Mode. Each envelope is
// acked before handle sees it; only message and member_left_channel events
// reach handle, every other envelope (including an inner event type slack-go
// cannot parse) is acked and ignored. socketmode reconnects by itself after a
// disconnect; handle gets EventConnected after each (re)connect. It returns
// nil when ctx ends, and an error on invalid_auth or when the connection
// cannot be reestablished.
func (c *Client) RunSocket(ctx context.Context, handle func(context.Context, slackapi.Event)) error {
	smc := socketmode.New(c.api)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	run := c.socketRun
	if run == nil {
		run = func(ctx context.Context, smc *socketmode.Client) error { return smc.RunContext(ctx) }
	}
	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, smc) }()

	for {
		select {
		case <-ctx.Done():
			c.awaitSocketStop(runErr) // no overlap with a restarted socket
			return nil
		case err := <-runErr:
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return socketRunError(err)
		case evt := <-smc.Events:
			if err := c.onSocketEvent(ctx, smc, evt, handle); err != nil {
				cancel()
				c.awaitSocketStop(runErr)
				return err
			}
		}
	}
}

// defaultSocketStopWait bounds how long RunSocket waits for slack-go's
// RunContext to return after its context ends.
const defaultSocketStopWait = 10 * time.Second

// awaitSocketStop waits for the RunContext goroutine to finish so a restarted
// socket does not overlap it, but only for a bounded time: slack-go does not
// guarantee that RunContext returns promptly, and shutdown must not hang on
// it. On timeout the goroutine is abandoned (it holds no lock of ours).
func (c *Client) awaitSocketStop(runErr <-chan error) {
	wait := c.socketStopWait
	if wait <= 0 {
		wait = defaultSocketStopWait
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-runErr:
	case <-t.C:
		c.log.Warn("slack socket: the socket client did not stop in time; abandoning it", "wait", wait.String())
	}
}

// socketRunError names the setting for a credential refusal from
// apps.connections.open; the error text never holds the token.
func socketRunError(err error) error {
	var sc slack.StatusCodeError
	if errors.As(err, &sc) && sc.Code == http.StatusNotFound {
		return errors.New("slack socket: invalid_auth: Slack refused the app-level token (slack_app_token_file)")
	}
	switch err.Error() {
	case "invalid_auth", "account_inactive", "token_revoked", "not_authed":
		return fmt.Errorf("slack socket: %s: Slack refused the app-level token (slack_app_token_file)", err.Error())
	}
	return fmt.Errorf("slack socket: %w", err)
}

func (c *Client) ack(ctx context.Context, smc *socketmode.Client, envelopeID string) {
	if envelopeID == "" {
		return
	}
	if err := smc.AckCtx(ctx, envelopeID, nil); err != nil && ctx.Err() == nil {
		c.log.Warn("slack socket: ack failed", "envelope_id", envelopeID, "err", err.Error())
	}
}

func (c *Client) onSocketEvent(ctx context.Context, smc *socketmode.Client, evt socketmode.Event, handle func(context.Context, slackapi.Event)) error {
	switch evt.Type {
	case socketmode.EventTypeConnected:
		handle(ctx, slackapi.Event{Kind: slackapi.EventConnected})
	case socketmode.EventTypeInvalidAuth:
		return errors.New("slack socket: invalid_auth: Slack refused the app-level token (slack_app_token_file)")
	case socketmode.EventTypeEventsAPI:
		if evt.Request == nil {
			return nil
		}
		c.ack(ctx, smc, evt.Request.EnvelopeID)
		ev, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return nil
		}
		switch inner := ev.InnerEvent.Data.(type) {
		case *slackevents.MessageEvent:
			handle(ctx, slackapi.Event{
				Kind: slackapi.EventMessage, EnvelopeID: evt.Request.EnvelopeID,
				Channel: inner.Channel, User: inner.User,
				Message: slackapi.Message{
					TS: inner.TimeStamp, ThreadTS: inner.ThreadTimeStamp, User: inner.User,
					BotID: inner.BotID, Subtype: inner.SubType, Text: inner.Text,
				},
			})
		case *slackevents.MemberLeftChannelEvent:
			handle(ctx, slackapi.Event{
				Kind: slackapi.EventMemberLeft, EnvelopeID: evt.Request.EnvelopeID,
				Channel: inner.Channel, User: inner.User,
			})
		default:
			c.log.Debug("slack socket: ignored event", "inner_type", ev.InnerEvent.Type)
		}
	case socketmode.EventTypeErrorBadMessage:
		// slack-go could not parse the envelope (an inner event type it does
		// not know, for one): ack it so Slack stops redelivering, handle nothing.
		id, errType := "", ""
		if bm, ok := evt.Data.(*socketmode.ErrorBadMessage); ok {
			errType = fmt.Sprintf("%T", bm.Cause)
			var env struct {
				EnvelopeID string `json:"envelope_id"`
			}
			if json.Unmarshal(bm.Message, &env) == nil {
				id = env.EnvelopeID
			}
		}
		c.ack(ctx, smc, id)
		c.log.Warn("slack socket: dropped an unparsable envelope", "envelope_id", id, "error_type", errType)
	case socketmode.EventTypeSlashCommand, socketmode.EventTypeInteractive:
		if evt.Request != nil {
			c.ack(ctx, smc, evt.Request.EnvelopeID)
		}
		c.log.Debug("slack socket: ignored envelope", "type", string(evt.Type))
	default:
		c.log.Debug("slack socket: event", "type", string(evt.Type))
	}
	return nil
}
