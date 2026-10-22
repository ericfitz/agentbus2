// Package agentview is the agent's view of the Bus (decision 20): agents never
// receive memory channels. It wraps a bus.Bus on the client side; the TUI, the
// bus observer, does not use it.
package agentview

import (
	"context"
	"sync"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
)

const (
	// kindTTL bounds how long a cached channel-kind map is trusted: a channel
	// deleted and recreated under the same name with another kind is picked
	// up within it.
	kindTTL = time.Minute
	// missTTL bounds how long a name the map lacks (a deleted channel) is
	// remembered as absent, so a stream of messages from one does not list
	// channels each time. It is short: a channel created meanwhile as a
	// memory channel must not be missed for long.
	missTTL = 10 * time.Second
	// maxSkipped bounds how many consecutive memory-only batches ReceiveAgent
	// acknowledges before it returns an empty result.
	maxSkipped = 50
)

// KindIndex maps channel names to kinds for the agent-facing paths that must
// not push memory-channel messages. One index serves one process; it is safe
// for concurrent use.
type KindIndex struct {
	mu     sync.Mutex
	kinds  map[string]bus.Kind
	at     time.Time
	misses map[string]time.Time
	now    func() time.Time // nil: time.Now
}

// NewKindIndex returns an empty index.
func NewKindIndex() *KindIndex { return &KindIndex{} }

func (k *KindIndex) clock() time.Time {
	if k.now != nil {
		return k.now()
	}
	return time.Now()
}

// load refreshes the map from ListChannels. k.mu is held.
func (k *KindIndex) load(ctx context.Context, m bus.Messaging) error {
	cs, err := m.ListChannels(ctx)
	if err != nil {
		return err
	}
	k.kinds = make(map[string]bus.Kind, len(cs))
	for _, c := range cs {
		k.kinds[c.Name] = c.Kind
	}
	k.misses = map[string]time.Time{}
	k.at = k.clock()
	return nil
}

// IsMemory reports whether the channel is a memory channel. A cached map older
// than kindTTL, or a name it lacks (unless looked up missTTL ago), is looked
// up afresh; a name still unknown (a deleted channel, or one the caller cannot
// read) is not a memory channel.
func (k *KindIndex) IsMemory(ctx context.Context, m bus.Messaging, name string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.clock()
	if now.Sub(k.at) < kindTTL {
		if kind, ok := k.kinds[name]; ok {
			return kind == bus.KindMemory, nil
		}
		if t, ok := k.misses[name]; ok && now.Sub(t) < missTTL {
			return false, nil
		}
	}
	if err := k.load(ctx, m); err != nil {
		return false, err
	}
	kind, ok := k.kinds[name]
	if !ok {
		k.misses[name] = k.at
	}
	return kind == bus.KindMemory, nil
}

// DropMemory returns msgs without those on memory channels (tag matches
// included: a message carries the channel it lives on). It returns msgs
// itself when nothing is dropped.
func (k *KindIndex) DropMemory(ctx context.Context, m bus.Messaging, msgs []bus.Message) ([]bus.Message, error) {
	var out []bus.Message
	for i, msg := range msgs {
		mem, err := k.IsMemory(ctx, m, msg.Channel)
		if err != nil {
			return nil, err
		}
		if mem && out == nil {
			out = append(make([]bus.Message, 0, len(msgs)), msgs[:i]...)
		} else if !mem && out != nil {
			out = append(out, msg)
		}
	}
	if out == nil {
		return msgs, nil
	}
	return out, nil
}

// AgentWake drops the memory channels from a wake. A wake whose channels were
// all memory channels comes back with Woke false: it is not one for an agent,
// and the caller re-arms with the floor advanced to r.Seq. When some channels
// were dropped the Count no longer says anything about what is left, so it is
// set to 0 (unknown); filtered reports that.
func (k *KindIndex) AgentWake(ctx context.Context, m bus.Messaging, r bus.WatchResult) (res bus.WatchResult, filtered bool, err error) {
	if !r.Woke || len(r.Channels) == 0 {
		return r, false, nil
	}
	kept := make([]string, 0, len(r.Channels))
	for _, n := range r.Channels {
		mem, err := k.IsMemory(ctx, m, n)
		if err != nil {
			return r, false, err
		}
		if !mem {
			kept = append(kept, n)
		}
	}
	if len(kept) == len(r.Channels) {
		return r, false, nil
	}
	r.Channels = kept
	r.Count = 0
	r.Woke = len(kept) > 0
	return r, true, nil
}

// ReceiveAgent is Receive as an agent runs it: messages on memory channels,
// tag matches included, are dropped from the batch, but the batch is still
// acknowledged, so a dropped memory never comes back. A batch with nothing
// left is acknowledged here and the next one read, until a batch has
// something an agent should see or none is pending; after maxSkipped such
// batches, or when the acknowledgment is ignored, it returns what it has
// (its batch token is still valid: the caller acknowledges it as usual). Gaps and the notice of a skipped batch
// are carried to the result. The TUI uses Receive directly.
func (k *KindIndex) ReceiveAgent(ctx context.Context, b bus.Bus, in bus.ReceiveInput) (bus.ReceiveResult, error) {
	var gaps []bus.Gap
	var notice string
	ackIgnored := false
	for skipped := 0; ; skipped++ {
		res, err := b.Receive(ctx, in)
		if err != nil {
			return bus.ReceiveResult{}, err
		}
		ackIgnored = ackIgnored || res.AckIgnored
		kept, err := k.DropMemory(ctx, b, res.Messages)
		if err != nil {
			return bus.ReceiveResult{}, err
		}
		dropped := len(kept) != len(res.Messages)
		res.Messages = kept
		if dropped && len(kept) == 0 && res.Batch != "" && skipped < maxSkipped && !res.AckIgnored {
			// Only memories: acknowledge the batch and read on.
			gaps = append(gaps, res.Gaps...)
			if res.Notice != "" {
				notice = res.Notice
			}
			in.Ack = res.Batch
			continue
		}
		res.AckIgnored = ackIgnored
		res.Gaps = append(gaps, res.Gaps...)
		if res.Notice == "" {
			res.Notice = notice
		}
		return res, nil
	}
}
