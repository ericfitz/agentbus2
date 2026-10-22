# Post-build Decisions 4-11 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Eric's post-build decisions 4 and 6-11 (decision 5 keeps existing code and needs only the spec note already written).

**Architecture:** Seven independent changes, one task each, each with its own tests. Task 7 changes the wire protocol (`api/openapi.yaml`, then `make generate`) and the Bus contract; the others are local to one package or two.

**Tech Stack:** Go, SQLite (`modernc.org/sqlite`), Postgres (`pgx`), oapi-codegen (`make generate`), Bubble Tea v2, `golang.org/x/sys/windows`.

**Spec:** `docs/superpowers/specs/2026-10-21-gate-0-decisions.md`, section "Post-build decisions (Eric, 2026-10-21)", items 4-11. Background: `docs/superpowers/specs/2026-10-19-agentbus2-design.md` (Decision 71 identity selection, Decision 18 resume, Decision 58 socket), ADR 0002, `docs/tui.md`.

## Global Constraints

- Done gate: `make verify` (needs Docker); a run that skips the Postgres suites is not a pass. Per-task checks: `make test`, `make lint`, plus `make test-conformance` for tasks touching `internal/store` or `internal/core`.
- Never edit `internal/gen` or `internal/gen/spec` by hand; change `api/openapi.yaml` and run `make generate`.
- Import rules in `CLAUDE.md` (Decision 47, Milestones 5-8) hold; `internal/tui` imports only `internal/bus` and `internal/version` of this module.
- Every task updates the docs it makes wrong (`CLAUDE.md`, `docs/tui.md`, `docs/ops/`, ADR 0002, `docs/cli*`), checked against the code.
- Reject unknown inputs; no placeholder branch accepts input.
- American English; commit messages `type(scope): ...` ending with the Co-Authored-By and Claude-Session lines; work lands on `main`.

## Review Focus

1. Task 7: an omitted field and an explicitly empty one differ on every path (wire JSON, the generated client, MCP, CLI): omitted keeps, empty clears. The generated client must not drop an empty map or empty string through `omitempty`.
2. Task 6: a retry after a crash reuses the pending key, and a pending key left from a different `-algorithm` is not silently used.
3. Task 1: a Windows `DOMAIN\name` login name and a name failing `store.FileNameRule` (for example one containing `:`) must not crash or produce an invalid identity.
4. Task 5: a database already at schema version 3 with rows in `assertion_jti` migrates to 4 on both engines.
5. Task 2: `not_found` from a call that is not the session loop's Watch or Receive (for example a channel lookup) must not trigger a session resume.

---

### Task 1: The TUI falls back to the OS login name (decision 11)

**Files:**
- Modify: `internal/profile/select.go` (`IdentitySelection`, `ResolveIdentity`), `internal/client/options.go` (`Options`), the `Resolve` call site in `internal/client`, `internal/cli/tui.go`
- Create: `internal/cli/osuser.go`
- Test: `internal/profile/select_test.go`, `internal/client/resolve_test.go`, `internal/cli/tui_test.go` (or the existing TUI CLI test file)
- Docs: `docs/tui.md` line 11, `CLAUDE.md` `tui` bullet

**Interfaces:**
- Produces: `profile.IdentitySelection.Fallback string`; source string `"os-user"`; `client.Options.FallbackIdentity string`; `func osUserName() string` in `internal/cli/osuser.go`.

Order: flag, env, profile `identity`, (server mode) the only credential, then `Fallback` when non-empty. `Fallback` is set only by `RunTUI`.

