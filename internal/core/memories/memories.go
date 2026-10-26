package memories

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

const taskListMsg = "%s is a task list; use task_create, task_update, task_claim, task_release"

// Get returns the live revision of memory id. It needs read on the memory's
// channel (Decision 95) and moves the memory's access time to now (v1
// TouchMemoryAccess, ADR 0013). The touch is a write, so it runs in Tx
// (Decision 49). A task row has no access row and gets none (a task must
// never become eligible for memory expiry).
func (s Service) Get(ctx context.Context, identity string, id int64) (bus.Message, error) {
	var out bus.Message
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		m, ch, err := s.LiveRevision(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.Access.Require(ctx, tx, p, access.Channel{ID: ch.ID}, access.Read, access.WithNotFound(core.MemoryNotLiveFmt, id)); err != nil {
			return err
		}
		if ch.Kind != store.KindTasks {
			if err := tx.TouchMemoryAccess(ctx, []int64{id}, s.NowMs()); err != nil {
				return core.FromStore(err)
			}
		}
		msgs, err := s.ToBusMessages(ctx, tx, []store.Message{m})
		if err != nil {
			return err
		}
		out = msgs[0]
		return nil
	})
	if err != nil {
		return bus.Message{}, core.FromStore(err)
	}
	return out, nil
}

// Revisions returns every revision of memory id by revision, tombstoned ones
// included (v1 MemoryRevisions). It needs read on the memory's channel and is
// not_found when the memory has no rows.
func (s Service) Revisions(ctx context.Context, identity string, id int64) ([]bus.Message, error) {
	var out []bus.Message
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		rows, err := tx.MemoryRevisions(ctx, id)
		if err != nil {
			return core.FromStore(err)
		}
		if len(rows) == 0 {
			return core.E(bus.CodeNotFound, "memory %d does not exist", id)
		}
		if err := s.Access.Require(ctx, tx, p, access.Channel{ID: rows[0].ChannelID}, access.Read, access.WithNotFound("memory %d does not exist", id)); err != nil {
			return err
		}
		out, err = s.ToBusMessages(ctx, tx, rows)
		return err
	})
	if err != nil {
		return nil, core.FromStore(err)
	}
	return out, nil
}

