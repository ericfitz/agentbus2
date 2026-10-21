package server

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Channel, ACL and subscription handlers (Task 6). Each passes the identity
// from the context and the idempotency key from the header straight to the
// core; names arrive as bound by the generated code (Decision 80).

func idemKey(k *string) string {
	if k == nil {
		return ""
	}
	return *k
}

func channelACLFromGen(in []gen.ACLEntry) []bus.ACLEntry {
	out := make([]bus.ACLEntry, len(in))
	for i, e := range in {
		out[i] = bus.ACLEntry{Identity: e.Identity, Permissions: make([]bus.Permission, len(e.Permissions))}
		for j, p := range e.Permissions {
			out[i].Permissions[j] = bus.Permission(p)
		}
	}
	return out
}

func channelACLToGen(in []bus.ACLEntry) []gen.ACLEntry {
	out := make([]gen.ACLEntry, len(in))
	for i, e := range in {
		out[i] = gen.ACLEntry{Identity: e.Identity, Permissions: make([]gen.ACLEntryPermissions, len(e.Permissions))}
		for j, p := range e.Permissions {
			out[i].Permissions[j] = gen.ACLEntryPermissions(p)
		}
	}
	return out
}

func channelInfoToGen(c bus.ChannelInfo) gen.ChannelInfo {
	return gen.ChannelInfo{Name: c.Name, Kind: gen.ChannelInfoKind(c.Kind), Creator: c.Creator, CreatedAt: c.CreatedAt}
}

func (a *api) ListChannels(ctx context.Context, _ gen.ListChannelsRequestObject) (gen.ListChannelsResponseObject, error) {
	chs, err := a.channels.List(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := make(gen.ListChannels200JSONResponse, len(chs))
	for i, c := range chs {
		out[i] = gen.Channel{Name: c.Name, Kind: gen.ChannelKind(c.Kind)}
	}
	return out, nil
}

func (a *api) CreateChannel(ctx context.Context, req gen.CreateChannelRequestObject) (gen.CreateChannelResponseObject, error) {
	in := bus.CreateChannelInput{IdempotencyKey: idemKey(req.Params.IdempotencyKey)}
	if req.Body != nil {
		in.Name = req.Body.Name
		if req.Body.Kind != nil {
			in.Kind = bus.Kind(*req.Body.Kind)
		}
		// Absent acl is nil (the configured default); [] is empty (Milestone 2 choice 9).
		if req.Body.Acl != nil {
			in.ACL = channelACLFromGen(*req.Body.Acl)
		}
	}
	info, err := a.channels.Create(ctx, IdentityFrom(ctx), in)
	if err != nil {
		return nil, err
	}
	return gen.CreateChannel201JSONResponse(channelInfoToGen(info)), nil
}

func (a *api) DeleteChannel(ctx context.Context, req gen.DeleteChannelRequestObject) (gen.DeleteChannelResponseObject, error) {
	err := a.channels.Delete(ctx, IdentityFrom(ctx), bus.DeleteChannelInput{Name: req.Channel, IdempotencyKey: idemKey(req.Params.IdempotencyKey)})
	if err != nil {
		return nil, err
	}
	return gen.DeleteChannel204Response{}, nil
}

func (a *api) RenameChannel(ctx context.Context, req gen.RenameChannelRequestObject) (gen.RenameChannelResponseObject, error) {
	in := bus.RenameChannelInput{Name: req.Channel, IdempotencyKey: idemKey(req.Params.IdempotencyKey)}
	if req.Body != nil {
		in.NewName = req.Body.Name
	}
	if err := a.channels.Rename(ctx, IdentityFrom(ctx), in); err != nil {
		return nil, err
	}
	return gen.RenameChannel204Response{}, nil
}

func (a *api) GetACL(ctx context.Context, req gen.GetACLRequestObject) (gen.GetACLResponseObject, error) {
	acl, err := a.channels.ACL(ctx, IdentityFrom(ctx), req.Channel)
	if err != nil {
		return nil, err
	}
	return gen.GetACL200JSONResponse(channelACLToGen(acl)), nil
}

func (a *api) SetACL(ctx context.Context, req gen.SetACLRequestObject) (gen.SetACLResponseObject, error) {
	in := bus.SetACLInput{Channel: req.Channel, IdempotencyKey: idemKey(req.Params.IdempotencyKey)}
	if req.Body != nil {
		in.Entries = channelACLFromGen(*req.Body)
	}
	if err := a.channels.SetACL(ctx, IdentityFrom(ctx), in); err != nil {
		return nil, err
	}
	return gen.SetACL204Response{}, nil
}

func (a *api) Subscribe(ctx context.Context, req gen.SubscribeRequestObject) (gen.SubscribeResponseObject, error) {
	in := bus.SubscribeInput{Channel: req.Channel, IdempotencyKey: idemKey(req.Params.IdempotencyKey)}
	if req.Body != nil && req.Body.From != nil {
		in.From = string(*req.Body.From)
	}
	if err := a.channels.Subscribe(ctx, IdentityFrom(ctx), in); err != nil {
		return nil, err
	}
	return gen.Subscribe204Response{}, nil
}

func (a *api) Unsubscribe(ctx context.Context, req gen.UnsubscribeRequestObject) (gen.UnsubscribeResponseObject, error) {
	err := a.channels.Unsubscribe(ctx, IdentityFrom(ctx), bus.UnsubscribeInput{Channel: req.Channel, IdempotencyKey: idemKey(req.Params.IdempotencyKey)})
	if err != nil {
		return nil, err
	}
	return gen.Unsubscribe204Response{}, nil
}

func (a *api) ListTagSubscriptions(ctx context.Context, _ gen.ListTagSubscriptionsRequestObject) (gen.ListTagSubscriptionsResponseObject, error) {
	sets, err := a.channels.TagSubscriptions(ctx, IdentityFrom(ctx))
	if err != nil {
		return nil, err
	}
	out := make(gen.ListTagSubscriptions200JSONResponse, len(sets))
	for i, s := range sets {
		out[i] = append([]string{}, s...)
	}
	return out, nil
}

func (a *api) SubscribeTags(ctx context.Context, req gen.SubscribeTagsRequestObject) (gen.SubscribeTagsResponseObject, error) {
	in := bus.TagSubscribeInput{IdempotencyKey: idemKey(req.Params.IdempotencyKey)}
	if req.Body != nil {
		in.Patterns = req.Body.Patterns
	}
	if err := a.channels.SubscribeTags(ctx, IdentityFrom(ctx), in); err != nil {
		return nil, err
	}
	return gen.SubscribeTags204Response{}, nil
}

func (a *api) UnsubscribeTags(ctx context.Context, req gen.UnsubscribeTagsRequestObject) (gen.UnsubscribeTagsResponseObject, error) {
	err := a.channels.UnsubscribeTags(ctx, IdentityFrom(ctx), bus.TagSubscribeInput{Patterns: req.Patterns, IdempotencyKey: idemKey(req.Params.IdempotencyKey)})
	if err != nil {
		return nil, err
	}
	return gen.UnsubscribeTags204Response{}, nil
}