- [ ] **Step 1: Failing tests.** In `select_test.go` add rows to the `ResolveIdentity` table: local profile, no flag/env/identity, `Fallback: "efitz"` → `("efitz", "os-user")`; flag `"pat"` with `Fallback` → flag wins; profile identity wins over `Fallback`; server profile with one credential `"c"` and `Fallback` → `"c"`, source `"credential"`; `Fallback: "a:b"` → the FileNameRule error naming source `os-user`; `Fallback: ""` → the existing "identity is required" error (other commands unchanged). Add `TestOSUserName` in `internal/cli` over an injectable `func() (*user.User, error)`: `CORP\eric` → `eric`; `a/b` → `a-b`; error or empty username → `""`.
- [ ] **Step 2: Run** `go test ./internal/profile/ ./internal/cli/ -run 'ResolveIdentity|OSUserName'` → FAIL (unknown field / undefined).
- [ ] **Step 3: Implement.** Port v1 `defaultTUIName` (`/Users/efitz/Projects/agentbus/internal/config/config.go:186-200`) as `osUserName`, returning `""` instead of `"human"` on failure. `RunTUI` sets `o.FallbackIdentity = osUserName()` before `open`. Thread `FallbackIdentity` into `IdentitySelection.Fallback` in `client.Resolve`. Update the error text only if the fallback was tried and invalid (the FileNameRule error covers that). Name the v1 source file in the commit message.
- [ ] **Step 4: Run** `make test` → PASS. A CLI test covers `tui` resolving the fallback; the interactive check in Terminal.app (`./agentbus2 tui` with no identity set) is Eric's, alongside the gear-icon check.
- [ ] **Step 5: Commit** `feat(tui): fall back to the OS login name when no identity is set (post-build decision 11; ported from v1 internal/config/config.go)`.

### Task 2: A swept TUI session resumes instead of retrying forever (decision 7)

**Files:**
- Modify: `internal/tui/client.go` (`fail`, around line 213)
- Test: `internal/tui/client_test.go`

**Interfaces:** none new.

- [ ] **Step 1: Failing tests.** `TestSweptSessionResumes`: stub Bus whose `Watch` returns `bus.Errorf(bus.CodeNotFound, false, "session not found")` once; `Open` with the resume token returns a fresh session `s2` (hint `tui`); assert the loop continues on `s2`, sends no `receiveErrMsg`, and `Open` was called once with `ResumeToken: "t1"`. `TestSweptSessionNotFoundTwiceIsReported`: `Watch` keeps answering `not_found` after the resume; assert a `receiveErrMsg` (the existing `lapsedOnce` guard), not an endless resume loop. Same pair for `Receive` returning `not_found`.
- [ ] **Step 2: Run** `go test ./internal/tui/ -run Swept` → FAIL.
- [ ] **Step 3: Implement.** In `fail`, treat `bus.CodeNotFound` exactly like `bus.CodeSessionLapsed`. `fail` has exactly two callers, the loop's Watch (`client.go:251`) and Receive (`client.go:268`), both session-scoped, so no other `not_found` reaches it (Review Focus 5); `ok()` resets `lapsedOnce` after a successful call, so the guard allows one resume per lapse. Update the comment on `resumeLapsed`.
- [ ] **Step 4: Run** `make test` and `scripts/m8-exit-checks.sh --deps-only` → PASS.
- [ ] **Step 5: Commit** `fix(tui): a swept session (not_found) resumes like a lapsed one (post-build decision 7)`.

### Task 3: Refuse a group- or world-readable SQLite database (decision 8)

**Files:**
- Modify: `internal/store/sqlite/sqlite.go` (`ensurePrivateFile`, `Options`, the `Logger` comment at line 38), `internal/server/cmd` (where it opens SQLite for `server` and `server admin`)
- Test: the existing test of `ensurePrivateFile` in `internal/store/sqlite` (find with `rg -n ensurePrivateFile internal/store/sqlite`)
- Docs: `docs/ops/` (configuration or deploy page that mentions the warning; `rg -n -i 'readable|chmod 600' docs/ops`)

