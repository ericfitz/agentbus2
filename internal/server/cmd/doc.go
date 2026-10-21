// Package cmd is the agentbus2 server command and its host commands. Run
// loads the configuration (Decision 85: a file, AGENTBUS2_SERVER_* variables
// and flags), opens a Store, binds the listener, loads or creates the token
// signing key (a Store row), builds the issuer and the bearer authenticator,
// and calls Serve, which starts the core, the general channel and the
// maintenance loop and serves the API handler (Milestone 4 choices 14 and 15,
// Milestone 5 choices 6, 7, 10 and 14). RunAdmin is `agentbus2 server admin`:
// host commands against the Store with no listener.
package cmd
