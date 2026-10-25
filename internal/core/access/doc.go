// Package access is the one access check (spec 2026-10-25-accesscheck-design,
// Eric's decisions of 2026-10-25): every server-side decision about who may do
// what to which object is made by decide in decide.go, over facts the Store
// serves (internal/store carries no rule). Check, Require and the set forms
// Channels, Identities and Holders load the facts and call decide; nothing
// else in the server-side packages decides (Task 10's gate proves it).
//
// The package imports only internal/store and internal/bus of this module.
package access
