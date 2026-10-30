package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// statusReq sends one request over the in-process transport and returns the
// status code and the raw body.
func statusReq(t *testing.T, e *env, identity, method, path, key, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://inproc"+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp := e.raw(identity, req)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func TestSetAgentStatusWire(t *testing.T) {
	e := newSessionEnv(t, nil)
	path := "/v1/sessions/" + e.session.Id + "/status"

	code, raw := statusReq(t, e.env, "pat", http.MethodPost, path, "", `{"tag":"implementing","text":"Task 3"}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, raw)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["problem"]; has {
		t.Errorf("problem key present for an empty problem: %s", raw)
	}
	if _, has := m["last_tag"]; has {
		t.Errorf("last_tag present on a set: %s", raw)
	}
	if m["identity"] != "pat" || m["tag"] != "implementing" || m["text"] != "Task 3" ||
		m["session"] != e.session.Id || m["display_name"] != e.session.DisplayName || m["revision"] != float64(1) {
		t.Errorf("body fields: %s", raw)
	}

	for name, body := range map[string]string{
		"offline":     `{"tag":"offline","text":"x"}`,
		"empty tag":   `{"tag":"","text":"x"}`,
		"unknown tag": `{"tag":"bogus","text":"x"}`,
	} {
		code, raw := statusReq(t, e.env, "pat", http.MethodPost, path, "", body)
		var p gen.Problem
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatalf("%s: %v: %s", name, err, raw)
		}
		if code != 400 || p.Code != gen.ProblemCode(ab2bus.CodeValidation) {
			t.Errorf("%s: status %d code %q: %s", name, code, p.Code, raw)
		}
	}
	_, raw = statusReq(t, e.env, "pat", http.MethodPost, path, "", `{"tag":"","text":"x"}`)
	if !strings.Contains(string(raw), "tag is required") {
		t.Errorf("empty tag text: %s", raw)
	}

	// The same Idempotency-Key twice returns the same revision.
	_, a := statusReq(t, e.env, "pat", http.MethodPost, path, "k1", `{"tag":"waiting","text":"w"}`)
	_, b := statusReq(t, e.env, "pat", http.MethodPost, path, "k1", `{"tag":"waiting","text":"w"}`)
	var ga, gb gen.AgentStatus
	if err := json.Unmarshal(a, &ga); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &gb); err != nil {
		t.Fatal(err)
	}
	if ga.Revision != gb.Revision || ga.Revision != 2 {
		t.Errorf("replay revisions %d and %d, want 2 and 2", ga.Revision, gb.Revision)
	}
}

func TestListAgentStatusesWire(t *testing.T) {
	e := newSessionEnv(t, nil)
	code, raw := statusReq(t, e.env, "pat", http.MethodGet, "/v1/agent-statuses", "", "")
	if code != 200 || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("empty list: %d %s", code, raw)
	}
	statusReq(t, e.env, "pat", http.MethodPost, "/v1/sessions/"+e.session.Id+"/status", "", `{"tag":"blocked","text":"b","problem":"p"}`)
	code, raw = statusReq(t, e.env, "pat", http.MethodGet, "/v1/agent-statuses", "", "")
	if code != 200 {
		t.Fatalf("status %d: %s", code, raw)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil || len(got) != 1 {
		t.Fatalf("list: %v %s", err, raw)
	}
	st, _ := got[0]["status"].(map[string]any)
	if got[0]["identity"] != "pat" || got[0]["kind"] != "agent" || got[0]["live"] != true ||
		st["tag"] != "blocked" || st["last_tag"] != "blocked" || st["problem"] != "p" {
		t.Errorf("entry: %s", raw)
	}
}

func TestAgentStatusHistoryWire(t *testing.T) { // open note: agent-status 13
	e := newSessionEnv(t, nil)
	path := "/v1/sessions/" + e.session.Id + "/status"
	for _, tag := range []string{"planning", "implementing", "reviewing"} {
		if code, raw := statusReq(t, e.env, "pat", http.MethodPost, path, "", `{"tag":"`+tag+`","text":"t"}`); code != 200 {
			t.Fatalf("set %s: %d %s", tag, code, raw)
		}
	}
	code, raw := statusReq(t, e.env, "sam", http.MethodGet, "/v1/identities/pat/statuses", "", "")
	if code != 200 {
		t.Fatalf("status %d: %s", code, raw)
	}
	var got []gen.AgentStatus
	if err := json.Unmarshal(raw, &got); err != nil || len(got) != 3 {
		t.Fatalf("history: %v %s", err, raw)
	}
	for i, tag := range []string{"planning", "implementing", "reviewing"} {
		if got[i].Tag != tag || got[i].Revision != int64(i+1) {
			t.Errorf("row %d: %+v", i, got[i])
		}
	}
	code, raw = statusReq(t, e.env, "sam", http.MethodGet, "/v1/identities/ghost/statuses", "", "")
	if code != 404 || !strings.Contains(string(raw), `identity \"ghost\" is not registered`) {
		t.Errorf("ghost: %d %s", code, raw)
	}
}

func TestListIdentitiesCarriesStatus(t *testing.T) {
	e := newSessionEnv(t, nil)
	if _, err := e.client("sam").OpenSessionWithResponse(context.Background(), gen.OpenSessionJSONRequestBody{}); err != nil {
		t.Fatal(err)
	}
	statusReq(t, e.env, "pat", http.MethodPost, "/v1/sessions/"+e.session.Id+"/status", "", `{"tag":"idle","text":"i"}`)
	code, raw := statusReq(t, e.env, "sam", http.MethodGet, "/v1/identities", "", "")
	if code != 200 {
		t.Fatalf("status %d: %s", code, raw)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, i := range got {
		_, has := i["status"]
		seen[i["name"].(string)] = has
	}
	if !seen["pat"] || seen["sam"] || len(seen) != 2 {
		t.Errorf("status presence by identity: %v: %s", seen, raw)
	}
}

// The remote Bus over the real handler returns what the core stored.
func TestAgentStatusRemoteRoundTrip(t *testing.T) {
	e := newSessionEnv(t, nil)
	rb := remote.New(e.client("pat"))
	ctx := context.Background()
	set, err := rb.SetAgentStatus(ctx, ab2bus.SetAgentStatusInput{Session: e.session.Id, Tag: ab2bus.StatusReviewing, Text: "r", Problem: "q"})
	if err != nil {
		t.Fatal(err)
	}
	hist, err := rb.AgentStatusHistory(ctx, "pat")
	if err != nil || len(hist) != 1 || !reflect.DeepEqual(hist[0], set) {
		t.Fatalf("history %+v err %v, want %+v", hist, err, set)
	}
	list, err := rb.AgentStatuses(ctx)
	if err != nil || len(list) != 1 || !list[0].Live || list[0].Status.LastTag != ab2bus.StatusReviewing {
		t.Fatalf("list %+v err %v", list, err)
	}
	ids, err := rb.Identities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ids {
		if i.Name == "pat" && (i.Status == nil || i.Status.Tag != ab2bus.StatusReviewing) {
			t.Errorf("identity status: %+v", i.Status)
		}
	}
}
