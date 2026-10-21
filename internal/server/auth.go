package server

import (
	"context"
	"net/http"
)

// Identity travels on the request context under two keys (Milestone 4
// choice 5):
//
//   - inprocIdentityKey, set only by InProcessTransport and read only by
//     InProcess.Authenticate. A context value never crosses a socket, so a
//     network request cannot carry it.
//   - identityKey, written by the auth middleware always, with the id
//     possibly "", after calling the Authenticator. IdentityFrom and the
//     validator's AuthenticationFunc read only this key, so they never see a
//     value the middleware did not put there: a RefuseAll server behind the
//     in-process transport still sees "".
type (
	identityKey       struct{}
	inprocIdentityKey struct{}
)

// Authenticator maps a request to the registered identity it acts as. ""
// with a nil error means no identity (protected operations then fail the
// document's security requirement); an error is a 401 with err.Error() as
// the problem detail.
type Authenticator interface {
	Authenticate(r *http.Request) (identity string, err error)
}

// RefuseAll never attaches an identity. It is the default, and what
// agentbus2 server uses until Milestone 5.
type RefuseAll struct{}

// Authenticate returns no identity.
func (RefuseAll) Authenticate(*http.Request) (string, error) { return "", nil }

// InProcess reads the identity InProcessTransport put on the context (local
// mode, Decisions 35 and 36).
type InProcess struct{}

// Authenticate returns the transport's identity, "" when there is none.
func (InProcess) Authenticate(r *http.Request) (string, error) {
	id, _ := r.Context().Value(inprocIdentityKey{}).(string)
	return id, nil
}

func withInprocIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, inprocIdentityKey{}, identity)
}

// WithIdentity attaches identity under the identity key.
func WithIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

// IdentityFrom is the identity key's value, "" when unset.
func IdentityFrom(ctx context.Context) string {
	id, _ := ctx.Value(identityKey{}).(string)
	return id
}
