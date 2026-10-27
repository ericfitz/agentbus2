package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/version"
)

func TestHealthProbeTier(t *testing.T) {
	e := newEnv(t, envOptions{})
	r, err := e.client("").HealthWithResponse(context.Background())
	if err != nil || r.JSON200 == nil {
		t.Fatalf("%v %v", r, err)
	}
	h := r.JSON200
	if h.Tier != "probe" || h.Status != "ok" || h.Version != version.Version || h.Now != fixedNowMs {
		t.Fatalf("%+v", h)
	}
	if h.ProtocolVersion != version.Protocol {
		t.Fatalf("protocol_version %q, want %q", h.ProtocolVersion, version.Protocol)
	}
	if h.StartedAt != nil || h.Store != nil || h.Embeddings != nil || h.Storage != nil || h.Notices != nil || h.Maintenance != nil {
		t.Fatalf("probe tier carries members: %+v", h)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://inproc/v1/health", nil)
	resp := e.raw("", req)
	body, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 5 {
		t.Fatalf("probe body keys = %v, want exactly status, tier, version, protocol_version, now", m)
	}
	for _, k := range []string{"status", "tier", "version", "protocol_version", "now"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("probe body %s lacks %q", body, k)
		}
	}
}

func TestHealthIdentityAndAdminTiers(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("pat", false)
	e.identity("root", true)
	ctx := context.Background()

	r, err := e.client("pat").HealthWithResponse(ctx)
	if err != nil || r.JSON200 == nil {
		t.Fatalf("%v %v", r, err)
	}
	h := r.JSON200
	if h.Tier != "identity" || h.Store == nil || h.Store.Engine != "sqlite" || h.Store.SchemaVersion <= 0 ||
		h.Embeddings == nil || h.Embeddings.State != "off" || h.Embeddings.Model != nil {
		t.Fatalf("identity tier: %+v", h)
	}
	if h.Storage != nil || h.Notices != nil || h.Maintenance != nil {
		t.Fatalf("identity tier carries admin members: %+v", h)
	}

	r, err = e.client("root").HealthWithResponse(ctx)
	if err != nil || r.JSON200 == nil {
		t.Fatalf("%v %v", r, err)
	}
	h = r.JSON200
	if h.Tier != "admin" || h.Storage == nil || h.Storage.Bytes <= 0 || h.Notices == nil || len(*h.Notices) != 0 ||
		h.Maintenance == nil || h.Maintenance.Lock != "unknown" || h.Maintenance.Jobs == nil || len(h.Maintenance.Jobs) != 0 {
		t.Fatalf("admin tier: %+v", h)
	}
}

func TestHealthBadBearerIs401(t *testing.T) {
	e := newEnv(t, envOptions{issuer: true, registration: identities.ModeOpen})
	req, _ := http.NewRequest(http.MethodGet, "http://inproc/v1/health", nil)
	req.Header.Set("Authorization", "Bearer not-a-token")
	resp := e.raw("", req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("status %d type %q body %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.Code != "unauthenticated" {
		t.Fatalf("code = %q (%v) body %s", p.Code, err, body)
	}
	if strings.Contains(string(body), "not-a-token") {
		t.Fatalf("body carries the token: %s", body)
	}
}

func TestHealthUnregisteredIdentity(t *testing.T) {
	e := newEnv(t, envOptions{})
	r, err := e.client("ghost").HealthWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode() != 403 || r.JSON200 != nil || r.ApplicationproblemJSONDefault == nil || r.ApplicationproblemJSONDefault.Code != "not_registered" {
		t.Fatalf("status %d problem %+v", r.StatusCode(), r.ApplicationproblemJSONDefault)
	}
}

func TestHealthStoreDownIs503(t *testing.T) {
	e := newEnv(t, envOptions{})
	ctx := context.Background()
	c := e.client("")
	if r, err := c.HealthWithResponse(ctx); err != nil || r.StatusCode() != 200 {
		t.Fatalf("before close: %v %v", r, err)
	}
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := c.HealthWithResponse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := r.ApplicationproblemJSONDefault
	if r.StatusCode() != 503 || p == nil || p.Code != "unavailable" || !p.Retryable {
		t.Fatalf("status %d problem %+v", r.StatusCode(), p)
	}
	if p.Detail == nil || *p.Detail != "store unreachable" || strings.Contains(*p.Detail, e.dbPath) {
		t.Fatalf("detail = %v", p.Detail)
	}
}

func TestHealthOptionalSecurityIsInTheDocument(t *testing.T) {
	sec := Spec().Paths.Find("/v1/health").Get.Security
	if sec == nil || len(*sec) != 2 {
		t.Fatalf("security = %v, want two requirements", sec)
	}
	if len((*sec)[0]) != 0 {
		t.Fatalf("first requirement = %v, want empty", (*sec)[0])
	}
	if _, ok := (*sec)[1]["bearerAuth"]; !ok || len((*sec)[1]) != 1 {
		t.Fatalf("second requirement = %v, want bearerAuth", (*sec)[1])
	}
}

func TestHealthProbeBodyOmitsNoticeText(t *testing.T) {
	e := newEnv(t, envOptions{})
	err := e.store.Tx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.SetNotice(ctx, store.Notice{Kind: "capacity", Message: "SECRET-NOTICE-TEXT", SetAt: fixedNowMs})
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://inproc/v1/health", nil)
	resp := e.raw("", req)
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "SECRET-NOTICE-TEXT") || !strings.Contains(string(body), `"degraded"`) {
		t.Fatalf("probe body = %s", body)
	}
}