// Edit tombstones the live revision of memory in.ID and inserts the next one
// (v1 EditMemory). A nil Subject, Content, Type, Metadata or Tags keeps the
// live revision's; a pointer to "", an empty map or an empty list clears,
// except Content, which cannot be cleared (post-build decision 10). A nonzero
// IfRevision must equal the live revision, checked in the same transaction
// before the tombstone. It needs write on the memory's
// channel and refuses a tasks-kind channel. The idempotency key replays the
// stored result (Decision 46); a replay charges nothing and publishes nothing.
func (s Service) Edit(ctx context.Context, identity string, in bus.EditMemoryInput) (bus.EditResult, error) {
	if in.Content != nil && *in.Content == "" {
		return bus.EditResult{}, core.E(bus.CodeValidation, "content must not be empty; delete the memory instead")
	}
	if in.IfRevision < 0 {
		return bus.EditResult{}, core.E(bus.CodeValidation, "if_revision must be positive")
	}
	if in.Subject == nil && in.Content == nil && in.Type == nil && in.Metadata == nil && in.Tags == nil {
		return bus.EditResult{}, core.E(bus.CodeValidation, "nothing to change: give at least one of subject, content, type, metadata or tags")
	}
	if in.Subject != nil {
		sub, err := rules.NormalizeSubject(*in.Subject)
		if err != nil {
			return bus.EditResult{}, err
		}
		in.Subject = &sub
	}
	if in.Tags != nil {
		tags, err := rules.NormalizeTags(in.Tags)
		if err != nil {
			return bus.EditResult{}, err
		}
		if tags == nil {
			tags = []string{} // stays non-nil: "clear", not "keep"
		}
		in.Tags = tags
	}
	key := in.IdempotencyKey
	in.IdempotencyKey = ""

	var (
		res      bus.EditResult
		size     int
		chanID   int64
		replayed bool
	)
	err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
		res, size, chanID, replayed = bus.EditResult{}, 0, 0, false // fn may run again (Decision 26)
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		replayed, err = s.WithReceipt(ctx, tx, identity, key, in, &res, func() error {
			old, ch, err := s.LiveRevision(ctx, tx, in.ID)
			if err != nil {
				return err
			}
			if err := s.Access.Require(ctx, tx, p, access.Channel{ID: ch.ID}, access.Write, access.WithNotFound(core.MemoryNotLiveFmt, in.ID)); err != nil {
				return err
			}
			if ch.Kind == store.KindTasks {
				return core.E(bus.CodeValidation, taskListMsg, ch.Name)
			}
			live := int64(1)
			if old.Revision != nil {
				live = *old.Revision
			}
			if in.IfRevision != 0 && in.IfRevision != live {
				return core.E(bus.CodeConflict, "memory %d is at revision %d, not %d", in.ID, live, in.IfRevision)
			}
			subject, content, mtype, meta := old.Subject, old.Content, old.Type, old.Metadata
			if in.Subject != nil {
				subject = *in.Subject
			}
			if in.Content != nil {
				content = *in.Content
			}
			if in.Type != nil {
				mtype = *in.Type
			}
			if in.Metadata != nil {
				meta = *in.Metadata
			}
			tags := old.Tags
			if in.Tags != nil {
				tags = in.Tags
			}
			ai := core.AppendInput{
				ChannelID: ch.ID, Channel: ch.Name, Kind: store.KindMemory, Sender: identity, SenderKind: p.Kind,
				Type: mtype, Subject: subject, Content: content, Metadata: meta, Tags: tags,
			}
			size, err = s.EnvelopeSize(ai, identity)
			if err != nil {
				return err
			}
			if !s.Limits.Check(identity, size, s.Now()) {
				return core.E(bus.CodeRateLimited, "send_messages_per_second rate limit exceeded for %s", identity)
			}
			now := s.NowMs()
			if _, err := tx.TombstoneMessage(ctx, old.Seq, now); err != nil {
				return core.FromStore(err)
			}
			if err := tx.DeleteEmbedding(ctx, old.Seq); err != nil {
				return core.FromStore(err)
			}
			// The ordinary append path, then the revision links.
			ai.Kind = store.KindOrdinary
			seq, _, err := s.Append(ctx, tx, ai)
			if err != nil {
				return err
			}
			rev := live + 1
			if err := tx.SetMemory(ctx, seq, in.ID, rev); err != nil {
				return core.FromStore(err)
			}
			if err := tx.TouchMemoryAccess(ctx, []int64{in.ID}, now); err != nil {
				return core.FromStore(err)
			}
			res = bus.EditResult{Seq: seq, MemoryID: in.ID, Revision: rev, Replaced: old.Seq}
			chanID = ch.ID
			return nil
		})
		return err
	})
	if err != nil {
		return bus.EditResult{}, core.FromStore(err)
	}
	if !replayed {
		s.Limits.Charge(identity, size, s.Now())
		s.Publish(notify.Hint{ChannelID: chanID, Seq: res.Seq})
		if s.EmbedSoon != nil {
			s.EmbedSoon()
		}
	}
	return res, nil
}

// Delete tombstones the live revision of memory in.ID (v1 DeleteMemory),
// dropping its embedding and access row. It needs write on the memory's
// channel and refuses a tasks-kind channel. A keyed retry returns nil without
// touching the Store again (Decision 46). It charges one operation and no
// bytes after the commit.
func (s Service) Delete(ctx context.Context, identity string, in bus.DeleteMemoryInput) error {
	payload := map[string]any{"delete": in.ID}
	var replayed bool
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		replayed = false // fn may run again (Decision 26)
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		var ignored *struct{}
		replayed, err = s.WithReceipt(ctx, tx, identity, in.IdempotencyKey, payload, &ignored, func() error {
			m, ch, err := s.LiveRevision(ctx, tx, in.ID)
			if err != nil {
				return err
			}
			if err := s.Access.Require(ctx, tx, p, access.Channel{ID: ch.ID}, access.Write, access.WithNotFound(core.MemoryNotLiveFmt, in.ID)); err != nil {
				return err
			}
			if ch.Kind == store.KindTasks {
				return core.E(bus.CodeValidation, taskListMsg, ch.Name)
			}
			if !s.Limits.Check(identity, 0, s.Now()) {
				return core.E(bus.CodeRateLimited, "send_messages_per_second rate limit exceeded for %s", identity)
			}
			ok, err := s.TombstoneMemory(ctx, tx, in.ID, m.Seq, s.NowMs())
			if err != nil {
				return err
			}
			if !ok {
				return core.E(bus.CodeNotFound, "memory %d has no live revision", in.ID)
			}
			return nil
		})
		return err
	})
	if err != nil {
		return core.FromStore(err)
	}
	if !replayed {
		s.Limits.Charge(identity, 0, s.Now())
	}
	return nil
}
