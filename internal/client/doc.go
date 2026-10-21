// Package client is the connect layer: it resolves the profile and the
// identity (Decisions 21, 71) and builds one bus.Bus for them. A local
// profile (Decisions 35, 36, 68) runs the core and the HTTP handler in this
// process over the shared SQLite file, behind the in-process transport; a
// server profile (Decisions 19, 70, 84) dials the server with a bearer minted
// from the identity's credential. Either way the caller holds a bus.Bus whose
// remote implementation is the one the wire tests exercise.
//
// Every function that reads the environment takes it from Options.Environ.
package client
