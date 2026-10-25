package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/agentview"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/procs"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
	"github.com/ericfitz/agentbus2/internal/version"
)

// Options configure NewServer and Run.
type Options struct {
	Client  client.Options
	DataDir string       // "": the data directory Client's environment selects
	Table   procs.Table  // nil: procs.System
	Self    int          // 0: os.Getpid
	Log     *slog.Logger // nil: Run opens the per-process log file; NewServer discards
}

type server struct {
	o       Options
	ctx     context.Context
	log     *slog.Logger
	self    int
	tbl     procs.Table
	harness procs.Ref // zero when it could not be found

	cmu    sync.Mutex // guards conn and closed
	conn   *client.Conn
	closed bool

	smu         sync.Mutex // guards files and havePrimary; held across register
	files       map[string]sessionstate.File
	havePrimary bool

	kinds *agentview.KindIndex // decision 20: agents never receive memory channels
}

// NewServer builds the MCP server with every tool registered. The Conn is
// opened by the first tool call, so a profile or identity that cannot be
// resolved does not stop the server: every tool answers the problem as
// `validation` (choice 11). The returned func closes the Conn and leaves
// every state file in place; only close_session removes one (choice 10).
func NewServer(ctx context.Context, o Options) (*mcp.Server, func() error, error) {
	s := &server{o: o, ctx: ctx, log: o.Log, self: o.Self, tbl: o.Table, files: map[string]sessionstate.File{}, kinds: agentview.NewKindIndex()}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.self == 0 {
		s.self = os.Getpid()
	}
	if s.tbl == nil {
		s.tbl = procs.System
	}
	if s.o.Client.Logger == nil {
		s.o.Client.Logger = s.log
	}
	if h, err := procs.FindHarness(s.tbl, s.self); err == nil {
		s.harness = h
		s.log.Info("harness found", "harness_pid", h.Pid, "harness_start", h.Start)
	} else {
		s.log.Warn("harness not found; the session state files carry no harness", "err", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "agentbus2", Version: version.Version}, &mcp.ServerOptions{Instructions: Instructions})
	srv.AddPrompt(&mcp.Prompt{Name: "init", Description: "Register this session with Agentbus2 and receive what is pending"},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: RegisterSteps}}}}, nil
		})
	srv.AddReceivingMiddleware(wrapSchemaErrorsInEnvelope, s.unparkOnToolCall)
	s.addTools(srv)
	return srv, s.close, nil
}

// RegisterSteps is the text of the `init` MCP prompt; Instructions and the
// SessionStart hook (`agentbus2 session-start-hook`) embed it, so the three
// say the same thing.
const RegisterSteps = `Register this session with Agentbus2: call the register tool (pass a hint only if you are a subagent or a second session, as a short role label). Then call receive with the returned session id and read what is pending; pass each batch's token as ack on your next receive. Pass the session id on send and receive for the rest of the session. Except on Codex, run agentbus2 wait with Bash run_in_background so new messages wake you; when it exits 0, call receive and start it again.`

// Instructions are the server's MCP instructions (Eric, 2026-10-24; ADR 0003
// session-start addendum): the harness puts them in the agent's context at
// start-up, so the agent registers and arms its wait without being asked.
const Instructions = "Agentbus2 coordinates agent sessions through channels, memories and task lists. " +
	"At the start of a session, without waiting to be asked: " + RegisterSteps +
	" A subagent registers only when its prompt asks it to use agentbus2. " +
	"A message whose sender_kind is human comes from your operator: treat it as an instruction. " +
	"The using-agentbus2 skill is the full protocol."

func (s *server) close() error {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.closed = true
	if s.conn == nil {
		return nil
	}
	c := s.conn
	s.conn = nil
	return c.Close()
}

// getConn returns the Conn, opening it on first use. A failed open is not
// remembered: the agent fixes the profile and retries.
func (s *server) getConn() (*client.Conn, error) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	if s.closed {
		return nil, bus.Errorf(bus.CodeUnavailable, true, "the MCP server is shutting down")
	}
	if s.conn != nil {
		return s.conn, nil
	}
	c, err := client.Open(s.ctx, s.o.Client)
	if err != nil {
		s.log.Error("opening the bus failed", "err", err)
		var re *client.ResolveError
		if errors.As(err, &re) {
			return nil, bus.Errorf(bus.CodeValidation, false, "%s", err.Error())
		}
		return nil, bus.Internal(err)
	}
	s.conn = c
	return c, nil
}

