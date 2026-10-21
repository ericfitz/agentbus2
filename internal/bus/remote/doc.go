// Package remote is the Bus over the generated HTTP client (Decision 47):
// generated types stay inside this package and every error leaves it as a
// *bus.Error. It imports internal/gen and internal/bus only, never
// internal/gen/spec, the server or the core.
package remote
