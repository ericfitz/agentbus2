// Package slackgo is the slackapi.Client adapter over github.com/slack-go/slack
// (Web API and Socket Mode). It is the only package that imports slack-go
// (scripts/m7-exit-checks.sh). No error text or log line carries a Slack token
// or a message body.
package slackgo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/slack-go/slack"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

const (
	pageLimit = 200
	// metadataType is the event_type of the metadata every posted message
	// carries (slackapi.PostInput).
	metadataType = "agentbus2_message"
)

// Options configures New. APIURL "" is Slack's; tests point it at httptest.
type Options struct {
	APIURL string
	HTTP   *http.Client
	Logger *slog.Logger
}

// Client implements slackapi.Client over slack-go.
type Client struct {
	api *slack.Client
	log *slog.Logger
}

var _ slackapi.Client = (*Client)(nil)

// New builds a Client. An app token without the xapp- prefix or a bot token
// without xoxb- is refused; the error names the setting, never the value.
func New(appToken, botToken string, opts Options) (*Client, error) {
	if !strings.HasPrefix(appToken, "xapp-") {
		return nil, errors.New(`slack_app_token_file: the app-level token must start with "xapp-"`)
	}
	if !strings.HasPrefix(botToken, "xoxb-") {
		return nil, errors.New(`slack_bot_token_file: the bot token must start with "xoxb-"`)
	}
	so := []slack.Option{slack.OptionAppLevelToken(appToken)}
	if opts.APIURL != "" {
		so = append(so, slack.OptionAPIURL(opts.APIURL))
	}
	if opts.HTTP != nil {
		so = append(so, slack.OptionHTTPClient(opts.HTTP))
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{api: slack.New(botToken, so...), log: log}, nil
}

// mapErr maps a slack-go error to the slackapi errors. method names the Web API
// method for the error text. nil maps to nil.
func mapErr(method string, err error) error {
	if err == nil {
		return nil
	}
	var rl *slack.RateLimitedError
	if errors.As(err, &rl) {
		return &slackapi.RateLimitedError{Method: method, RetryAfter: rl.RetryAfter}
	}
	var se slack.SlackErrorResponse
	if errors.As(err, &se) {
		switch se.Err {
		case "name_taken":
			return slackapi.ErrNameTaken
		case "already_archived":
			return slackapi.ErrAlreadyArchived
		case "channel_not_found", "is_archived":
			return slackapi.ErrChannelGone
		case "users_not_found", "user_not_found":
			return slackapi.ErrUserNotFound
		}
		return &slackapi.APIError{Method: method, Code: se.Err}
	}
	var sc slack.StatusCodeError
	if errors.As(err, &sc) {
		return &slackapi.APIError{Method: method, Code: "http_" + strconv.Itoa(sc.Code)}
	}
	return fmt.Errorf("slack: %s: %w", method, err)
}

// AuthTest implements slackapi.Client.
func (c *Client) AuthTest(ctx context.Context) (slackapi.Self, error) {
	r, err := c.api.AuthTestContext(ctx)
	if err != nil {
		return slackapi.Self{}, mapErr("auth.test", err)
	}
	return slackapi.Self{TeamID: r.TeamID, UserID: r.UserID, BotID: r.BotID}, nil
}

// CreatePrivateChannel implements slackapi.Client.
func (c *Client) CreatePrivateChannel(ctx context.Context, name string) (string, error) {
	ch, err := c.api.CreateConversationContext(ctx, slack.CreateConversationParams{ChannelName: name, IsPrivate: true})
	if err != nil {
		return "", mapErr("conversations.create", err)
	}
	return ch.ID, nil
}

// FindPrivateChannel implements slackapi.Client; archived channels are
// included because an archived channel keeps its name.
func (c *Client) FindPrivateChannel(ctx context.Context, name string) (slackapi.ChannelInfo, error) {
	cursor := ""
	for {
		chans, next, err := c.api.GetConversationsContext(ctx, &slack.GetConversationsParameters{
			Types: []string{"private_channel"}, ExcludeArchived: false, Limit: pageLimit, Cursor: cursor,
		})
		if err != nil {
			return slackapi.ChannelInfo{}, mapErr("conversations.list", err)
		}
		for _, ch := range chans {
			if ch.Name == name {
				return slackapi.ChannelInfo{ID: ch.ID, Name: ch.Name, Archived: ch.IsArchived}, nil
			}
		}
		if next == "" {
			return slackapi.ChannelInfo{}, slackapi.ErrChannelNotFound
		}
		cursor = next
	}
}

// inviteFatal are the conversations.invite codes that are not about one user:
// they fail the whole call (and every later user), so Invite returns them as an
// error instead of an InviteFailure.
var inviteFatal = map[string]bool{
	"channel_not_found": true, "is_archived": true, "not_in_channel": true,
	"method_not_supported_for_channel_type": true,
	"invalid_auth":                          true, "not_authed": true, "account_inactive": true,
	"token_revoked": true, "token_expired": true, "missing_scope": true, "access_denied": true,
	"fatal_error": true, "internal_error": true, "service_unavailable": true, "request_timeout": true,
}

// Invite implements slackapi.Client: one conversations.invite per user, no
// force, so each user's own code is visible.
func (c *Client) Invite(ctx context.Context, channelID string, userIDs []string) ([]slackapi.InviteFailure, error) {
	var failures []slackapi.InviteFailure
	for _, u := range userIDs {
		_, err := c.api.InviteUsersToConversationContext(ctx, channelID, u)
		if err == nil {
			continue
		}
		var se slack.SlackErrorResponse
		if errors.As(err, &se) {
			if se.Err == "already_in_channel" {
				continue
			}
			if !inviteFatal[se.Err] {
				failures = append(failures, slackapi.InviteFailure{User: u, Code: se.Err})
				continue
			}
		}
		return failures, mapErr("conversations.invite", err)
	}
	return failures, nil
}

// Archive implements slackapi.Client.
func (c *Client) Archive(ctx context.Context, channelID string) error {
	return mapErr("conversations.archive", c.api.ArchiveConversationContext(ctx, channelID))
}

// Post implements slackapi.Client.
func (c *Client) Post(ctx context.Context, in slackapi.PostInput) (string, error) {
	blocks := []slack.Block{
		slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, in.Section, false, false), nil, nil),
	}
	if len(in.Context) > 0 {
		els := make([]slack.MixedElement, 0, len(in.Context))
		for _, t := range in.Context {
			els = append(els, slack.NewTextBlockObject(slack.MarkdownType, t, false, false))
		}
		blocks = append(blocks, slack.NewContextBlock("", els...))
	}
	opts := []slack.MsgOption{
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(in.Fallback, false),
		slack.MsgOptionMetadata(slack.SlackMetadata{EventType: metadataType, EventPayload: map[string]any{"seq": in.Seq}}),
	}
	if in.Username != "" {
		opts = append(opts, slack.MsgOptionUsername(in.Username))
	}
	if in.IconEmoji != "" {
		opts = append(opts, slack.MsgOptionIconEmoji(in.IconEmoji))
	}
	if in.ThreadTS != "" {
		opts = append(opts, slack.MsgOptionTS(in.ThreadTS))
	}
	_, ts, err := c.api.PostMessageContext(ctx, in.Channel, opts...)
	if err != nil {
		return "", mapErr("chat.postMessage", err)
	}
	return ts, nil
}

