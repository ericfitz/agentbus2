package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Bridges is the seam to the Slack bridge (spec 6.1a); the server imports no
// Slack package. Each method receives the authenticated identity and checks
// that it is an administrator.
type Bridges interface {
	Create(ctx context.Context, admin string, in bus.CreateBridgeInput) (bus.BridgeCreated, error)
	List(ctx context.Context, admin string) ([]bus.Bridge, error)
	Get(ctx context.Context, admin, channel string) (bus.Bridge, error)
	Remove(ctx context.Context, admin string, in bus.RemoveBridgeInput) error
}

// bridges is Options.Bridges, or the refusing stand-in.
func (a *api) bridges() Bridges {
	if a.opts.Bridges == nil {
		return noBridges{}
	}
	return a.opts.Bridges
}

// noBridges answers every call unavailable (not retryable) with
// bus.BridgeNotConfigured.
type noBridges struct{}

func notConfigured() error {
	return bus.Errorf(bus.CodeUnavailable, false, "%s", bus.BridgeNotConfigured)
}

func (noBridges) Create(context.Context, string, bus.CreateBridgeInput) (bus.BridgeCreated, error) {
	return bus.BridgeCreated{}, notConfigured()
}
func (noBridges) List(context.Context, string) ([]bus.Bridge, error) { return nil, notConfigured() }
func (noBridges) Get(context.Context, string, string) (bus.Bridge, error) {
	return bus.Bridge{}, notConfigured()
}
func (noBridges) Remove(context.Context, string, bus.RemoveBridgeInput) error {
	return notConfigured()
}

func bridgeToGen(b bus.Bridge) gen.Bridge {
	out := gen.Bridge{
		Channel: b.Channel, Scope: b.Scope, SlackName: b.SlackName,
		State: gen.BridgeState(b.State), CreatedAt: b.CreatedAt,
	}
	if b.SlackChannelID != "" {
		out.SlackChannelId = &b.SlackChannelID
	}
	if b.ArchivedAt != 0 {
		out.ArchivedAt = &b.ArchivedAt
	}
	return out
}

func (a *api) CreateBridge(ctx context.Context, req gen.CreateBridgeRequestObject) (gen.CreateBridgeResponseObject, error) {
	in := bus.CreateBridgeInput{Channel: req.Body.Channel}
	if req.Body.SlackName != nil {
		in.SlackName = *req.Body.SlackName
	}
	res, err := a.bridges().Create(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	// not_invited is required on the wire: an empty array, never null.
	out := gen.BridgeCreated{Bridge: bridgeToGen(res.Bridge), NotInvited: make([]gen.BridgeNotInvited, len(res.NotInvited))}
	for i, n := range res.NotInvited {
		out.NotInvited[i] = gen.BridgeNotInvited{Identity: n.Identity, Reason: n.Reason}
	}
	return gen.CreateBridge200JSONResponse(out), nil
}

func (a *api) ListBridges(ctx context.Context, _ gen.ListBridgesRequestObject) (gen.ListBridgesResponseObject, error) {
	all, err := a.bridges().List(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := gen.BridgeList{Bridges: make([]gen.Bridge, len(all))}
	for i, b := range all {
		out.Bridges[i] = bridgeToGen(b)
	}
	return gen.ListBridges200JSONResponse(out), nil
}

func (a *api) GetBridge(ctx context.Context, req gen.GetBridgeRequestObject) (gen.GetBridgeResponseObject, error) {
	b, err := a.bridges().Get(ctx, IdentityFrom(ctx), req.Channel)
	if err != nil {
		return nil, err
	}
	return gen.GetBridge200JSONResponse(bridgeToGen(b)), nil
}

func (a *api) RemoveBridge(ctx context.Context, req gen.RemoveBridgeRequestObject) (gen.RemoveBridgeResponseObject, error) {
	if err := a.bridges().Remove(ctx, IdentityFrom(ctx), bus.RemoveBridgeInput{Channel: req.Channel}); err != nil {
		return nil, err
	}
	return gen.RemoveBridge204Response{}, nil
}
