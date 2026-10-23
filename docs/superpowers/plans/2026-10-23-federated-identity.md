# Federated Human Identity (Part 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Humans sign in through a corporate OIDC provider (browser, authorization code with PKCE), exchange the provider's access token for agentbus2's own bearer (RFC 8693), and every message says whether its sender is a human or an agent; `users` gates the exchange, `admins` sets the admin flag, and the token endpoint and discovery align with the OAuth RFCs.

**Architecture:** Store schema version 5 adds the identity kind, one binding row per human, the `users`/`admins` reference lists and `messages.sender_kind`. `internal/core/identities` owns every rule (email normalization, `CreateIdentityByEmail`, reference evaluation, `SignInHuman`). The token endpoint dispatches on `grant_type` and speaks only RFC 6749 error bodies; the auth leaf (`internal/auth/token`, `assertion`) gains the RFC 9068 and draft-ietf-oauth-rfc7523bis shapes. A new stdlib-only client package (`internal/oidcclient`) runs the browser flow; `internal/credential.HumanSource` caches the result; the TUI on a server profile signs in that way and nothing else.

**Tech Stack:** Go 1.27, go-jose v4, kin-openapi + oapi-codegen (`make generate`), modernc SQLite and pgx Postgres, testcontainers for the Postgres suites, Bubble Tea (TUI).