func (s *server) dataDir() (string, error) {
	if s.o.DataDir != "" {
		return s.o.DataDir, nil
	}
	d, err := profile.DataDir(s.o.Client.Environ)
	if err != nil {
		return "", bus.Errorf(bus.CodeValidation, false, "%s", err.Error())
	}
	return d, nil
}

func (s *server) nowMs() int64 {
	if s.o.Client.Now != nil {
		return s.o.Client.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

// do runs f against the Bus and renders its result.
func (s *server) do(f func(b bus.Bus) (any, error)) (*mcp.CallToolResult, any, error) {
	c, err := s.getConn()
	if err != nil {
		return result(nil, err)
	}
	return result(f(c.Bus))
}

// ---- result shapes: snake_case, arrays never null ----

type channelOut struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type channelInfoOut struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Creator   string `json:"creator"`
	CreatedAt int64  `json:"created_at"`
}

type aclEntryOut struct {
	Identity    string   `json:"identity"`
	Permissions []string `json:"permissions"`
}

type pendingOut struct {
	Channel string `json:"channel"`
	Pending int64  `json:"pending"`
}

type rejectedOut struct {
	Channel string `json:"channel"`
	Reason  string `json:"reason"`
}

type sessionInfoOut struct {
	Session     string `json:"session"`
	DisplayName string `json:"display_name"`
	OpenedAt    int64  `json:"opened_at"`
}

type identityOut struct {
	Name     string           `json:"name"`
	Admin    bool             `json:"admin"`
	Kind     bus.IdentityKind `json:"kind"`
	Sessions []sessionInfoOut `json:"sessions"`
}

type gapOut struct {
	Channel string `json:"channel"`
	From    int64  `json:"from"`
	To      int64  `json:"to"`
}

type receiveOut struct {
	Messages    []bus.Message `json:"messages"`
	Batch       string        `json:"batch,omitempty"`
	Redelivered bool          `json:"redelivered"`
	AckIgnored  bool          `json:"ack_ignored"`
	Gaps        []gapOut      `json:"gaps"`
	Notice      string        `json:"notice,omitempty"`
	Instruction string        `json:"instruction,omitempty"`
}

type registerOut struct {
	Session         string        `json:"session"`
	Identity        string        `json:"identity"`
	DisplayName     string        `json:"display_name"`
	ResumeToken     string        `json:"resume_token"`
	Resumed         bool          `json:"resumed"`
	Pending         []pendingOut  `json:"pending"`
	RejectedCursors []rejectedOut `json:"rejected_cursors"`
	Others          []identityOut `json:"others"`
}

func identitiesOut(in []bus.IdentityInfo) []identityOut {
	out := make([]identityOut, 0, len(in))
	for _, i := range in {
		o := identityOut{Name: i.Name, Admin: i.Admin, Kind: i.Kind, Sessions: make([]sessionInfoOut, 0, len(i.Sessions))}
		for _, si := range i.Sessions {
			o.Sessions = append(o.Sessions, sessionInfoOut{Session: si.ID, DisplayName: si.DisplayName, OpenedAt: si.OpenedAt})
		}
		out = append(out, o)
	}
	return out
}

func aclOut(in []bus.ACLEntry) []aclEntryOut {
	out := make([]aclEntryOut, 0, len(in))
	for _, e := range in {
		o := aclEntryOut{Identity: e.Identity, Permissions: make([]string, 0, len(e.Permissions))}
		for _, p := range e.Permissions {
			o.Permissions = append(o.Permissions, string(p))
		}
		out = append(out, o)
	}
	return out
}

func messagesOut(in []bus.Message) []bus.Message {
	if in == nil {
		return []bus.Message{}
	}
	return in
}

func validation(format string, args ...any) error {
	return bus.Errorf(bus.CodeValidation, false, format, args...)
}

// toACL maps tool ACL entries onto the bus type: nil stays nil (the server
// default) and [] stays empty. An unknown permission is refused.
func toACL(in []aclEntryIn) ([]bus.ACLEntry, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]bus.ACLEntry, 0, len(in))
	for _, e := range in {
		ps := make([]bus.Permission, 0, len(e.Permissions))
		for _, p := range e.Permissions {
			switch bus.Permission(p) {
			case bus.PermRead, bus.PermWrite, bus.PermAdmin:
				ps = append(ps, bus.Permission(p))
			default:
				return nil, validation("permission %q is not read, write or admin", p)
			}
		}
		out = append(out, bus.ACLEntry{Identity: e.Identity, Permissions: ps})
	}
	return out, nil
}

