package bus

import (
	"context"
	"sync"
	"time"
)

// kindTTL bounds how long a cached channel-kind map is trusted: a channel
// deleted and recreated under the same name with another kind is picked up
// within it. An unknown name refreshes at once.
const kindTTL = time.Minute

// KindIndex maps channel names to kinds for the agent-facing paths that must
// not push memory-channel messages (decision 20). One index serves one
// process; it is safe for concurrent use. The TUI, which observes memories,
// does not use it.
type KindIndex struct {
	mu    sync.Mutex
	kinds map[string]Kind
	at    time.Time
	now   func() time.Time // nil: time.Now
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
func (k *KindIndex) load(ctx context.Context, m Messaging) error {
	cs, err := m.ListChannels(ctx)
	if err != nil {
		return err
	}
	k.kinds = make(map[string]Kind, len(cs))
	for _, c := range cs {
		k.kinds[c.Name] = c.Kind
	}
	k.at = k.clock()
	return nil
}

// IsMemory reports whether the channel is a memory channel. A name the cached
// map does not know, or a map older than kindTTL, is looked up afresh; a name
// still unknown (a deleted channel, or one the caller cannot read) is not a
// memory channel.
func (k *KindIndex) IsMemory(ctx context.Context, m Messaging, name string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if kind, ok := k.kinds[name]; ok && k.clock().Sub(k.at) < kindTTL {
		return kind == KindMemory, nil
	}
	if err := k.load(ctx, m); err != nil {
		return false, err
	}
	return k.kinds[name] == KindMemory, nil
}

// DropMemory returns msgs without those on memory channels (tag matches
// included: a message carries the channel it lives on). It returns msgs
// itself when nothing is dropped.
func (k *KindIndex) DropMemory(ctx context.Context, m Messaging, msgs []Message) ([]Message, error) {
	var out []Message
	for i, msg := range msgs {
		mem, err := k.IsMemory(ctx, m, msg.Channel)
		if err != nil {
			return nil, err
		}
		if mem && out == nil {
			out = append(make([]Message, 0, len(msgs)), msgs[:i]...)
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
// and the caller re-arms with the floor advanced to r.Seq.
func (k *KindIndex) AgentWake(ctx context.Context, m Messaging, r WatchResult) (WatchResult, error) {
	if !r.Woke || len(r.Channels) == 0 {
		return r, nil
	}
	kept := make([]string, 0, len(r.Channels))
	for _, n := range r.Channels {
		mem, err := k.IsMemory(ctx, m, n)
		if err != nil {
			return r, err
		}
		if !mem {
			kept = append(kept, n)
		}
	}
	if len(kept) == len(r.Channels) {
		return r, nil
	}
	r.Channels = kept
	r.Woke = len(kept) > 0
	return r, nil
}

// ReceiveAgent is Receive as an agent runs it: messages on memory channels,
// tag matches included, are dropped from the batch, but the batch is still
// acknowledged, so a dropped memory never comes back. A batch with nothing
// left is acknowledged here and the next one read, until a batch has
// something an agent should see or none is pending. Gaps and the notice of a
// skipped batch are carried to the result. The TUI uses Receive directly.
func (k *KindIndex) ReceiveAgent(ctx context.Context, b Bus, in ReceiveInput) (ReceiveResult, error) {
	var gaps []Gap
	var notice string
	ackIgnored := false
	for {
		res, err := b.Receive(ctx, in)
		if err != nil {
			return ReceiveResult{}, err
		}
		ackIgnored = ackIgnored || res.AckIgnored
		kept, err := k.DropMemory(ctx, b, res.Messages)
		if err != nil {
			return ReceiveResult{}, err
		}
		dropped := len(kept) != len(res.Messages)
		res.Messages = kept
		if dropped && len(kept) == 0 && res.Batch != "" {
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
