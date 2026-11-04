package slackgo

import (
	"context"

	"github.com/slack-go/slack"

	"github.com/ericfitz/agentmesh/internal/slackapi"
)

// mapCanvasErr keeps every Slack code as an *APIError (no sentinel).
func mapCanvasErr(method string, err error) error { return mapCode(method, err) }

func markdownDoc(md string) slack.DocumentContent {
	return slack.DocumentContent{Type: "markdown", Markdown: md}
}

// CreateChannelCanvas implements slackapi.Client.
func (c *Client) CreateChannelCanvas(ctx context.Context, channelID, title, markdown string) (string, error) {
	id, err := c.api.CreateChannelCanvasContext(ctx, channelID, markdownDoc(markdown), slack.CreateChannelCanvasOptionTitle(title))
	if err != nil {
		return "", mapCanvasErr("conversations.canvases.create", err)
	}
	return id, nil
}

// ChannelCanvases implements slackapi.Client: the channel's canvas tabs from
// conversations.info.
func (c *Client) ChannelCanvases(ctx context.Context, channelID string) ([]slackapi.CanvasTab, error) {
	ch, err := c.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: channelID})
	if err != nil {
		return nil, mapCanvasErr("conversations.info", err)
	}
	tabs := []slackapi.CanvasTab{}
	for _, t := range ch.Properties.Tabs {
		if t.Type == "canvas" && t.Data != nil && t.Data.FileID != "" {
			tabs = append(tabs, slackapi.CanvasTab{ID: t.Data.FileID, Label: t.Label})
		}
	}
	return tabs, nil
}

// CanvasContains implements slackapi.Client.
func (c *Client) CanvasContains(ctx context.Context, canvasID, text string) (bool, error) {
	secs, err := c.api.LookupCanvasSectionsContext(ctx, slack.LookupCanvasSectionsParams{
		CanvasID: canvasID, Criteria: slack.LookupCanvasSectionsCriteria{ContainsText: text},
	})
	if err != nil {
		return false, mapCanvasErr("canvases.sections.lookup", err)
	}
	return len(secs) > 0, nil
}

// ReplaceCanvas implements slackapi.Client.
func (c *Client) ReplaceCanvas(ctx context.Context, canvasID, markdown string) error {
	return mapCanvasErr("canvases.edit", c.api.EditCanvasContext(ctx, slack.EditCanvasParams{
		CanvasID: canvasID,
		Changes:  []slack.CanvasChange{{Operation: "replace", DocumentContent: markdownDoc(markdown)}},
	}))
}

// SetCanvasChannelAccess implements slackapi.Client.
func (c *Client) SetCanvasChannelAccess(ctx context.Context, canvasID, channelID, level string) error {
	if level != "read" && level != "write" {
		return slackapi.ErrCanvasAccessLevel
	}
	return mapCanvasErr("canvases.access.set", c.api.SetCanvasAccessContext(ctx, slack.SetCanvasAccessParams{
		CanvasID: canvasID, AccessLevel: level, ChannelIDs: []string{channelID},
	}))
}
