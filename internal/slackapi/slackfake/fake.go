// Package slackfake is an in-memory slackapi.Client for tests: it enforces
// Slack's channel, archive and user rules, records calls, and delivers Socket
// Mode events with optional drop, repeat and reorder. It imports only the
// standard library and internal/slackapi.
package slackfake

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/slackapi"
)

// methods is the closed set of names Calls, FailNext and RateLimit accept.
var methods = []string{
	"AuthTest", "CreatePrivateChannel", "FindPrivateChannel", "Invite", "Archive",
	"Post", "History", "Replies", "Members", "UserInfo", "LookupByEmail", "RunSocket",
}

// FakeChannel is a snapshot of one channel.
type FakeChannel struct {
	ID, Name string
	Private  bool
	Archived bool
	Members  []string
	Messages []slackapi.Message
	Posts    []slackapi.PostInput
}

type channel struct {
	FakeChannel
}

type rateLimit struct {
	times      int
	retryAfter time.Duration
}

// Fake is the in-memory Slack. Its methods are safe for concurrent use.
type Fake struct {
	self slackapi.Self

	mu        sync.Mutex
	changed   chan struct{} // closed and replaced on every state change a socket waits for
	users     map[string]slackapi.User
	channels  map[string]*channel
	byName    map[string]string
	nextChan  int
	nextTS    int
	nextEnv   int
	pageSize  int
	calls     map[string]int
	fails     map[string][]error
	postFails []error // PostThenFail: Post succeeds, then returns the error
	limits    map[string]*rateLimit
	queue     []slackapi.Event
	acks      []string
	dropLeft  int
	repeat    bool
	reorderN  int // events still to collect for the current reorder group
	hold      []slackapi.Event
	gen       int // bumped by Disconnect
	active    int
	maxActive int
	done      chan struct{}
	doneShut  bool
	echo      bool
}

// New returns a Fake whose AuthTest reports self.
func New(self slackapi.Self) *Fake {
	return &Fake{
		self:     self,
		changed:  make(chan struct{}),
		users:    map[string]slackapi.User{},
		channels: map[string]*channel{},
		byName:   map[string]string{},
		pageSize: 100,
		calls:    map[string]int{},
		fails:    map[string][]error{},
		limits:   map[string]*rateLimit{},
		done:     make(chan struct{}),
	}
}

func checkMethod(m string) {
	if !slices.Contains(methods, m) {
		panic(fmt.Sprintf("slackfake: unknown method %q", m))
	}
}

// signal wakes every waiting socket. f.mu must be held.
func (f *Fake) signal() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// begin counts a call and applies a queued failure or rate limit. f.mu must
// be held.
func (f *Fake) begin(method string) error {
	f.calls[method]++
	if q := f.fails[method]; len(q) > 0 {
		f.fails[method] = q[1:]
		return q[0]
	}
	if rl := f.limits[method]; rl != nil && rl.times > 0 {
		rl.times--
		return &slackapi.RateLimitedError{Method: method, RetryAfter: rl.retryAfter}
	}
	return nil
}

func (f *Fake) newTS() string {
	f.nextTS++
	return fmt.Sprintf("%d.%06d", 1700000000+f.nextTS/1000000, f.nextTS%1000000)
}

func (f *Fake) newChannel(id, name string, private bool) *channel {
	c := &channel{FakeChannel{ID: id, Name: name, Private: private}}
	f.channels[id] = c
	return c
}

// chanFor returns the channel, creating a bare one for an id the test uses
// before creating it. f.mu must be held.
func (f *Fake) chanFor(id string) *channel {
	if c := f.channels[id]; c != nil {
		return c
	}
	return f.newChannel(id, id, true)
}

// --- slackapi.Client ---

func (f *Fake) AuthTest(context.Context) (slackapi.Self, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("AuthTest"); err != nil {
		return slackapi.Self{}, err
	}
	return f.self, nil
}

func (f *Fake) CreatePrivateChannel(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("CreatePrivateChannel"); err != nil {
		return "", err
	}
	if !slackapi.ValidChannelName(name) {
		return "", &slackapi.APIError{Method: "CreatePrivateChannel", Code: "invalid_name"}
	}
	if _, ok := f.byName[name]; ok {
		return "", slackapi.ErrNameTaken
	}
	f.nextChan++
	id := fmt.Sprintf("C%08d", f.nextChan)
	c := f.newChannel(id, name, true)
	c.Members = []string{f.self.UserID}
	f.byName[name] = id
	return id, nil
}

