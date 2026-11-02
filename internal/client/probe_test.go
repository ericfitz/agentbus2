package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/version"
)

func TestDiscoverServer(t *testing.T) {
	base := startServer(t)
	info, err := DiscoverServer(context.Background(), nil, base)
	if err != nil {
		t.Fatal(err)
	}
	if info.ProtocolVersion != version.Protocol {
		t.Errorf("ProtocolVersion = %q, want %q", info.ProtocolVersion, version.Protocol)
	}
	if info.ServerVersion == "" {
		t.Error("ServerVersion is empty")
	}
}

func TestProbeHealth(t *testing.T) {
	base := startServer(t)
	h, err := ProbeHealth(context.Background(), &http.Client{Timeout: 10 * time.Second}, base)
	if err != nil {
		t.Fatal(err)
	}
	if h.Tier != bus.TierProbe {
		t.Errorf("Tier = %q, want probe", h.Tier)
	}
}

func TestProbeClosedPort(t *testing.T) {
	hc := &http.Client{Timeout: 5 * time.Second}
	if _, err := DiscoverServer(context.Background(), hc, "http://127.0.0.1:1"); err == nil {
		t.Error("DiscoverServer on a closed port: no error")
	}
	if _, err := ProbeHealth(context.Background(), hc, "http://127.0.0.1:1"); err == nil {
		t.Error("ProbeHealth on a closed port: no error")
	}
}

// A server that answers 200 with something that is not a discovery document,
// and one that answers a non-200, are errors naming what they sent.
func TestDiscoverServerRefusesNonServers(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"not json", "<html>", 200}, {"status", "{}", 404}, {"no protocol", `{"server_version":"x"}`, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newStubServer(t, tc.status, tc.body)
			if _, err := DiscoverServer(context.Background(), nil, srv); err == nil {
				t.Error("no error")
			}
		})
	}
}

func newStubServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
