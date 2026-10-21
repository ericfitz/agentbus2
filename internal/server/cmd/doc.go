// Package cmd is the agentbus2 server command: it opens a Store, starts the
// core, the general channel and the maintenance loop, and serves the API
// handler over TCP (Milestone 4 choices 14 and 15). Until Milestone 5 the
// only authenticator it constructs is server.RefuseAll, so every
// authenticated operation answers 401.
package cmd
