// Package auth is the parent of the authentication leaf packages: keys
// (Ed25519 and ES256 key handling), assertion (RFC 7523 client assertions)
// and token (the server's issuer, bearer verification and external JWKS).
//
// Import rule (Decision 47 extended, Milestone 5 Global Constraints): every
// package under internal/auth imports only the standard library and
// github.com/go-jose/go-jose/v4, and never internal/store, internal/core,
// internal/server, internal/gen or internal/bus. keys and assertion ship in
// the client binaries; internal/bus/remote may import them and never token.
package auth
