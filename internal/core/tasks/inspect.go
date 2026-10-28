package tasks

import (
	"maps"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/store"
)

// taskPrepared is the inspection request for a task write: the merged
// subject, description and metadata, with the sender kind and channel kind as
// the Store holds them (ADR 0011). target is the task id, 0 on create.
// open note: write-inspection 9 (no base_revision, and the digest never covers the revision or rank)
func taskPrepared(op, identity string, pr access.Principal, ch store.Channel, target int64, t bus.Task, size int) core.Prepared {
	return core.Prepared{
		Inspect: true,
		Request: core.InspectRequest{
			Operation: op, Source: core.SourceAPI, Sender: identity, SenderKind: string(pr.Kind),
			Channel: core.InspectChannel{Name: ch.Name, Kind: string(ch.Kind)},
			Record:  core.InspectRecord{Subject: t.Subject, Description: t.Description, Metadata: t.Metadata},
		},
		ChannelID: ch.ID,
		Target:    target,
		Size:      size,
	}
}

// contentChanged reports whether a patch changes what the inspector judges:
// the subject, the description or the metadata. Status, owner, rank, lease
// and links are not content.
func contentChanged(cur, patched task) bool {
	return cur.Subject != patched.Subject || cur.Description != patched.Description ||
		!maps.Equal(cur.Metadata, patched.Metadata)
}
