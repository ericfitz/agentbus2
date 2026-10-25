package store

import "context"

// BridgeState is the lifecycle state of a Slack bridge (Slack bridge spec 8).
type BridgeState string

const (
	BridgeCreating BridgeState = "creating"
	BridgeLive     BridgeState = "live"
	BridgeArchived BridgeState = "archived"
)

// Bridge maps one bus channel to one Slack channel. ChannelID has no foreign
// key: a bridge outlives a channel removed behind its back (spec 6.1b).
// SlackChannelID is "" until the Slack channel exists; ArchivedAt is 0 until
// the bridge is archived; CursorTS is the Slack ts of the newest Slack
// message handled ("" before any).
type Bridge struct {
	ID, ChannelID             int64
	ChannelName, Scope        string
	SlackChannelID, SlackName string
	State                     BridgeState
	CursorTS                  string
	CreatedAt, ArchivedAt     int64
}

// BridgeMessage maps a bus message (Seq) to the Slack message (SlackTS)
// carrying it; RootTS is the ts of the thread root (SlackTS itself for a
// top-level post).
type BridgeMessage struct {
	BridgeID, Seq   int64
	SlackTS, RootTS string
	CreatedAt       int64
}

// SlackUser binds a human identity to a Slack account.
type SlackUser struct {
	Identity, TeamID, UserID string
	CreatedAt                int64
}

// Bridges holds the Slack bridge state (spec 8). Slack ts values are
// fixed-width "seconds.micros" ASCII, so a byte-order comparison orders them.
type Bridges interface {
	// CreateBridge inserts b in state creating (b.State must be "" or
	// creating) and returns it with its ID. ErrConflict when the channel has
	// a row or the Slack channel ID is taken; ErrInvalid for an empty
	// ChannelName, SlackName or Scope, a text failing TextRule, another
	// State, a ChannelID <= 0 or a non-zero ArchivedAt.
	CreateBridge(ctx context.Context, b Bridge) (Bridge, error)
	Bridge(ctx context.Context, id int64) (Bridge, error)                            // ErrNotFound
	BridgeByChannel(ctx context.Context, channelID int64) (Bridge, error)            // ErrNotFound
	BridgeBySlackChannel(ctx context.Context, slackChannelID string) (Bridge, error) // ErrNotFound
	ListBridges(ctx context.Context) ([]Bridge, error)                               // by channel name in byte order
	ScopeBridges(ctx context.Context, scope string) ([]Bridge, error)                // by channel name in byte order
	// SetBridgeSlackChannel records the Slack channel ID: only in creating
	// (ErrConflict otherwise, or when another bridge has the ID); ErrInvalid
	// when empty; nil when the bridge already has that ID.
	SetBridgeSlackChannel(ctx context.Context, id int64, slackChannelID string) error
	// SetBridgeLive: creating with a Slack ID becomes live; live is nil; no
	// Slack ID is ErrInvalid; archived is ErrConflict.
	SetBridgeLive(ctx context.Context, id int64) error
	// SetBridgeArchived: any state becomes archived with ArchivedAt = now;
	// already archived is nil and keeps its ArchivedAt.
	SetBridgeArchived(ctx context.Context, id, now int64) error
	// AdvanceBridgeCursor sets CursorTS to ts when ts is greater in byte
	// order (never moves it back); ErrNotFound for an unknown bridge.
	AdvanceBridgeCursor(ctx context.Context, id int64, ts string) error
	// DeleteBridge removes the bridge with its map and drop rows; ErrNotFound.
	DeleteBridge(ctx context.Context, id int64) error
	// PutBridgeMessage: ErrConflict on (bridge, seq) or (bridge, slack_ts);
	// ErrNotFound for an unknown bridge; ErrInvalid for an empty SlackTS or
	// RootTS.
	PutBridgeMessage(ctx context.Context, m BridgeMessage) error
	BridgeMessageBySeq(ctx context.Context, bridgeID, seq int64) (BridgeMessage, error)      // ErrNotFound
	BridgeMessageByTS(ctx context.Context, bridgeID int64, ts string) (BridgeMessage, error) // ErrNotFound
	// NewestInThread answers the largest seq in the thread rooted at rootTS
	// whose slack_ts < beforeTS; ErrNotFound when there is none.
	NewestInThread(ctx context.Context, bridgeID int64, rootTS, beforeTS string) (int64, error)
	// ThreadRoots answers the distinct root_ts values having a map row with
	// slack_ts >= sinceTS, the most recently active thread first, at most
	// limit (ErrInvalid when limit < 1).
	ThreadRoots(ctx context.Context, bridgeID int64, sinceTS string, limit int) ([]string, error)
	// PutBridgeDrop records a Slack message dropped as unauthorized;
	// idempotent; ErrNotFound for an unknown bridge; ErrInvalid for an empty ts.
	PutBridgeDrop(ctx context.Context, bridgeID int64, ts string, now int64) error
	BridgeDropped(ctx context.Context, bridgeID int64, ts string) (bool, error)
	SlackUserOf(ctx context.Context, identity string) (SlackUser, error)            // ErrNotFound
	IdentityOfSlackUser(ctx context.Context, teamID, userID string) (string, error) // ErrNotFound
	// PutSlackUser binds u; the same row again is nil; the identity bound to
	// another Slack user, or the Slack user bound to another identity, is
	// ErrConflict; an unknown identity is ErrNotFound; an empty TeamID or
	// UserID is ErrInvalid.
	PutSlackUser(ctx context.Context, u SlackUser) error
	// DeleteSlackUser reports whether a binding existed.
	DeleteSlackUser(ctx context.Context, identity string) (bool, error)
}
