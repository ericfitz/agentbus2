// Package server is the HTTP face of the core: an http.Handler built from the
// generated strict server interface, with every error written as
// application/problem+json (spec 3.3, Decision 46), the document's security
// requirements enforced by the request validator, the Authenticator seam
// (Decisions 8, 36, 64), discovery computed from the embedded document
// (Decisions 43, 65, 91) and the in-process transport (Decision 35).
//
// It imports internal/gen, internal/gen/spec, internal/bus and
// internal/core/...; it never imports internal/bus/local or
// internal/bus/remote (Decision 47).
package server