**Interfaces:** `sqlite.Options.RequirePrivate bool`. Decided here (the recorded decision says "the server refuses"): `agentbus2 server` and `agentbus2 server admin` set it and refuse; local mode (`internal/client`) leaves it false and keeps the warning, whose text drops "holds the server signing key" (a local database has no signing key; it says the file holds the bus's messages). The hooks open local mode, so they never hit the refusal and keep falling open.

- [ ] **Step 1: Failing test.** Unix only (`runtime.GOOS == "windows"` → skip), `RequirePrivate: true`: existing file at 0644 → error containing the path, `0644` and `chmod 600`; 0640 → error; 0600 → nil; missing file → created 0600. `RequirePrivate: false` at 0644 → opens, one warning without the words "signing key". A `server/cmd` test: `server -sqlite <0644 file>` exits non-zero naming the file.
- [ ] **Step 2: Run** `go test ./internal/store/sqlite/ -run Private` → FAIL.
- [ ] **Step 3: Implement.** With `RequirePrivate`, return `fmt.Errorf("sqlite: %s has mode %#o and holds the server signing key; chmod 600 it", path, perm)`. Remove the "design call left open" sentence.
- [ ] **Step 4: Run** `make test` → PASS.
- [ ] **Step 5: Commit** `feat(sqlite): refuse a database file readable by other accounts (post-build decision 8)`.

### Task 4: Owner-only DACL on the Windows unix socket (decision 9)

**Files:**
- Create: `internal/fileacl/acl_windows.go`, `internal/fileacl/acl_other.go`, `internal/fileacl/acl_windows_test.go`, `internal/fileacl/doc.go`
- Modify: `internal/credential/acl_windows.go`, `acl_other.go` (delete; call `fileacl.OwnerOnly`), `internal/credential/source.go`, `store.go`; `internal/server/cmd/listen.go` (`Listen`)
- Test: `internal/server/cmd/listen_windows_test.go`

**Interfaces:**
- Produces: `func fileacl.OwnerOnly(path string) error` (no-op off Windows). `internal/fileacl` is a leaf: it imports nothing from this module.

- [ ] **Step 1: Move** `ownerOnly` from `internal/credential` to `fileacl.OwnerOnly` unchanged, move its Windows test with it, update the callers. `GOOS=windows go vet ./internal/...` and `make test` → PASS.
- [ ] **Step 2: Failing test** (Windows only, `//go:build windows`): `TestListenUnixSetsOwnerOnlyACL`: `Listen("unix:" + filepath.Join(t.TempDir(), "s"))`, then read the DACL of the socket path with `windows.GetNamedSecurityInfo` and assert one ACE granting the current user's SID, protected from inheritance (mirror the credential test's assertions).
- [ ] **Step 3: Implement.** In `Listen`, after the `os.Chmod`, call `fileacl.OwnerOnly(path)`; on error close the listener and return `setting the ACL of socket %s: %w`. Update the doc comment (Decision 58 now holds on Windows).
- [ ] **Step 4: Run** `make test`, `GOOS=windows go vet ./...`, `make vet-cross` → PASS. `make verify` on macOS does not run the Windows test: the task is done only when `windows.yml` passes on the pushed commit, and the report says so.
- [ ] **Step 5: Commit** `feat(server): owner-only DACL on the unix socket on Windows (post-build decision 9)`.

### Task 5: `assertion_jti` keyed on (identity, jti) (decision 4)

**Files:**
- Create: `internal/store/sqlite/schema/0004_assertion_jti_identity.sql`, `internal/store/postgres/schema/0004_assertion_jti_identity.sql`
- Modify: both `migrations.go` (add Version 4), `internal/store/replay.go`, `internal/store/sqlstore/replay.go`, `internal/server/auth_handlers.go:70`, `internal/auth/token/issuer.go` comment if it names the key
- Test: `internal/conformance/store` (the replay tests; `rg -n PutAssertionJTI internal/conformance`), `internal/conformance/bus/auth_test.go` if it counts replay cases
- Docs: `CLAUDE.md` ("Schema version 3 adds ..." line: add version 4), ADR 0002 where it describes the jti record

**Interfaces:**
- Produces: `PutAssertionJTI(ctx context.Context, identity, jti string, expiresAt int64) error`. Empty identity → `ErrInvalid`; identity validated with the same text check as jti.

Migration 4 is deliberately a new version rather than an edit of version 3, because development databases are already at version 3. It drops and recreates the table (`identity TEXT NOT NULL, jti TEXT NOT NULL, expires_at, PRIMARY KEY (identity, jti)`, plus the `expires_at` index). Dropping the rows is safe: each row lives about two minutes and only blocks a replay of an assertion that has already expired or will within that window; the migration comment says so.

