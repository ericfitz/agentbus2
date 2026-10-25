package server

import (
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Converters between the domain types and the generated wire types, mirroring
// internal/bus/remote/convert.go.

func derefOr[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func toGenMessage(m bus.Message) gen.Message {
	out := gen.Message{
		Seq: m.Seq, Channel: m.Channel, Sender: m.Sender, SenderKind: gen.MessageSenderKind(m.SenderKind), Display: m.Display,
		CreatedAt: m.CreatedAt, Type: m.Type, Subject: m.Subject, Content: m.Content,
		ReplyTo: m.ReplyTo, MemoryId: m.MemoryID, Revision: m.Revision,
	}
	if m.Session != "" {
		s := m.Session
		out.Session = &s
	}
	if m.Metadata != nil {
		out.Metadata = &m.Metadata
	}
	if m.Tags != nil {
		out.Tags = &m.Tags
	}
	if m.MatchedTags != nil {
		out.MatchedTags = &m.MatchedTags
	}
	return out
}

// optStr is nil for "": an optional member is absent, never null.
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// healthToGen maps the health document to the wire; members below the
// caller's tier stay nil so they are absent, never null.
func healthToGen(h bus.Health) gen.Health {
	out := gen.Health{
		Status: gen.HealthStatus(h.Status), Tier: gen.HealthTier(h.Tier),
		Version: h.Version, Now: h.Now,
	}
	if h.StartedAt != 0 {
		out.StartedAt = &h.StartedAt
	}
	if h.Store != nil {
		out.Store = &gen.HealthStore{Engine: gen.HealthStoreEngine(h.Store.Engine), SchemaVersion: h.Store.SchemaVersion}
	}
	if e := h.Embeddings; e != nil {
		g := &gen.HealthEmbeddings{State: gen.HealthEmbeddingsState(e.State), Backlog: e.Backlog, Rejected: e.Rejected}
		if e.Model != "" {
			g.Model = &e.Model
		}
		if e.LastError != "" {
			g.LastError = &e.LastError
		}
		if e.LastErrorAt != 0 {
			g.LastErrorAt = &e.LastErrorAt
		}
		if e.RejectedLast != "" {
			g.RejectedLast = &e.RejectedLast
		}
		out.Embeddings = g
	}
	if u := h.Storage; u != nil {
		g := &gen.Usage{Bytes: u.Bytes, BudgetBytes: u.BudgetBytes, Evictable: u.Evictable}
		if u.Notice != "" {
			g.Notice = &u.Notice
		}
		out.Storage = g
	}
	if h.Notices != nil {
		ns := make([]gen.Notice, len(h.Notices))
		for i, n := range h.Notices {
			ns[i] = gen.Notice{Kind: n.Kind, Message: n.Message, SetAt: n.SetAt}
		}
		out.Notices = &ns
	}
	if m := h.Maintenance; m != nil {
		g := &gen.HealthMaintenance{
			IntervalSeconds: m.IntervalSeconds, Owner: m.Owner, Lock: gen.HealthMaintenanceLock(m.Lock),
			Jobs: make([]gen.HealthJob, len(m.Jobs)),
		}
		if m.LastTickAt != 0 {
			g.LastTickAt = &m.LastTickAt
		}
		if m.LockError != "" {
			g.LockError = &m.LockError
		}
		for i, j := range m.Jobs {
			gj := gen.HealthJob{Name: j.Name, Changed: j.Changed, Skipped: j.Skipped}
			if j.Error != "" {
				gj.Error = &j.Error
			}
			g.Jobs[i] = gj
		}
		out.Maintenance = g
	}
	return out
}
