package remote

import (
	"fmt"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func ptr[T any](v T) *T { return &v }

// strPtr is nil for "".
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// int64Ptr is nil for 0.
func int64Ptr(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

// slicePtr is nil for a nil slice and keeps an empty one.
func slicePtr[T any](s []T) *[]T {
	if s == nil {
		return nil
	}
	return &s
}

func mapPtr(m map[string]string) *map[string]string {
	if m == nil {
		return nil
	}
	return &m
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// fromGenIdentityKind maps a wire identity kind; anything but agent and
// human is an internal error naming the field (a server speaking another
// protocol).
func fromGenIdentityKind(field, k string) (ab2bus.IdentityKind, error) {
	switch ab2bus.IdentityKind(k) {
	case ab2bus.IdentityAgent, ab2bus.IdentityHuman:
		return ab2bus.IdentityKind(k), nil
	}
	return "", &ab2bus.Error{Code: ab2bus.CodeInternal, Message: fmt.Sprintf("server sent unknown %s %q", field, k)}
}

func fromGenMessage(m gen.Message) (ab2bus.Message, error) {
	kind, err := fromGenIdentityKind("sender_kind", string(m.SenderKind))
	if err != nil {
		return ab2bus.Message{}, err
	}
	return ab2bus.Message{
		Seq: m.Seq, Channel: m.Channel, Sender: m.Sender, SenderKind: kind, Session: deref(m.Session),
		Display: m.Display, CreatedAt: m.CreatedAt, Type: m.Type, Subject: m.Subject,
		Content: m.Content, ReplyTo: m.ReplyTo, Metadata: deref(m.Metadata),
		MemoryID: m.MemoryId, Revision: m.Revision, Tags: deref(m.Tags),
		MatchedTags: deref(m.MatchedTags),
	}, nil
}

func toGenMessage(m ab2bus.Message) gen.Message {
	return gen.Message{
		Seq: m.Seq, Channel: m.Channel, Sender: m.Sender, SenderKind: gen.MessageSenderKind(m.SenderKind), Session: strPtr(m.Session),
		Display: m.Display, CreatedAt: m.CreatedAt, Type: m.Type, Subject: m.Subject,
		Content: m.Content, ReplyTo: m.ReplyTo, Metadata: mapPtr(m.Metadata),
		MemoryId: m.MemoryID, Revision: m.Revision, Tags: slicePtr(m.Tags),
		MatchedTags: slicePtr(m.MatchedTags),
	}
}

func fromGenKind(k gen.ChannelKind) ab2bus.Kind { return ab2bus.Kind(k) }

func toGenKind(k ab2bus.Kind) gen.ChannelKind { return gen.ChannelKind(k) }

func fromGenACLEntry(e gen.ACLEntry) ab2bus.ACLEntry {
	out := ab2bus.ACLEntry{Identity: e.Identity}
	if e.Permissions != nil {
		out.Permissions = make([]ab2bus.Permission, len(e.Permissions))
		for i, p := range e.Permissions {
			out.Permissions[i] = ab2bus.Permission(p)
		}
	}
	return out
}

func toGenACLEntry(e ab2bus.ACLEntry) gen.ACLEntry {
	out := gen.ACLEntry{Identity: e.Identity, Permissions: make([]gen.ACLEntryPermissions, len(e.Permissions))}
	for i, p := range e.Permissions {
		out.Permissions[i] = gen.ACLEntryPermissions(p)
	}
	return out
}

func fromGenTask(t gen.Task) ab2bus.Task {
	return ab2bus.Task{
		ID: t.Id, Channel: t.Channel, Revision: t.Revision, Subject: t.Subject,
		Description: deref(t.Description), Status: t.Status, Owner: deref(t.Owner),
		Parent: deref(t.Parent), Rank: t.Rank, BlockedBy: deref(t.BlockedBy),
		LeasedUntil: deref(t.LeasedUntil), Metadata: deref(t.Metadata), Blocked: t.Blocked,
		OpenBlockers: deref(t.OpenBlockers), UpdatedAt: t.UpdatedAt, UpdatedBy: t.UpdatedBy,
	}
}

func toGenTask(t ab2bus.Task) gen.Task {
	return gen.Task{
		Id: t.ID, Channel: t.Channel, Revision: t.Revision, Subject: t.Subject,
		Description: strPtr(t.Description), Status: t.Status, Owner: strPtr(t.Owner),
		Parent: int64Ptr(t.Parent), Rank: t.Rank, BlockedBy: slicePtr(t.BlockedBy),
		LeasedUntil: int64Ptr(t.LeasedUntil), Metadata: mapPtr(t.Metadata), Blocked: t.Blocked,
		OpenBlockers: slicePtr(t.OpenBlockers), UpdatedAt: t.UpdatedAt, UpdatedBy: t.UpdatedBy,
	}
}

func fromGenTaskSummary(s gen.TaskSummary) ab2bus.TaskSummary {
	return ab2bus.TaskSummary{
		ID: s.Id, Subject: s.Subject, Status: s.Status, Owner: deref(s.Owner),
		Parent: deref(s.Parent), Depth: s.Depth, OpenBlockers: deref(s.OpenBlockers),
		LeasedUntil: deref(s.LeasedUntil), HasDetails: deref(s.HasDetails),
	}
}

func toGenTaskSummary(s ab2bus.TaskSummary) gen.TaskSummary {
	out := gen.TaskSummary{
		Id: s.ID, Subject: s.Subject, Status: s.Status, Owner: strPtr(s.Owner),
		Parent: int64Ptr(s.Parent), Depth: s.Depth, OpenBlockers: slicePtr(s.OpenBlockers),
		LeasedUntil: int64Ptr(s.LeasedUntil),
	}
	if s.HasDetails {
		out.HasDetails = ptr(true)
	}
	return out
}

func unknownEnum(member, v string) error {
	return &ab2bus.Error{Code: ab2bus.CodeInternal, Message: fmt.Sprintf("server sent unknown health %s %q", member, v)}
}

// fromGenHealth maps the wire health document; an enum value outside spec
// 2026-10-25 section 4 is an internal error naming the member (a server
// speaking another protocol). Absent members stay nil; the arrays of the
// admin tier are never nil.
func fromGenHealth(g gen.Health) (ab2bus.Health, error) {
	out := ab2bus.Health{
		Status: ab2bus.HealthStatus(g.Status), Tier: ab2bus.HealthTier(g.Tier),
		Version: g.Version, Now: g.Now, StartedAt: deref(g.StartedAt),
	}
	if !ab2bus.ValidHealthStatus(out.Status) {
		return ab2bus.Health{}, unknownEnum("status", string(g.Status))
	}
	if !ab2bus.ValidHealthTier(out.Tier) {
		return ab2bus.Health{}, unknownEnum("tier", string(g.Tier))
	}
	if s := g.Store; s != nil {
		if s.Engine != "sqlite" && s.Engine != "postgres" {
			return ab2bus.Health{}, unknownEnum("store.engine", string(s.Engine))
		}
		out.Store = &ab2bus.HealthStore{Engine: string(s.Engine), SchemaVersion: s.SchemaVersion}
	}
	if e := g.Embeddings; e != nil {
		st := ab2bus.EmbeddingState(e.State)
		if !ab2bus.ValidEmbeddingState(st) {
			return ab2bus.Health{}, unknownEnum("embeddings.state", string(e.State))
		}
		out.Embeddings = &ab2bus.HealthEmbeddings{
			State: st, Backlog: e.Backlog, Rejected: e.Rejected, Model: deref(e.Model),
			LastError: deref(e.LastError), LastErrorAt: deref(e.LastErrorAt), RejectedLast: deref(e.RejectedLast),
		}
	}
	if u := g.Storage; u != nil {
		out.Storage = &ab2bus.Usage{Bytes: u.Bytes, BudgetBytes: u.BudgetBytes, Evictable: u.Evictable, Notice: deref(u.Notice)}
	}
	if g.Notices != nil {
		out.Notices = make([]ab2bus.Notice, len(*g.Notices))
		for i, n := range *g.Notices {
			out.Notices[i] = ab2bus.Notice{Kind: n.Kind, Message: n.Message, SetAt: n.SetAt}
		}
	}
	if m := g.Maintenance; m != nil {
		lock := ab2bus.MaintenanceLock(m.Lock)
		if !ab2bus.ValidMaintenanceLock(lock) {
			return ab2bus.Health{}, unknownEnum("maintenance.lock", string(m.Lock))
		}
		hm := &ab2bus.HealthMaintenance{
			IntervalSeconds: m.IntervalSeconds, Owner: m.Owner, Lock: lock,
			LastTickAt: deref(m.LastTickAt), LockError: deref(m.LockError),
			Jobs: make([]ab2bus.HealthJob, len(m.Jobs)),
		}
		for i, j := range m.Jobs {
			hm.Jobs[i] = ab2bus.HealthJob{Name: j.Name, Changed: j.Changed, Error: deref(j.Error), Skipped: j.Skipped}
		}
		out.Maintenance = hm
	}
	return out, nil
}
