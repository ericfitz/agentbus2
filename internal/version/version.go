// Package version holds the agentmesh release version.
package version

// Version is the release version. Release builds override it with
// -ldflags "-X github.com/ericfitz/agentmesh/internal/version.Version=<v>".
var Version = "2.0.0-dev"

// Protocol is the wire protocol version, api/openapi.yaml info.version (Eric's
// decision 2026-10-26): the major is the /v1 path, the minor is bumped on
// every addition, the patch on a documentation-only change. It is independent
// of Version.
const Protocol = "1.0.0"
