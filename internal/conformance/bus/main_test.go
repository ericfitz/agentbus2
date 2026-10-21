package bus

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/server"
)

func TestMain(m *testing.M) {
	transport = os.Getenv(transportEnv)
	if transport == "" {
		transport = "local"
	}
	switch transport {
	case "local":
	case "inproc":
		newBus = func(h *harness, identity string) ab2bus.Bus {
			c, err := gen.NewClientWithResponses("http://inproc", gen.WithHTTPClient(&http.Client{Transport: server.InProcessTransport(handlerFor(h), identity)}))
			if err != nil {
				h.t.Fatalf("gen.NewClientWithResponses: %v", err)
			}
			return remote.New(c)
		}
	default:
		fmt.Fprintf(os.Stderr, "AGENTBUS2_BUS_TRANSPORT must be local or inproc, got %q\n", transport)
		os.Exit(2)
	}
	os.Exit(dbtest.Run(m))
}