func (f *Fake) FindPrivateChannel(_ context.Context, name string) (slackapi.ChannelInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("FindPrivateChannel"); err != nil {
		return slackapi.ChannelInfo{}, err
	}
	id, ok := f.byName[name]
	if !ok {
		return slackapi.ChannelInfo{}, slackapi.ErrChannelNotFound
	}
	c := f.channels[id]
	return slackapi.ChannelInfo{ID: c.ID, Name: c.Name, Archived: c.Archived}, nil
}

func (f *Fake) Invite(_ context.Context, channelID string, userIDs []string) ([]slackapi.InviteFailure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("Invite"); err != nil {
		return nil, err
	}
	c := f.channels[channelID]
	if c == nil || c.Archived {
		return nil, slackapi.ErrChannelGone
	}
	var fails []slackapi.InviteFailure
	for _, u := range userIDs {
		if _, ok := f.users[u]; !ok {
			fails = append(fails, slackapi.InviteFailure{User: u, Code: "user_not_found"})
			continue
		}
		if !slices.Contains(c.Members, u) {
			c.Members = append(c.Members, u)
		}
	}
	return fails, nil
}

func (f *Fake) Archive(_ context.Context, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("Archive"); err != nil {
		return err
	}
	c := f.channels[channelID]
	if c == nil {
		return slackapi.ErrChannelGone
	}
	if c.Archived {
		return slackapi.ErrAlreadyArchived
	}
	c.Archived = true
	return nil
}

// Post appends the message to the channel's history and records the input. It
// queues no event unless EchoPosts is on; otherwise use SayAsBot or Emit to
// simulate Slack echoing the app's own post.
func (f *Fake) Post(_ context.Context, in slackapi.PostInput) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("Post"); err != nil {
		return "", err
	}
	c := f.channels[in.Channel]
	if c == nil || c.Archived {
		return "", slackapi.ErrChannelGone
	}
	in.Context = slices.Clone(in.Context)
	c.Posts = append(c.Posts, in)
	m := slackapi.Message{TS: f.newTS(), ThreadTS: in.ThreadTS, User: f.self.UserID, BotID: f.self.BotID, Text: in.Fallback, MetadataSeq: in.Seq}
	f.addMessage(c, m)
	if f.echo {
		f.enqueue(slackapi.Event{Kind: slackapi.EventMessage, Channel: in.Channel, User: m.User, Message: m})
	}
	if len(f.postFails) > 0 {
		err := f.postFails[0]
		f.postFails = f.postFails[1:]
		return "", err
	}
	return m.TS, nil
}

// addMessage appends m and bumps its thread root's reply count. f.mu held.
func (f *Fake) addMessage(c *channel, m slackapi.Message) {
	c.Messages = append(c.Messages, m)
	if m.ThreadTS != "" && m.ThreadTS != m.TS {
		for i := range c.Messages {
			if c.Messages[i].TS == m.ThreadTS {
				c.Messages[i].ReplyCount++
				break
			}
		}
	}
}

// page cuts one page out of ascending msgs; the cursor is "after:<ts>".
func (f *Fake) page(method string, msgs []slackapi.Message, cursor string) (slackapi.Page, error) {
	if cursor != "" {
		after, ok := strings.CutPrefix(cursor, "after:")
		if !ok || !slackapi.ValidTS(after) {
			return slackapi.Page{}, &slackapi.APIError{Method: method, Code: "invalid_cursor"}
		}
		i := 0
		for i < len(msgs) && msgs[i].TS <= after {
			i++
		}
		msgs = msgs[i:]
	}
	var p slackapi.Page
	if len(msgs) > f.pageSize {
		p.NextCursor = "after:" + msgs[f.pageSize-1].TS
		msgs = msgs[:f.pageSize]
	}
	p.Messages = slices.Clone(msgs)
	return p, nil
}

// History returns the channel's top-level messages and thread_broadcast
// replies (other thread replies excluded) newer than oldest. The fake keeps
// ascending order; the interface leaves per-page order unspecified.
func (f *Fake) History(_ context.Context, channelID, oldest, cursor string) (slackapi.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("History"); err != nil {
		return slackapi.Page{}, err
	}
	c := f.channels[channelID]
	if c == nil {
		return slackapi.Page{}, slackapi.ErrChannelGone
	}
	var msgs []slackapi.Message
	for _, m := range c.Messages {
		if (m.ThreadTS == "" || m.ThreadTS == m.TS || m.Subtype == "thread_broadcast") && m.TS > oldest {
			msgs = append(msgs, m)
		}
	}
	return f.page("History", msgs, cursor)
}

