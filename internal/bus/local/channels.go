package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/channels"
)

func (b *Bus) channels() channels.Service { return channels.Service{Core: b.c} }

func (b *Bus) ListChannels(ctx context.Context) ([]ab2bus.Channel, error) {
	return b.channels().List(ctx, b.identity)
}

func (b *Bus) CreateChannel(ctx context.Context, in ab2bus.CreateChannelInput) (ab2bus.ChannelInfo, error) {
	return b.channels().Create(ctx, b.identity, in)
}

// DeleteChannel serves both Messaging and Admin (identical signatures).
func (b *Bus) DeleteChannel(ctx context.Context, in ab2bus.DeleteChannelInput) error {
	return b.channels().Delete(ctx, b.identity, in)
}

// RenameChannel serves both Messaging and Admin (identical signatures).
func (b *Bus) RenameChannel(ctx context.Context, in ab2bus.RenameChannelInput) error {
	return b.channels().Rename(ctx, b.identity, in)
}

func (b *Bus) ACL(ctx context.Context, channel string) ([]ab2bus.ACLEntry, error) {
	return b.channels().ACL(ctx, b.identity, channel)
}

func (b *Bus) SetACL(ctx context.Context, in ab2bus.SetACLInput) error {
	return b.channels().SetACL(ctx, b.identity, in)
}

func (b *Bus) Subscribe(ctx context.Context, in ab2bus.SubscribeInput) error {
	return b.channels().Subscribe(ctx, b.identity, in)
}

func (b *Bus) Unsubscribe(ctx context.Context, in ab2bus.UnsubscribeInput) error {
	return b.channels().Unsubscribe(ctx, b.identity, in)
}

func (b *Bus) SubscribeTags(ctx context.Context, in ab2bus.TagSubscribeInput) error {
	return b.channels().SubscribeTags(ctx, b.identity, in)
}

func (b *Bus) UnsubscribeTags(ctx context.Context, in ab2bus.TagSubscribeInput) error {
	return b.channels().UnsubscribeTags(ctx, b.identity, in)
}

func (b *Bus) TagSubscriptions(ctx context.Context) ([][]string, error) {
	return b.channels().TagSubscriptions(ctx, b.identity)
}