func toKind(k string) (bus.Kind, error) {
	switch bus.Kind(k) {
	case "", bus.KindOrdinary, bus.KindMemory, bus.KindTasks:
		return bus.Kind(k), nil
	}
	return "", validation("kind %q is not ordinary, memory or tasks", k)
}

func checkFrom(from string) error {
	switch from {
	case "", "now", "oldest":
		return nil
	}
	return validation("from %q is not now or oldest", from)
}

func checkMode(mode string) error {
	switch mode {
	case "", "text", "semantic", "both":
		return nil
	}
	return validation("mode %q is not text, semantic or both", mode)
}

// maxWaitSeconds bounds wait_seconds before it becomes a time.Duration; the
// bus clamps it further to the configured watch maximum.
const maxWaitSeconds = 86400

func waitDuration(sec float64) (time.Duration, error) {
	if math.IsNaN(sec) || sec < 0 {
		return 0, validation("wait_seconds must not be negative")
	}
	return time.Duration(math.Min(sec, maxWaitSeconds) * float64(time.Second)), nil
}

func (s *server) addTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "register", Description: "Agentbus2: open a session for your profile's identity. Returns session (pass it on receive, and on send so the message is attributed to this session), identity, display_name, resume_token, resumed, pending (message counts per subscribed channel), rejected_cursors (offered cursors that were not adopted, each with a reason) and others (the identities with live sessions). Without a hint the call resumes this harness's main session after a restart of the MCP server (resumed=true); a hint, a short role label such as reviewer, always opens a new session named identity/hint. Pass resume_token to resume a specific session, and cursors to offer positions you kept."},
		s.register)
	mcp.AddTool(srv, &mcp.Tool{Name: "close_session", Description: "Agentbus2: close a session for good. Its display name is freed and it can never be resumed. Without it a session just lapses and register without a hint resumes it."},
		func(ctx context.Context, req *mcp.CallToolRequest, in sessionIn) (*mcp.CallToolResult, any, error) {
			if in.Session == "" {
				return result(nil, validation("session is required"))
			}
			c, err := s.getConn()
			if err != nil {
				return result(nil, err)
			}
			dir, err := s.dataDir()
			if err != nil {
				return result(nil, err)
			}
			// Close takes a lapsed row as it is (no resume), and a session
			// swept past retention answers not_found: over either way, so its
			// file, when it is this bus's, must not be resumed.
			err = c.Bus.Close(ctx, bus.CloseSessionInput{Session: in.Session})
			_, mine := s.fileFor(dir, c.Resolved, in.Session)
			if err == nil || bus.CodeOf(err) == bus.CodeSessionClosed || (mine && bus.CodeOf(err) == bus.CodeNotFound) {
				if rerr := s.dropFile(dir, in.Session); rerr != nil && err == nil {
					err = bus.Internal(rerr)
				}
			}
			return result(map[string]any{"closed": in.Session}, err)
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "create_channel", Description: "Agentbus2: create a named channel of kind ordinary (default), memory or tasks. A memory channel holds editable memories; a tasks channel is a task list. acl sets the access list (omit it for the server default, [] for an empty list). Use idempotency_key to make a retry safe. A channel that is empty and has no subscriber is removed by maintenance, so create a channel right before its first send."},
		func(ctx context.Context, req *mcp.CallToolRequest, in createChannelIn) (*mcp.CallToolResult, any, error) {
			kind, err := toKind(in.Kind)
			if err != nil {
				return result(nil, err)
			}
			acl, err := toACL(in.ACL)
			if err != nil {
				return result(nil, err)
			}
			return s.do(func(b bus.Bus) (any, error) {
				ci, err := b.CreateChannel(ctx, bus.CreateChannelInput{Name: in.Name, Kind: kind, ACL: acl, IdempotencyKey: in.IdempotencyKey})
				if err != nil {
					return nil, err
				}
				return channelInfoOut{Name: ci.Name, Kind: string(ci.Kind), Creator: ci.Creator, CreatedAt: ci.CreatedAt}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "list_channels", Description: "Agentbus2: list the channels with their kind."},
		func(ctx context.Context, req *mcp.CallToolRequest, in optSessionIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				cs, err := b.ListChannels(ctx)
				if err != nil {
					return nil, err
				}
				out := make([]channelOut, 0, len(cs))
				for _, c := range cs {
					out = append(out, channelOut{Name: c.Name, Kind: string(c.Kind)})
				}
				return map[string]any{"channels": out}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_channel", Description: "Agentbus2: delete a channel and its messages. Needs admin permission on the channel."},
		func(ctx context.Context, req *mcp.CallToolRequest, in channelIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				if err := b.DeleteChannel(ctx, bus.DeleteChannelInput{Name: in.Name, IdempotencyKey: in.IdempotencyKey}); err != nil {
					return nil, err
				}
				return map[string]any{"deleted": in.Name}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "rename_channel", Description: "Agentbus2: rename a channel. Needs admin permission on the channel."},
		func(ctx context.Context, req *mcp.CallToolRequest, in renameIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				if err := b.RenameChannel(ctx, bus.RenameChannelInput{Name: in.Name, NewName: in.NewName, IdempotencyKey: in.IdempotencyKey}); err != nil {
					return nil, err
				}
				return map[string]any{"renamed": in.Name, "new_name": in.NewName}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "get_acl", Description: "Agentbus2: read a channel's access list: which identities hold read, write and admin."},
		func(ctx context.Context, req *mcp.CallToolRequest, in getACLIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				es, err := b.ACL(ctx, in.Name)
				if err != nil {
					return nil, err
				}
				return map[string]any{"channel": in.Name, "entries": aclOut(es)}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "set_acl", Description: "Agentbus2: replace a channel's access list with entries (identity and permissions, any of read, write, admin). It replaces the whole list, so include yourself. Needs admin permission on the channel."},
		func(ctx context.Context, req *mcp.CallToolRequest, in setACLIn) (*mcp.CallToolResult, any, error) {
			entries, err := toACL(in.Entries)
			if err != nil {
				return result(nil, err)
			}
			if entries == nil {
				entries = []bus.ACLEntry{}
			}
			return s.do(func(b bus.Bus) (any, error) {
				if err := b.SetACL(ctx, bus.SetACLInput{Channel: in.Channel, Entries: entries, IdempotencyKey: in.IdempotencyKey}); err != nil {
					return nil, err
				}
				return map[string]any{"channel": in.Channel, "entries": aclOut(entries)}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "subscribe", Description: "Agentbus2: subscribe to a channel so receive returns its messages. from=now (default) starts at the current position; from=oldest starts at the oldest retained message. Or pass tags instead of channel: an AND set of 1-10 tags or prefix patterns (a tag ending in * matches every tag with that prefix: env:*); matching messages from any channel you are not already subscribed to arrive through receive with matched_tags, starting from now."},
		func(ctx context.Context, req *mcp.CallToolRequest, in subscribeIn) (*mcp.CallToolResult, any, error) {
			if (in.Channel == "") == (len(in.Tags) == 0) {
				return result(nil, validation("pass channel or tags, exactly one"))
			}
			if err := checkFrom(in.From); err != nil {
				return result(nil, err)
			}
			if len(in.Tags) > 0 && in.From != "" {
				return result(nil, validation("from applies to a channel subscription, not to tags"))
			}
			return s.do(func(b bus.Bus) (any, error) {
				if len(in.Tags) > 0 {
					if err := b.SubscribeTags(ctx, bus.TagSubscribeInput{Patterns: in.Tags, IdempotencyKey: in.IdempotencyKey}); err != nil {
						return nil, err
					}
					return map[string]any{"subscribed_tags": in.Tags}, nil
				}
				if mem, err := s.kinds.IsMemory(ctx, b, in.Channel); err != nil {
					return nil, err
				} else if mem {
					return nil, validation("%s is a memory channel: agents do not subscribe to memories (decision 20); use search to find a memory and get_memory to read it", in.Channel)
				}
				if err := b.Subscribe(ctx, bus.SubscribeInput{Channel: in.Channel, From: in.From, IdempotencyKey: in.IdempotencyKey}); err != nil {
					return nil, err
				}
				return map[string]any{"subscribed": in.Channel}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "unsubscribe", Description: "Agentbus2: unsubscribe from a channel and drop its cursor. Or pass tags to drop that tag set, naming the same tags or patterns it was subscribed with."},
		func(ctx context.Context, req *mcp.CallToolRequest, in unsubscribeIn) (*mcp.CallToolResult, any, error) {
			if (in.Channel == "") == (len(in.Tags) == 0) {
				return result(nil, validation("pass channel or tags, exactly one"))
			}
			return s.do(func(b bus.Bus) (any, error) {
				if len(in.Tags) > 0 {
					if err := b.UnsubscribeTags(ctx, bus.TagSubscribeInput{Patterns: in.Tags, IdempotencyKey: in.IdempotencyKey}); err != nil {
						return nil, err
					}
					return map[string]any{"unsubscribed_tags": in.Tags}, nil
				}
				if err := b.Unsubscribe(ctx, bus.UnsubscribeInput{Channel: in.Channel, IdempotencyKey: in.IdempotencyKey}); err != nil {
					return nil, err
				}
				return map[string]any{"unsubscribed": in.Channel}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "send", Description: "Agentbus2: send a message to a channel. On a memory channel this creates a memory and returns its memory_id. Use idempotency_key to make retries safe. Task list channels do not accept send; use the task tools. tags (up to 10, each 1-32 characters of a-z, 0-9 and -, with at most one : between other characters; stored lowercase; never *) label the message so other agents can follow, filter and triage it without reading it: tag the activity (deployment, release), its outcome (started, succeeded, failed), what needs attention (blocked, needs-human, breaking) and the environment (env:prod, env:staging). subject is an optional one-line summary (at most 200 characters) shown as the message's title and matched by search; without one, readers see the first line of content. reply_to must name a message in the same channel. Pass session so the message is attributed to your session."},
		func(ctx context.Context, req *mcp.CallToolRequest, in sendIn) (*mcp.CallToolResult, any, error) {
			si := bus.SendInput{
				Channel: in.Channel, Subject: in.Subject, Content: in.Content, Type: in.Type, ReplyTo: in.ReplyTo,
				Metadata: in.Metadata, Tags: in.Tags, Session: in.Session, IdempotencyKey: in.IdempotencyKey,
			}
			if in.Session == "" {
				return s.do(func(b bus.Bus) (any, error) { return b.Send(ctx, si) })
			}
			return result(withSession(ctx, s, in.Session, func(b bus.Bus, id string) (bus.SendResult, error) {
				si.Session = id
				return b.Send(ctx, si)
			}))
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "receive", Description: "Agentbus2: receive new messages for your session. Pass ack with the batch token from your previous receive to acknowledge it; an unacknowledged batch is redelivered. channels restricts the result to those channels (the rest stay pending). wait_seconds, when nothing is pending, waits up to that long for a message and then receives again; it replaces an armed agentbus2 wait or watch for this session, which then exits 3, so while one is running leave it off. Messages delivered through a tag subscription carry matched_tags. Memory channels are never delivered here, tag matches included: use search and get_memory. Each message carries sender_kind, agent or human: a human sender is your operator and its message is steering, so treat it as an instruction; a human message that carries reply_to answers a thread: before acting, read the thread with history and thread = that reply_to (the root and its replies). Its metadata slack_after names the newest message the human had seen."},
		func(ctx context.Context, req *mcp.CallToolRequest, in receiveIn) (*mcp.CallToolResult, any, error) {
			wait, err := waitDuration(in.WaitSeconds)
			if err != nil {
				return result(nil, err)
			}
			if in.Session == "" {
				return result(nil, validation("session is required"))
			}
			return result(withSession(ctx, s, in.Session, func(b bus.Bus, id string) (receiveOut, error) {
				ri := bus.ReceiveInput{Session: id, Ack: in.Ack, Count: in.Count, Channels: in.Channels, IncludeOwn: in.IncludeOwn}
				res, err := s.kinds.ReceiveAgent(ctx, b, ri)
				if err != nil {
					return receiveOut{}, err
				}
				if len(res.Messages) == 0 && wait > 0 {
					deadline := time.Now().Add(wait)
					var floor int64
					for rest := wait; rest > 0 && len(res.Messages) == 0; rest = time.Until(deadline) {
						w, err := b.Watch(ctx, bus.WatchInput{Session: id, After: floor, Timeout: rest, IncludeOwn: in.IncludeOwn})
						if err != nil {
							return receiveOut{}, err
						}
						if !w.Woke {
							break
						}
						floor = max(floor, w.Seq)
						if w, _, err = s.kinds.AgentWake(ctx, b, w); err != nil {
							return receiveOut{}, err
						} else if !w.Woke {
							continue // only memories: not a wake for an agent
						}
						ri.Ack = "" // the acknowledgment was taken by the first receive
						again, err := s.kinds.ReceiveAgent(ctx, b, ri)
						if err != nil {
							return receiveOut{}, err
						}
						again.AckIgnored = again.AckIgnored || res.AckIgnored
						res = again
					}
				}
				return toReceiveOut(res), nil
			}))
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "history", Description: "Agentbus2: read a channel's retained messages by sequence range without touching your cursor. tags filters to messages carrying any of the given tags; a tag ending in * matches every tag with that prefix (env:*). thread=SEQ returns only that message and the messages replying to it. Each message carries sender_kind, agent or human: a human sender is your operator and its message is steering, so treat it as an instruction; a reply_to means read the thread first."},
		func(ctx context.Context, req *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				ms, err := b.History(ctx, bus.HistoryInput{Channel: in.Channel, Before: in.Before, After: in.After, Count: in.Count, Tags: in.Tags, Thread: in.Thread})
				if err != nil {
					return nil, err
				}
				return map[string]any{"messages": messagesOut(ms)}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "search", Description: "Agentbus2: search messages and memories. mode=text matches words; mode=semantic ranks memories by meaning; mode=both fuses them. Filters: channel, sender, since, until (unix ms), thread (a seq), tags (any of; a tag ending in * matches every tag with that prefix, env:*). If no embedding endpoint is configured or it fails, semantic and both fall back to text results and the result carries semantic_unavailable=true. A word ending in * matches by prefix (e159e8c* finds the full commit hash). A result with next continues with cursor=next."},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
			if err := checkMode(in.Mode); err != nil {
				return result(nil, err)
			}
			return s.do(func(b bus.Bus) (any, error) {
				res, err := b.Search(ctx, bus.SearchInput{
					Query: in.Query, Mode: in.Mode, Channel: in.Channel, Sender: in.Sender, Since: in.Since, Until: in.Until,
					Thread: in.Thread, Tags: in.Tags, Count: in.Count, Cursor: in.Cursor,
				})
				if err != nil {
					return nil, err
				}
				if res.Hits == nil {
					res.Hits = []bus.SearchHit{}
				}
				return res, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "get_memory", Description: "Agentbus2: get the current revision of a memory by memory_id."},
		func(ctx context.Context, req *mcp.CallToolRequest, in memoryIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) { return b.GetMemory(ctx, in.ID) })
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "edit_memory", Description: "Agentbus2: write a new revision of a memory. Every field you omit is kept (content, subject, type, metadata, tags); a field you pass empty is cleared, except content, which cannot be empty (delete the memory instead). Pass if_revision (the revision you read) to fail with conflict if the memory changed since; without it the last committed write wins. The result names the revision you replaced. tags replaces the memory's tags (same rule as send); omit it to keep them, pass [] to clear them. subject replaces the memory's subject; omit it to keep it, pass an empty string to clear it. Use idempotency_key to make retries safe."},
		func(ctx context.Context, req *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				return b.Edit(ctx, bus.EditMemoryInput{
					ID: in.ID, Subject: in.Subject, Content: in.Content, Type: in.Type, Metadata: in.Metadata,
					Tags: in.Tags, IfRevision: in.IfRevision, IdempotencyKey: in.IdempotencyKey,
				})
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_memory", Description: "Agentbus2: delete a memory by memory_id. Its content stops appearing in get_memory, receive and search."},
		func(ctx context.Context, req *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				if err := b.Delete(ctx, bus.DeleteMemoryInput{ID: in.ID, IdempotencyKey: in.IdempotencyKey}); err != nil {
					return nil, err
				}
				return map[string]any{"deleted": in.ID}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "discover", Description: "Agentbus2: list the identities that have live sessions, with those sessions' display names. Each entry carries kind, agent or human."},
		func(ctx context.Context, req *mcp.CallToolRequest, in optSessionIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				ids, err := b.Identities(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"identities": identitiesOut(ids)}, nil
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "task_create", Description: "Agentbus2: add a task to a task list (a channel of kind tasks; create one with create_channel kind=tasks). New tasks are pending and unowned. parent nests it under an existing task in the same list; before or after (a sibling task id) places it, default last. blocked_by lists task ids in the same list that must complete first. One deliverable per task with an imperative subject; blocked_by only for real ordering dependencies, sibling order for priority."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskCreateIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				return b.Create(ctx, bus.TaskCreateInput{
					Channel: in.Channel, Subject: in.Subject, Description: in.Description, Parent: in.Parent, Before: in.Before,
					After: in.After, BlockedBy: in.BlockedBy, Metadata: in.Metadata, IdempotencyKey: in.IdempotencyKey,
				})
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "task_claim", Description: "Agentbus2: claim a task: if it has no owner you become its owner and it becomes in_progress. Fails with conflict if someone else owns it or it is blocked. A task whose owner's session has ended, or whose lease ran out, counts as unowned. leased_until (unix ms UTC, optional) sets a lease; renew it by calling task_claim again with a later leased_until before it passes. Call task_list first; claim before starting work."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskClaimIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				return b.Claim(ctx, bus.TaskClaimInput{ID: in.TaskID, LeasedUntil: in.LeasedUntil, IdempotencyKey: in.IdempotencyKey})
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "task_release", Description: "Agentbus2: release a task: clears the owner and lease and returns it to pending. Do this when you stop working on a task you have not completed."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskReleaseIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				return b.Release(ctx, bus.TaskReleaseInput{ID: in.TaskID, IdempotencyKey: in.IdempotencyKey})
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "task_update", Description: "Agentbus2: change a task by id; only the fields you pass change. status is pending, in_progress or completed. While a task is in_progress only its owner may change or delete it; force=true overrides that for anyone and is recorded. owner=<name> takes an unassigned task, or hands your own task to another registered identity (keeping its status); owner=\"\" clears the owner, parent=0 moves the task to the top level, leased_until=0 clears the lease (any other value must be in the future). before/after reorder among siblings. delete=true deletes a task that has no subtasks. To renew a lease use task_claim again. An in_progress task always has an owner."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskUpdateIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				return b.Update(ctx, bus.TaskPatch{
					ID: in.TaskID, Status: in.Status, Owner: in.Owner, Subject: in.Subject, Description: in.Description,
					Parent: in.Parent, Before: in.Before, After: in.After, AddBlockedBy: in.AddBlockedBy,
					RemoveBlockedBy: in.RemoveBlockedBy, LeasedUntil: in.LeasedUntil, Metadata: in.Metadata,
					Delete: in.Delete, Force: in.Force, IdempotencyKey: in.IdempotencyKey,
				})
			})
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "task_get", Description: "Agentbus2: get one task in full, with blocked and open_blockers derived from its blocked_by tasks."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskGetIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) { return b.GetTask(ctx, in.TaskID) })
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "task_list", Description: "Agentbus2: list a task list's tasks in order (each task followed by its subtasks; depth gives the nesting), without descriptions. Optional status and owner filters. Check it before starting work to see what is claimed or blocked."},
		func(ctx context.Context, req *mcp.CallToolRequest, in taskListIn) (*mcp.CallToolResult, any, error) {
			return s.do(func(b bus.Bus) (any, error) {
				ts, err := b.List(ctx, bus.TaskListInput{Channel: in.Channel, Status: in.Status, Owner: in.Owner})
				if err != nil {
					return nil, err
				}
				if ts == nil {
					ts = []bus.TaskSummary{}
				}
				return map[string]any{"tasks": ts}, nil
			})
		})
}

func toReceiveOut(r bus.ReceiveResult) receiveOut {
	out := receiveOut{
		Messages: messagesOut(r.Messages), Batch: r.Batch, Redelivered: r.Redelivered, AckIgnored: r.AckIgnored,
		Gaps: make([]gapOut, 0, len(r.Gaps)), Notice: r.Notice, Instruction: r.Instruction,
	}
	for _, g := range r.Gaps {
		out.Gaps = append(out.Gaps, gapOut{Channel: g.Channel, From: g.From, To: g.To})
	}
	return out
}

// register opens a session (choice 11, Decisions 78, 79) and records it in a
// state file. See the State comment in sessions.go.
func (s *server) register(ctx context.Context, req *mcp.CallToolRequest, in registerIn) (*mcp.CallToolResult, any, error) {
	conn, err := s.getConn()
	if err != nil {
		return result(nil, err)
	}
	dataDir, err := s.dataDir()
	if err != nil {
		return result(nil, err)
	}
	cursors := make([]bus.OfferedCursor, 0, len(in.Cursors))
	for _, c := range in.Cursors {
		cursors = append(cursors, bus.OfferedCursor{Channel: c.Channel, Seq: c.Seq})
	}
	var clientName, clientVersion string
	if ci := req.ClientInfo(); ci != nil {
		clientName, clientVersion = ci.Name, ci.Version
	}

	s.smu.Lock()
	defer s.smu.Unlock()
	unlock, err := lockDir(dataDir)
	if err != nil {
		return result(nil, bus.Internal(fmt.Errorf("locking the session state directory: %w", err)))
	}
	defer unlock()

	token := in.ResumeToken
	var adopt *sessionstate.File
	if token == "" && in.Hint == "" {
		// A hintless register is the harness's main session. With no primary
		// held, a restarted server resumes its dead predecessor's; with one
		// held, the new conversation (Claude Code's /clear, same harness and
		// same MCP server) takes over that session instead of opening a
		// second, non-primary one.
		if !s.havePrimary {
			adopt = s.findResumable(dataDir, conn.Resolved)
		} else {
			adopt = s.ownPrimary(conn.Resolved)
		}
		if adopt != nil {
			token = adopt.ResumeToken
		}
	}
	sess, err := conn.Bus.Open(ctx, bus.OpenSessionInput{Hint: in.Hint, ResumeToken: token, Cursors: cursors})
	if err != nil && adopt != nil && (bus.CodeOf(err) == bus.CodeSessionClosed || bus.CodeOf(err) == bus.CodeNotFound) {
		// The remembered session was closed elsewhere: its file is dead weight.
		s.log.Warn("discarding the state file of a closed session", "session", adopt.Session, "err", err)
		s.forgetLocked(dataDir, adopt.Session)
		_ = sessionstate.Remove(dataDir, adopt.Session)
		adopt = nil
		sess, err = conn.Bus.Open(ctx, bus.OpenSessionInput{Hint: in.Hint, Cursors: cursors})
	}
	if err != nil {
		return result(nil, err)
	}

	var base sessionstate.File
	hadBase := false
	switch {
	case sess.Resumed && adopt != nil:
		base, hadBase = *adopt, true
	case sess.Resumed:
		if f, rerr := sessionstate.Read(dataDir, sess.ID); rerr == nil {
			base, hadBase = f, true
		}
	case adopt != nil:
		// The retention sweep removed the row: a fresh session replaces the file.
		s.forgetLocked(dataDir, adopt.Session)
		_ = sessionstate.Remove(dataDir, adopt.Session)
	}
	primary := in.Hint == "" && !s.havePrimary
	if hadBase {
		primary = base.Primary
	}
	f := base
	f.Parked = false // register is activity: a park left by a dead session ends (decision 18)
	f.Session, f.Identity, f.Profile = sess.ID, sess.Identity, conn.Resolved.Profile.Name
	f.DisplayName, f.ResumeToken = sess.DisplayName, sess.ResumeToken
	f.HarnessPID, f.HarnessStart = s.harness.Pid, s.harness.Start
	if clientName != "" {
		f.HarnessClient, f.HarnessClientVersion = clientName, clientVersion
	}
	f.MCPPID, f.Primary = s.self, primary
	if !hadBase {
		f.OpenedAt = s.nowMs()
	}
	if err := s.putLocked(dataDir, f); err != nil {
		return result(nil, bus.Internal(err))
	}
	if primary {
		s.havePrimary = true
	}
	// Any register unparks this harness's sessions, a hinted one included.
	s.unparkSessions(dataDir, s.registryIDsLocked())

	out := registerOut{
		Session: sess.ID, Identity: sess.Identity, DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		Resumed: sess.Resumed, Pending: make([]pendingOut, 0, len(sess.Pending)),
		RejectedCursors: make([]rejectedOut, 0, len(sess.RejectedCursors)),
	}
	for _, p := range sess.Pending {
		// Decision 20: the identity's subscriptions are shared, so a memory
		// subscription made outside the agent paths is dropped here.
		if mem, merr := s.kinds.IsMemory(ctx, conn.Bus, p.Channel); merr != nil {
			s.log.Warn("classifying a pending channel failed", "channel", p.Channel, "err", merr)
		} else if mem {
			if uerr := conn.Bus.Unsubscribe(ctx, bus.UnsubscribeInput{Channel: p.Channel}); uerr != nil {
				s.log.Warn("dropping a memory subscription failed", "channel", p.Channel, "err", uerr)
			}
			continue
		}
		out.Pending = append(out.Pending, pendingOut{Channel: p.Channel, Pending: p.Pending})
	}
	for _, r := range sess.RejectedCursors {
		out.RejectedCursors = append(out.RejectedCursors, rejectedOut{Channel: r.Channel, Reason: r.Reason})
	}
	ids, ierr := conn.Bus.Identities(ctx)
	if ierr != nil {
		s.log.Warn("listing identities for register failed", "err", ierr)
	}
	out.Others = identitiesOut(ids)
	return result(out, nil)
}
