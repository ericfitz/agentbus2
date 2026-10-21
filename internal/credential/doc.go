// Package credential holds the host-side credential state of Decisions 22,
// 23, 55, 56, 70 and 84: private key files (PKCS#8 PEM, mode 0600, replaced
// atomically), the credential_process protocol, the bearer token source with
// its disk cache and the transport that refreshes a bearer once on a 401.
//
// It never logs or quotes a secret: errors name files and programs, not
// contents. It imports internal/auth/keys, internal/auth/assertion,
// internal/bus/remote, internal/gen and internal/store (for the name rule)
// and nothing of the core or the server.
package credential
