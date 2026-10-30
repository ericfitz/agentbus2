package remote

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func TestRemoteAgentStatusUnknownTagPassesThrough(t *testing.T) { // open note: agent-status 1
	body := `{"identity":"pat","revision":1,"tag":"future","text":"t","session":"s","display_name":"d","set_at":5}`
	b := newBus(t, &canned{status: 200, ctype: "application/json", body: body})
	got, err := b.SetAgentStatus(context.Background(), ab2bus.SetAgentStatusInput{Session: "s", Tag: ab2bus.StatusIdle, Text: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != ab2bus.StatusTag("future") {
		t.Errorf("tag %q", got.Tag)
	}
	list := `[{"identity":"pat","kind":"agent","live":true,"status":` + body + `}]`
	b = newBus(t, &canned{status: 200, ctype: "application/json", body: list})
	es, err := b.AgentStatuses(context.Background())
	if err != nil || len(es) != 1 || es[0].Status.Tag != ab2bus.StatusTag("future") {
		t.Errorf("list %+v err %v", es, err)
	}
}

func TestRemoteAgentStatusRoundTrip(t *testing.T) {
	want := ab2bus.AgentStatus{Identity: "pat", Revision: 3, Tag: ab2bus.StatusBlocked, Text: "t", Problem: "p",
		Session: "s", DisplayName: "d", SetAt: 9}
	// Problem and LastTag empty: absent on the wire, empty again after decode.
	bare := ab2bus.AgentStatus{Identity: "pat", Revision: 1, Tag: ab2bus.StatusIdle, Text: "t", Session: "s", DisplayName: "d", SetAt: 1}
	raw := string(mustJSON(t, genAgentStatus(bare)))
	for _, k := range []string{`"problem"`, `"last_tag"`} {
		if strings.Contains(raw, k) {
			t.Errorf("%s present on the wire: %s", k, raw)
		}
	}
	for _, in := range []ab2bus.AgentStatus{want, bare} {
		body := "[" + string(mustJSON(t, genAgentStatus(in))) + "]"
		b := newBus(t, &canned{status: 200, ctype: "application/json", body: body})
		got, err := b.AgentStatusHistory(context.Background(), "pat")
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], in) {
			t.Errorf("round trip: got %+v err %v, want %+v", got, err, in)
		}
	}
	// A list entry carries last_tag.
	e := ab2bus.AgentStatusEntry{Identity: "pat", Kind: ab2bus.IdentityAgent, Live: false,
		Status: ab2bus.AgentStatus{Identity: "pat", Revision: 2, Tag: ab2bus.StatusOffline, LastTag: ab2bus.StatusWaiting, Text: "t", Session: "s", DisplayName: "d", SetAt: 2}}
	body := "[" + string(mustJSON(t, genAgentStatusEntry(e))) + "]"
	b := newBus(t, &canned{status: 200, ctype: "application/json", body: body})
	got, err := b.AgentStatuses(context.Background())
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], e) {
		t.Errorf("entry: got %+v err %v, want %+v", got, err, e)
	}
}

func TestRemoteAgentStatusBadKind(t *testing.T) {
	body := `[{"identity":"pat","kind":"robot","live":true,"status":{"identity":"pat","revision":1,"tag":"idle","text":"t","session":"s","display_name":"d","set_at":1}}]`
	b := newBus(t, &canned{status: 200, ctype: "application/json", body: body})
	if _, err := b.AgentStatuses(context.Background()); err == nil {
		t.Error("unknown kind accepted")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// genAgentStatus builds the wire form the way the server does: problem and
// last_tag are absent when empty.
func genAgentStatus(a ab2bus.AgentStatus) gen.AgentStatus {
	g := gen.AgentStatus{Identity: a.Identity, Revision: a.Revision, Tag: string(a.Tag), Text: a.Text,
		Session: a.Session, DisplayName: a.DisplayName, SetAt: a.SetAt}
	if a.Problem != "" {
		g.Problem = &a.Problem
	}
	if a.LastTag != "" {
		lt := string(a.LastTag)
		g.LastTag = &lt
	}
	return g
}

func genAgentStatusEntry(e ab2bus.AgentStatusEntry) gen.AgentStatusEntry {
	return gen.AgentStatusEntry{Identity: e.Identity, Kind: gen.AgentStatusEntryKind(e.Kind), Live: e.Live, Status: genAgentStatus(e.Status)}
}
