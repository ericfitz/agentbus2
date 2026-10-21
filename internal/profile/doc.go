// Package profile holds the client-side configuration of agentbus2: the
// profiles in config.json (Decision 74), the config and data directories
// (Decisions 32, 33), the profile selection order (Decision 21) and the
// identity selection order (Decision 71).
//
// It is a leaf: it never imports the core, the server, the local Bus or a
// store engine. Every function that reads the environment takes an
// environ slice, so tests never touch the developer's own directories.
package profile
