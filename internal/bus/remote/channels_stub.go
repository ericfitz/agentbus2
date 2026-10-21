package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) ListChannels(_ context.Context) ([]ab2bus.Channel, error) {
	return nil, notImplemented("ListChannels")
}

func (b *Bus) CreateChannel(_ context.Context, _ ab2bus.CreateChannelInput) (ab2bus.ChannelInfo, error) {
	return ab2bus.ChannelInfo{}, notImplemented("CreateChannel")
}

func (b *Bus) DeleteChannel(_ context.Context, _ ab2bus.DeleteChannelInput) error {
	return notImplemented("DeleteChannel")
}

func (b *Bus) RenameChannel(_ context.Context, _ ab2bus.RenameChannelInput) error {
	return notImplemented("RenameChannel")
}

func (b *Bus) ACL(_ context.Context, _ string) ([]ab2bus.ACLEntry, error) {
	return nil, notImplemented("ACL")
}

func (b *Bus) SetACL(_ context.Context, _ ab2bus.SetACLInput) error {
	return notImplemented("SetACL")
}

func (b *Bus) Subscribe(_ context.Context, _ ab2bus.SubscribeInput) error {
	return notImplemented("Subscribe")
}

func (b *Bus) Unsubscribe(_ context.Context, _ ab2bus.UnsubscribeInput) error {
	return notImplemented("Unsubscribe")
}

func (b *Bus) SubscribeTags(_ context.Context, _ ab2bus.TagSubscribeInput) error {
	return notImplemented("SubscribeTags")
}

func (b *Bus) UnsubscribeTags(_ context.Context, _ ab2bus.TagSubscribeInput) error {
	return notImplemented("UnsubscribeTags")
}

func (b *Bus) TagSubscriptions(_ context.Context) ([][]string, error) {
	return nil, notImplemented("TagSubscriptions")
}

func (b *Bus) Status(_ context.Context) (ab2bus.Status, error) {
	return ab2bus.Status{}, notImplemented("Status")
}
