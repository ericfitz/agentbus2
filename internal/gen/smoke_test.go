package gen_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ericfitz/agentbus2/internal/gen"
)

// smokeStub implements WellKnown only; the nil embedded interface panics on any other call.
type smokeStub struct{ gen.StrictServerInterface }

func (smokeStub) WellKnown(_ context.Context, _ gen.WellKnownRequestObject) (gen.WellKnownResponseObject, error) {
	return gen.WellKnown200JSONResponse{Discover: "/v1/discover"}, nil
}

func TestGeneratedServerAndClientRoundTrip(t *testing.T) {
	mux := http.NewServeMux()
	gen.HandlerFromMux(gen.NewStrictHandler(smokeStub{}, nil), mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := gen.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := c.WellKnownWithResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rsp.StatusCode() != 200 || rsp.JSON200 == nil || rsp.JSON200.Discover != "/v1/discover" {
		t.Fatalf("status %d body %+v", rsp.StatusCode(), rsp.JSON200)
	}
}
