package client

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/ericfitz/agentbus2/internal/bus/remote"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/oidcclient"
)

// openServer wires the token source and the refreshing transport (Decisions
// 19, 70, 84). The client the Source exchanges tokens through carries neither
// the refreshing transport nor the bearer editor: a 401 from the token
// endpoint must not trigger a refresh of itself. Only the second client,
// whose every request carries the bearer, is wrapped.
func openServer(ctx context.Context, o Options, r Resolved) (*Conn, error) {
	if o.HumanSignIn {
		return openHuman(ctx, o, r)
	}
	p := r.Profile
	var signer crypto.Signer
	if len(p.CredentialProcess) == 0 {
		if !r.Credentials.Exists(r.Identity) {
			return nil, resolveErr(fmt.Errorf("no credential for %q in profile %q: run agentbus2 register", r.Identity, p.Name))
		}
		k, _, err := r.Credentials.Load(r.Identity)
		if err != nil {
			return nil, resolveErr(err)
		}
		signer = k
	}
	base := o.HTTP
	if base == nil {
		base = &http.Client{}
	}
	plain, err := gen.NewClientWithResponses(p.Server, gen.WithHTTPClient(base))
	if err != nil {
		return nil, resolveErr(fmt.Errorf("profile %q: server %q: %w", p.Name, p.Server, err))
	}
	src := &credential.Source{
		Server:   p.Server,
		Identity: r.Identity,
		Signer:   signer,
		Process:  p.CredentialProcess,
		Client:   plain,
		HTTP:     base,
		Cache:    credential.Cache{Dir: filepath.Join(r.DataDir, "tokens", p.Name)},
		Now:      o.Now,
	}
	authedHTTP := &http.Client{
		Transport:     credential.Transport(base.Transport, src),
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
	}
	authed, err := gen.NewClientWithResponses(p.Server, gen.WithHTTPClient(authedHTTP), gen.WithRequestEditorFn(src.Editor()))
	if err != nil {
		return nil, resolveErr(fmt.Errorf("profile %q: server %q: %w", p.Name, p.Server, err))
	}
	return &Conn{
		Bus:      remote.New(authed),
		Resolved: r,
		Client:   authed,
		Source:   src,
		close:    func() error { base.CloseIdleConnections(); return nil },
	}, nil
}

// openHuman wires a HumanSource (spec 5.3, Decision 11): a human signs in
// through the profile's corporate provider, so unlike the agent path Open
// obtains the bearer now, which may open the browser. The identity is the
// bearer's subject. The exchange client carries no editor and no refreshing
// transport, for the reason openServer gives.
func openHuman(ctx context.Context, o Options, r Resolved) (*Conn, error) {
	p := r.Profile
	base := o.HTTP
	if base == nil {
		base = &http.Client{}
	}
	plain, err := gen.NewClientWithResponses(p.Server, gen.WithHTTPClient(base))
	if err != nil {
		return nil, resolveErr(fmt.Errorf("profile %q: server %q: %w", p.Name, p.Server, err))
	}
	src := &credential.HumanSource{
		Server: p.Server,
		OIDC: oidcclient.Config{
			Issuer:       p.OIDC.Issuer,
			ClientID:     p.OIDC.ClientID,
			RedirectPort: p.OIDC.RedirectPort,
			Scopes:       p.OIDC.Scopes,
			OpenBrowser:  o.HumanOpenBrowser,
			Announce:     o.HumanAnnounce,
		},
		Client: plain,
		HTTP:   base,
		Cache:  credential.HumanCache{Dir: filepath.Join(r.DataDir, "tokens", p.Name)},
		Now:    o.Now,
	}
	authedHTTP := &http.Client{
		Transport:     credential.Transport(base.Transport, src),
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
	}
	authed, err := gen.NewClientWithResponses(p.Server, gen.WithHTTPClient(authedHTTP), gen.WithRequestEditorFn(src.Editor()))
	if err != nil {
		return nil, resolveErr(fmt.Errorf("profile %q: server %q: %w", p.Name, p.Server, err))
	}
	if !o.HumanDeferSignIn {
		if _, err := src.Token(ctx); err != nil {
			base.CloseIdleConnections()
			return nil, err
		}
		r.Identity = src.Identity()
		if r.Identity == "" {
			base.CloseIdleConnections()
			return nil, errors.New("client: the sign-in produced no identity")
		}
	}
	return &Conn{
		Bus:      remote.New(authed),
		Resolved: r,
		Client:   authed,
		Human:    src,
		close:    func() error { base.CloseIdleConnections(); return nil },
	}, nil
}
