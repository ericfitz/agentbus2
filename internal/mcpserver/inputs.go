package mcpserver

// One standalone input struct per tool. None embeds a bus type: the bus input
// types carry no json tags or no descriptions, and every member here is
// snake_case with a hand-written description. Each handler maps its struct
// onto the bus input member by member.
//
// Every tool but register and receive accepts an optional session, which
// attributes a send and is ignored elsewhere; close_session and receive
// require it.

type cursorIn struct {
	Channel string `json:"channel" jsonschema:"channel the cursor is for"`
	Seq     int64  `json:"seq" jsonschema:"the last sequence number you processed on that channel"`
}

type registerIn struct {
	Hint        string     `json:"hint,omitempty" jsonschema:"short label for this session, such as reviewer; it becomes part of the display name. Omit it for the main session of this harness, which resumes after a restart"`
	ResumeToken string     `json:"resume_token,omitempty" jsonschema:"resume token from an earlier register to resume that session explicitly"`
	Cursors     []cursorIn `json:"cursors,omitempty" jsonschema:"positions you kept from an earlier session; each is adopted unless rejected_cursors says why not"`
}

// sessionIn is close_session's input (session required).
type sessionIn struct {
	Session string `json:"session" jsonschema:"the session id returned by register"`
}

// optSessionIn is the input of the tools that take nothing but the optional session.
type optSessionIn struct {
	Session string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
}

type aclEntryIn struct {
	Identity    string   `json:"identity" jsonschema:"identity the entry grants permissions to"`
	Permissions []string `json:"permissions" jsonschema:"any of read, write, admin"`
}

