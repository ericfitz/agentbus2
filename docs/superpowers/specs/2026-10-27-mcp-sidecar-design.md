# MCP sidecar mode: draft design brief

## 1. Status

- Draft of 2026-10-27, written by an agent.
- Nothing in it is a decision until Eric marks it so.
- **Decisions: 0 decided, 7 open.** Eric's 2026-10-27 review recorded no answer on this brief's decisions (it is postponed). His one change that reaches it is the binary name: the MCP-only binary is `agentbus2-mcponly` (decision 4 of the #12 brief), and this brief names it that way throughout, including the container image.
- Depends on `2026-10-27-mcp-only-client-design.md` (the #12 brief): the MCP-only binary, `internal/client` without local mode, the simplified flags and `-credential`, and guided help. The sidecar commands live in that binary.
- **Eric's decision 2026-10-27:** the sidecar implementation is a separate issue with a separate spec (this one) that depends on the #12 spec, and it is postponed.
- Issue: #21 (postponed). It was split out of #12 ("feat: mcp-only client build with simplified CLI, guided help and sidecar mode"). Related: #13 (MCP events), which a network MCP transport makes easier.
- Baseline: `main` at 9fc48e7. Every section below was moved from the #12 brief with its content unchanged; only headings, section numbers, decision numbers and one protocol-version sentence (Decision 2 B, section 4.1, per the ruling that the protocol stays 1.0.0 before the first release) changed (the old number is in parentheses where it differs).

## 2. Problem and scope

Issue #12 also asks that the MCP-only binary be able to run as a sidecar: one command generates the mutual TLS keys, the certificates and a config file for running the MCP server as a sidecar, and the agent and the sidecar authenticate each other with mutual TLS.

Acceptance criteria, restated from the issue (numbers 4 and 5 as in the #12 brief):

4. Sidecar mode: one command generates the mutual TLS keys, the certificates and a config file for running the MCP server as a sidecar, and the agent and the sidecar authenticate each other with mutual TLS.
5. Tests cover the sidecar key and config generation, including a second run over existing files. They also cover an agent-to-sidecar connection that refuses a client without the right certificate.

The issue's notes require two things before implementation:

- The non-stdio MCP transport and the mutual TLS scheme are architectural decisions, to be recorded in the spec or an ADR.
- The sidecar authenticates upstream to the server with its own registered identity and key, as today.

Out of scope (proposed):

- Server-side changes, unless Eric picks option 4.1.B.

## 3. What exists today

### 3.1 Decisions a sidecar runs into

- ADR 0001 decision 3 (spec Decision 3, Eric) chose "a local stdio MCP server with an embedded API client, not a network MCP server" (`docs/adr/0001-transport-and-api.md:35-39`, alternatives at `:125-128`; spec `2026-10-19-agentbus2-design.md:117`).
- Sidecar mode serves MCP over a network socket, so **it changes ADR 0001 decision 3**. This brief treats that as an architectural change for Eric (Decision 1 in section 5).

### 3.2 The server terminates no TLS

- The server terminates no TLS. `internal/server` contains no `crypto/tls`, `ServeTLS` or `ListenAndServeTLS` outside tests. TLS comes from a reverse proxy (`docs/ops/deploy.md:17,89-91`).

### 3.3 The SDK has the transport

- The SDK in use, `github.com/modelcontextprotocol/go-sdk` v1.7.0, ships `NewStreamableHTTPHandler` and `StreamableHTTPOptions` (`mcp/streamable.go:128,232` in the module cache). No new dependency is needed for an HTTP transport.

### 3.4 Measured cost of the sidecar transport (go1.27.3)

The method, the other builds and the other table rows are in the #12 brief, section 3.6. The sidecar prototype:

  - **Sidecar**: the split main plus `NewStreamableHTTPHandler`, a `tls.Config` with `RequireAndVerifyClientCert`, and x509 certificate generation.

| Build | darwin/arm64 stripped | linux/amd64 stripped | windows/amd64 stripped | linux/amd64 gzip -9 | Modules |
|---|---|---|---|---|---|
| Same plus streamable HTTP and mutual TLS | 10,805,810 | not built | 11,738,624 | not measured | 13 |

- Streamable HTTP plus mutual TLS adds about 0.3 MB (darwin) to 0.4 MB (windows) and no modules.

## 4. Options

### 4.1 Sidecar mode: transport, mutual TLS and what the server accepts

**Topology.**

- The sidecar is `agentbus2-mcponly sidecar serve`. It is the agent's MCP server over streamable HTTP with TLS 1.3 and `tls.RequireAndVerifyClientCert`.
- Upstream it is an ordinary agent client: its own registered key, `private_key_jwt` and a bearer. The private key lives only in the sidecar's volume, never in the agent's container.
- Any client that presents a valid certificate acts as the sidecar's identity. So one sidecar serves one identity (Decision 5).

**Server side.**

- **A. The server accepts nothing new (Recommended; matches the issue's note).**
  - Mutual TLS covers only the agent-to-sidecar hop. The sidecar-to-server hop is today's HTTPS or proxy plus bearer.
  - No change to ADR 0002, `api/openapi.yaml`, the protocol version, the Store or the engines.
  - Both engines and both bus transports are unaffected, because the sidecar is a client of the `remote` Bus.
- **B. The server also accepts client certificates as client authentication** (RFC 8705 `tls_client_auth` or `self_signed_tls_client_auth`, optionally with certificate-bound tokens).
  - The server would have to terminate TLS itself (it does none today, section 3.4), or trust a client-certificate header from the proxy.
  - It also needs a way to bind a certificate to an identity, and new `token_endpoint_auth_methods_supported` metadata.
  - **This changes ADR 0002 decision 1 and Decision 70 and is an architectural change for Eric.** It means a wire change that ships in protocol 1.0.0 (`info.version` and `version.Protocol` stay `1.0.0` before the first release: Eric's decision 2026-10-27) and new conformance tests on both engines.
  - It buys nothing the sidecar's key does not already provide.

**Certificate scheme (under A).**

- `sidecar init -dir D -host H... [-server URL -identity I -enrollment-token-file F]` writes:
  - a private CA (`ca.pem` and `ca.key`);
  - the sidecar's server certificate (`server.pem` and `server.key`, with the given SANs plus `localhost`, `127.0.0.1` and `::1`);
  - one agent client certificate (`client.pem` and `client.key`);
  - `sidecar.json`, which names the files, the listen address and the upstream server and identity, and holds the allowed client-certificate SHA-256 fingerprints.
- Keys are ECDSA P-256 for the widest TLS client support, at mode 0600 with `fileacl.OwnerOnly` on Windows. Every file is written to a temporary file and renamed, the same discipline as `credential.Store`.
- With `-server` and `-identity`, init also runs `register` for the sidecar's upstream key and stores it under `D`. The enrollment token is never written to `sidecar.json`.
- **Issuance.** Only `sidecar init` and `sidecar add-client NAME` issue certificates, and only on the host that holds `ca.key`. There is no network enrollment for agent certificates.
  - Option: after init, delete `ca.key` ("ephemeral CA"). Adding a client then means a new CA.
  - Recommended: keep `ca.key`, and document mounting only `server.*`, `ca.pem` and `sidecar.json` into the running sidecar.
- **Rotation.** Rerunning `sidecar init` renews any leaf within 30 days of expiry (proposed lifetimes: leaves 1 year, CA 5 years) and leaves everything else byte-identical.
  - `sidecar serve` re-reads its certificate and pins through `tls.Config.GetConfigForClient` when the files' modification times change. Polling is used, not SIGHUP, so Windows behaves the same.
  - A renewed client certificate keeps its pin until the agent switches over, when `-retire-old` removes the old pin.
- **Revocation.**
  - `sidecar revoke-client NAME` removes the pin, and the sidecar refuses that certificate on the next handshake. There is no CRL and no OCSP.
  - Rotating the CA revokes every client at once.
  - Chain verification and the pin check both apply. A certificate from the right CA without a pin is refused.
- **Interaction with enrollment tokens.** None on the agent hop.
  - The enrollment token authorizes the sidecar's upstream key, once, as today. A second `sidecar init` finds the saved credential and re-registers it idempotently without the token (`internal/cli/register.go:136-190`; ADR 0002 lines 433-446).
  - Mutual TLS certificates never reach the server, and `reset-credential` on the server leaves them valid. The sidecar then fails upstream with `unauthenticated`, which `check` reports.

**Alternatives for the agent-to-sidecar hop** (Decision 3):

- **A. Mutual TLS on TCP (Recommended; the issue asks for it).** It works across containers, pods and hosts. Its weak point is that the agent's MCP client must present a client certificate. The Go SDK can (it takes an `http.Client`), and the Python SDK can through httpx. Whether Claude Code, the TypeScript SDK and generic clients support client certificates for HTTP MCP servers is **not verified**. `sidecar connect` (Decision 4) covers clients that cannot.
- **B. A Unix socket with file permissions.** Simplest, with no certificates, but only works where the two share a filesystem. AF_UNIX exists on Windows 10 1803 and later. It does not meet the issue's mutual TLS requirement on its own.
- **C. A static bearer in the agent's MCP config** (an `Authorization` header). Supported by most MCP clients, but the secret sits in the agent's config file, and it does not meet the issue's requirement.

**Sessions without a harness.**

- In sidecar mode no harness process exists, so `FindHarness` fails and a restarted sidecar never resumes a main session (#12 brief, section 3.3).
- **A. Key the main session on the client certificate's fingerprint instead of the harness reference (Recommended).** A reconnecting agent resumes its session. The session state file gains a `client_fingerprint` field, changed in place under the pre-release rule.
- **B. No resumption.** The agent passes `resume_token`.

### 4.2 The sidecar commands

Proposed additions to the `agentbus2-mcponly` command table of the #12 brief, section 4.2:

| Command | Purpose |
|---|---|
| `sidecar init` | generate the sidecar's keys, certificates and config |
| `sidecar serve` | run the sidecar |
| `sidecar connect` | stdio-to-sidecar bridge (Decision 4) |

### 4.3 A container image for the sidecar

Option C of the #12 brief, section 4.5 (packaging):

- **C. B plus a container image `agentbus2-mcponly`** (Chainguard static, `ENTRYPOINT ["/agentbus2-mcponly"]`), published by `publish-image.yml` next to the server image.
  - Sidecars are mostly containers. Recommended as a later task of the same issue.

### 4.4 Windows support for the sidecar

- **Sidecar on Windows.**
  - TLS and streamable HTTP are platform-neutral, and all certificate and key files get `fileacl.OwnerOnly`. The modification-time poll replaces SIGHUP for reloading.
  - Windows containers are rare. The supported case is a sidecar run as a process or a service on the same host.
  - The 0600 mode check is Unix-only, as it is for credentials today.
- **CI.** The mutual TLS tests must use loopback TCP with ephemeral ports and no fixed paths.

### 4.5 Waking an agent over the sidecar transport

The #12 brief, section 4.7, chooses `receive` with `wait_seconds` for this binary. Server-to-client notifications over the streamable HTTP SSE stream are issue #13. It is left there, but the sidecar transport should not rule it out (stateful sessions, not `Stateless: true`).

## 5. Decisions for Eric

1. **May the MCP server be a network endpoint in sidecar mode?** This amends ADR 0001 decision 3 / spec Decision 3, an architectural change.
   - A. Yes, for `agentbus2-mcponly sidecar serve` only: streamable HTTP with mutual TLS. stdio stays the default.
   - B. No; sidecar mode is dropped from #12.
   - C. Yes, and the full `agentbus2 mcp` gains it too.
   - Recommendation: A.
2. **What does the server accept from a sidecar?**
   - A. Nothing new: the sidecar is a normal agent with `private_key_jwt`, and ADR 0002 is unchanged.
   - B. The server also accepts client certificates (RFC 8705), which changes ADR 0002 decision 1 and Decision 70, the wire and TLS termination.
   - Recommendation: A.
3. **What authenticates the agent-to-sidecar hop?**
   - A. Mutual TLS on TCP, pinned client fingerprints plus a private CA, ECDSA P-256, TLS 1.3.
   - B. A Unix socket.
   - C. A static bearer header.
   - Recommendation: A, as the issue asks.
4. **Should there be a `sidecar connect` stdio bridge** (runs beside the agent, presents the client certificate, so any stdio-only MCP client can use a sidecar)?
   - A. Yes, in this issue.
   - B. Later.
   - C. Never.
   - Recommendation: A, because client-certificate support in common MCP clients is unverified.
5. **How many agents may one sidecar serve?**
   - A. One identity per sidecar. Several MCP connections are allowed, each with its own bus session, and the main session is keyed on the client fingerprint.
   - B. One connection at a time.
   - C. Several identities per sidecar.
   - Recommendation: A. Option C needs per-certificate identity mapping and would put several bus keys in one process.
6. **What happens to the CA key?**
   - A. Kept at 0600 on the init host and not mounted into the running sidecar.
   - B. Deleted after init (ephemeral CA).
   - Recommendation: A.
7. **Is a container image published for the sidecar?** (Split from decision 9 C of the #12 brief.)
   - A. Yes: image `agentbus2-mcponly` (Chainguard static, `ENTRYPOINT ["/agentbus2-mcponly"]`), published by `publish-image.yml` next to the server image, as the last task. Section 4.3.
   - B. No.
   - Recommendation: A, as the #12 brief recommended with its option C.

## 6. Dependencies

- **Libraries.** None new.
  - `github.com/modelcontextprotocol/go-sdk` v1.7.0 already has the streamable HTTP server and client.
  - `crypto/tls` and `crypto/x509` are standard library.
- **Issues.**
  - #13 (MCP events) is optional follow-up work over the sidecar's SSE stream.
  - #6 (server signing-key rotation) and #8 (key provider) do not block: the sidecar is a client.
- **External.**
  - Client-certificate support in third-party MCP clients is unverified (section 4.1).
- **Spec.** The #12 brief and its implementation (the MCP-only binary and the `internal/client` split) come first.

## 7. Implementation outline (provisional)

Tasks, in order. Each starts with failing tests. Tasks 1 to 5 of the #12 brief come first.

1. **ADR and spec.**
   - New ADR "MCP sidecar" (the #12 brief proposes ADR 0012 for the MCP-only client; this one takes the next free number).
   - Addenda to ADR 0001 (decision 3 amended, if Decision 1 is A) and ADR 0002 ("server unchanged" if Decision 2 is A).
   - Gate: Eric's approval.
2. **`sidecar init`, `add-client`, `revoke-client`.** Tests (Definition of Done D):
   - first run, with file modes;
   - a second run is byte-identical;
   - a missing leaf is regenerated;
   - a key that does not match its certificate, or a certificate from another CA, is exit 2 naming the file;
   - near-expiry renewal with an injected clock;
   - two overlapping runs serialize on a lock in `D` (`internal/filelock`);
   - a killed run's temporary files are removed or ignored on the next run;
   - with `-server`, a second run re-registers idempotently without the token.
3. **`sidecar serve`.**
   - Streamable HTTP, TLS 1.3, `RequireAndVerifyClientCert` plus pin check, reload by modification time, main session keyed on the fingerprint.
   - Tests:
     - an agent with the right certificate lists the 25 tools and calls `register`;
     - refused: no certificate, a certificate from another CA, a revoked pin, an expired certificate, plain HTTP;
     - a reload picks up a revocation without a restart;
     - the upstream path runs against the e2e server (the SQLite server; the server code is unchanged).
   - Gates: `make verify`, `windows.yml`.
4. **`sidecar connect`** (if Decision 4 is A). Tests: an end-to-end stdio client through the bridge to the sidecar, and a bridge refused by the sidecar.
5. **Container image** (Decision 7 A). A second Dockerfile stage or file, `publish-image.yml`, and a `docker-smoke` variant.
6. **Docs.** A new `docs/ops/sidecar.md`, README, and the CLAUDE.md command list.

Import edges:

- `internal/mcpserver` gains `net/http` and `crypto/tls` only. It must still not import `internal/gen` or `internal/store` (M6 consumer group).

Wire changes: none under Decision 2 A. No `api/openapi.yaml` change, no `version.Protocol` bump and no `make generate`. Under Decision 2 B, the server metadata and the token endpoint change and `make generate` runs; the addition ships in protocol 1.0.0, and `info.version` and `version.Protocol` stay `1.0.0` before the first release (Eric's decision 2026-10-27).

Docs and ADRs: the new ADR; addenda to ADRs 0001 and 0002; `docs/ops/sidecar.md` (new), README and CLAUDE.md.

## 8. Risks and test strategy

- **Client-certificate support in agents (highest risk).** If the target agents cannot present certificates, the native mutual TLS endpoint helps nobody but Go and Python clients. `sidecar connect` mitigates it. Checking two or three real clients before task 8 is a cheap way to retire the risk.
- **Concurrency.** Several MCP connections to one sidecar share one `client.Conn` and one token source. `credential.Source` guards its discovery and key state with a mutex (`internal/credential/source.go:115`). A race test with parallel tool calls runs under `make test-race`.
- **Replicas.** Not affected. The sidecar is a client, and server replicas are unchanged.
- **Both engines and both transports.** Under Decision 2 A the Store and Bus conformance suites are unchanged and still run on SQLite and Postgres, `local` and `inproc`. The sidecar's upstream tests use the e2e server (SQLite). Postgres adds nothing on the client side, and the brief states that rather than claiming coverage.
- **Second run, overlapping runs, leftovers (Definition of Done D).**
  - `sidecar init` is idempotent, serialized by a directory lock and writes atomically (section 7, task 2).
  - `register` already handles a second run and overlap through its pending file.
  - A restarted `sidecar serve` resumes by fingerprint (Decision 5 A).
  - A killed `sidecar serve` leaves only its session state file. The existing sweep removes it once the MCP server and the harness are both dead (`internal/sessionstate/state.go:303`). A zero harness PID already counts as dead (`internal/procs/procs.go:41-42`). Under Decision 5 A, the fingerprint-keyed file must survive a sidecar restart long enough to be resumed, so the sweep needs a sidecar rule and a test for it.
- **Security.**
  - A stolen client certificate equals the identity until its pin is revoked.
  - `ca.key` theft lets an attacker mint certificates, but those still need a pin, so pinning plus chain verification limits the damage to denial of service by revoking.
  - The streamable handler's `CrossOriginProtection` stays on.
  - The listener defaults to loopback, and a non-loopback `-listen` needs an explicit flag.
- **Windows CI.** Loopback TLS tests with ephemeral ports. No SIGHUP. Owner-only ACL assertions reuse `acl_windows_test.go`'s helpers. `windows.yml` must be green on amd64 and arm64.

## 9. Size estimate

**L**: a TLS sidecar with certificate lifecycle and its state tests, a stdio bridge, and a container image, on top of the #12 binary. It becomes **XL** if Decision 2 B (server-side mutual TLS) is chosen.
