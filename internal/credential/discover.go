package credential

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// discoveryTimeout bounds one discovery request.
const discoveryTimeout = 10 * time.Second

// maxDiscoveryBody bounds the document read.
const maxDiscoveryBody = 1 << 20

// Discovery is the part of the RFC 8414 authorization server metadata a client uses.
type Discovery struct {
	Issuer        string `json:"issuer"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURI       string `json:"jwks_uri"`
}

// Discover reads server + "/.well-known/oauth-authorization-server". hc nil: a
// client with a 10 s timeout. The assertion audience is the document's issuer
// and the exchange goes to its token_endpoint; both may differ from the
// address the client dials (Milestone 5 choice 7).
func Discover(ctx context.Context, hc *http.Client, server string) (Discovery, error) {
	if hc == nil {
		hc = &http.Client{Timeout: discoveryTimeout}
	}
	url := strings.TrimRight(server, "/") + "/.well-known/oauth-authorization-server"
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Discovery{}, fmt.Errorf("credential: discovery %s: %w", url, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return Discovery{}, fmt.Errorf("credential: discovery %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf("credential: discovery %s: HTTP %d", url, resp.StatusCode)
	}
	var d Discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDiscoveryBody)).Decode(&d); err != nil {
		return Discovery{}, fmt.Errorf("credential: discovery %s: %w", url, err)
	}
	if d.Issuer == "" {
		return Discovery{}, fmt.Errorf("credential: discovery %s: no issuer", url)
	}
	if d.TokenEndpoint == "" {
		return Discovery{}, fmt.Errorf("credential: discovery %s: no token_endpoint", url)
	}
	return d, nil
}