// Replies returns the thread root, then its replies newer than oldest.
func (f *Fake) Replies(_ context.Context, channelID, threadTS, oldest, cursor string) (slackapi.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("Replies"); err != nil {
		return slackapi.Page{}, err
	}
	c := f.channels[channelID]
	if c == nil {
		return slackapi.Page{}, slackapi.ErrChannelGone
	}
	var msgs []slackapi.Message
	found := false
	for _, m := range c.Messages {
		switch {
		case m.TS == threadTS:
			found = true
			msgs = append(msgs, m)
		case m.ThreadTS == threadTS && m.TS > oldest:
			msgs = append(msgs, m)
		}
	}
	if !found {
		return slackapi.Page{}, &slackapi.APIError{Method: "Replies", Code: "thread_not_found"}
	}
	return f.page("Replies", msgs, cursor)
}

func (f *Fake) Members(_ context.Context, channelID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("Members"); err != nil {
		return nil, err
	}
	c := f.channels[channelID]
	if c == nil {
		return nil, slackapi.ErrChannelGone
	}
	return slices.Clone(c.Members), nil
}

func (f *Fake) UserInfo(_ context.Context, userID string) (slackapi.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("UserInfo"); err != nil {
		return slackapi.User{}, err
	}
	u, ok := f.users[userID]
	if !ok {
		return slackapi.User{}, slackapi.ErrUserNotFound
	}
	return u, nil
}

func (f *Fake) LookupByEmail(_ context.Context, email string) (slackapi.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("LookupByEmail"); err != nil {
		return slackapi.User{}, err
	}
	for _, u := range f.users {
		if u.Email != "" && strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return slackapi.User{}, slackapi.ErrUserNotFound
}

// --- test controls ---

// AddUser registers (or replaces) a Slack user.
func (f *Fake) AddUser(u slackapi.User) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
}

// SetPageSize sets the History and Replies page size (default 100).
func (f *Fake) SetPageSize(n int) {
	if n < 1 {
		panic("slackfake: page size must be at least 1")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pageSize = n
}

// Say adds a user's message to the channel's history with the next ts and
// queues its event. A channel id never created is created bare; Say into an
// archived channel panics (a test-authoring error).
func (f *Fake) Say(channelID, user, text, threadTS string) slackapi.Message {
	return f.SaySubtype(channelID, user, text, threadTS, "")
}

// SayBroadcast is Say for a thread reply also sent to the channel
// (subtype thread_broadcast).
func (f *Fake) SayBroadcast(channelID, user, text, threadTS string) slackapi.Message {
	return f.SaySubtype(channelID, user, text, threadTS, "thread_broadcast")
}

// SaySubtype is Say with an explicit message subtype; the event carries it.
func (f *Fake) SaySubtype(channelID, user, text, threadTS, subtype string) slackapi.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.say(channelID, slackapi.Message{User: user, Text: text, ThreadTS: threadTS, Subtype: subtype})
}

// EchoPosts, when on, makes a successful Post also queue a message event for
// the app's own post (User and BotID are the app's), as real Slack does.
func (f *Fake) EchoPosts(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.echo = on
}

// SayAsBot is Say for a bot message carrying a bus seq in its metadata.
func (f *Fake) SayAsBot(channelID, botID, text, threadTS string, seq int64) slackapi.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.say(channelID, slackapi.Message{BotID: botID, Text: text, ThreadTS: threadTS, MetadataSeq: seq})
}

func (f *Fake) say(channelID string, m slackapi.Message) slackapi.Message {
	c := f.chanFor(channelID)
	if c.Archived {
		panic(fmt.Sprintf("slackfake: message into archived channel %q", channelID))
	}
	m.TS = f.newTS()
	f.addMessage(c, m)
	f.enqueue(slackapi.Event{Kind: slackapi.EventMessage, Channel: channelID, User: m.User, Message: m})
	return m
}

// Join adds a member without an event.
func (f *Fake) Join(channelID, user string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.chanFor(channelID)
	if !slices.Contains(c.Members, user) {
		c.Members = append(c.Members, user)
	}
}

