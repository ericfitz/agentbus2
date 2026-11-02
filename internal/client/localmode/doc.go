// Package localmode is local (SQLite) mode of the connect layer: the
// client.LocalOpener the full agentbus2 binary passes as client.Options.Local.
// It composes the core, the HTTP handler and the in-process transport over the
// profile's SQLite file (Decisions 35, 36, 68; Milestone 5 choice 17), so the
// package internal/client itself links no core, server or Store engine and a
// binary without local mode never links them.
//
// State (Definition of Done D): a second Open over the same file finds the
// migrations at their version, the identity and general present, and writes
// nothing; two Opens at once are serialized by the Store's migration lock; an
// Open after a crash finds the maintenance lease expired after twice the
// interval.
package localmode
