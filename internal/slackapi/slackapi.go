// Package slackapi is the seam between the Slack bridge and Slack: the
// Client interface, its domain types and its errors. It imports only the
// standard library; the adapter over slack-go is internal/slackapi/slackgo
// and the in-memory fake is internal/slackapi/slackfake. No error text
// carries a Slack token.
package slackapi

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNameTaken is Slack's name_taken: a channel of that name exists
	// (an archived one keeps its name).
	ErrNameTaken = errors.New("slack: channel name is taken")
	// ErrAlreadyArchived is already_archived.
	ErrAlreadyArchived = errors.New("slack: channel is already archived")
	// ErrChannelGone is channel_not_found or is_archived: the channel can
	// no longer be posted to.
	ErrChannelGone = errors.New("slack: channel not found or archived")
	// ErrChannelNotFound is a FindPrivateChannel miss.
	ErrChannelNotFound = errors.New("slack: no such private channel")
	// ErrUserNotFound is users_not_found or user_not_found.
	ErrUserNotFound = errors.New("slack: user not found")
)

// RateLimitedError is a 429 answer; RetryAfter is the Retry-After wait.
type RateLimitedError struct {
	Method     string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("slack: %s: rate limited, retry after %s", e.Method, e.RetryAfter)
}

// APIError is any other Slack error code.
type APIError struct {
	Method, Code string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("slack: %s: %s", e.Method, e.Code)
}

// Self is the app's own identity, from auth.test.
type Self struct{ TeamID, UserID, BotID string }

// User is a Slack user as users.info and users.lookupByEmail report it.
type User struct {
	ID, TeamID, Email, DisplayName                        string
	IsBot, Deleted, Restricted, UltraRestricted, Stranger bool
}

// Message is a Slack message. MetadataSeq is the bus sequence number from the
// message metadata the bridge wrote (0: none).
type Message struct {
	TS, ThreadTS, User, BotID, Subtype, Text string
	ReplyCount                               int
	MetadataSeq                              int64
}

// Page is one page of messages in ascending ts order; NextCursor is empty on
// the last page.
type Page struct {
	Messages   []Message
	NextCursor string
}

// ChannelInfo is a private channel found by name.
type ChannelInfo struct {
	ID, Name string
	Archived bool
}

// InviteFailure is one user's refusal during Invite (for example
// user_not_found).
type InviteFailure struct{ User, Code string }

// PostInput is a message to post. Section is mrkdwn, Context the tag chips,
// Fallback the plain notification text; Seq goes into the message metadata
// {"event_type": "agentbus2_message", "event_payload": {"seq": N}}.
type PostInput struct {
	Channel, Fallback, Section string
	Context                    []string
	Username, IconEmoji        string
	ThreadTS                   string
	Seq                        int64
}

// EventKind names a Socket Mode event the bridge handles.
type EventKind string

const (
	EventConnected  EventKind = "connected"
	EventMessage    EventKind = "message"
	EventMemberLeft EventKind = "member_left_channel"
)

// Event is one handled Socket Mode event. EnvelopeID is empty for
// EventConnected.
type Event struct {
	Kind          EventKind
	EnvelopeID    string
	Channel, User string
	Message       Message
}

// Client is every Slack call the bridge makes.
//
// Invite invites one user per Web API call (no force) so each user's error
// code is visible. A refusal about one user (user_not_found,
// cant_invite_guest and similar) is an InviteFailure in the result and the
// other users are still invited; already_in_channel counts as success. A
// failure that applies to every user is returned as an error (with the
// failures so far): a channel-level code (channel_not_found, is_archived,
// not_in_channel), a credential code (invalid_auth, not_authed,
// account_inactive, token_revoked, missing_scope), a server or HTTP error, a
// rate limit or a transport error.
//
// History's per-page order is unspecified (real Slack returns newest first);
// callers that need order read all pages and sort by ts.
//
// RunSocket acks each envelope before calling handle, calls handle with
// EventConnected after every (re)connect, and returns nil when ctx ends.
type Client interface {
	AuthTest(ctx context.Context) (Self, error)
	CreatePrivateChannel(ctx context.Context, name string) (string, error)
	FindPrivateChannel(ctx context.Context, name string) (ChannelInfo, error)
	Invite(ctx context.Context, channelID string, userIDs []string) ([]InviteFailure, error)
	Archive(ctx context.Context, channelID string) error
	Post(ctx context.Context, in PostInput) (string, error)
	History(ctx context.Context, channelID, oldest, cursor string) (Page, error)
	Replies(ctx context.Context, channelID, threadTS, oldest, cursor string) (Page, error)
	Members(ctx context.Context, channelID string) ([]string, error)
	UserInfo(ctx context.Context, userID string) (User, error)
	LookupByEmail(ctx context.Context, email string) (User, error)
	RunSocket(ctx context.Context, handle func(context.Context, Event)) error
}

// ValidTS reports whether ts is a Slack timestamp: ten digits, a dot, six
// digits.
func ValidTS(ts string) bool {
	if len(ts) != 17 || ts[10] != '.' {
		return false
	}
	for i := range len(ts) {
		if i != 10 && (ts[i] < '0' || ts[i] > '9') {
			return false
		}
	}
	return true
}

// ValidChannelName reports whether name is 1 to 80 characters of a-z, 0-9,
// "-" and "_".
func ValidChannelName(name string) bool {
	if len(name) == 0 || len(name) > 80 {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}