// Leave removes a member and queues member_left_channel.
func (f *Fake) Leave(channelID, user string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.chanFor(channelID)
	c.Members = slices.DeleteFunc(c.Members, func(u string) bool { return u == user })
	f.enqueue(slackapi.Event{Kind: slackapi.EventMemberLeft, Channel: channelID, User: user})
}

// ArchiveByHand archives the channel as a Slack user would, without a call
// and without an event.
func (f *Fake) ArchiveByHand(channelID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chanFor(channelID).Archived = true
}

// Emit queues an arbitrary event (an EnvelopeID is assigned when empty).
func (f *Fake) Emit(ev slackapi.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueue(ev)
}

// DropNext discards the next n events before delivery (history keeps them).
// It overwrites any earlier count rather than adding to it.
func (f *Fake) DropNext(n int) {
	if n < 0 {
		panic("slackfake: DropNext of a negative count")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropLeft = n
}

// RepeatNext delivers the next event twice with the same envelope id.
func (f *Fake) RepeatNext() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repeat = true
}

// ReorderNext holds the next n events and delivers them in reverse order once
// the nth has been queued. It replaces any group still being held (those
// events are lost) rather than adding to it.
func (f *Fake) ReorderNext(n int) {
	if n < 0 {
		panic("slackfake: ReorderNext of a negative count")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reorderN = n
	f.hold = nil
}

// enqueue applies drop, reorder and repeat, then queues. f.mu held.
func (f *Fake) enqueue(ev slackapi.Event) {
	if ev.EnvelopeID == "" && ev.Kind != slackapi.EventConnected {
		f.nextEnv++
		ev.EnvelopeID = fmt.Sprintf("env-%d", f.nextEnv)
	}
	if f.dropLeft > 0 {
		f.dropLeft--
		return
	}
	if f.reorderN > 0 {
		f.hold = append(f.hold, ev)
		f.reorderN--
		if f.reorderN == 0 {
			slices.Reverse(f.hold)
			f.queue = append(f.queue, f.hold...)
			f.hold = nil
			f.signal()
		}
		return
	}
	f.queue = append(f.queue, ev)
	if f.repeat {
		f.repeat = false
		f.queue = append(f.queue, ev)
	}
	f.signal()
}

// Disconnect makes every running RunSocket deliver EventConnected again.
func (f *Fake) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gen++
	f.signal()
}

// FailNext makes the next call of method return err (queued: one per call).
func (f *Fake) FailNext(method string, err error) {
	checkMethod(method)
	if err == nil {
		panic("slackfake: FailNext of a nil error")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails[method] = append(f.fails[method], err)
}

// PostThenFail makes the next Post (queued: one per call, after any FailNext)
// store the message, so it shows in History and Replies, and then return err
// to the caller: a request that reached Slack whose answer was lost.
func (f *Fake) PostThenFail(err error) {
	if err == nil {
		panic("slackfake: PostThenFail of a nil error")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postFails = append(f.postFails, err)
}

// RateLimit makes the next times calls of method return a RateLimitedError.
// It overwrites any earlier limit for method rather than adding to it.
func (f *Fake) RateLimit(method string, times int, retryAfter time.Duration) {
	checkMethod(method)
	if times < 0 {
		panic("slackfake: RateLimit of a negative count")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limits[method] = &rateLimit{times: times, retryAfter: retryAfter}
}

// Channel returns a deep copy of the channel.
func (f *Fake) Channel(id string) (FakeChannel, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.channels[id]
	if c == nil {
		return FakeChannel{}, false
	}
	out := c.FakeChannel
	out.Members = slices.Clone(c.Members)
	out.Messages = slices.Clone(c.Messages)
	out.Posts = make([]slackapi.PostInput, len(c.Posts))
	for i, p := range c.Posts {
		p.Context = slices.Clone(p.Context)
		out.Posts[i] = p
	}
	return out, true
}

// Calls is how many times method was called, failed calls included.
func (f *Fake) Calls(method string) int {
	checkMethod(method)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// Acks lists the envelope ids acked so far, in order.
func (f *Fake) Acks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.acks)
}

// MaxConcurrentSockets is the most RunSocket calls ever running at once.
func (f *Fake) MaxConcurrentSockets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

// SocketDone is closed when the most recent RunSocket's delivery loop has
// exited. Before any RunSocket, or while one runs, it is open.
func (f *Fake) SocketDone() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}
