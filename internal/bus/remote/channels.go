package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Channel, ACL and subscription methods. Every channel name goes to the
// generated client as given (choice 17); the generated client escapes it.

func malformed(what string) error {
	return &ab2bus.Error{Code: ab2bus.CodeInternal, Message: "malformed " + what + " response"}
}

func (b *Bus) ListChannels(ctx context.Context) ([]ab2bus.Channel, error) {
	r, err := b.c.ListChannelsWithResponse(ctx)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, malformed("ListChannels")
	}
	out := make([]ab2bus.Channel, len(*r.JSON200))
	for i, c := range *r.JSON200 {
		out[i] = ab2bus.Channel{Name: c.Name, Kind: fromGenKind(c.Kind)}
	}
	return out, nil
}

func (b *Bus) CreateChannel(ctx context.Context, in ab2bus.CreateChannelInput) (ab2bus.ChannelInfo, error) {
	body := gen.CreateChannelRequest{Name: in.Name}
	if in.Kind != "" {
		k := gen.CreateChannelRequestKind(in.Kind)
		body.Kind = &k
	}
	if in.ACL != nil { // nil is the configured default; empty is "no entries"
		acl := make([]gen.ACLEntry, len(in.ACL))
		for i, e := range in.ACL {
			acl[i] = toGenACLEntry(e)
		}
		body.Acl = &acl
	}
	r, err := b.c.CreateChannelWithResponse(ctx, &gen.CreateChannelParams{IdempotencyKey: key(in.IdempotencyKey)}, body)
	if err != nil {
		return ab2bus.ChannelInfo{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return ab2bus.ChannelInfo{}, err
	}
	if r.JSON201 == nil {
		return ab2bus.ChannelInfo{}, malformed("CreateChannel")
	}
	return fromGenChannelInfo(*r.JSON201), nil
}

func fromGenChannelInfo(c gen.ChannelInfo) ab2bus.ChannelInfo {
	return ab2bus.ChannelInfo{
		Channel: ab2bus.Channel{Name: c.Name, Kind: ab2bus.Kind(c.Kind)},
		Creator: c.Creator, CreatedAt: c.CreatedAt,
	}
}

func (b *Bus) DeleteChannel(ctx context.Context, in ab2bus.DeleteChannelInput) error {
	r, err := b.c.DeleteChannelWithResponse(ctx, in.Name, &gen.DeleteChannelParams{IdempotencyKey: key(in.IdempotencyKey)})
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func (b *Bus) RenameChannel(ctx context.Context, in ab2bus.RenameChannelInput) error {
	r, err := b.c.RenameChannelWithResponse(ctx, in.Name, &gen.RenameChannelParams{IdempotencyKey: key(in.IdempotencyKey)},
		gen.RenameChannelRequest{Name: in.NewName})
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func (b *Bus) ACL(ctx context.Context, channel string) ([]ab2bus.ACLEntry, error) {
	r, err := b.c.GetACLWithResponse(ctx, channel)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, malformed("ACL")
	}
	out := make([]ab2bus.ACLEntry, len(*r.JSON200))
	for i, e := range *r.JSON200 {
		out[i] = fromGenACLEntry(e)
	}
	return out, nil
}

func (b *Bus) SetACL(ctx context.Context, in ab2bus.SetACLInput) error {
	entries := make([]gen.ACLEntry, len(in.Entries))
	for i, e := range in.Entries {
		entries[i] = toGenACLEntry(e)
	}
	r, err := b.c.SetACLWithResponse(ctx, in.Channel, &gen.SetACLParams{IdempotencyKey: key(in.IdempotencyKey)}, entries)
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func (b *Bus) Subscribe(ctx context.Context, in ab2bus.SubscribeInput) error {
	var body gen.SubscribeRequest
	if in.From != "" {
		f := gen.SubscribeRequestFrom(in.From)
		body.From = &f
	}
	r, err := b.c.SubscribeWithResponse(ctx, in.Channel, &gen.SubscribeParams{IdempotencyKey: key(in.IdempotencyKey)}, body)
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func (b *Bus) Unsubscribe(ctx context.Context, in ab2bus.UnsubscribeInput) error {
	r, err := b.c.UnsubscribeWithResponse(ctx, in.Channel, &gen.UnsubscribeParams{IdempotencyKey: key(in.IdempotencyKey)})
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func (b *Bus) SubscribeTags(ctx context.Context, in ab2bus.TagSubscribeInput) error {
	r, err := b.c.SubscribeTagsWithResponse(ctx, &gen.SubscribeTagsParams{IdempotencyKey: key(in.IdempotencyKey)},
		gen.TagPatterns{Patterns: in.Patterns})
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func (b *Bus) UnsubscribeTags(ctx context.Context, in ab2bus.TagSubscribeInput) error {
	if len(in.Patterns) == 0 {
		// An empty set has no path segment: the wire would answer the
		// router's 404 for "/v1/tag-subscriptions/". Answer what the core
		// answers (the one client-side rule here; no path-parameter rule
		// is applied to any other input, choice 17).
		return &ab2bus.Error{Code: ab2bus.CodeValidation, Message: "a tag subscription needs at least one pattern"}
	}
	r, err := b.c.UnsubscribeTagsWithResponse(ctx, in.Patterns, &gen.UnsubscribeTagsParams{IdempotencyKey: key(in.IdempotencyKey)})
	if err != nil {
		return errorOf(ctx, 0, nil, nil, err)
	}
	return errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil)
}

func (b *Bus) TagSubscriptions(ctx context.Context) ([][]string, error) {
	r, err := b.c.ListTagSubscriptionsWithResponse(ctx)
	if err != nil {
		return nil, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return nil, err
	}
	if r.JSON200 == nil {
		return nil, malformed("TagSubscriptions")
	}
	return *r.JSON200, nil
}
