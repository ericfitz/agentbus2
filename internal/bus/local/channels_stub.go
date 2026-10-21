// This file is replaced by Task 4: channels, ACL operations and subscriptions.

package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) ListChannels(ctx context.Context) ([]ab2bus.Channel, error) {
	return nil, ErrNotImplemented
}

func (b *Bus) CreateChannel(ctx context.Context, in ab2bus.CreateChannelInput) (ab2bus.ChannelInfo, error) {
	return ab2bus.ChannelInfo{}, ErrNotImplemented
}

// DeleteChannel serves both Messaging and Admin (identical signatures).
func (b *Bus) DeleteChannel(ctx context.Context, in ab2bus.DeleteChannelInput) error {
	return ErrNotImplemented
}

// RenameChannel serves both Messaging and Admin (identical signatures).
func (b *Bus) RenameChannel(ctx context.Context, in ab2bus.RenameChannelInput) error {
	return ErrNotImplemented
}

func (b *Bus) ACL(ctx context.Context, channel string) ([]ab2bus.ACLEntry, error) {
	return nil, ErrNotImplemented
}

func (b *Bus) SetACL(ctx context.Context, in ab2bus.SetACLInput) error { return ErrNotImplemented }

func (b *Bus) Subscribe(ctx context.Context, in ab2bus.SubscribeInput) error {
	return ErrNotImplemented
}

func (b *Bus) Unsubscribe(ctx context.Context, in ab2bus.UnsubscribeInput) error {
	return ErrNotImplemented
}

func (b *Bus) SubscribeTags(ctx context.Context, in ab2bus.TagSubscribeInput) error {
	return ErrNotImplemented
}

func (b *Bus) UnsubscribeTags(ctx context.Context, in ab2bus.TagSubscribeInput) error {
	return ErrNotImplemented
}

func (b *Bus) TagSubscriptions(ctx context.Context) ([][]string, error) {
	return nil, ErrNotImplemented
}
