// Package config is the server configuration of Decision 85: a strict JSON
// file, AGENTBUS2_SERVER_* environment variables and flag overrides, applied
// in that order over defaults built from core.Defaults, and mapped onto
// core.Config. It imports internal/core, internal/store and the
// standard-library-only internal/strictjson.
package config
