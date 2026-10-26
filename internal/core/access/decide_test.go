package access

import (
	"slices"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

var (
	r   = []store.Permission{store.PermRead}
	w   = []store.Permission{store.PermWrite}
	rw  = []store.Permission{store.PermRead, store.PermWrite}
	rwa = []store.Permission{store.PermRead, store.PermWrite, store.PermAdmin}
)

func entry(id string, p []store.Permission) store.ACLEntry {
	return store.ACLEntry{Identity: id, Permissions: p}
}

func chFacts(scope string, kind store.Kind, rows ...store.ACLEntry) channelFacts {
	return channelFacts{Channel: store.Channel{ID: 1, Name: "work", Kind: kind, Scope: scope}, Rows: rows}
}

var (
	pat   = Principal{Name: "pat", Kind: store.IdentityAgent}
	root  = Principal{Name: "root", Kind: store.IdentityAgent, Admin: true}
	hal   = Principal{Name: "hal", Kind: store.IdentityHuman}
	coder = Principal{Name: "job1-coder", Kind: store.IdentityAgent, Scope: "job1"}
)

func TestDecideChannelRules(t *testing.T) {
	type tc struct {
		name   string
		p      Principal
		f      facts
		a      Action
		want   bool
		reason string
	}
	ord := store.KindOrdinary
	cases := []tc{
		{"read via everyone", pat, chFacts("", ord, entry(store.Everyone, r)), Read, true, ""},
		{"read via admins row, admin", root, chFacts("", ord, entry(store.Admins, r)), Read, true, ""},
		{"read via admins row, non-admin", pat, chFacts("", ord, entry(store.Admins, r)), Read, false, ReasonNoPermission},
		{"write row does not read", pat, chFacts("", ord, entry("pat", w)), Read, false, ReasonNoPermission},
		{"admin by flag, no rows", root, chFacts("", ord), Admin, true, ""},
		{"creator without a row is not admin", pat, channelFacts{Channel: store.Channel{ID: 1, Name: "work", Creator: "pat"}}, Admin, false, ReasonNoPermission},
		{"read+write rows do not admin", pat, chFacts("", ord, entry("pat", rw)), Admin, false, ReasonNoPermission},
		{"stored admin row admins", pat, chFacts("", ord, entry("pat", rwa)), Admin, true, ""},
		{"implicit admin is not read", root, chFacts("", ord), Read, false, ReasonNoPermission},
		{"implicit admin is not write", root, chFacts("", ord), Write, false, ReasonNoPermission},
		{"list unscoped, no rows", pat, chFacts("", ord), List, true, ""},
		{"list unscoped, rows", pat, chFacts("", ord, entry("kim", r)), List, true, ""},
		{"deliver agent ordinary", pat, chFacts("", ord, entry("pat", r)), Deliver, true, ""},
		{"deliver agent memory", pat, chFacts("", store.KindMemory, entry("pat", r)), Deliver, false, ReasonMemoryChannel},
		{"deliver human memory", hal, chFacts("", store.KindMemory, entry("hal", r)), Deliver, true, ""},
		{"deliver agent without read", pat, chFacts("", ord), Deliver, false, ReasonNoPermission},
		{"read memory as agent", pat, chFacts("", store.KindMemory, entry("pat", r)), Read, true, ""},
		{"post human with write", hal, chFacts("", ord, entry("hal", w)), Post, true, ""},
		{"post agent with write", pat, chFacts("", ord, entry("pat", w)), Post, false, ReasonNotHuman},
		{"post human without write", hal, chFacts("", ord, entry("hal", r)), Post, false, ReasonNoWrite},
		{"scoped in scope read", coder, chFacts("job1", ord, entry(store.Everyone, rw)), Read, true, ""},
		{"scoped in scope list", coder, chFacts("job1", ord), List, true, ""},
	}
	for _, c := range cases {
		d := decide(c.p, c.f, c.a)
		if d.Allowed != c.want || d.Reason != c.reason {
			t.Errorf("%s: got %+v, want allowed=%v reason=%q", c.name, d, c.want, c.reason)
		}
	}
}

func TestDeliverMemoryFailsClosedOnUnknownKind(t *testing.T) {
	f := chFacts("", store.KindMemory, entry(store.Everyone, r))
	unknown := Principal{Name: "x", Kind: "robot"}
	if d := decide(unknown, f, Deliver); d.Allowed || d.Reason != ReasonMemoryChannel {
		t.Errorf("unknown kind on a memory channel = %+v, want memory_channel denial", d)
	}
	if !decide(unknown, chFacts("", store.KindOrdinary, entry(store.Everyone, r)), Deliver).Allowed {
		t.Error("unknown kind denied an ordinary channel it may read")
	}
}

func TestDecideDenialTexts(t *testing.T) {
	d := decide(pat, chFacts("", store.KindOrdinary, entry("pat", r)), Write)
	if d.Code != bus.CodeForbidden || d.Text != `pat needs write on channel "work"` {
		t.Errorf("write denial = %+v", d)
	}
	d = decide(pat, chFacts("", store.KindMemory, entry("pat", r)), Deliver)
	want := "work is a memory channel: agents do not subscribe to memories (decision 20); use search to find a memory and get_memory to read it"
	if d.Code != bus.CodeValidation || d.Text != want {
		t.Errorf("memory denial = %+v", d)
	}
}

func TestDecideServer(t *testing.T) {
	if d := decide(root, serverFacts{}, Administer); !d.Allowed {
		t.Errorf("admin Administer = %+v", d)
	}
	d := decide(pat, serverFacts{}, Administer)
	if d.Allowed || d.Code != bus.CodeForbidden || d.Reason != ReasonNotAdmin || d.Text != "administrator privilege required" {
		t.Errorf("non-admin Administer = %+v", d)
	}
	open := serverFacts{ChannelCreation: "open"}
	admins := serverFacts{ChannelCreation: "admins"}
	if !decide(pat, open, CreateChannel).Allowed {
		t.Error("open creation denied")
	}
	if !decide(coder, open, CreateChannel).Allowed {
		t.Error("scoped creation under open denied (the gate exempts CreateChannel)")
	}
	if decide(pat, admins, CreateChannel).Allowed {
		t.Error("admins creation allowed a non-admin")
	}
	if !decide(root, admins, CreateChannel).Allowed {
		t.Error("admins creation denied an admin")
	}
	if decide(coder, admins, CreateChannel).Allowed {
		t.Error("a scoped non-administrator created a channel under admins")
	}
	if d := decide(pat, serverFacts{ChannelCreation: "bogus"}, CreateChannel); d.Allowed || d.Code != bus.CodeInternal {
		t.Errorf("unknown channel_creation = %+v, want internal", d)
	}
	if d := decide(pat, serverFacts{}, CreateChannel); d.Allowed || d.Code != bus.CodeInternal {
		t.Errorf("empty channel_creation = %+v, want internal", d)
	}
}

func TestDecideSessionAndIdentity(t *testing.T) {
	d := decide(pat, sessionFacts{store.Session{ID: "s1", Identity: "pat"}}, Use)
	if !d.Allowed {
		t.Errorf("own session = %+v", d)
	}
	d = decide(pat, sessionFacts{store.Session{ID: "s1", Identity: "kim"}}, Use)
	if d.Allowed || d.Code != bus.CodeNotFound || d.Reason != ReasonNotOwner || d.Text != `session "s1" does not exist` {
		t.Errorf("other's session = %+v", d)
	}
	if !decide(pat, identityFacts{store.Identity{Name: "kim"}}, See).Allowed {
		t.Error("unscoped principal cannot see an identity")
	}
	if !decide(root, identityFacts{store.Identity{Name: "job1-x", Scope: "job1"}}, See).Allowed {
		t.Error("unscoped admin cannot see a scoped identity")
	}
}

func TestHumansAndAdminsVisibleAndHumansGroup(t *testing.T) {
	ord := store.KindOrdinary
	// Identity/See: an unscoped principal sees everyone; a scoped agent sees its
	// scope, every human and every administrator, and not an out-of-scope agent.
	humanID := store.Identity{Name: "eric@corp.example", Kind: store.IdentityHuman}
	adminID := store.Identity{Name: "root", Kind: store.IdentityAgent, Admin: true}
	agentID := store.Identity{Name: "pat", Kind: store.IdentityAgent}
	for _, id := range []store.Identity{humanID, adminID, agentID} {
		if !decide(pat, identityFacts{id}, See).Allowed {
			t.Errorf("unscoped principal cannot see %s", id.Name)
		}
	}
	for id, want := range map[string]bool{humanID.Name: true, adminID.Name: true, agentID.Name: false} {
		var f store.Identity
		for _, c := range []store.Identity{humanID, adminID, agentID} {
			if c.Name == id {
				f = c
			}
		}
		if got := decide(coder, identityFacts{f}, See).Allowed; got != want {
			t.Errorf("scoped See %s = %v, want %v", id, got, want)
		}
	}
	// a humans row grants Read to a human and not to an agent
	if !decide(hal, chFacts("", ord, entry(store.Humans, r)), Read).Allowed {
		t.Error("humans [read] did not grant Read to a human")
	}
	if decide(pat, chFacts("", ord, entry(store.Humans, r)), Read).Allowed {
		t.Error("humans [read] granted Read to an agent")
	}
	if decide(root, chFacts("", ord, entry(store.Humans, r)), Read).Allowed {
		t.Error("humans [read] granted Read to an administrator agent")
	}
	// humans [write] makes Post allowed for a human with no row of its own
	if !decide(hal, chFacts("", ord, entry(store.Humans, w)), Post).Allowed {
		t.Error("humans [write] did not allow Post for a human")
	}
}

func TestScopeGate(t *testing.T) {
	ord := store.KindOrdinary
	rows := entry(store.Everyone, rw)
	for _, scope := range []string{"", "job2"} {
		f := chFacts(scope, ord, rows)
		for _, a := range []Action{Read, Write, Admin, List, Deliver, Post} {
			d := decide(coder, f, a)
			if d.Allowed || d.Reason != ReasonOutOfScope || d.Code != bus.CodeNotFound || d.Text != `channel "work" does not exist` {
				t.Errorf("scope %q %s: %+v", scope, a, d)
			}
			// the unscoped principal with identical rows is not restricted
			if a == Read || a == Write || a == List {
				if !decide(pat, f, a).Allowed {
					t.Errorf("unscoped principal denied %s on scope %q", a, scope)
				}
			}
		}
	}
	// a scoped identity is never an administrator (an administrator is never
	// scoped, so the flag and a scope do not occur together after Resolve)
	d := decide(coder, serverFacts{}, Administer)
	if d.Allowed || d.Reason != ReasonNotAdmin || d.Code != bus.CodeForbidden || d.Text != "administrator privilege required" {
		t.Errorf("scoped Administer = %+v", d)
	}
	if decide(coder, chFacts("job1", ord), Admin).Allowed {
		t.Error("a scoped identity got the implicit admins entry on a channel")
	}
	// identities
	if !decide(coder, identityFacts{store.Identity{Name: "job1-b", Scope: "job1"}}, See).Allowed {
		t.Error("scoped principal cannot see its own scope's identity")
	}
	for _, id := range []store.Identity{{Name: "x", Scope: "job2"}, {Name: "y"}} {
		d := decide(coder, identityFacts{id}, See)
		if d.Allowed || d.Code != bus.CodeNotFound || d.Reason != ReasonOutOfScope || d.Text != `identity "`+id.Name+`" is not registered` {
			t.Errorf("See %q = %+v", id.Name, d)
		}
	}
	// a session is the caller's own, not scope-gated
	if !decide(coder, sessionFacts{store.Session{ID: "s", Identity: "job1-coder"}}, Use).Allowed {
		t.Error("scoped principal denied its own session")
	}
}

func TestAnonymousDeniedEverything(t *testing.T) {
	all := []Action{Read, Write, Admin, List, Deliver, Post, Administer, CreateChannel, Use, See}
	fs := []facts{
		chFacts("", store.KindOrdinary, entry(store.Everyone, rwa)),
		serverFacts{ChannelCreation: "open"},
		sessionFacts{store.Session{ID: "s", Identity: ""}},
		identityFacts{store.Identity{Name: "pat"}},
	}
	for _, f := range fs {
		for _, a := range all {
			if d := decide(Anonymous, f, a); d.Allowed {
				t.Errorf("Anonymous allowed %s on %T", a, f)
			}
		}
	}
}

func TestUndefinedPairIsInternal(t *testing.T) {
	d := decide(pat, serverFacts{}, Read)
	if d.Allowed || d.Code != bus.CodeInternal || d.Text != `access: action "read" is not defined for server` {
		t.Errorf("decision = %+v", d)
	}
	for _, f := range []facts{chFacts("", store.KindOrdinary), sessionFacts{}, identityFacts{}} {
		for _, a := range []Action{Administer, CreateChannel, "bogus", ""} {
			if d := decide(root, f, a); d.Allowed || d.Code != bus.CodeInternal || !strings.HasPrefix(d.Text, "access: action ") {
				t.Errorf("%T/%q = %+v, want internal", f, a, d)
			}
		}
	}
	if d := decide(root, chFacts("", store.KindOrdinary), Use); d.Allowed || d.Code != bus.CodeInternal {
		t.Errorf("channel/use = %+v", d)
	}
}

func TestReplaceACLProtectedEntries(t *testing.T) {
	a := []store.Permission{store.PermAdmin}
	stored := []store.ACLEntry{
		entry(store.Everyone, rw), entry(store.Admins, a), entry(store.Humans, r),
		entry("hal", rw), entry("root", r), entry("kim", r), entry("pat", rwa),
	}
	named := map[string]store.Identity{
		"hal":  {Name: "hal", Kind: store.IdentityHuman},
		"root": {Name: "root", Kind: store.IdentityAgent, Admin: true},
		"kim":  {Name: "kim", Kind: store.IdentityAgent},
		"pat":  {Name: "pat", Kind: store.IdentityAgent},
	}
	without := func(names ...string) []store.ACLEntry {
		var out []store.ACLEntry
		for _, e := range stored {
			if !slices.Contains(names, e.Identity) {
				out = append(out, e)
			}
		}
		return out
	}
	with := func(name string, p []store.Permission) []store.ACLEntry {
		var out []store.ACLEntry
		for _, e := range stored {
			if e.Identity == name {
				e.Permissions = p
			}
			out = append(out, e)
		}
		return out
	}
	lost := func(perms, name string) string {
		return "an agent cannot remove " + perms + " from ACL entry \"" + name + "\": humans and administrators keep their access"
	}
	type tc struct {
		name     string
		p        Principal
		rows     []store.ACLEntry
		proposed []store.ACLEntry
		scope    string
		want     string // "" allowed, else the denial text
		code     bus.Code
		reason   string
	}
	protected := func(name, text string, proposed []store.ACLEntry) tc {
		return tc{name: name, p: pat, rows: stored, proposed: proposed, want: text, code: bus.CodeForbidden, reason: ReasonProtectedEntry}
	}
	cases := []tc{
		protected("drop everyone", lost("read, write", store.Everyone), without(store.Everyone)),
		protected("everyone to read", lost("write", store.Everyone), with(store.Everyone, r)),
		protected("drop admins", lost("admin", store.Admins), without(store.Admins)),
		protected("drop humans", lost("read", store.Humans), without(store.Humans)),
		protected("drop a human", lost("read, write", "hal"), without("hal")),
		protected("drop an administrator", lost("read", "root"), without("root")),
		protected("human to write only", lost("read", "hal"), with("hal", w)),
		{name: "drop an agent", p: pat, rows: stored, proposed: without("kim")},
		{name: "drop own row", p: pat, rows: stored, proposed: without("pat")},
		{name: "add an entry", p: pat, rows: stored, proposed: append(slices.Clone(stored), entry("kim", w))},
		{name: "only adds", p: pat, rows: stored, proposed: append(slices.Clone(stored), entry("zed", rwa))},
		{
			name: "duplicate rows covered", p: pat,
			rows:     []store.ACLEntry{entry("hal", r), entry("hal", w), entry("pat", rwa)},
			proposed: []store.ACLEntry{entry("hal", rw), entry("pat", rwa)},
		},
		{
			name: "duplicate rows not covered", p: pat,
			rows:     []store.ACLEntry{entry("hal", r), entry("hal", w), entry("pat", rwa)},
			proposed: []store.ACLEntry{entry("hal", r), entry("pat", rwa)},
			want:     lost("write", "hal"), code: bus.CodeForbidden, reason: ReasonProtectedEntry,
		},
		{name: "human with admin clears", p: hal, rows: append(slices.Clone(stored), entry("hal", rwa)), proposed: []store.ACLEntry{}},
		{name: "administrator clears", p: root, rows: stored, proposed: []store.ACLEntry{}},
		{
			name: "no channel admin precedes the protected set", p: Principal{Name: "kim", Kind: store.IdentityAgent}, rows: stored, proposed: without(store.Everyone),
			want: "kim needs admin on channel \"work\"", code: bus.CodeForbidden, reason: ReasonNoPermission,
		},
		{
			name: "out of scope", p: coder, rows: stored, proposed: []store.ACLEntry{}, scope: "job2",
			want: `channel "work" does not exist`, code: bus.CodeNotFound, reason: ReasonOutOfScope,
		},
	}
	for _, c := range cases {
		f := aclChangeFacts{
			Channel: store.Channel{ID: 1, Name: "work", Scope: c.scope},
			Rows:    c.rows, Proposed: c.proposed, Named: named,
		}
		d := decide(c.p, f, ReplaceACL)
		if c.want == "" {
			if !d.Allowed {
				t.Errorf("%s: denied %+v", c.name, d)
			}
			continue
		}
		if d.Allowed || d.Text != c.want || d.Code != c.code || d.Reason != c.reason {
			t.Errorf("%s: got %+v, want %q %s %s", c.name, d, c.want, c.code, c.reason)
		}
	}
	if d := decide(pat, aclChangeFacts{}, Read); d.Allowed || d.Code != bus.CodeInternal {
		t.Errorf("Read on an ACL change = %+v, want internal", d)
	}
}
