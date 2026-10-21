package server

// Stubs replaced by Task 6. Each answers 500 with a detail naming it.

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/gen"
)

func (a *api) ListChannels(context.Context, gen.ListChannelsRequestObject) (gen.ListChannelsResponseObject, error) {
	return nil, notImplemented("ListChannels")
}

func (a *api) CreateChannel(context.Context, gen.CreateChannelRequestObject) (gen.CreateChannelResponseObject, error) {
	return nil, notImplemented("CreateChannel")
}

func (a *api) DeleteChannel(context.Context, gen.DeleteChannelRequestObject) (gen.DeleteChannelResponseObject, error) {
	return nil, notImplemented("DeleteChannel")
}

func (a *api) RenameChannel(context.Context, gen.RenameChannelRequestObject) (gen.RenameChannelResponseObject, error) {
	return nil, notImplemented("RenameChannel")
}

func (a *api) GetACL(context.Context, gen.GetACLRequestObject) (gen.GetACLResponseObject, error) {
	return nil, notImplemented("GetACL")
}

func (a *api) SetACL(context.Context, gen.SetACLRequestObject) (gen.SetACLResponseObject, error) {
	return nil, notImplemented("SetACL")
}

func (a *api) Subscribe(context.Context, gen.SubscribeRequestObject) (gen.SubscribeResponseObject, error) {
	return nil, notImplemented("Subscribe")
}

func (a *api) Unsubscribe(context.Context, gen.UnsubscribeRequestObject) (gen.UnsubscribeResponseObject, error) {
	return nil, notImplemented("Unsubscribe")
}

func (a *api) ListTagSubscriptions(context.Context, gen.ListTagSubscriptionsRequestObject) (gen.ListTagSubscriptionsResponseObject, error) {
	return nil, notImplemented("ListTagSubscriptions")
}

func (a *api) SubscribeTags(context.Context, gen.SubscribeTagsRequestObject) (gen.SubscribeTagsResponseObject, error) {
	return nil, notImplemented("SubscribeTags")
}

func (a *api) UnsubscribeTags(context.Context, gen.UnsubscribeTagsRequestObject) (gen.UnsubscribeTagsResponseObject, error) {
	return nil, notImplemented("UnsubscribeTags")
}

func (a *api) AdminStatus(context.Context, gen.AdminStatusRequestObject) (gen.AdminStatusResponseObject, error) {
	return nil, notImplemented("AdminStatus")
}