**Spec:** `docs/superpowers/specs/2026-10-23-federated-identity-design.md` (frozen; every Decision and Claude's call ratified by Eric on 2026-10-23). External facts: the fact check of 2026-10-23 (items 1 to 6 and 15 to 19 bear on this part). Part 2 (`2026-10-23-slack-bridge-design.md`) consumes the interfaces listed under "Interfaces for part 2" below.

## Global Constraints

- Done gate: `make verify` (needs Docker; a run that skips the Postgres suites is not a pass). Work is done only when `ci.yml` and `windows.yml` are green on the pushed commit, `release-check` and `check-gates-selftest` jobs included.
- Per task: `make lint` and `make test`; a task that changes `internal/store`, `internal/core` or a Bus behavior also runs `make test-conformance` (both engines, both transports). A task that edits `api/openapi.yaml` runs `make generate` and commits `internal/gen` (never edited by hand).
- Import rules (CLAUDE.md): `internal/auth/...` imports only the standard library, go-jose and `internal/auth/keys`; `internal/bus/remote` never imports `internal/auth/token`; `internal/tui` imports only `internal/bus` and `internal/version`; `internal/mcpserver` and `internal/cli` never import `internal/gen` or `internal/store`; `internal/core/...` and `internal/bus/local` never import `internal/gen` or `internal/server`; `internal/store/...` never imports `internal/core/...`. New: `internal/oidcclient` imports only the standard library; `internal/oidctest` imports only the standard library and go-jose. Run `bash scripts/m4-exit-checks.sh --deps-only`, `m5`, `m6`, `m7`, `m8` `--deps-only` in every task that adds an import.
- Gate-script anchors stay verbatim and exactly once, or the task that moves one updates `scripts/check-gates-selftest.sh` in the same commit and runs `make check-gates-selftest`: `if err := tok.Claims(i.key.Public(), &c); err != nil {` (`internal/auth/token/issuer.go`, block 14), `if !id.Admin {` (`internal/core/authz.go`, block 15), `return tx.PutAssertionJTI(ctx, p.Identity, p.JTI, keepUntil)` (`internal/server/auth_handlers.go`, block 21). A change that refuses the mutated input before the anchor runs (a new header or claim check the test's input fails) masks the block exactly as moving the line would: fix the test input in the same task.
- Counted tests keep their names and counts: `TestAuthEndToEnd` 11 leaves per engine (`scripts/m5-exit-checks.sh` `WANT=11`), `TestRunTUIServerProfile` and `TestRunTUILocalProfile` (`m8`), the `internal/e2e` counts (`m6`). New end-to-end coverage goes in a new test, not in these.
- Identity kinds are exactly `agent` and `human`; binding claim types exactly `email`; reference lists exactly `users` and `admins`; reference kinds exactly `email`, `group`, `subject`, `identity`. Every other value is refused (ErrInvalid in the Store, `validation` in the core, exit 2 in host commands), with a test.
- Email normalization (spec 4.2): trim, lowercase the whole address; the result passes `store.NameRule`, is not `everyone` or `admins`, and (plan choice, Definition of Done B) contains one `@` with a non-empty part on each side.
- Defaults (spec 5.1): `claims` = `{email: "email", groups: "groups", subject: "sub"}`; `human_token_lifetime_seconds` 900 (bounds 1 to 86400); agents keep `token_lifetime_seconds` 3600.
- Token endpoint (spec 8.1): only `/v1/auth/token` answers the OAuth body `{"error", "error_description"}` as `application/json`; status 400, or 401 for `invalid_client`; plan choice: 503 `temporarily_unavailable` and 500 `server_error` for an unconfigured issuer or an internal failure. Every other endpoint keeps problem+json.
- URNs: grant `urn:ietf:params:oauth:grant-type:token-exchange`; token types `urn:ietf:params:oauth:token-type:access_token` (the only accepted `subject_token_type` and `requested_token_type`) and `urn:ietf:params:oauth:token-type:id_token` (refused, `invalid_request`).
- Own bearer (spec 7, 8.4): header `typ: at+jwt`; claims `iss`, `exp`, `aud` (= issuer URL), `sub`, `client_id` (= `sub`, plan choice recorded in ADR 0002), `iat`, `jti`, `agentbus2_kind` (`agent` or `human`). Client assertion (8.3): `aud` = issuer URL, header `typ: client-authentication+jwt`; the server accepts `aud` = issuer URL or token endpoint URL.
- The corporate token is never stored and never logged; no log line, error text or response body carries any token.
- Docs move with the code in the task that changes the behavior: `docs/ops/configuration.md`, `docs/ops/admin-bootstrap.md`, ADR 0002 (one addendum, "Federated identity addendum (2026-10-23)", started in Task 7 and extended by later tasks), CLAUDE.md lines that become stale, `internal/cli/skills/using-agentbus2/SKILL.md`, `README.md`, `docs/install.md`, `docs/tui.md`. American English.
- Commits land on `main`, one per task, ending with the session's `Co-Authored-By` and `Claude-Session` lines. No push without Eric.

## Review Focus

1. A pre-created human (`create-human`) whose first sign-in races a second tab: two concurrent exchanges for the same email and subject must yield one identity, one binding and two bearers, never a conflict error to the user (Task 10, `TestSignInHumanConcurrent`).
2. An email with surrounding spaces or mixed case in the token, in `create-human`, and in a `users`/`admins` email reference all reach the same identity (`Eric@Corp.Example ` = `eric@corp.example`) (Task 4 `TestNormalizeEmail`, Task 10 `TestSignInHumanEmailNormalized`).
3. A corporate access token presented directly as a bearer on any API (the path Decision 10 removed) is refused 401 with "unknown issuer", and creates no identity in any registration mode (Task 9 `TestBearerRefusesCorporateToken`, Task 11 subtest `direct-corporate-bearer-refused`).
4. A browser callback that never comes (user closes the tab), a busy redirect port, or a provider `error=access_denied` redirect ends `agentbus2 tui`/`login -human` with exit 1 and a message naming the cause, never a hang (Task 12 `TestSignInTimeout`, `TestSignInPortBusy`, `TestSignInProviderError`).
5. An unknown `sender_kind`, whether a core caller hands one to the Store or a client receives one from a newer server, is refused rather than shown as an agent's message (Task 1 `TestAppendSenderKind`, Task 2 `TestFromGenMessageRejectsUnknownSenderKind`).

## Interfaces for part 2

Part 2 cites these names; each is produced by the task shown.

- Identity kind: `store.IdentityKind`, `store.IdentityAgent`, `store.IdentityHuman`, `store.Identity.Kind` (Task 1); `bus.IdentityKind`, `bus.IdentityAgent`, `bus.IdentityHuman`, `bus.IdentityInfo.Kind`, wire `Identity.kind` (Task 2).
- Bindings: `store.HumanBinding{Identity, Issuer, ClaimType, Value, ProviderSubject, CreatedAt}`, `store.ClaimEmail`, `Tx.HumanBinding(ctx, identity)`, `Tx.HumanBindingByValue(ctx, claimType, value)`, `Tx.HumanBindingBySubject(ctx, issuer, subject)` (Task 1); lookup by email: `identities.Service.LookupHumanByEmail(ctx, email) (string, error)` (Task 5).
- Creating a human: `identities.Service.CreateIdentityByEmail(ctx, email) (identity string, created bool, err error)` and `CreateIdentityByEmailAs(ctx, admin, email, key)` (Task 5); `bus.Admin.CreateIdentityByEmail(ctx, bus.CreateIdentityByEmailInput) (bus.CreateIdentityByEmailResult, error)`, wire `POST /v1/admin/identities/by-email` (Task 6).
- Sender marker: `store.Message.SenderKind`, `store.NewMessage.SenderKind`, `core.AppendInput.SenderKind` (Tasks 1 and 2); `bus.Message.SenderKind` (JSON `sender_kind`), wire `Message.sender_kind` (Task 2).
- Admins evaluation: `identities.NormalizeEmail`, `identities.HumanClaims`, `identities.MatchReferences(refs []store.Reference, identity string, c HumanClaims) bool`, `identities.Service.AddReference/RemoveReference/ListReferences` (Task 4); `identities.Service.SignInHuman(ctx, HumanClaims) (HumanSignIn, error)` (Task 10). The admin flag on `store.Identity.Admin` stays the evaluated result, so `core.RequireAdmin` and the `admins` ACL principal are unchanged.

---

### Task 1: Store schema version 5 (kind, bindings, references, sender_kind)

**Files:**
- Create: `internal/store/sqlite/schema/0005_human_identity.sql`, `internal/store/postgres/schema/0005_human_identity.sql`, `internal/store/references.go`, `internal/store/sqlstore/humans.go`, `internal/store/sqlstore/references.go`
- Modify: `internal/store/sqlite/migrations.go`, `internal/store/postgres/migrations.go` (add `{Version: 5, ...}`), `internal/store/identities.go`, `internal/store/messages.go`, `internal/store/meta.go` (ResetData/ResetRegistry docs), `internal/store/store.go` (`Tx` embeds `References`), `internal/store/sqlstore/identities.go` (`identityCols`, `identityRow.Kind`), `internal/store/sqlstore/messages.go` (insert and select `sender_kind`), `internal/store/sqlstore/meta.go` (`ResetRegistry`)
- Test: `internal/conformance/store/humans_test.go`, `internal/conformance/store/references_test.go`, `internal/conformance/store/reset_test.go`, `internal/conformance/store/messages_test.go`, `internal/conformance/store/identities_test.go`

**Interfaces:**
- Consumes: `sqlstore.Migration`, `store.NameRule`, `store.TextRule`, the conformance helpers `openStore`, `rawDB`, `schemaVersion`, `migrationsFor`, `scalar`.
- Produces (in `internal/store`):
  - `type IdentityKind string`; `const IdentityAgent IdentityKind = "agent"`, `IdentityHuman IdentityKind = "human"`; `func ValidIdentityKind(k IdentityKind) bool`; `Identity.Kind IdentityKind` (between `Name` and `Admin`).
  - `const ClaimEmail = "email"`; `type HumanBinding struct { Identity, Issuer, ClaimType, Value, ProviderSubject string; CreatedAt int64 }` (`Issuer` is the provider's `iss` URL; `Issuer` and `ProviderSubject` are `""` until the first sign-in and after a reset).
  - `Identities` gains: `CreateHuman(ctx, b HumanBinding, now int64) error` (identity row of kind human plus its binding; ErrInvalid for a name failing NameRule, a reserved name, a ClaimType other than ClaimEmail, an empty Value, a subject without issuer or text failing TextRule; ErrConflict when the name exists, the `(claim_type, value)` is bound, or a non-empty `(issuer, provider_subject)` is bound); `MarkHuman(ctx, name string) error` (agent to human, idempotent for a human; ErrNotFound; ErrConflict when the identity has keys); `HumanBinding(ctx, identity string) (HumanBinding, error)`, `HumanBindingByValue(ctx, claimType, value string) (HumanBinding, error)`, `HumanBindingBySubject(ctx, issuer, subject string) (HumanBinding, error)` (all ErrNotFound; a name failing TextRule is ErrNotFound without a query); `SetHumanSubject(ctx, identity, issuer, subject string) error` (both `""` clears; ErrNotFound with no binding; ErrConflict when bound to another identity; ErrInvalid when exactly one is empty).
  - `Message.SenderKind IdentityKind`, `NewMessage.SenderKind IdentityKind`: AppendMessage stores `""` as `agent` (documented default) and refuses any other value but the two kinds with ErrInvalid.
  - `references.go`: `type RefList string` (`ListUsers = "users"`, `ListAdmins = "admins"`); `type RefKind string` (`RefEmail`, `RefGroup`, `RefSubject`, `RefIdentity`); `type Reference struct { List RefList; Kind RefKind; Issuer, Value string; CreatedAt int64 }`; `type References interface { AddReference(ctx, r Reference) (bool, error); RemoveReference(ctx, r Reference) (bool, error); ListReferences(ctx, list RefList) ([]Reference, error) }`. Add answers false for an existing row; Remove false for none; List orders by kind, issuer, value in byte order. ErrInvalid: unknown list or kind, `RefIdentity` with an issuer, a claim kind without one, an empty value, text failing TextRule.
  - `ResetRegistry(ctx, keep)` also deletes every `users` row and every `admins` row except `{admins, identity, "", keep}`; bindings cascade with their identities. `ResetData` keeps `human_bindings` and `access_refs`.

- [ ] **Step 1: Write the failing tests** (each under `dbtest.ForEachEngine`)
  - `TestHumanCreateAndLookups`: `CreateHuman({Identity: "eric@corp.example", ClaimType: ClaimEmail, Value: "eric@corp.example"}, 1000)`; `Identity` has `Kind == IdentityHuman`, `Admin == false`; `HumanBinding`, `HumanBindingByValue(ClaimEmail, "eric@corp.example")` return it with empty Issuer and ProviderSubject; `CreateIdentity("pat")` gives `Kind == IdentityAgent`; `ListIdentities` carries both kinds.
  - `TestHumanRejects`: ClaimType `"user_id"` -> ErrInvalid; Value `""` -> ErrInvalid; Identity `"everyone"` and `"a/b"` -> ErrInvalid; same name twice -> ErrConflict; a second identity with the same Value -> ErrConflict; `CreateHuman` over an existing agent name -> ErrConflict.
  - `TestHumanSubjectBinding`: `SetHumanSubject("eric@corp.example", "https://idp", "guid-1")` then `HumanBindingBySubject("https://idp", "guid-1")` returns eric; the same subject on a second human -> ErrConflict; `("", "")` clears and the subject lookup is ErrNotFound; `("https://idp", "")` -> ErrInvalid; unknown identity -> ErrNotFound.
  - `TestMarkHuman`: an agent with no keys becomes human; again is nil; an agent with a key (`PutPublicKey`) -> ErrConflict; unknown -> ErrNotFound.
  - `TestReferencesAddRemoveList`: add `{users, group, "corp", "pai-users"}` -> true, again -> false; add `{admins, identity, "", "pat"}`; `ListReferences(ListUsers)` has one row; remove -> true, again -> false.
  - `TestReferencesReject`: list `"owners"`, kind `"role"`, `{users, identity, "corp", "x"}`, `{users, email, "", "x"}`, empty value, value `"a\x00b"`: each ErrInvalid.
  - `TestAppendSenderKind`: append with `SenderKind: IdentityHuman` reads back human; `""` reads back agent; `"robot"` -> ErrInvalid and no row.
  - `TestResetRegistryReferences` (in `reset_test.go`): humans eric (with subject) and kim, agent pat; users `{group corp pai-users}`; admins `{identity pat}`, `{identity kim}`, `{group corp pai-admins}`; `ResetRegistry("pat")` leaves identities `[pat]`, no bindings, no users rows, admins exactly `[{admins identity "" pat}]`; a second `ResetRegistry("pat")` gives the same state.
  - `TestResetDataKeepsHumans`: after `ResetData`, bindings and both lists are unchanged.
  - `TestOpenMigratesVersion4Identities` (in `identities_test.go`, modeled on `TestOpenMigratesVersion3AssertionJTI`): open with `full[:4]`; raw-insert identities `adm` (is_admin true) and `pat`, a channel and a message from `pat`; reopen: `schemaVersion == len(full)`, both identities are agents, `ListReferences(ListAdmins)` is exactly `[{admins identity "" adm}]`, the message reads back `SenderKind == IdentityAgent`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -p 1 -run 'TestHuman|TestMarkHuman|TestReferences|TestAppendSenderKind|TestResetRegistryReferences|TestResetDataKeepsHumans|TestOpenMigratesVersion4' ./internal/conformance/store/`
Expected: FAIL to compile (`undefined: store.HumanBinding`, `IdentityHuman`, ...).

- [ ] **Step 3: Write `0005_human_identity.sql` for each engine**

One statement list per engine, no BEGIN/COMMIT: `ALTER TABLE identities ADD COLUMN kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent','human'))`; the same for `messages.sender_kind`; `CREATE TABLE human_bindings (identity TEXT PRIMARY KEY REFERENCES identities(name) ON DELETE CASCADE, issuer TEXT NOT NULL DEFAULT '', claim_type TEXT NOT NULL CHECK (claim_type IN ('email')), value TEXT NOT NULL, provider_subject TEXT NOT NULL DEFAULT '', created_at <INTEGER|BIGINT> NOT NULL, UNIQUE (claim_type, value))`; `CREATE UNIQUE INDEX human_bindings_subject ON human_bindings (issuer, provider_subject) WHERE provider_subject <> ''`; `CREATE TABLE access_refs (ref_list TEXT NOT NULL CHECK (ref_list IN ('users','admins')), ref_kind TEXT NOT NULL CHECK (ref_kind IN ('email','group','subject','identity')), issuer TEXT NOT NULL DEFAULT '', value TEXT NOT NULL, created_at <INTEGER|BIGINT> NOT NULL, PRIMARY KEY (ref_list, ref_kind, issuer, value))`; then `INSERT INTO access_refs SELECT 'admins', 'identity', '', name, created_at FROM identities WHERE is_admin` (`= 1` on SQLite). Header comment: spec 6.2, existing flags become `{identity: NAME}` entries.

State (Definition of Done D), written in the file header: `runStep` locks, re-reads the version and runs the step in one transaction, so a second open skips it, two overlapping opens run it once, and a failed run rolls back to version 4 (DDL is transactional on both engines) and the next open reruns it from clean.

- [ ] **Step 4: Implement the Store methods** in `internal/store/sqlstore/humans.go` and `references.go`, extend `identities.go`, `messages.go`, `meta.go` as in Produces. Map unique and foreign-key violations with the existing `isConflict`/`isForeignKey`; use `t.writable()` / `guardIdentityName` like the neighbors.

- [ ] **Step 5: Run the tests and the suites**

Run: `make test && make test-conformance`
Expected: PASS on SQLite and Postgres; `TestMigrationSetsAlign` passes (versions 1 to 5 on both).

- [ ] **Step 6: Commit**

```bash
git add internal/store internal/conformance/store
git commit -m "feat(store): schema version 5: identity kind, human bindings, users/admins lists, sender_kind"
```

---

### Task 2: The human marker on the wire (`sender_kind`, identity kind)

**Files:**
- Modify: `internal/core/append.go` (`AppendInput.SenderKind`, passed to `NewMessage`), `internal/core/convert.go` (`ToBusMessages` copies it), `internal/core/messaging/send.go`, `internal/core/memories/memories.go`, `internal/core/tasks/service.go`, `internal/core/tasks/create.go` (set `SenderKind` from the `RequireIdentity` result each already holds), `internal/core/sessions/identities.go` (`IdentityInfo.Kind`), `internal/bus/types.go`, `internal/bus/sessions.go`, `api/openapi.yaml` (`Message.sender_kind` and `Identity.kind`, both required, `enum: [agent, human]`), `internal/gen/*` (generated), `internal/server/convert.go`, the `/v1/identities` handler in `internal/server/sessions.go`, `internal/bus/remote/convert.go`, `internal/bus/remote/sessions.go` and the callers of `fromGenMessage`
- Test: `internal/conformance/bus/fixtures_test.go` (`h.human`), `internal/conformance/bus/send_test.go`, `internal/conformance/bus/sessions_test.go`, `internal/bus/remote/messages_test.go`

**Interfaces:**
- Consumes: Task 1 `store.IdentityKind`, `Identity.Kind`, `NewMessage.SenderKind`, `Tx.CreateHuman`.
- Produces: `bus.IdentityKind` (`IdentityAgent = "agent"`, `IdentityHuman = "human"`); `bus.Message.SenderKind IdentityKind` with JSON tag `sender_kind` (always present); `bus.IdentityInfo.Kind IdentityKind`; `core.AppendInput.SenderKind store.IdentityKind` (Append answers `internal` for `""` or an unknown kind, before any write); `fromGenMessage(m gen.Message) (ab2bus.Message, error)` in `internal/bus/remote`; conformance fixture `func (h *harness) human(email string) ab2bus.Bus` (creates the human through `tx.CreateHuman` when missing).

- [ ] **Step 1: Write the failing tests**
  - `TestSenderKind` (bus suite, both engines, both transports): `pat := h.as("pat")`, `eric := h.human("eric@corp.example")`, a channel both may write; each sends once; `pat` receives eric's message with `SenderKind == IdentityHuman` and his own via `History` with `IdentityAgent`; `History` returns the same kinds.
  - `TestIdentitiesKind` (bus suite): with live sessions for pat and eric, `Identities` reports `Kind` agent and human respectively.
  - `TestFromGenMessageRejectsUnknownSenderKind` (`internal/bus/remote`): a `gen.Message` with `SenderKind: "robot"` returns a `*bus.Error` with `CodeInternal` naming `sender_kind`; `"human"` converts.
  - Existing tests that pin an exact envelope size (`rules.EnvelopeUpperBound` sizes the `bus.Message` JSON) are updated to the new size, not loosened.

- [ ] **Step 2: Run to verify failure**

Run: `go test -count=1 -p 1 -run 'TestSenderKind|TestIdentitiesKind' ./internal/conformance/bus/ && go test -run TestFromGenMessageRejectsUnknownSenderKind ./internal/bus/remote/`
Expected: FAIL to compile (`SenderKind` undefined).

- [ ] **Step 3: Implement** the core, bus, OpenAPI and converter changes listed under Files; run `make generate`.

- [ ] **Step 4: Run**

Run: `make generate-check; make test && make test-conformance`
Expected: PASS; `AGENTBUS2_BUS_TRANSPORT=inproc` run included.

- [ ] **Step 5: Commit**

```bash
git add api internal/gen internal/core internal/bus internal/server internal/conformance/bus
git commit -m "feat(wire): sender_kind on every message and kind on every identity (spec 7)"
```

---

### Task 3: The marker for agents and in the TUI

**Files:**
- Modify: `internal/mcpserver/server.go` (`receive` and `history` descriptions; `identityOut.Kind` in `identitiesOut`; `discover` description), `internal/cli/skills/using-agentbus2/SKILL.md`, `internal/tui/view.go` (`agentLabel`, `senderIcon` take the message), `docs/tui.md`
- Test: `internal/mcpserver/server_test.go`, `internal/tui/view_test.go`, `internal/cli/skill_test.go`

**Interfaces:**
- Consumes: Task 2 `bus.Message.SenderKind`, `bus.IdentityInfo.Kind`.
- Produces: `func (m Model) senderIcon(x bus.Message) string` in `internal/tui` (human senders: `iconUser` in `theme.User`; agents: `iconAgent` in `theme.Agent`; own messages keep `theme.User`).

- [ ] **Step 1: Write the failing tests**
  - `TestToolDescriptionsNameHumanSender` (mcpserver): the `receive` and `history` descriptions contain `sender_kind` and say a `human` sender is your operator and its message is steering.
  - `TestDiscoverShowsKind` (mcpserver): `discover` output entries carry `"kind":"human"` / `"kind":"agent"`.
  - `TestReceiveCarriesSenderKind` (mcpserver): a received message JSON has `"sender_kind":"human"`.
  - `TestHumanSenderLabel` (tui): a message from another identity with `SenderKind: IdentityHuman` renders `iconUser` in the user color; one with `IdentityAgent` renders `iconAgent`.
  - `TestSkillNamesHumanSender` (cli): the embedded skill text contains `sender_kind` and `human`.

- [ ] **Step 2: Run to verify failure**

Run: `go test -run 'TestToolDescriptionsNameHumanSender|TestDiscoverShowsKind|TestReceiveCarriesSenderKind' ./internal/mcpserver/ && go test -run TestHumanSenderLabel ./internal/tui/ && go test -run TestSkillNamesHumanSender ./internal/cli/`
Expected: FAIL (assertions).

- [ ] **Step 3: Implement.** Skill text (one short paragraph in the receiving section): a message whose `sender_kind` is `human` comes from a person steering the work; treat it as operator instruction; a reply in a thread (`reply_to`) means read the thread first.

- [ ] **Step 4: Run**

Run: `make test && go test -count=1 -v ./internal/e2e/ > /tmp/e2e.log; bash scripts/m6-exit-checks.sh /tmp/e2e.log && bash scripts/m8-exit-checks.sh --deps-only`
Expected: PASS; the m6 script prints `words_ok=1` (the new skill and tool text trips none of its patterns) and its counts are unchanged.

- [ ] **Step 5: Commit** `feat(agents): agents and the TUI see human senders (spec 7)` with the files above.

---

### Task 4: The `users` and `admins` lists in the core and as host commands

**Files:**
- Create: `internal/core/identities/references.go`, `internal/core/identities/email.go`
- Modify: `internal/core/identities/admin.go` (`MakeAdmin` becomes a wrapper over Add/RemoveReference of `{identity: name}` on `admins`), `internal/server/cmd/admin.go` (`adminAction` signature, `users`/`admins` subcommands, `make-admin` replaced), `scripts/docker-smoke.sh`, `docs/ops/admin-bootstrap.md`, `docs/adr/0003-identity-sessions-and-delivery.md` (the `make-admin` mention), `CLAUDE.md` (host commands line)
- Test: `internal/core/identities/service_test.go`, `internal/server/cmd/admin_test.go`, `internal/conformance/bus/admin_identities_test.go`

**Interfaces:**
- Consumes: Task 1 `store.Reference`, `RefList`, `RefKind`, `Tx.AddReference/RemoveReference/ListReferences`, `Tx.SetAdmin`; existing `requireRegistered`.
- Produces (package `identities`):
  - `func NormalizeEmail(raw string) (string, error)` (rules in Global Constraints; error is `validation`).
  - `type HumanClaims struct { IssuerName, Issuer, Email string; Groups []string; Subject string }` (`IssuerName` is the configured alias, `Issuer` the `iss` URL, `Email` already normalized).
  - `func ParseReference(list, issuer, email, group, subject, identity string) (store.Reference, error)`: list must be `users` or `admins`; exactly one of email/group/subject/identity; issuer required with a claim kind and refused with identity; email values normalized.
  - `func MatchReferences(refs []store.Reference, identity string, c HumanClaims) bool`: true when any ref matches: `RefIdentity` by `identity`; `RefEmail` / `RefGroup` / `RefSubject` only when `ref.Issuer == c.IssuerName`, against `c.Email`, membership in `c.Groups`, `c.Subject`. Pure; no I/O.
  - `func (s Service) AddReference(ctx, r store.Reference, issuers []string) (bool, error)`: claim kinds need `r.Issuer` in `issuers` (`validation` "unknown issuer %q; configured: ..."); `RefIdentity` needs the identity to exist (`not_registered` via `requireRegistered`, spec 6.1) and on `admins` sets its flag in the same transaction.
  - `func (s Service) RemoveReference(ctx, r store.Reference) (bool, error)`: no issuer check (cleanup of a removed issuer works); removing `{admins, identity, NAME}` clears NAME's flag when the row exists (plan choice: a human admitted by a claim ref regains it at the next exchange).
  - `func (s Service) ListReferences(ctx, list store.RefList) ([]store.Reference, error)`.
  - Host commands: `agentbus2 server admin users|admins add|remove|list [-issuer NAME] [-email V | -group V | -subject V | -identity NAME]`; `list` prints one `{issuer: corp, group: pai-users}` / `{identity: pat}` line per entry; `add` prints `added` or `already present`, `remove` prints `removed` or `not present` (both exit 0). `type adminEnv struct { Svc identities.Service; Issuers []string }` and `type adminAction func(context.Context, adminEnv, io.Writer) error`, `Issuers` from the `config.Config` that `openFromConfig` already returns (today discarded), mapped to entry names in Task 9 (until then `nil`, so every claim-kind `add` is refused, which Task 9's test flips). `make-admin` exits 2 with `make-admin was replaced by: admins add -identity NAME (admins remove -identity NAME)`.

- [ ] **Step 1: Write the failing tests**
  - `TestNormalizeEmail` (table): `" Eric@Corp.Example "` -> `"eric@corp.example"`; `"everyone"`, `"admins"`, `"no-at"`, `"@corp"`, `"eric@"`, `"a/b@c"`, `"a\nb@c"`, 129-byte address, `""` -> validation.
  - `TestParseReferenceRejects` (table): list `"owners"`; two of email/group; none; identity with `-issuer`; group without issuer; each validation.
  - `TestMatchReferences` (table): identity ref matches name only; email ref of issuer `corp` matches `c.Email` with IssuerName `corp`, not with `other`; group ref matches membership; subject ref; empty refs false.
  - `TestReferencesAddIdentityWritesFlag`: `AddReference({admins identity pat})` -> `Identity("pat").Admin == true`; again -> `false, nil`; `RemoveReference` -> flag false; again -> `false, nil`.
  - `TestReferencesRefuseUnknownIdentity`: `{admins identity ghost}` -> `not_registered`, no row.
  - `TestReferencesRefuseUnknownIssuer`: `{users group nope pai}` with issuers `["corp"]` -> validation; with `["corp"]` and issuer corp -> added.
  - `TestMakeAdminUsesAdminsList`: `MakeAdmin(pat, true)` leaves `{admins identity pat}` and the flag; `MakeAdmin(pat, false)` removes both.
  - `TestAdminUsersAdminsCommands` (host, SQLite file): `create-identity pat`, `admins add -identity pat` (stdout `added`), again (`already present`), `admins list` prints `{identity: pat}`, `users add -issuer corp -group x` exits 1 (no issuer configured yet), `admins add -group x` exits 2 (no issuer), `admins add -identity pat -email a@b` exits 2, `admins remove -identity pat` (`removed`).
  - `TestAdminMakeAdminReplaced`: `make-admin pat` exits 2 with the replacement text on stderr.

- [ ] **Step 2: Run to verify failure**

Run: `go test -run 'TestNormalizeEmail|TestParseReference|TestMatchReferences|TestReferences|TestMakeAdminUsesAdminsList' ./internal/core/identities/ && go test -run 'TestAdminUsersAdminsCommands|TestAdminMakeAdminReplaced' ./internal/server/cmd/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement** as in Produces; update `scripts/docker-smoke.sh` (`admins add -identity`), the docs and the CLAUDE.md host-commands line (`users|admins add|remove|list`, no `make-admin`).

- [ ] **Step 4: Run**

Run: `make test && make test-conformance`
Expected: PASS (bus-suite tests that call `MakeAdmin` pass unchanged through the wrapper).

- [ ] **Step 5: Commit** `feat(admin): users and admins reference lists; make-admin becomes admins add (spec 6)`.

---

### Task 5: Human identities in the core (create by email, reset scopes, local mode)

**Files:**
- Create: `internal/core/identities/human.go`
- Modify: `internal/core/identities/register.go` (`registerTx` refuses a human; `RegisterLocalHuman`), `internal/core/identities/admin.go` (`resetCredential` on a human clears the subject; `Reset` everything restores `{admins identity admin}`), `internal/server/cmd/admin.go` (`create-human EMAIL`), `internal/client/local.go` (mark the OS-login TUI identity human), `docs/ops/admin-bootstrap.md`, `CLAUDE.md` (host commands line)
- Test: `internal/core/identities/service_test.go`, `internal/conformance/bus/admin_identities_test.go`, `internal/server/cmd/admin_test.go`, `internal/client/local_test.go`

**Interfaces:**
- Consumes: Task 1 `Tx.CreateHuman`, `MarkHuman`, `HumanBinding*`, `SetHumanSubject`, `ResetRegistry`; Task 4 `NormalizeEmail`, `AddReference`; existing `createWithGeneral`, `maxAttempts`, `WithReceipt`.
- Produces (package `identities`):
  - `func (s Service) CreateIdentityByEmail(ctx context.Context, email string) (identity string, created bool, err error)`: normalized; idempotent for a human whose binding value is that email (`created == false`); `conflict` "identity %q is an agent" for an agent of that name; the new human gets the general subscription (as `createWithGeneral`) and a binding with empty issuer and subject (plan choice; see spec gap 1 in the report). Reruns on ErrConflict up to `maxAttempts`.
  - `func (s Service) CreateIdentityByEmailAs(ctx context.Context, admin, email, key string) (string, bool, error)`: admin only, idempotent under key (as `ResetCredentialAs`).
  - `func (s Service) LookupHumanByEmail(ctx context.Context, email string) (string, error)`: normalizes, then `HumanBindingByValue(ClaimEmail, ...)`; `not_found` when unbound.
  - `func (s Service) RegisterLocalHuman(ctx context.Context, name string) error`: create (as `RegisterLocal`) then `MarkHuman`, one transaction, idempotent.
  - `Register` on an existing human: `forbidden` "identity %q is a human; humans sign in through the corporate provider", consuming nothing (before rule (b)).
  - `ResetCredential` on a human: clears issuer and subject; returns 1 when a subject was recorded, else 0; repeat returns 0.
  - `Reset` with `everything` (spec Decision 12): after `ResetRegistry(admin)`, `AddReference({admins identity admin})` and `SetAdmin(admin, true)` in the same transaction; `data` touches neither list nor bindings.
  - Host command `agentbus2 server admin create-human EMAIL` prints `<identity> created` or `<identity> already exists`.
  - `openLocal`: when `o.HumanIdentity && r.IdentitySource == "os-user"` it calls `RegisterLocalHuman`, else `RegisterLocal` (a `-identity` given to the TUI never turns an agent human).

- [ ] **Step 1: Write the failing tests**
  - `TestCreateIdentityByEmail` (core): `"Eric@Corp.Example"` -> `("eric@corp.example", true)`; again -> `false`; `Identity` is human with general subscription; `create-identity pat` then `CreateIdentityByEmail("pat@x")` works but an agent named `kim@corp.example` -> conflict; `"not-an-email"` -> validation.
  - `TestLookupHumanByEmail`: `" ERIC@corp.example"` -> `"eric@corp.example"`; unknown -> not_found; an agent of that name -> not_found.
  - `TestRegisterRefusesHuman`: open mode, key registration on `eric@corp.example` -> forbidden; no key stored; an enrollment token supplied is not consumed.
  - `TestResetCredentialHumanClearsSubject`: after `SetHumanSubject`, `ResetCredential` returns 1 and the binding has empty issuer and subject; again returns 0.
  - `TestResetScopesHumans` (bus suite, both engines): humans eric and kim, agent adm (admin), users and admins refs; `Reset(data)` keeps identities, bindings, both lists; `Reset(everything)` as adm leaves only adm, no bindings, no users, admins exactly `{identity adm}`, adm still admin; a second `Reset(everything)` gives the same state.
  - `TestAdminCreateHuman` (host): exits 0 twice with `created` then `already exists`; `create-human bad` exits 1 naming the rule.
  - `TestOpenLocalMarksOSUserHuman` (client): `Resolve` via `FallbackIdentity` with `HumanIdentity` -> the row is human; with `-identity agentx` and `HumanIdentity` -> agent.

- [ ] **Step 2: Run to verify failure**

Run: `go test -run 'TestCreateIdentityByEmail|TestLookupHumanByEmail|TestRegisterRefusesHuman|TestResetCredentialHumanClearsSubject' ./internal/core/identities/`
Expected: FAIL to compile.

- [ ] **Step 3: Implement** as in Produces.

- [ ] **Step 4: Run**

Run: `make test && make test-conformance`
Expected: PASS.

- [ ] **Step 5: Commit** `feat(identities): human identities by email, reset scopes, local OS-login human (spec 4, Decision 12)`.

---

### Task 6: `createIdentityByEmail` over the API

**Files:**
- Modify: `api/openapi.yaml` (path `POST /v1/admin/identities/by-email`, operationId `createIdentityByEmail`, tag admin, `IdempotencyKeyParam`, body `CreateIdentityByEmailRequest {email}` required, response 200 `HumanIdentity {identity, created}` both required, default Problem), `internal/gen/*`, `internal/bus/admin.go`, `internal/bus/local/admin_identities.go`, `internal/bus/remote/admin_identities.go`, `internal/server/admin_auth.go`
- Test: `internal/conformance/bus/admin_identities_test.go`, `internal/server/admin_auth_test.go`

**Interfaces:**
- Consumes: Task 5 `CreateIdentityByEmailAs`.
- Produces: `bus.CreateIdentityByEmailInput{ Email, IdempotencyKey string }`, `bus.CreateIdentityByEmailResult{ Identity string; Created bool }`, `bus.Admin.CreateIdentityByEmail(ctx context.Context, in CreateIdentityByEmailInput) (CreateIdentityByEmailResult, error)`; `func (a *api) CreateIdentityByEmail(ctx, gen.CreateIdentityByEmailRequestObject) (gen.CreateIdentityByEmailResponseObject, error)`.

- [ ] **Step 1: Write the failing tests**
  - `TestCreateIdentityByEmailBus` (bus suite, both engines and transports): admin creates `"Eric@Corp.Example"` -> `{eric@corp.example, true}`; again -> `false`; same idempotency key replays the first result; non-admin -> forbidden; agent name -> conflict; `"x"` -> validation.
  - `TestCreateIdentityByEmailWire` (server): POST without a body -> 400 problem+json; with an unknown member -> 400; success body has both members.

- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run TestCreateIdentityByEmailBus ./internal/conformance/bus/` -> FAIL to compile.

- [ ] **Step 3: Implement**; `make generate`.

- [ ] **Step 4: Run** `make generate-check; make test && make test-conformance` -> PASS.

- [ ] **Step 5: Commit** `feat(api): admin createIdentityByEmail (spec 4.3)`.

---

### Task 7: Own tokens, client assertions and authorization server metadata (spec 8.2, 8.3, 8.4, 8.6)

**Files:**
- Modify: `internal/auth/token/issuer.go` (`Mint`, `Verify`, `MetadataURI`), `internal/auth/assertion/assertion.go` (`Input.Audience`, `JWTType`, `Parse`, `Verify`), `internal/server/auth_handlers.go` (verify against both audiences; `Mint(p.Identity, token.KindAgent, 0)`), `api/openapi.yaml` (path `/.well-known/oauth-authorization-server`, operationId `authorizationServerMetadata`, security `[]`; schema `AuthorizationServerMetadata` replaces `OpenIDConfiguration` on both well-known paths; remove the `oidc` security scheme and `- oidc: []` from top-level `security`; `bearerAuth` description names the RFC 8414 document; `Discovery.authorization_server` optional string), `internal/gen/*`, `internal/server/discovery.go`, `internal/credential/discover.go` (fetch `/.well-known/oauth-authorization-server`; require `issuer`), `internal/credential/source.go` (`assertion.Input{Audience: d.Issuer}`; `ProcessInput.Audience = d.Issuer`), `internal/conformance/bus/auth_test.go` (`mintBearer` passes `issuer.URL()`; the `forged-bearer-refused` signer at line 274 uses `WithType("at+jwt")` instead of `"JWT"`, so block 14's unsafe-claims mutation still reaches the signature check; the back-dated `past.Mint("root")` becomes `past.Mint("root", token.KindAgent, 0)`), `docs/adr/0002-authentication-and-credentials.md` (start the addendum)
- Test: `internal/auth/token/issuer_test.go`, `internal/auth/assertion/assertion_test.go`, `internal/server/discovery_test.go`, `internal/credential/discover_test.go`, `internal/credential/source_test.go`, `internal/credential/process_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `token.KindAgent = "agent"`, `token.KindHuman = "human"`, `token.KindClaim = "agentbus2_kind"`; `func (i *Issuer) Mint(identity, kind string, lifetime time.Duration) (string, time.Time, error)` (lifetime 0: `i.Lifetime()`; an unknown kind or negative lifetime is an error; header `typ: at+jwt`; claims per Global Constraints); `func (i *Issuer) Verify(raw string, now time.Time) (string, error)` additionally requires header `typ` `at+jwt` or `application/at+jwt` (case-insensitive); `func (i *Issuer) MetadataURI() string`. The block-14 anchor line stays verbatim.
  - `assertion.Input{ Identity, Audience string; Key crypto.Signer; KID string; Now time.Time; Lifetime time.Duration }`; `assertion.JWTType = "client-authentication+jwt"` (sent as `typ`); `Parse` refuses `typ` `at+jwt` / `application/at+jwt` (ErrMalformed) and accepts no `typ`, `JWT` or `JWTType`; `func (p Parsed) Verify(pub crypto.PublicKey, audiences []string, now time.Time, leeway time.Duration) error` (any listed audience matches; empty list refused).
  - Metadata body (both paths, identical): `issuer`, `token_endpoint`, `jwks_uri`, `response_types_supported: []`, `grant_types_supported: ["client_credentials", "urn:ietf:params:oauth:grant-type:token-exchange"]`, `token_endpoint_auth_methods_supported: ["private_key_jwt", "none"]` (`none` is the token exchange's public client), `token_endpoint_auth_signing_alg_values_supported: ["EdDSA", "ES256"]`.
  - `credential.Discover(ctx, hc, server) (Discovery, error)` reads `server + "/.well-known/oauth-authorization-server"`; an empty `issuer` or `token_endpoint` is an error.

- [ ] **Step 1: Write the failing tests**
  - `TestMintAccessTokenProfile`: decode a minted token: header `typ == "at+jwt"`; claims `iss`, `aud == [iss]`, `sub`, `client_id == sub`, `iat`, `exp`, non-empty `jti`, `agentbus2_kind == "agent"`; `Mint("x", "robot", 0)` errors; `Mint("x", KindHuman, 900*time.Second)` has `exp - iat == 900`.
  - `TestVerifyRequiresAccessTokenType`: a token signed with the issuer key and `typ: JWT` -> ErrInvalid; `application/at+jwt` -> accepted.
  - `TestSignSendsIssuerAudienceAndType`: `Sign` output has `aud == ["https://bus.example"]` and `typ == "client-authentication+jwt"`.
  - `TestVerifyAcceptsIssuerOrTokenEndpoint`: aud = issuer passes, aud = token endpoint passes, aud = other fails with `jwt.ErrInvalidAudience`, empty audiences list fails.
  - `TestParseRefusesAccessTokenType`: an assertion with `typ: at+jwt` -> ErrMalformed.
  - `TestAuthorizationServerMetadata` (server): both well-known paths return identical JSON; `response_types_supported` is present and `[]`; grant types as above; issuer is the configured one.
  - `TestDiscoverHasNoOpenIDConnectScheme` (server): no `auth_methods` entry has type `openIdConnect`; `authorization_server` ends with `/.well-known/oauth-authorization-server`.
  - `TestDiscoverReadsRFC8414Path`, `TestDiscoverRequiresIssuer` (credential).
  - `TestMintSignsForIssuer` (credential `source_test.go`): the assertion the fake token endpoint receives has `aud == [issuer]`; `TestProcessInputAudienceIsIssuer`: a `credential_process` receives `audience == issuer` and `token_endpoint` unchanged.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/auth/... ./internal/credential/ && go test -run 'TestAuthorizationServerMetadata|TestDiscoverHasNoOpenIDConnectScheme' ./internal/server/`
Expected: FAIL.

- [ ] **Step 3: Implement**; `make generate`. Start the ADR 0002 addendum "Federated identity addendum (2026-10-23)": a "Decisions (Eric, 2026-10-23)" list restating Decisions 1 to 12 of the spec, each labeled as Eric's; a "Claude's calls (ratified by Eric, 2026-10-23)" list; a "Standards alignment" list with items 8.2, 8.3 (token endpoint URL kept as an accepted `aud` until the draft is published and shipped clients send the issuer), 8.4 (`client_id = sub`, plan choice), 8.6.

- [ ] **Step 4: Run** `make generate-check; make test && make test-conformance && make check-gates-selftest` -> PASS (`TestAuthEndToEnd` 11 leaves per engine; selftest block 14 still fails `TestAuthEndToEnd/sqlite/forged-bearer-refused` under its mutation).

- [ ] **Step 5: Commit** `feat(auth): RFC 9068 bearers, rfc7523bis assertions, RFC 8414 metadata (spec 8.2-8.4, 8.6)`.

---

### Task 8: Token endpoint errors and parameters (spec 8.1, 8.5)

**Files:**
- Create: `internal/server/oauth.go`
- Modify: `api/openapi.yaml` (`TokenRequest`: `grant_type` required plain string; optional `client_assertion_type`, `client_assertion`, `client_id`, `scope`, `subject_token`, `subject_token_type`, `requested_token_type`, `audience`, `resource`, `actor_token`, `actor_token_type`; `AccessToken.issued_token_type` optional; token responses `"400"`, `"401"`, `"500"`, `"503"` with schema `OAuthError {error (required), error_description}`, no default Problem), `internal/gen/*`, `internal/server/auth_handlers.go` (dispatch on `grant_type`; every failure returns an `oauthError`), `internal/server/server.go` (validator `ErrorHandlerWithOpts`, strict `RequestErrorHandlerFunc` and mux `ErrorHandlerFunc` write `invalid_request` when `r.URL.Path == tokenPath`), `internal/bus/remote/auth.go` (`Token` maps the OAuth body), `docs/adr/0002-authentication-and-credentials.md`
- Test: `internal/server/auth_test.go`, `internal/bus/remote/auth_test.go`

**Interfaces:**
- Consumes: Task 7 `assertion.Parsed.Verify(pub, audiences, ...)`, `Issuer.Mint`.
- Produces:
  - In `internal/server/oauth.go`: `const tokenPath = "/v1/auth/token"`; `type oauthError struct { Status int; Code, Description string }` implementing `gen.TokenResponseObject` (`VisitTokenResponse` writes `application/json`, `Cache-Control: no-store`); `func oauthFail(status int, code, format string, args ...any) oauthError`; `func oauthFromBus(err error, exchange bool) oauthError` (client_credentials: `unauthenticated` -> 401 `invalid_client`; `validation` -> 400 `invalid_request`; exchange: `forbidden` -> 400 `access_denied`; `unauthenticated`, `conflict`, `validation` -> 400 `invalid_request` (RFC 8693 section 2.2.2); both: `unavailable` -> 503 `temporarily_unavailable`; anything else -> 500 `server_error`); `func writeOAuthError(w http.ResponseWriter, e oauthError)`.
  - `client_credentials` rules: `client_assertion_type` other than `assertion.Type`, or no `client_assertion` -> 401 `invalid_client`; parse, unknown key, verification, replay -> 401 `invalid_client`; `client_id` present and not equal to the assertion's `iss` -> 401 `invalid_client`; `scope` ignored. An empty `grant_type` -> 400 `invalid_request`; any grant other than `client_credentials` -> 400 `unsupported_grant_type` (Task 11 adds the exchange). The block-21 anchor line stays verbatim.
  - `remote.Token` maps: `invalid_client`, `invalid_grant` -> `CodeUnauthenticated`; `access_denied` -> `CodeForbidden`; `invalid_request`, `unsupported_grant_type`, `invalid_target`, `invalid_scope` -> `CodeValidation`; `temporarily_unavailable` -> `CodeUnavailable` (retryable); `server_error` and any unrecognized code -> `CodeInternal` ("unrecognized OAuth error %q" for the latter); the message is `error_description`, else `error`.

- [ ] **Step 1: Write the failing tests**
  - `TestTokenOAuthErrors` (server, table over a `server.New` handler): missing `grant_type` -> 400 `invalid_request`; `grant_type=password` -> 400 `unsupported_grant_type`; JSON body instead of form -> 400 `invalid_request`; wrong `client_assertion_type` -> 401 `invalid_client`; garbage assertion -> 401 `invalid_client`; replayed assertion -> 401 `invalid_client`; `client_id` mismatch -> 401 `invalid_client`; issuer unset -> 503 `temporarily_unavailable`. Each: `Content-Type: application/json`, body decodes strictly to `{error, error_description}` with no `type`/`status`/`code` members, `error_description` contains no assertion text.
  - `TestTokenAcceptsClientIDAndScope`: matching `client_id` plus `scope=anything` -> 200.
  - `TestOtherEndpointsKeepProblemJSON`: a malformed `POST /v1/channels` still answers `application/problem+json`.
  - `TestTokenMapsOAuthErrors` (remote, table with an `httptest` server): each code above maps as listed; `{"error":"weird"}` -> `CodeInternal` naming `weird`.

- [ ] **Step 2: Run to verify failure** — `go test -run 'TestTokenOAuthErrors|TestTokenAcceptsClientIDAndScope|TestOtherEndpointsKeepProblemJSON' ./internal/server/` -> FAIL.

- [ ] **Step 3: Implement**; `make generate`. ADR addendum: items 8.1 (supersedes addendum item 3 of Milestone 5) and 8.5, and the 503/500 plan choice.

- [ ] **Step 4: Run** `make generate-check; make test && make test-conformance` -> PASS. Update any `TestAuthEndToEnd` or `TestReplicasWake` assertion on the token endpoint's error shape or text (not its leaf names or count), then `go test -count=1 -p 1 -v -run '^TestAuthEndToEnd$' ./internal/conformance/bus/ > /tmp/auth.log; bash scripts/m5-exit-checks.sh /tmp/auth.log` -> `auth_sqlite=11 auth_postgres=11 fail=0`; `make check-gates-selftest` -> blocks 14, 15 and 21 still fire.

- [ ] **Step 5: Commit** `feat(auth): RFC 6749 error bodies and standard parameters at the token endpoint (spec 8.1, 8.5)`.

---

### Task 9: Issuers configuration; direct acceptance removed (spec 5.1, Decision 10)

**Files:**
- Modify: `internal/server/config/config.go` (`Issuer`, `IssuerClaims`, `validateIssuer`; comment cites Decision 57, not 84), `internal/auth/token/external.go` (claims, no `Subjects`), `internal/auth/token/verifier.go` (own issuer only), `internal/server/auth_bearer.go` (no `AutoCreate`, no `Logger`, no unmapped-subject reason), `internal/core/identities/register.go` (delete `EnsureIdentity`), `internal/server/cmd/cmd.go` (`externalIssuers` maps the new fields; no autoCreate; `Bearer{Verifier: token.Verifier{Own: issuer}}`), `internal/server/cmd/admin.go` (`adminEnv.Issuers` = entry names), `docs/ops/configuration.md` (the `issuers` row, the environment example, the rules paragraph; Decision 57), `docs/adr/0002-authentication-and-credentials.md`
- Test: `internal/server/config/config_test.go`, `internal/server/config/load_test.go`, `internal/auth/token/external_test.go`, `internal/server/auth_test.go`, `internal/server/cmd/cmd_test.go`, `internal/server/cmd/config_test.go`, `internal/server/cmd/admin_test.go`

**Interfaces:**
- Consumes: Task 4 `adminEnv`.
- Produces:
  - `config.Issuer{ Name, Issuer, JWKSURI, Audience string; Algorithms []string; Claims IssuerClaims; HumanTokenLifetimeSeconds int }` with JSON names `name`, `issuer`, `jwks_uri`, `audience`, `algorithms`, `claims`, `human_token_lifetime_seconds`; `config.IssuerClaims{ Email, Groups, Subject string }` (`email`, `groups`, `subject`; `""` means the default). Rules: `name` required, passes `store.NameRule`, unique across entries; `human_token_lifetime_seconds` 0 (default 900) or 1 to 86400; the old `subjects` member is an unknown key (strict load refuses it).
  - `token.ClaimNames{ Email, Groups, Subject string }`; `token.External` loses `Subjects` and gains `Name string; Claims ClaimNames; HumanLifetime time.Duration`; `token.Federated{ Issuer, Name, Email string; Groups []string; Subject string }`; `func (e *External) Verify(ctx context.Context, raw string) (Federated, error)` (signature, `iss`, `aud`, `exp`, `nbf`, algorithm allowlist as today; email claim must be a non-empty string, else `ErrNoEmail` wrapped with `ErrInvalid`; groups: absent -> nil, a string -> one, an array of strings -> them, anything else -> `ErrInvalid`; subject claim a non-empty string, else `ErrInvalid`); `var ErrNoEmail = errors.New("token: no email claim")`; `ErrUnmappedSubject` deleted.
  - `token.Verifier{ Own *Issuer; Now func() time.Time }`; `func (v Verifier) Verify(ctx context.Context, raw string) (string, error)` (any `iss` but the own one is `ErrUnknownIssuer`).
  - `cmd.externalIssuers(cfg config.Config) []*token.External` fills `Claims` defaults `email`/`groups`/`sub` and `HumanLifetime` 900 s when zero.

- [ ] **Step 1: Write the failing tests**
  - `TestIssuerValidation` (config, table): missing name; duplicate name; name `"a/b"`; lifetime -1 and 86401 refused; 0, 1, 86400 accepted; `TestIssuerSubjectsRefused` (load): a file entry with `"subjects"` fails naming the key.
  - `TestExternalIssuersMapping` (cmd, updated): defaults `email`/`groups`/`sub` and 900 s; explicit `{"email":"mail","subject":"user_id"}` and 600 carried.
  - `TestExternalVerifyClaims`, `TestExternalGroupsShapes` (absent, string, string array, array with a number, object), `TestExternalNoEmailRefused` (`errors.Is(err, ErrNoEmail) && errors.Is(err, ErrInvalid)`), `TestExternalCustomClaimNames`, `TestExternalNoSubjectRefused` (token, over the existing JWKS fixtures).
  - `TestBearerRefusesCorporateToken` (server): a token the configured External would accept, sent as `Authorization: Bearer`, gets 401 problem+json with "unknown issuer"; with registration open, `ListIdentities` gains no row.
  - `TestAdminUsersAddKnownIssuer` (host): with `-set issuers=[{"name":"corp",...}]`, `users add -issuer corp -group pai-users` prints `added`; `-issuer nope` exits 1 naming `corp`.
  - Docs: `go test ./internal/server/config/` (`docs_test.go`) passes with the rewritten row and example.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/server/config/ ./internal/auth/token/ ./internal/server/cmd/` -> FAIL.

- [ ] **Step 3: Implement**; delete the AutoCreate tests and `EnsureIdentity` with its tests. ADR addendum: Decision 10 removal (Verifier.External, Bearer.AutoCreate, `subjects`) and the issuers entry fields.

- [ ] **Step 4: Run** `make test && make test-conformance && bash scripts/m5-exit-checks.sh --deps-only` -> PASS.

- [ ] **Step 5: Commit** `feat(auth): issuers name, claims and human lifetime; external bearers no longer accepted (spec 5.1, Decision 10)`.

---

### Task 10: Human sign-in in the core (the gate, bindings, admins)

**Files:**
- Modify: `internal/core/identities/human.go` (`SignInHuman`)
- Test: `internal/conformance/bus/signin_test.go` (new; both engines)

**Interfaces:**
- Consumes: Task 1 Store methods; Task 4 `HumanClaims`, `MatchReferences`, `NormalizeEmail`; Task 5 `createWithGeneral` path for humans.
- Produces: `type HumanSignIn struct { Identity string; Admin, Created bool }`; `func (s Service) SignInHuman(ctx context.Context, c HumanClaims) (HumanSignIn, error)`.

Rule order, one `Store.Tx`, rerun on ErrConflict up to `maxAttempts`: (1) `NormalizeEmail(c.Email)` (failure: `unauthenticated` "email claim is not a valid identity name"); (2) look up the binding by email and by `(c.Issuer, c.Subject)`; (3) evaluate `users` with `MatchReferences(users, boundIdentityOrEmail, c)`; no match: `forbidden` "access denied: no users entry matches", nothing written; (4) subject bound to another email: `unauthenticated` "subject bound to identity %q"; email bound with a recorded subject or issuer that differs: `unauthenticated` "identity bound to a different subject"; (5) no binding and an identity row of that name: `conflict` "identity %q is an agent" (a human row without a binding is the same refusal); (6) no binding: `CreateHuman` with issuer and subject, plus the general subscription; binding with empty subject: `SetHumanSubject(identity, c.Issuer, c.Subject)`; (7) `SetAdmin(identity, MatchReferences(admins, identity, c))`.

State (Definition of Done D): a second sign-in with the same claims changes nothing but the admin flag's re-evaluation; two overlapping first sign-ins end with one identity and one binding (the loser reruns and finds it); a failed run writes nothing (one transaction); a crash after commit and before minting leaves a correct identity and flag, and the next exchange is the same idempotent path.

- [ ] **Step 1: Write the failing tests** (`dbtest.ForEachEngine`, harness `svc := identities.Service{Core: h.core}`)
  - `TestSignInHumanGate`: no users refs -> forbidden and `Identity("eric@corp.example")` not found; `{users email corp eric@corp.example}` -> created human, binding issuer `https://idp` subject `guid-1`, subscribed to general; group ref; subject ref; a ref for issuer `other` does not admit `corp` claims.
  - `TestSignInHumanBindsPrecreated`: `CreateIdentityByEmail` then `{users identity eric@corp.example}`; sign-in -> `Created == false`, binding gains the subject; a second sign-in succeeds unchanged.
  - `TestSignInHumanReassignedEmail`: bound to `guid-1`; claims with `guid-2` -> unauthenticated "different subject"; after `ResetCredential` -> success bound to `guid-2`.
  - `TestSignInHumanChangedEmail`: `guid-1` bound to eric; claims `{email: eric2@corp.example, subject: guid-1}` -> unauthenticated naming eric; no eric2 row.
  - `TestSignInHumanAgentName`: agent `kim@corp.example` exists, users admit kim -> conflict, no binding.
  - `TestSignInHumanAdmins`: `{admins group corp pai-admins}`; groups include it -> `Admin == true`; next sign-in without it -> `false`; `{admins identity eric@corp.example}` -> true regardless of groups.
  - `TestSignInHumanEmailNormalized`: email `" Eric@Corp.Example "` reaches `eric@corp.example`.
  - `TestSignInHumanConcurrent`: 8 goroutines sign in the same new claims; all succeed; one identity, one binding, exactly one result has `Created == true`.

- [ ] **Step 2: Run to verify failure** — `go test -count=1 -p 1 -run TestSignInHuman ./internal/conformance/bus/` -> FAIL to compile.

- [ ] **Step 3: Implement** `SignInHuman`.

- [ ] **Step 4: Run** `make test && make test-conformance` -> PASS.

- [ ] **Step 5: Commit** `feat(identities): human sign-in: users gate, bindings, admins evaluation (spec 4.2, 6.2)`.

---

### Task 11: The token exchange endpoint and the federated end-to-end test

**Files:**
- Create: `internal/oidctest/provider.go`, `internal/oidctest/provider_test.go`, `internal/server/auth_exchange.go`, `internal/server/auth_exchange_test.go`, `internal/conformance/bus/federated_test.go`
- Modify: `internal/server/auth_handlers.go` (dispatch the token-exchange grant), `internal/bus/remote/auth.go` (`TokenExchange`), `docs/ops/configuration.md` (issuers serve the exchange only; a worked OCI-shaped example with `claims.subject = "user_id"`), `docs/ops/admin-bootstrap.md` (users/admins/create-human walk-through), `docs/adr/0002-authentication-and-credentials.md`

**Interfaces:**
- Consumes: Task 7 `Issuer.Mint`, Task 8 `oauthError`/`oauthFromBus`, Task 9 `token.External.Verify`, `Federated`, Task 10 `SignInHuman`.
- Produces:
  - `internal/oidctest`: `type Options struct { Audience string; ClientID string; Algorithm string /* "RS256" default */ }`; `func New(tb testing.TB, o Options) *Provider` (an `httptest` server closed by `tb.Cleanup`, serving `/.well-known/openid-configuration` and `/jwks`); `func (p *Provider) URL() string`, `func (p *Provider) JWKSURI() string`, `func (p *Provider) Mint(claims map[string]any) string` (fills `iss`, `aud` = Audience, `iat`, `exp` = now + 5 min unless present; a member set to `nil` is omitted; signed with a `kid`).
  - `func (a *api) tokenExchange(ctx context.Context, b *gen.TokenFormdataRequestBody) gen.TokenResponseObject`: `subject_token` required; `subject_token_type` must be the access-token URN (the id_token URN: `invalid_request` "ID tokens are not accepted; present the provider's access token"); `requested_token_type`, if set, the access-token URN; `audience`/`resource`, if set, equal to the server's issuer URL, else `invalid_target`; `actor_token` set: `invalid_request`; `scope` ignored; `iss` matches no External: `invalid_request` "unknown issuer"; `External.Verify` failure: `invalid_request` with a fixed reason; then `SignInHuman` and `oauthFromBus(err, true)`; success: `Mint(identity, token.KindHuman, ext.HumanLifetime)`, 200 with `issued_token_type` = the access-token URN, `token_type` `Bearer`, `expires_in`. Refusals are logged through `Options.Logger` with issuer and reason (and identity when known), never the token.
  - `func TokenExchange(ctx context.Context, c *gen.ClientWithResponses, subjectToken string) (AccessToken, error)` in `internal/bus/remote` (same error mapping as `Token`).

- [ ] **Step 1: Write the harness self-tests** (Definition of Done C): `TestProviderMintDefaults` (claims filled, `kid` present, verifies against `/jwks`), `TestProviderMintOverridesAndOmits`, `TestProviderDiscovery`, `TestProviderClosedAfterCleanup` (a sub-test's provider refuses connections after the sub-test ends).

- [ ] **Step 2: Write the failing exchange tests**
  - `TestTokenExchangeIssuesHumanBearer`: issuers entry for the provider, users `{email corp eric@corp.example}`, `Registration: identities.ModeClosed` (Decision 10: the first sign-in creates the identity in every mode); exchange -> 200, `issued_token_type` the access-token URN, `expires_in == 900`; the bearer's `sub == "eric@corp.example"`, `agentbus2_kind == "human"`, `typ == "at+jwt"`; `GET /v1/identities` with it as bearer succeeds and lists eric with `kind: human`; with `human_token_lifetime_seconds: 120`, `expires_in == 120`.
  - `TestTokenExchangeRefusals` (table; each body has the listed `error`): no `subject_token` / `invalid_request`; id_token type / `invalid_request`; `urn:x:unknown` type / `invalid_request`; `requested_token_type` refresh-token URN / `invalid_request`; `audience=https://other` / `invalid_target`; `resource=https://other` / `invalid_target`; `actor_token` / `invalid_request`; unknown `iss` / `invalid_request`; expired / `invalid_request`; wrong `aud` / `invalid_request`; tampered signature / `invalid_request`; no email / `invalid_request`; not in users / `access_denied` with no identity row created.
  - `TestTokenExchangeNeverLogsToken`: with a captured logger, run a success and three refusals; the log contains none of the subject tokens or the minted bearer.
  - `TestFederatedEndToEnd` (bus suite, both engines, transport-independent like `TestAuthEndToEnd`): before `cmd.Run`, `cmd.RunAdmin` adds `users {group corp pai-users}`, `admins {group corp pai-admins}` and `create-human kim@corp.example`; `cmd.Run` with `-listen 127.0.0.1:0` and `-set issuers=<json for the provider, name corp>`; an agent registers a key. Subtests: `exchange-then-send` (eric exchanges, sends; the agent receives `SenderKind == IdentityHuman`), `not-in-users-refused` (`CodeForbidden`, no row), `precreated-binds` (kim exchanges, `Created` path gives the existing identity), `admins-group-sets-flag` (eric with `pai-admins` succeeds at `Status`), `group-removed-clears-flag` (re-exchange without it, `Status` forbidden), `key-registration-on-human-refused` (`remote.Register` on eric -> forbidden), `direct-corporate-bearer-refused` (401). Seven leaves per engine.

- [ ] **Step 3: Run to verify failure** — `go test -run TestTokenExchange ./internal/server/` -> FAIL (`unsupported_grant_type`).

- [ ] **Step 4: Implement** `internal/oidctest`, the handler, the dispatch and `remote.TokenExchange`; docs; ADR addendum: the `access_denied` departure (RFC 8693 section 2.2.2 says `invalid_request` for a policy refusal; the spec chose `access_denied`), the id_token refusal and the single-valued `audience`/`resource`.

- [ ] **Step 5: Run** `make test && make test-conformance && bash scripts/m5-exit-checks.sh --deps-only` -> PASS; `TestFederatedEndToEnd` 7 leaves per engine on both transports.

- [ ] **Step 6: Commit** `feat(auth): RFC 8693 token exchange for humans; fake OIDC provider; federated end-to-end test (spec 5.2)`.

---

### Task 12: The browser sign-in client (`internal/oidcclient`)

**Files:**
- Create: `internal/oidcclient/oidcclient.go`, `internal/oidcclient/browser.go`, `internal/oidcclient/oidcclient_test.go`
- Modify: `internal/oidctest/provider.go` (authorize and token endpoints), `internal/oidctest/provider_test.go`

**Interfaces:**
- Consumes: Task 11 `oidctest.Provider`.
- Produces:
  - `type Config struct { Issuer, ClientID string; RedirectPort int; Scopes []string; HTTP *http.Client; OpenBrowser func(url string) error; Announce func(url string); Timeout time.Duration }` (`OpenBrowser` nil: `OpenBrowser`; `Announce` nil: nothing; `Timeout` 0: 5 min for the callback).
  - `type Tokens struct { AccessToken, RefreshToken string; ExpiresAt time.Time }`.
  - `func SignIn(ctx context.Context, c Config) (Tokens, error)`: GET `Issuer + "/.well-known/openid-configuration"` (needs `authorization_endpoint` and `token_endpoint`; plan choice: the document's `issuer` is not compared with `Issuer`, because OCI's documented issuer differs from the domain URL (fact check item 6) and the server verifies `iss`); verifier 32 random bytes, base64url; S256 challenge; 16-byte `state`; listen on `127.0.0.1:RedirectPort` (failure names the port); redirect URI `http://127.0.0.1:<port>/callback`; authorization URL with `response_type=code`, `client_id`, `redirect_uri`, `scope` (space-joined), `state`, `code_challenge`, `code_challenge_method=S256`; `Announce(url)` then `OpenBrowser(url)` (its error is announced, not fatal); the callback with a wrong `state` or an `error` parameter ends the sign-in with an error naming it; the code exchange POSTs `grant_type=authorization_code`, `code`, `redirect_uri`, `client_id`, `code_verifier`; the response needs `access_token` and `token_type` `Bearer` (case-insensitive). No token is logged or put in an error.
  - `func Refresh(ctx context.Context, c Config, refreshToken string) (Tokens, error)`; `var ErrRefreshRejected = errors.New("oidcclient: refresh token rejected")` for a 400 `invalid_grant`.
  - `func OpenBrowser(url string) error`: `open` (darwin), `rundll32 url.dll,FileProtocolHandler` (windows), `xdg-open` (others), started without a shell.
  - `oidctest.Options` gains `RefreshTokens bool`, `AuthorizeError string`, `Claims map[string]any` (the claims of tokens the token endpoint issues); the provider serves `/authorize` (requires the configured `client_id`, a loopback `redirect_uri`, `code_challenge_method=S256`; 302 to `redirect_uri?code=..&state=..`, or `?error=..&state=..`) and `/token` (`authorization_code`: checks `client_id`, `redirect_uri` and `code_verifier` against the stored challenge, single-use codes; `refresh_token` grant), and `func (p *Provider) Requests() []string` for assertions.

- [ ] **Step 1: Harness self-tests first** (Definition of Done C): `TestProviderAuthorizeRejectsPlainPKCE`, `TestProviderTokenChecksVerifier`, `TestProviderCodeSingleUse`, `TestProviderRefreshGrant`, `TestProviderAuthorizeError`.
- [ ] **Step 2: Write the failing client tests** (the `OpenBrowser` seam GETs the URL with a client that follows redirects to the loopback listener)
  - `TestSignInPKCE`: tokens returned; the provider saw `code_challenge_method=S256` and a verifier that hashes to the challenge; the callback page says the window may be closed.
  - `TestSignInStateMismatch`: the seam rewrites `state` -> error naming `state`.
  - `TestSignInProviderError`: `AuthorizeError: "access_denied"` -> error containing `access_denied`.
  - `TestSignInPortBusy`: the port already bound -> error naming the port.
  - `TestSignInTimeout`: a seam that never calls back with `Timeout: 200ms` -> error within 2 s, listener closed (the port can be bound again).
  - `TestSignInDiscoveryIncomplete`: no `token_endpoint` -> error naming it.
  - `TestRefresh`, `TestRefreshRejected` (`errors.Is(err, ErrRefreshRejected)`).
- [ ] **Step 3: Run to verify failure** — `go test ./internal/oidcclient/ ./internal/oidctest/` -> FAIL.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run** `make test && bash scripts/m6-exit-checks.sh --deps-only` -> PASS.
- [ ] **Step 6: Commit** `feat(oidcclient): browser sign-in with PKCE and loopback redirect (spec 5.3)`.

---

### Task 13: Human sign-in on the client (profile `oidc`, `HumanSource`, TUI and `login -human`)

**Files:**
- Create: `internal/credential/human.go`, `internal/credential/human_test.go`
- Modify: `internal/profile/config.go` (`OIDC`, `Profile.OIDC`, validation), `internal/credential/transport.go` (`TokenSource`), `internal/client/options.go` (`HumanSignIn`), `internal/client/server.go` (human path), `internal/client/conn.go` (`Conn.Human`), `internal/cli/tui.go`, `internal/cli/login.go`, `README.md`, `docs/install.md`, `docs/tui.md`, `CLAUDE.md` (tui, login, `internal/profile`, `internal/credential` lines)
- Test: `internal/profile/config_test.go`, `internal/client/server_test.go`, `internal/client/resolve_test.go`, `internal/cli/tui_test.go`, `internal/cli/login_test.go`

**Interfaces:**
- Consumes: Task 11 `remote.TokenExchange`, `oidctest`; Task 12 `oidcclient.Config`, `SignIn`, `Refresh`, `ErrRefreshRejected`.
- Produces:
  - `profile.OIDC{ Issuer string json:"issuer"; ClientID string json:"client_id"; RedirectPort int json:"redirect_port"; Scopes []string json:"scopes" }`; `Profile.OIDC *OIDC json:"oidc,omitempty"`. Validation: server mode only; `issuer` an absolute http(s) URL; `client_id` non-empty; `redirect_port` 1 to 65535 (Watch item 2: required); `scopes` non-empty, no empty or whitespace-containing entry.
  - `credential.TokenSource interface { Token(ctx context.Context) (string, error); Refresh(ctx context.Context) (string, error) }`; `func Transport(base http.RoundTripper, s TokenSource) http.RoundTripper`.
  - `credential.HumanSession{ Server, Identity, AccessToken string; ExpiresAt time.Time; Discovery Discovery; ProviderRefreshToken string }`; `credential.HumanCache{ Dir string }` at `<Dir>/human/session.json` (file 0600, directory 0700; `<Dir>` is today's `<data dir>/tokens/<profile>`, so it never collides with an `<identity>.json`) with `Load() (HumanSession, bool, error)`, `Save(HumanSession) error`, `Remove() error`.
  - `credential.HumanSource{ Server string; OIDC oidcclient.Config; Client *gen.ClientWithResponses; HTTP *http.Client; Cache HumanCache; Now func() time.Time }` with `Token(ctx)` (cached bearer valid beyond `Skew`; else `Refresh`), `Refresh(ctx)` (provider refresh token when present, then `TokenExchange`; `ErrRefreshRejected` or no refresh token falls to `SignIn`), `SignIn(ctx)` (always the browser), `Identity() string` (the unverified `sub` of the cached bearer; empty `sub` is an error at save), `SetAnnounce(func(string))` (mutex-guarded), `Editor() gen.RequestEditorFn`. A cache for another server is a miss.
  - `client.Options.HumanSignIn bool`: `Resolve` requires a server profile with `OIDC` (`ResolveError` "profile %q has no oidc setting: on a server profile a human signs in through the corporate provider") and skips identity selection (`IdentitySource` `"sign-in"`); `openServer` builds a `HumanSource`, calls `Token`, sets `Resolved.Identity` from `Identity()`; `Conn.Human *credential.HumanSource`.
  - `RunTUI`: on a server profile sets `HumanSignIn`, no `FallbackIdentity`, and refuses `-identity` with exit 2 ("-identity does not apply on a server profile: the identity comes from sign-in"); the browser URL is announced on stderr before the TUI starts and silenced (`SetAnnounce(nil)`) once it runs; local profiles unchanged (Decision 11).
  - `RunLogin`: `-human` flag; needs a server profile with `oidc` (exit 2 naming `oidc` otherwise) and refuses `-identity` (exit 2); runs `SignIn`; prints `logged in as <identity> at <server> (token expires <RFC 3339>)`. Test seam: `var testOpenBrowser func(string) error` in `internal/cli`.

- [ ] **Step 1: Write the failing tests**
  - `TestOIDCValidate` (profile, table): local mode with `oidc` refused; port 0 and 65536 refused; empty scopes; scope `"a b"`; missing client_id; non-URL issuer.
  - `TestHumanSourceCachedToken` (no provider or server call), `TestHumanSourceRefreshTokenPath` (expired bearer plus refresh token: no browser call, one exchange), `TestHumanSourceBrowserWhenNoRefreshToken`, `TestHumanSourceRefreshRejectedFallsBackToBrowser`, `TestHumanSourceIdentityFromSub`, `TestHumanSourceIgnoresOtherServer`, `TestHumanCacheModes` (unix: 0600 file, 0700 dir; Windows: the existing ACL helper).
  - `TestResolveHumanSignInNeedsOIDC`, `TestOpenServerHumanSignIn` (client, against a server with the fake provider).
  - `TestRunTUIServerProfile` (rewritten, same name): a server with the provider as issuer `corp` and `users {email corp eric@corp.example}`; profile `lab` with `oidc`; the TUI signs in through `testOpenBrowser`; its identity is `eric@corp.example`; `tokens/lab/human/session.json` exists; `driveSend` with `sam` (an agent with a key) works and `sam` sees `SenderKind == IdentityHuman`.
  - `TestRunTUIServerIdentityFlagRefused`, `TestRunTUIServerNoOIDC` (both exit 2, message names the cause).
  - `TestLoginHuman` (prints `logged in as eric@corp.example`), `TestLoginHumanNoOIDC` (exit 2).
- [ ] **Step 2: Run to verify failure** — `go test ./internal/profile/ ./internal/credential/ ./internal/client/ && go test -run 'TestRunTUI|TestLoginHuman' ./internal/cli/` -> FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run**

Run: `make test && go test -count=1 -v -run '^(TestBothModes|TestNoInboxView|TestRunTUI.*)$' ./internal/tui/ ./internal/cli/ > /tmp/tui.log; bash scripts/m8-exit-checks.sh /tmp/tui.log && bash scripts/m6-exit-checks.sh --deps-only`
Expected: PASS; m8 counts unchanged (`TestRunTUILocalProfile` and `TestRunTUIServerProfile` each pass once).

- [ ] **Step 5: Commit** `feat(client): humans sign in through the corporate provider on server profiles (spec 5.3, Decision 11)`.

---

### Task 14: Standards audit, documentation sweep and the done gate

**Files:**
- Modify: `docs/adr/0002-authentication-and-credentials.md` (addendum: audit list), `CLAUDE.md` (schema version 5 line; `internal/oidcclient` and `internal/oidctest` lines and their import rules; host commands; `tui`, `login`, `internal/profile`, `internal/credential`, Decision 16/11 notes on the TUI identity), `docs/ops/upgrade.md` (schema 5 migration note), any doc `rg -n 'make-admin|openid-configuration|subjects|Decision 84' docs README.md CLAUDE.md` still finds outside history

- [ ] **Step 1: Audit (spec 8.7).** Read RFC 6749 (sections 2.3, 3.2, 4.4, 5), 7521, 7523, 8414, 8693, 9068 and OpenID Connect Discovery against `/v1/auth/token`, `/v1/auth/jwks` and both well-known documents; record each remaining departure in the addendum with its reason. At least: `access_denied` at the token endpoint (Task 11); the token endpoint URL accepted as assertion `aud` (Task 7); `/.well-known/openid-configuration` kept though agentbus2 is not an OpenID provider; `audience`/`resource` single-valued; `none` listed for the exchange's public client; 413 from the body limit answered as problem+json before routing.
- [ ] **Step 2: Docs sweep** with the `rg` above; fix every stale hit (history files under `docs/superpowers/` excepted).
- [ ] **Step 3: Done gate**

Run: `make verify`
Expected: ends with `verify: OK` (build, build-all, vet, fmt-check, lint, tidy-check, generate-check, unit tests, both conformance suites on SQLite and Postgres through both transports, check-gates).

Then: `make check-gates-selftest` (blocks 14, 15 and 21 still fail their named `TestAuthEndToEnd`/`TestReplicasWake` leaves) and `make docker-smoke` (the smoke now uses `admins add`).
Expected: both exit 0.

- [ ] **Step 4: Commit** `docs: federated identity standards audit and documentation sweep (spec 8.7)`; report the `make verify` output tail. CI (`ci.yml`, `windows.yml`) green on the pushed commit is the last condition and needs Eric's push.
