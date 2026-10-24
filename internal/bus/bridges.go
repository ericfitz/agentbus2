package bus

// BridgeNotConfigured is the fixed text of the forbidden error every bridge
// operation answers when no Slack bridge is wired: local mode, or a server
// started without one.
const BridgeNotConfigured = "the Slack bridge is not configured"

// BridgeState is the lifecycle state of a bridge (Slack bridge spec 6.1a).
type BridgeState string

// The bridge states.
const (
	BridgeCreating BridgeState = "creating"
	BridgeLive     BridgeState = "live"
	BridgeArchived BridgeState = "archived"
)

// Bridge is the link between a bus channel and a Slack channel. SlackChannelID
// is "" while the bridge is creating and before Slack answered; ArchivedAt is
// 0 until the bridge is archived.
type Bridge struct {
	Channel, Scope, SlackChannelID, SlackName string
	State                                     BridgeState
	CreatedAt, ArchivedAt                     int64
}

// CreateBridgeInput bridges Channel to a new private Slack channel. An empty
// SlackName takes the bridge's default name.
type CreateBridgeInput struct{ Channel, SlackName string }

// BridgeNotInvited names a human the bridge could not invite and why.
type BridgeNotInvited struct{ Identity, Reason string }

// BridgeCreated is the result of CreateBridge: the bridge and every human
// with access whom Slack could not invite.
type BridgeCreated struct {
	Bridge     Bridge
	NotInvited []BridgeNotInvited
}

// RemoveBridgeInput names the channel whose bridge is archived.
type RemoveBridgeInput struct{ Channel string }