// mapMessage converts a slack-go message; MetadataSeq is 0 unless the message
// carries the bridge's metadata with an integral seq.
func mapMessage(m slack.Msg) slackapi.Message {
	out := slackapi.Message{
		TS: m.Timestamp, ThreadTS: m.ThreadTimestamp, User: m.User, BotID: m.BotID,
		Subtype: m.SubType, Text: m.Text, ReplyCount: m.ReplyCount,
	}
	if m.Metadata.EventType == metadataType {
		switch v := m.Metadata.EventPayload["seq"].(type) {
		case float64:
			if v > 0 && v == float64(int64(v)) {
				out.MetadataSeq = int64(v)
			}
		case int64:
			out.MetadataSeq = max(v, 0)
		case int:
			out.MetadataSeq = max(int64(v), 0)
		}
	}
	return out
}

func mapMessages(ms []slack.Message) []slackapi.Message {
	out := make([]slackapi.Message, 0, len(ms))
	for _, m := range ms {
		out = append(out, mapMessage(m.Msg))
	}
	return out
}

// History implements slackapi.Client. Slack's per-page order (newest first) is
// returned as is.
func (c *Client) History(ctx context.Context, channelID, oldest, cursor string) (slackapi.Page, error) {
	r, err := c.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID: channelID, Oldest: oldest, Cursor: cursor, Limit: pageLimit, IncludeAllMetadata: true,
	})
	if err != nil {
		return slackapi.Page{}, mapErr("conversations.history", err)
	}
	return slackapi.Page{Messages: mapMessages(r.Messages), NextCursor: r.ResponseMetaData.NextCursor}, nil
}

// Replies implements slackapi.Client.
func (c *Client) Replies(ctx context.Context, channelID, threadTS, oldest, cursor string) (slackapi.Page, error) {
	ms, _, next, err := c.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channelID, Timestamp: threadTS, Oldest: oldest, Cursor: cursor, Limit: pageLimit, IncludeAllMetadata: true,
	})
	if err != nil {
		return slackapi.Page{}, mapErr("conversations.replies", err)
	}
	return slackapi.Page{Messages: mapMessages(ms), NextCursor: next}, nil
}

// Members implements slackapi.Client.
func (c *Client) Members(ctx context.Context, channelID string) ([]string, error) {
	var all []string
	cursor := ""
	for {
		ids, next, err := c.api.GetUsersInConversationContext(ctx, &slack.GetUsersInConversationParameters{
			ChannelID: channelID, Cursor: cursor, Limit: pageLimit,
		})
		if err != nil {
			return nil, mapErr("conversations.members", err)
		}
		all = append(all, ids...)
		if next == "" {
			return all, nil
		}
		cursor = next
	}
}

func mapUser(u *slack.User) slackapi.User {
	name := u.Profile.DisplayName
	if name == "" {
		name = u.RealName
	}
	if name == "" {
		name = u.Profile.RealName
	}
	return slackapi.User{
		ID: u.ID, TeamID: u.TeamID, Email: u.Profile.Email, DisplayName: name,
		IsBot: u.IsBot, Deleted: u.Deleted, Restricted: u.IsRestricted,
		UltraRestricted: u.IsUltraRestricted, Stranger: u.IsStranger,
	}
}

// UserInfo implements slackapi.Client.
func (c *Client) UserInfo(ctx context.Context, userID string) (slackapi.User, error) {
	u, err := c.api.GetUserInfoContext(ctx, userID)
	if err != nil {
		return slackapi.User{}, mapErr("users.info", err)
	}
	return mapUser(u), nil
}

// LookupByEmail implements slackapi.Client.
func (c *Client) LookupByEmail(ctx context.Context, email string) (slackapi.User, error) {
	u, err := c.api.GetUserByEmailContext(ctx, email)
	if err != nil {
		return slackapi.User{}, mapErr("users.lookupByEmail", err)
	}
	return mapUser(u), nil
}