type createChannelIn struct {
	Session        string       `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Name           string       `json:"name" jsonschema:"channel name"`
	Kind           string       `json:"kind,omitempty" jsonschema:"ordinary (default), memory or tasks"`
	ACL            []aclEntryIn `json:"acl,omitempty" jsonschema:"access list; omit it for the server default, pass [] for an empty list"`
	IdempotencyKey string       `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

// channelIn is the input of delete_channel.
type channelIn struct {
	Session        string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Name           string `json:"name" jsonschema:"channel name"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

// getACLIn is the input of get_acl, a read: it takes no idempotency key.
type getACLIn struct {
	Session string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Name    string `json:"name" jsonschema:"channel name"`
}

type renameIn struct {
	Session        string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Name           string `json:"name" jsonschema:"current channel name"`
	NewName        string `json:"new_name" jsonschema:"new channel name"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type setACLIn struct {
	Session        string       `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Channel        string       `json:"channel" jsonschema:"channel name"`
	Entries        []aclEntryIn `json:"entries" jsonschema:"the complete new access list; it replaces the old one"`
	IdempotencyKey string       `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type subscribeIn struct {
	Session        string   `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Channel        string   `json:"channel,omitempty" jsonschema:"channel to subscribe to; pass this or tags, not both"`
	From           string   `json:"from,omitempty" jsonschema:"now (default) or oldest; channel subscriptions only"`
	Tags           []string `json:"tags,omitempty" jsonschema:"instead of channel: 1-10 tags or prefix patterns (a tag ending in * matches every tag with that prefix, such as env:*) that must all match a message for it to be delivered"`
	IdempotencyKey string   `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type unsubscribeIn struct {
	Session        string   `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Channel        string   `json:"channel,omitempty" jsonschema:"channel to leave; pass this or tags, not both"`
	Tags           []string `json:"tags,omitempty" jsonschema:"instead of channel: the same tags or patterns the tag set was subscribed with"`
	IdempotencyKey string   `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type sendIn struct {
	Session        string            `json:"session,omitempty" jsonschema:"your session id as returned by register; attributes the message to this session"`
	Channel        string            `json:"channel" jsonschema:"channel to send to"`
	Subject        string            `json:"subject,omitempty" jsonschema:"optional one-line summary, at most 200 characters"`
	Content        string            `json:"content" jsonschema:"message body"`
	Type           string            `json:"type,omitempty" jsonschema:"optional message type label"`
	ReplyTo        *int64            `json:"reply_to,omitempty" jsonschema:"sequence number of the message this answers"`
	Metadata       map[string]string `json:"metadata,omitempty" jsonschema:"optional string key-value pairs"`
	Tags           []string          `json:"tags,omitempty" jsonschema:"up to 10 labels; lowercase letters, digits and - with at most one : between other characters"`
	IdempotencyKey string            `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key returns the first seq"`
}

type receiveIn struct {
	Session     string   `json:"session" jsonschema:"your session id as returned by register"`
	Ack         string   `json:"ack,omitempty" jsonschema:"batch token from your previous receive, to acknowledge it"`
	Count       int      `json:"count,omitempty" jsonschema:"maximum number of messages to return"`
	Channels    []string `json:"channels,omitempty" jsonschema:"only return messages from these channels; the rest stay pending"`
	IncludeOwn  bool     `json:"include_own,omitempty" jsonschema:"also return messages sent by this session"`
	WaitSeconds float64  `json:"wait_seconds,omitempty" jsonschema:"when nothing is pending, wait up to this many seconds for a message and then receive again. This replaces an armed agentbus2 wait or watch for the session, which then exits 3"`
}

type historyIn struct {
	Session string   `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Channel string   `json:"channel" jsonschema:"channel to read"`
	Before  *int64   `json:"before,omitempty" jsonschema:"only messages with a lower sequence number"`
	After   *int64   `json:"after,omitempty" jsonschema:"only messages with a higher sequence number"`
	Count   int      `json:"count,omitempty" jsonschema:"maximum number of messages"`
	Tags    []string `json:"tags,omitempty" jsonschema:"only messages carrying at least one of these tags; a tag ending in * matches every tag with that prefix"`
	Thread  *int64   `json:"thread,omitempty" jsonschema:"only the thread rooted at this sequence number: the message and every message whose reply_to is it. Complete for replies stored after the channel's Slack bridge went live (or on channels whose replies all point at the root); an earlier reply may point at another reply"`
}

type searchIn struct {
	Session string   `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Query   string   `json:"query,omitempty" jsonschema:"words to search for"`
	Mode    string   `json:"mode,omitempty" jsonschema:"text, semantic or both"`
	Channel string   `json:"channel,omitempty" jsonschema:"restrict to one channel"`
	Sender  string   `json:"sender,omitempty" jsonschema:"restrict to one sending identity"`
	Since   *int64   `json:"since,omitempty" jsonschema:"only messages created at or after this unix time in milliseconds"`
	Until   *int64   `json:"until,omitempty" jsonschema:"only messages created before this unix time in milliseconds"`
	Thread  *int64   `json:"thread,omitempty" jsonschema:"only the thread rooted at this sequence number"`
	Tags    []string `json:"tags,omitempty" jsonschema:"only messages carrying at least one of these tags; a tag ending in * matches every tag with that prefix"`
	Count   int      `json:"count,omitempty" jsonschema:"maximum number of hits"`
	Cursor  string   `json:"cursor,omitempty" jsonschema:"the next value of a previous result, to read the next page"`
}

type memoryIn struct {
	Session string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	ID      int64  `json:"id" jsonschema:"the memory_id of the memory"`
}

type editIn struct {
	Session        string             `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	ID             int64              `json:"id" jsonschema:"the memory_id of the memory to edit"`
	Subject        *string            `json:"subject,omitempty" jsonschema:"new subject; omit to keep it, pass an empty string to clear it"`
	Content        *string            `json:"content,omitempty" jsonschema:"the new content; omit to keep it (it cannot be empty: delete the memory instead)"`
	Type           *string            `json:"type,omitempty" jsonschema:"message type label; omit to keep it, pass an empty string to clear it"`
	Metadata       *map[string]string `json:"metadata,omitempty" jsonschema:"string key-value pairs; omit to keep them, pass {} to clear them"`
	Tags           []string           `json:"tags,omitempty" jsonschema:"new tags; omit to keep them, pass [] to clear them"`
	IfRevision     int64              `json:"if_revision,omitempty" jsonschema:"the revision you read; the edit fails with conflict if the memory changed since"`
	IdempotencyKey string             `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type deleteIn struct {
	Session        string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	ID             int64  `json:"id" jsonschema:"the memory_id of the memory to delete"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type taskCreateIn struct {
	Session        string            `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Channel        string            `json:"channel" jsonschema:"the task list channel"`
	Subject        string            `json:"subject" jsonschema:"imperative one-line subject"`
	Description    string            `json:"description,omitempty" jsonschema:"details"`
	Parent         int64             `json:"parent,omitempty" jsonschema:"id of an existing task in the same list to nest under"`
	Before         int64             `json:"before,omitempty" jsonschema:"sibling task id to place the new task before"`
	After          int64             `json:"after,omitempty" jsonschema:"sibling task id to place the new task after"`
	BlockedBy      []int64           `json:"blocked_by,omitempty" jsonschema:"ids of tasks in the same list that must complete first"`
	Metadata       map[string]string `json:"metadata,omitempty" jsonschema:"optional string key-value pairs"`
	IdempotencyKey string            `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type taskClaimIn struct {
	Session        string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	TaskID         int64  `json:"task_id" jsonschema:"id of the task to claim"`
	LeasedUntil    int64  `json:"leased_until,omitempty" jsonschema:"optional lease end as unix time in milliseconds; renew by claiming again with a later value"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type taskReleaseIn struct {
	Session        string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	TaskID         int64  `json:"task_id" jsonschema:"id of the task to release"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type taskUpdateIn struct {
	Session         string            `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	TaskID          int64             `json:"task_id" jsonschema:"id of the task to change"`
	Status          *string           `json:"status,omitempty" jsonschema:"pending, in_progress or completed"`
	Owner           *string           `json:"owner,omitempty" jsonschema:"new owner identity; an empty string clears the owner"`
	Subject         *string           `json:"subject,omitempty" jsonschema:"new subject"`
	Description     *string           `json:"description,omitempty" jsonschema:"new description"`
	Parent          *int64            `json:"parent,omitempty" jsonschema:"new parent task id; 0 moves the task to the top level"`
	Before          int64             `json:"before,omitempty" jsonschema:"reorder: sibling task id to move before"`
	After           int64             `json:"after,omitempty" jsonschema:"reorder: sibling task id to move after"`
	AddBlockedBy    []int64           `json:"add_blocked_by,omitempty" jsonschema:"task ids to add to blocked_by"`
	RemoveBlockedBy []int64           `json:"remove_blocked_by,omitempty" jsonschema:"task ids to remove from blocked_by"`
	LeasedUntil     *int64            `json:"leased_until,omitempty" jsonschema:"lease end as unix time in milliseconds; 0 clears the lease"`
	Metadata        map[string]string `json:"metadata,omitempty" jsonschema:"string key-value pairs to set"`
	Delete          bool              `json:"delete,omitempty" jsonschema:"delete a task that has no subtasks"`
	IdempotencyKey  string            `json:"idempotency_key,omitempty" jsonschema:"makes a retry safe: the same key replays the first result"`
}

type taskGetIn struct {
	Session string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	TaskID  int64  `json:"task_id" jsonschema:"id of the task"`
}

type taskListIn struct {
	Session string `json:"session,omitempty" jsonschema:"your session id as returned by register (optional)"`
	Channel string `json:"channel" jsonschema:"the task list channel"`
	Status  string `json:"status,omitempty" jsonschema:"only tasks with this status"`
	Owner   string `json:"owner,omitempty" jsonschema:"only tasks owned by this identity"`
}