- [ ] **Step 1: Failing conformance tests** (both engines, in the Store suite `internal/conformance/store`, never in `auth_test.go`, whose 11 leaves per engine `scripts/m5-exit-checks.sh` pins): same jti under identities `a` and `b` → both succeed; same (identity, jti) twice → `ErrConflict`; empty identity → `ErrInvalid`; a store migrated from version 3 with a row present opens at version 4 (follow `open_test.go`'s `migrationsFor` pattern) and `DeleteAssertionJTIBefore` still works.
- [ ] **Step 2: Run** `make test-conformance` → FAIL (compile).
- [ ] **Step 3: Implement** the migrations, the new signature and the call `tx.PutAssertionJTI(ctx, p.Identity, p.JTI, keepUntil)`.
- [ ] **Step 4: Run** `make test` and `make test-conformance` (Postgres included) → PASS; `scripts/m5-exit-checks.sh --deps-only` and `scripts/m7-exit-checks.sh --deps-only` → PASS.
- [ ] **Step 5: Commit** `feat(store): key assertion_jti on (identity, jti), schema version 4 (post-build decision 4)`.

### Task 6: `register` persists a pending key before the request (decision 6)

**Files:**
- Modify: `internal/credential/store.go`, `internal/cli/register.go` (lines 132-160 and the `RunRegister` doc comment)
- Test: `internal/credential/store_test.go`, `internal/cli/register_test.go`

**Interfaces:**
- Produces on `credential.Store`: `PendingPath(identity string) string` = `filepath.Join(s.Dir, ".pending", identity)` (a directory, so `List` already skips it); `SavePending(identity string, k crypto.Signer) error` (same 0600 / owner-only / 0700-dir rules as `Save`); `LoadPending(identity string) (crypto.Signer, keys.Algorithm, bool, error)` (false when absent; same mode check as `Load`); `CommitPending(identity string) error` (atomic rename onto `Path(identity)`, then remove the empty `.pending` dir if possible, ignoring "not empty").

The server's idempotent short-circuit (`internal/core/identities/register.go:97`, an already-registered matching key) runs before any enrollment token check and consumes nothing, so a retry with the pending key and the already-used token succeeds; no server change is needed. Key choice in `RunRegister`: saved credential, else pending key, else generate and `SavePending` before `client.Register`. After 201: `CommitPending` when the key came from pending (else nothing new to write). An explicit `-algorithm` that disagrees with a pending key's algorithm is exit 2 naming `PendingPath` (Review Focus 2). A failed request leaves the pending file for the retry.

- [ ] **Step 1: Failing tests.** Store: `SavePending` then `LoadPending` round-trips at 0600 (Unix) and `List` does not include it; `CommitPending` moves it and `Exists` becomes true; `LoadPending` of an absent identity → `(nil, "", false, nil)`. CLI (with the existing httptest server pattern in `register_test.go`): first attempt's server records the public key, commits, and answers by closing the connection (simulated lost response) → exit 1, pending file exists, no credential; second run against the same server with the same (now consumed) enrollment token → exit 0, the same public key is sent, the credential file exists and the pending one is gone; `-algorithm ES256` with an EdDSA pending key → exit 2 naming the pending path.
- [ ] **Step 2: Run** `go test ./internal/credential/ ./internal/cli/ -run 'Pending|Register'` → FAIL.
- [ ] **Step 3: Implement** as above; rewrite the "Nothing is written until the server answers 201" comment.
- [ ] **Step 4: Run** `make test` and `scripts/m6-exit-checks.sh --deps-only` → PASS.
- [ ] **Step 5: Commit** `feat(register): write the key to a pending file before registering and reuse it on retry (post-build decision 6)`.

State (Definition of Done D): second run after success reuses the saved credential (unchanged); overlapping runs for one identity race on the pending file (last `SavePending` wins; the loser's 201 for a key that is no longer pending is then a 409 or "existing key"); state this in the commit body; a failed run leaves exactly one pending file that the next run reuses.

### Task 7: Memory edit keeps omitted fields, clears empty ones, with a revision check (decision 10)

**Files:**
- Modify: `api/openapi.yaml` (`EditMemoryRequest`), then `make generate`; `internal/bus/memories.go` (`EditMemoryInput`); `internal/core/memories/memories.go` (`Edit`); `internal/server/memories.go`; `internal/bus/local/memories.go`; `internal/bus/remote` memories; `internal/mcpserver/inputs.go` (`editIn`) and the `edit_memory` tool description in `server.go:536`; `internal/cli/memories.go` (edit, lines 128-140)
- Test: `internal/conformance/bus` (memory edit tests, both engines and both transports), `internal/mcpserver` tests, `internal/cli` memories tests
- Docs: spec 3 text on memory edit if it states replace semantics (`rg -n -i 'edit' docs/superpowers/specs/2026-10-19-agentbus2-design.md | rg -i memor`), the CLI docs, the `using-agentbus2` skill text if it describes edit

**Interfaces:**
- Produces:
  ```go
  type EditMemoryInput struct {
      ID             int64
      Subject        *string            // nil keep, "" clear
      Content        *string            // nil keep; "" is CodeValidation "content must not be empty; delete the memory instead"
      Type           *string            // nil keep, "" clear
      Metadata       *map[string]string // nil keep, empty map clear
      Tags           []string           // nil keep, empty clear (unchanged)
      IfRevision     int64              // 0: no check; else CodeConflict when the live revision differs
      IdempotencyKey string
  }
  ```
  Wire: `EditMemoryRequest` drops `required: [content]`, adds `if_revision` (int64, minimum 1). An empty request body with no fields is `validation` ("nothing to change"). The conflict message names both revisions: `memory %d is at revision %d, not %d`.

- [ ] **Step 1: Failing Bus conformance tests** (`TestMemoryEditMerge`, both engines, `local` and `inproc` transports): create a memory with type `note`, metadata `{k:v}`, subject `s`; edit with only content → type, metadata, subject, tags kept; edit with `Type: ptr("")` → type cleared, rest kept; `Metadata: &map{}` → metadata cleared; edit with only `Metadata` → content kept; `Content: ptr("")` → validation; nothing set → validation; `IfRevision` equal to the live revision → success; stale `IfRevision` → `conflict` naming both revisions and no new revision written; idempotent replay with the same key still returns the first result.
- [ ] **Step 2: Run** `make test-conformance` → FAIL (compile).
- [ ] **Step 3: Implement** the wire change and `make generate`; check in the generated types that `type` and `metadata` are pointers so an explicit `""` and `{}` reach the server (Review Focus 1). Mirror how `subject` (pointer, `""` clears) and `tags` (`[]` clears) already survive the wire today; `type` and `metadata` must take the same mechanism. Core: read the live revision, apply the merge, check `IfRevision` inside the same transaction before tombstoning. Server handler and remote Bus map pointers both ways.
- [ ] **Step 4: MCP and CLI.** `editIn`: `Content *string`, `Type *string`, `Metadata *map[string]string`, `IfRevision int64` (`if_revision`, "the revision you read; the edit fails with conflict if the memory changed since"); the description says omit keeps, empty clears. MCP test: a JSON call with `"type": ""` clears, an omitted `type` keeps. CLI `memories edit`: `-type` and `-metadata` given → set (an explicit `-type ''` clears; a new `-clear-metadata` flag clears metadata, since `-metadata K=V` cannot express empty); content omitted → no `GetMemory` re-read, just omit it; add `-if-revision N`. CLI test for each.
- [ ] **Step 5: Run** `make test`, `make test-conformance`, `make generate-check`, `scripts/m4-exit-checks.sh --deps-only`, `scripts/m6-exit-checks.sh --deps-only` → PASS.
- [ ] **Step 6: Commit** `feat(memories): edit keeps omitted fields, clears empty ones, with an optional revision check (post-build decision 10)`.

### Final: done gate and records

- [ ] `make verify` → PASS, output shown in the report.
- [ ] Update `.local/reviews/2026-10-21/followups.md`: mark the seven ERIC DECISION lines decided with the commit ids; remove the "memories edit ... clears type/metadata" open note.
- [ ] `graphify update .` if `graphify-out/` exists.
