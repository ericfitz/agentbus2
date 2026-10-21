package bus

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
)

// TestServerServesBothEngines starts the agentbus2 server command's Serve over
// each engine. It is transport-independent: it runs the same under both
// AGENTBUS2_BUS_TRANSPORT values.
func TestServerServesBothEngines(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _ := openStore(t, e)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ready := make(chan net.Addr, 1)
		errc := make(chan error, 1)
		go func() {
			errc <- cmd.Serve(ctx, cmd.Config{Listen: "127.0.0.1:0", Registration: identities.ModeOpen, Store: s, Ready: func(a net.Addr) { ready <- a }})
		}()
		var addr string
		select {
		case a := <-ready:
			addr = a.String()
		case err := <-errc:
			t.Fatalf("Serve: %v", err)
		case <-time.After(15 * time.Second):
			t.Fatal("Serve did not become ready")
		}

		get := func(path string) (int, string) {
			resp, err := http.Get("http://" + addr + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		if code, body := get("/.well-known/agentbus2"); code != 200 {
			t.Fatalf("well-known: %d %s", code, body)
		}
		if code, body := get("/v1/discover"); code != 200 || !strings.Contains(body, "long_poll") {
			t.Fatalf("discover: %d %s", code, body)
		}
		if code, body := get("/v1/channels"); code != 401 {
			t.Fatalf("channels: %d %s", code, body)
		}

		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Fatalf("Serve after cancel: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return within 5 s")
		}
	})
}
