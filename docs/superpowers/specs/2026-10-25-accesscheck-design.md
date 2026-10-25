# Access check design (one `accesscheck(principal, object, action)` for every access decision)

Status: Agent design of 2026-10-25 for Eric's decisions of the same day.
Section 1 records his two framing decisions; section 4 records his
answers to the twelve questions the first draft raised and section 4.1
his rulings on the scope edges, each labeled **Eric's decision
2026-10-25**; section 4.2 is the one item still needing him (a create or
rename that collides with an out-of-scope name). Nothing else in this
document is a decision. The implementation plan is
`docs/superpowers/plans/2026-10-25-accesscheck.md`.

Related: `docs/superpowers/specs/2026-10-19-agentbus2-design.md` sections
4.6 and 4.6.1 (Decisions 8, 24, 59, 73, 80, 88, 89, 93, 94, 95, 96; the
operation-to-permission table), 3.1 (Decision 43, discovery capabilities);
ADR 0008 (channel ACLs: the stored and the implicit entries), ADR 0009
(scopes, scoped identities, the Slack bridge's Decisions 9, 11, 12, 17, 18,
19, 20), ADR 0003 (sessions), ADR 0010 (health tiers);
`docs/superpowers/specs/2026-10-25-channel-visibility-design.md` and its
plan `docs/superpowers/plans/2026-10-25-channel-visibility.md` (commit
0333662), which this design precedes and which is rebuilt on it (section
3.10); `scripts/check-gates-selftest.sh` (block 15, the administrator gate
anchor); `internal/conformance/bus/aclmatrix_test.go` and
`scripts/m3-exit-counts.sh` (the ACL matrix).

## 1. Eric's decisions (2026-10-25)

1. **Eric's decision 2026-10-25 (accesscheck 1): one access check.** "The
   right way to check if someone has access to something is
   accesscheck(principal, object). We should NOT have multiple different
   pieces of code each attempting to calculate this on their own. We need
   to implement a single accesscheck method, centralize all our business
   rules (admin, owner, etc.) into that piece of code, and use that
   everywhere."
2. **Eric's decision 2026-10-25 (accesscheck 2): this lands before the
   channel-visibility work.** The channel-visibility design and plan
   (commit 0333662) are rebuilt on the access check afterwards. Their
   proposed `Tx.VisibleChannels` query, a separate SQL statement of the
   rule "any ACL entry lists the channel; administrators see everything",
   is exactly what decision 1 forbids: that rule is one more action of
   the single access check (section 3.10), not a Store query of its own.

Section 4 holds twelve more decisions of the same day (questions 1 to 12),
which change the rule table itself: no task owner guard (1 and 12), no
implicit creator entry (6), scope enforced by the access check (11), the
agent memory filter inside the access check (4), `unavailable` for an
unconfigured bridge (7).

## Context: protocol versioning (Eric's decision 2026-10-25, not implemented here)

Recorded for the rebuilt channel-visibility plan (section 3.10); this
design changes no wire field.

- **Eric's decision 2026-10-25 (protocol versioning).** The OpenAPI
  `info.version` becomes the protocol version,
  semver-shaped and decoupled from the build version: the major is the
  `/v1` path, the minor is bumped on every addition, the patch on a
  documentation-only change. `GET /v1/discover` and `GET /v1/health`
  expose `protocol_version` beside the build version. New additions no
  longer get a discovery capability string; the existing `long_poll`,
  `semantic_search` and `health` stay. This supersedes Decision 43's
  "additions get a capability" for future additions (the pending
  `channel_capabilities` name of the visibility spec, section 5, is
  therefore moot: that addition is a minor bump).

## 2. What exists today (checked against source at 0333662, 2026-10-25)

Appendix A is the full inventory, one row per site that computes or
restates an access decision, with the rule each encodes. This section is
the summary.

### 2.1 Where the rule is computed

The channel ACL rule of Decision 80 (the union of the stored rows for the
identity, `everyone` and, for an administrator, `admins`, plus the implicit
`admin` of the creator and of every administrator) is written **twice in
SQL**, in `internal/store/sqlstore/acl.go`: `EffectivePermissions`
(:139, the point form for one channel) and `ReadableChannels` (:178, the
set form for `read`, a different statement that must agree with the first
by hand). The channel-visibility plan adds a third statement
(`VisibleChannels`). `isAdmin` (:123) is a third SQL reading of the
administrator flag, used by both.

Over those, `internal/core/authz.go` has four entry points: `Authorize`
(:18, permission on a channel), `RequireIdentity` (:60, registered and not
reserved), `RequireAdmin` (:75, the administrator flag; its line 80
`if !id.Admin {` is the mutation anchor of `check-gates-selftest.sh` block
15) and `ReadableChannelIDs` (:100). Twenty-four call sites in
`internal/core/...` use them (Appendix A rows C5 to C31), and
`internal/slackbridge/service.go:162` calls `RequireAdmin` for every bridge
operation.

Outside those entry points, **six more places decide access on their
own**:

- `internal/core/health/health.go:68` and `:111` read `id.Admin` directly
  to pick the health tier (the health plan avoided `RequireAdmin` to keep
  the gate anchor untouched).
- `internal/slackbridge/inbound.go:199-241` (`trust`) decides "this human
  may post through the bridge" by scanning the stored ACL rows for an
  explicit `write` entry naming the identity (Decision 11), plus the human
  kind and the Slack binding (Decision 18): a second implementation of
  "may write", by stored rows rather than effective permissions.
- `internal/slackbridge/create.go:252` (`writeHumans`) decides who is
  invited to the Slack channel by the same stored-row scan: a third.
- `internal/core/tasks/update.go:32` (`ownerGuardBlocked`): a task in
  progress, or owned, may be changed only by its owner (or by `Force`);
  the denial is `conflict`.
- Session ownership ("this session belongs to the caller, else
  `not_found`") is written five times: `sessions/service.go:77`,
  `sessions/open.go:82`, `sessions/close.go:30`, `delivery/watch.go:100`,
  `delivery/peek_counts.go:29`.
- Two client-side mirrors: `internal/tui/tasks.go:66` (`taskEditable`:
  unowned or own) restates the owner guard without `Force`;
  `internal/tui/model.go:1363-1394` (`statusCmd`) decides from the
  `Identities` listing's `Admin` flag whether to call `Status` at all.

### 2.2 Inconsistencies found

- **I1. Two SQL statements for one rule.** `EffectivePermissions` and
  `ReadableChannels` agree only by inspection; the agreement is tested
  nowhere (`TestACLReadableChannels` tests the set form alone). The
  visibility plan would make it three.
- **I2. The administrator flag is read four ways**: `RequireAdmin`
  (`id.Admin`), the SQL `isAdmin` and the `EXISTS (... is_admin = TRUE)`
  subquery of `ReadableChannels`, `health.go:68` (`id.Admin` again), and
  the TUI's `statusCmd` (from the listing).
- **I3. "May write" has two definitions.** The bus uses effective
  permissions (stored rows for the identity, `everyone`, `admins`); the
  Slack bridge uses stored rows naming the identity. They coincide today
  only because Decision 12 forbids `everyone`/`admins` `read`/`write` on a
  bridged channel; nothing checks that they stay equal.
- **I4. Denial codes differ by object**: channel and server-admin denials
  are `forbidden`; a session not owned is `not_found`; a task owned by
  another is `conflict`; a reserved or unknown principal is
  `not_registered`; an unconfigured bridge is `forbidden`. Each code is
  chosen at its site; no one place lists them.
- **I5. The principal is resolved redundantly or not at all.**
  `channels.mutate`/`read` call `RequireIdentity`, then `Authorize`
  re-checks the reserved names and `ReadableChannelIDs` re-checks them
  again. `Receive`, `Peek` and `Watch` never call `RequireIdentity`: they
  trust `Sessions.Require` (a session exists only for a registered
  identity); an identity row deleted while its session survives is
  evaluated by the SQL as a non-administrator with its stored rows, and a
  malformed name is bound to `everyone` (`badText`, `acl.go:159, 179`).
  That binding is a policy statement living in the Store.
- **I6. The implicit creator entry is stated in two places** with two
  meanings: `EffectivePermissions` grants the creator `admin`; the
  visibility plan's `VisibleChannels` would treat creator-ship as "listed".
  Both are ADR 0008's one rule, but each is a separate SQL clause.
- **I7. `Create`'s two administrator rules** (Decision 93 when creation is
  `admins`; ADR 0009 decision 9 when naming a scope) each call
  `RequireAdmin` and the second rewrites the message
  (`channels.go:74-80`); `SetACL` on a bridged channel does the same
  (`channels.go:244-250`). Three copies of "server administrator or
  `forbidden` with a site-specific text".
- **I8. The health tier duplicates `RequireAdmin`** for a test-anchor
  reason, not a design reason.
- **I9. The TUI's `taskEditable` omits `Force`** and so disagrees with the
  server's guard; the TUI's admin pre-check means a demoted administrator
  keeps the admin view until the next tick, and a promoted one gets no
  `Status` until then.
- **I10. `Authorize` panics on an unknown permission** (`authz.go:22`);
  Definition of Done B wants an `internal` error and a test.

### 2.3 What is not an access decision (and stays where it is)

Listed so the gate of section 3.7 is scoped on purpose, not by omission:

- ACL validity rules: Decision 96 (`sqlstore/acl.go:25`, an entry names a
  registered identity), `store.ValidateEntry`, `scopes.CheckBridgeACL`
  (Decision 12, the shape a bridged channel's ACL may take). They judge
  the ACL, not a principal; they stay in the Store and in `scopes`.
- State rules: a live bridge pins the channel (`channels.go:113-137`,
  `conflict`), `general` cannot be deleted or renamed (`:139`), a scope
  must be open to join (`scopes.RequireOpen`), the task status machine.
  None depends on who asks.
- Authentication and enrollment policy: `identities/register.go:97-170`
  (a human has no key, a scoped identity registers only with its bound
  token, a keyless administrator needs a token, the registration mode)
  and `SignInHuman` (`human.go:253, 287-296`: the `users` list admits, the
  `admins` list sets the flag). These decide who becomes a principal and
  with which flag; the access check consumes the result (Eric's answer to
  question 5: they stay in `identities`).
- `agentview` and its call sites (decision 20: agents never receive memory
  channels) were listed here in the first draft as presentation; Eric's
  answer to question 4 moves the rule into the access check (the
  `Deliver` action), so it is **not** outside the gate; the TUI is
  unfiltered because its identity is human, not because it is a client.
- Retention: `EmptyChannelsBefore`'s `slack-bridge` exclusion (ADR 0009
  decision 20) and `RetireIdentity`'s removal of ACL rows (decision 19)
  mutate or weigh ACL data; they decide nothing for a caller.
- Feature absence: `BridgeNotConfigured` is `forbidden` because the bridge
  is not wired, whoever asks (section 4 question 7).

## 3. Design

### 3.1 Vocabulary

- **Principal**: who asks. `access.Principal` is the identity row as the
  Store holds it: `Name`, `Kind` (`agent` or `human`), `Admin`, `Scope`
  (`""` for an unscoped identity; every human is unscoped).
  `access.Resolve(ctx, tx, name)` builds it: `""`, `everyone`, `admins`
  and `slack-bridge` and an unknown name are `not_registered` (today's
  `RequireIdentity`, moved). `access.Anonymous` is the unauthenticated
  caller (the health probe tier) and holds nothing.
- **Object**: what is asked about. One Go type per kind, each carrying
  only what the rule needs: `Channel{ID}` (facts loaded inside the check:
  the channel row with its kind and scope, the stored ACL rows), `Server`
  (the server-wide administrator operations: status, usage, scopes,
  identities, bridges, reset, reset-credential, creation when
  `channel_creation` is `admins`, naming a scope on creation, changing the
  ACL of a bridged channel), `Session{ID}` (facts: the owning identity),
  `Identity{Name}` (facts: its scope; for the `Identities` listing). Tasks
  and memories are messages on a channel and are checked as that channel
  (Eric's decision, question 1: no task object).
- **Action**: `Read`, `Write`, `Admin` on a channel (Decision 80's three
  discrete sets); `List` on a channel (the channel-visibility rule, section
  3.10); `Deliver` on a channel (`Read`, minus memory channels for an agent
  principal: decision 20, moved in by Eric's answer to question 4; used by
  Subscribe, Receive, Peek, Watch and so the Stop hook and `wait`);
  `Post` on a channel for the Slack bridge (`Write` held by a human,
  Decision 11); `Administer` and `CreateChannel` on the server; `Use` on a
  session; `See` on an identity. An action that is not defined for the
  object's kind is an `internal` error, never a panic (Definition of Done
  B; fixes I10).
- **Decision**: a value `{Allowed, Code, Text, Reason}` (Eric's answer to
  question 8). The code is declared once, in the access package's table
  (section 3.3), so I4 becomes a list in one file rather than a scatter:
  channel and server denials are `forbidden`; a session that is not the
  caller's is `not_found` (Eric's answer to question 2); an object outside
  a scoped principal's scope is `not_found` with the unknown-object text
  (Eric's decision 4.1 (d)); an agent
  subscribing to a memory channel keeps today's `validation` with its
  explanatory text. `Require` turns a denial into the `*bus.Error`.

### 3.2 The rule

One pure function, the only place the business rules live:

```
decide(p Principal, o facts, a Action) (ok bool)
```

with the channel facts being the channel row (kind, scope) and the stored
ACL rows of the channel for the names `p.Name`, `everyone` and, when
`p.Admin`, `admins`. The table it implements, transcribed from ADR 0008
and ADR 0009 and the section 4.6 table **as amended by Eric's answers in
section 4** (questions 1, 4, 6, 11):

**Scope gate (Eric's decision, question 11), applied first to every row
below.** `inScope(p, o)` is true when `p.Scope == ""` (an unscoped
principal is never restricted), and otherwise only when the object is in
`p.Scope`: a channel when `channel.scope == p.Scope` (an unscoped channel,
`general` included, is outside every scope); an identity when its scope is
`p.Scope`; the server never for `Administer` (the server is outside every
scope) but always for `CreateChannel` (the new channel takes `p.Scope`,
ADR 0009 decision 8, so creation is inside the scope by construction; the
one exemption of the gate); a session always (a session is the caller's
own or `not_found`). A denial by the scope gate is `not_found` with the
object kind's own not-found text (`channel %q does not exist`, `memory %d
does not exist`, the task text, `identity %q is not registered`), Reason
`out_of_scope`: a scoped identity cannot learn what exists outside its
scope (Eric's decision, section 4.1 (d)); in the set forms the object is
simply absent; a server-level operation answers what every
non-administrator gets. Inside its scope a scoped principal is governed by
the ACL rows like anyone else. The one answer that still discloses an
out-of-scope name, a create or rename `conflict`, is section 4.2.

| Object | Action | Allowed when (after the scope gate) |
| --- | --- | --- |
| Channel | Read, Write | the union of the stored rows for `p.Name`, `everyone`, and `admins` if `p.Admin`, holds that permission (Decision 80) |
| Channel | Admin | that union holds `admin`, or `p.Admin` (the implicit `admins` entry; ADR 0008 decision 1 as amended by Eric's answer to question 6: there is no implicit creator entry any more) |
| Channel | List | **interim, until the channel-visibility rebuild**: every channel for an unscoped principal (Decision 94 stands until that plan lands), so only the scope gate filters a scoped principal's list now; the rebuild replaces this clause with `p.Admin`, or any stored row for `p.Name`/`everyone`/`admins`-if-admin (Eric's visibility decision 1 as amended by question 6: creator-ship lists nothing) |
| Channel | Deliver | Channel/Read, and not (`p.Kind == agent` and `channel.kind == memory`) (decision 20 moved in by Eric's answer to question 4; Reason `memory_channel`, code `validation`, today's text) |
| Channel | Post (bridge) | `p.Kind == human` and Channel/Write (ADR 0009 decision 11; Reasons `not_human`, `no_write`) |
| Server | Administer | `p.Admin` (Decision 73; ADR 0009 decision 9); never for a scoped principal (scope gate) |
| Server | CreateChannel | `ChannelCreation == open`, or `p.Admin` (Decision 93; the one configuration value the rule reads); a scoped principal creates in its own scope only (naming any scope is Administer) |
| Session | Use | the session's identity is `p.Name` (ADR 0003; the five sites of 2.1; `not_found`) |
| Identity | See | the scope gate alone (the `Identities` listing; an unscoped principal sees every identity) |

The implicit `admins` entry grants `admin` only, never `read` or `write`
(ADR 0008; an administrator grants itself `read` through `SetACL`, which
is what Eric's answer to question 6 names). There is no task row: write
access to a task list permits changing any task on it (Eric's answers to
questions 1 and 12; section 3.9 lists what that removes).

### 3.3 The API (`internal/core/access`)

```go
package access

type Principal struct{ Name string; Kind store.IdentityKind; Admin bool; Scope string }
var Anonymous Principal // the probe tier; denied every action

func Resolve(ctx context.Context, tx store.Tx, name string) (Principal, error) // not_registered

type Action string
const (
    Read Action = "read"; Write Action = "write"; Admin Action = "admin"; List Action = "list"
    Deliver Action = "deliver"      // channel: Read, minus memory channels for an agent (decision 20)
    Post Action = "post"            // channel: Write held by a human (bridge)
    Administer Action = "administer"       // server
    CreateChannel Action = "create_channel" // server (Decision 93)
    Use Action = "use"              // session
    See Action = "see"              // identity (the Identities listing)
)

type Object interface{ accessObject() }
type Channel struct{ ID int64 }
type Server struct{}
type Session struct{ ID string }
type Identity struct{ Name string }

// Decision is the answer (Eric's decision, question 8): Reason names the
// denial (out_of_scope, not_human, no_write, memory_channel, no_permission,
// not_admin, not_owner) so a caller can map it to a fixed text without
// parsing Text.
type Decision struct{ Allowed bool; Code bus.Code; Text, Reason string }

// Checker holds the one configuration the rule reads (ChannelCreation,
// Decision 93) and nothing else.
type Checker struct{ Cfg Config }

// Check loads the object's facts in tx and decides. It never panics: an
// undefined (object, action) pair is an internal error.
func (c Checker) Check(ctx context.Context, tx store.Tx, p Principal, o Object, a Action) (Decision, error)
// Require is Check, returning the denial as the object kind's *bus.Error.
func (c Checker) Require(ctx context.Context, tx store.Tx, p Principal, o Object, a Action) error
// Channels is the set form: the ids of every channel for which Check
// would allow a, ascending. Used by Receive, Peek, Watch (Deliver),
// Search (Read) and List (List) (section 3.4).
func (c Checker) Channels(ctx context.Context, tx store.Tx, p Principal, a Action) ([]int64, error)
// Identities is the set form over identities: those p may See (the scope
// gate), by name.
func (c Checker) Identities(ctx context.Context, tx store.Tx, p Principal) ([]store.Identity, error)
// Holders is the reverse set form: the registered principals for whom
// Check allows a on the channel, by name (the bridge's invite list,
// Decision 11; everyone and admins are not principals and never appear).
func (c Checker) Holders(ctx context.Context, tx store.Tx, o Channel, a Action) ([]Principal, error)
```

`Require` builds the text as `Authorize` does today (`"%s needs %s on channel
%q"`, `"administrator privilege required"`, `"session %q does not exist"`),
so the ACL matrix and the e2e assertions on codes are unchanged; sites
that today rewrite the text (`channels.go:74-80, 244-250`) pass their text
through a `Require` option rather than catching and re-raising.

`core.Core` gains `Access access.Checker` (built in `core.New` from the
config); aggregates call `s.Access.Require(...)`. `core.Authorize`,
`RequireAdmin`, `RequireIdentity` and `ReadableChannelIDs` are deleted,
not forwarded: a forwarder compiles but gives the gate of 3.7 a second
name to allow.

### 3.4 The set form without a second rule

The set form must stay cheap (Receive runs on every poll) and must not
restate the rule in SQL. Three options were weighed:

- **A. The Go function is the only rule; the Store serves facts.** The
  Store gets one policy-free query, `Tx.ACLRowsFor(ctx, names
  ...string) ([]ACLRow, error)`: the `channel_acl` rows whose `identity`
  is one of the names, with channel id and the three flags, no `is_admin`
  join, no `creator` clause, no `can_read = TRUE` filter. `Channels` runs
  `ListChannels` (already carrying `creator`) and `ACLRowsFor(p.Name,
  everyone[, admins])`, groups the rows by channel and calls `decide` per
  channel. `Check` on one channel calls the existing `tx.Channel` and
  `tx.ACL`; the scope gate and the memory-kind test read the channel row
  that is already loaded. Cost: `channel_acl` holds about three rows per channel and
  `ReadableChannels` already returned a list of all readable ids, so the
  order of work is unchanged; the only index is `(channel_id, identity)`
  (`schema/0001_initial.sql:38`), so `WHERE identity IN (...)` scans the
  table, which is trivial at hundreds of channels. An `(identity)` index is
  a later schema item if a deployment ever shows it; it is not part of
  this work.
- **B. One decision table rendered twice**, to a SQL predicate builder and
  to the Go evaluator. Pushes the filter into the engine, but two renderers
  of one table are two implementations with a generator between them, for a
  table of seven rows over three columns; the agreement test of A would be
  needed anyway. Rejected.
- **C. SQL stays the rule and the point check calls the set form.** Only
  the `read` set exists; `write` and `admin` would need their own
  statements; a rule in SQL is untestable in isolation and lives in the
  storage layer, which Eric's decision places it outside of. Rejected.

**Recommendation: A.** The agreement that today rests on inspection
becomes a test (3.7): for random ACLs on both engines, `Channels(p, a)`
equals `{c : Check(p, c, a)}` for every action.

### 3.5 Store contract changes

- Removed from `store.Channels` (`internal/store/channels.go:68-74`):
  `EffectivePermissions`, `ReadableChannels`. Their only non-test callers
  are `core.Authorize` and `core.ReadableChannelIDs`. Removal, not
  deprecation: a method left on the contract is a second rule anyone can
  call, and the gate would have to forbid it anyway (question 9).
- Added: `ACLRowsFor(ctx, names ...string) ([]ACLRow, error)`, `ACLRow
  {ChannelID int64; Identity string; Read, Write, Admin bool}`, in
  `(channel_id, identity)` order; a name failing `TextRule` matches nothing
  (no `everyone` binding). `tx.ACL` and `tx.Channel` are unchanged.
- `sqlstore/acl.go` loses `isAdmin`, `EffectivePermissions`,
  `ReadableChannels` (and the `badText → everyone` policy with them).
- Store suite: `TestACLReadableChannels` (`acl_test.go:296`) goes;
  `TestACLImplicitEntriesGrantAdmin` (:105),
  `TestACLDuplicateEntriesAreAdditive` (:128) and
  `TestACLRemovingAdminsAndCreatorRowsKeepsImplicitAdmin` (:267) move to
  the access package's tests if they call `EffectivePermissions` (the plan
  checks); `TestACLRowsFor` is new on both engines. Schema: no change.
- ADR 0004 gains an addendum naming the change and its reason (the rule
  is the core's; the Store serves facts).

### 3.6 Where it lives and the import rules

`internal/core/access` is a package of `internal/core`, so the Milestone
import rules already cover it: it imports `internal/store` (types and
`Tx`) and `internal/bus` (codes and `Errorf`) and nothing else of this
module; never `internal/gen`, `internal/server`, `internal/bus/local` or
`internal/bus/remote` (`scripts/m4-exit-checks.sh` rule, which walks
`internal/core/...`). `internal/store/...` still never imports
`internal/core/...` (m7). The package does not import `internal/core`
itself (the root would then depend on it cyclically); it has its own
`bus.Errorf` calls with the retryable rule `core.E` applies (never
retryable for `forbidden`, `not_found`, `conflict`, `not_registered`,
`internal`).

Callers: every aggregate under `internal/core/...`, `internal/core/health`
(the tier), `internal/slackbridge` (the guard, `trust`, the invite list).
`internal/server`, `internal/bus/local`, `internal/bus/remote`,
`internal/mcpserver`, `internal/cli`, `internal/tui` never import it:
the Bus is the client seam and already carries the decision as an error
code.

### 3.7 The regression net and the gates

1. **The ACL matrix** (`TestACLMatrix`, 15 rows, 26 sub-cases, 52 leaves
   per engine; `scripts/m3-exit-counts.sh`) is untouched by this work and
   is the proof that the moved rule is the same rule. The visibility
   rebuild later changes the `list channels` row as its plan already says.
2. **The administrator gate anchor moves.** `check-gates-selftest.sh`
   block 15 mutates `internal/core/authz.go` at `if !id.Admin {` and
   expects `TestAuthEndToEnd/sqlite/admin-refused-as-non-admin` to fail.
   The literal moves to the one file that decides (`internal/core/access/
   decide.go`, the `Server`/`Administer` clause, written as
   `if !p.Admin {` and present exactly once in that file), and block 15's
   file and anchor text change in the same commit; `make
   check-gates-selftest` runs in that task. This supersedes the health
   plan's constraint "never edit `RequireAdmin`" (that constraint
   protected the anchor, and the anchor now guards the single rule, which
   is what it was for). Blocks 1 to 14 and 16 onward are unchanged; in
   particular block 8's anchor `if !readable[sub.ChannelID] {` in
   `internal/core/delivery/receive.go` stays exactly once: `load` keeps
   its `readable` set, now filled by `Access.Channels(p, Read)`.
3. **The agreement test** (`TestChannelsAgreesWithCheck`, both engines):
   random identities (admin or not), channels (with and without creator,
   with random stored rows over the identity, `everyone`, `admins`, other
   names), for each action `Channels(p, a)` equals the ids where `Check`
   allows. This replaces the by-inspection agreement of I1.
4. **The no-other-rule gate** (`TestAccessRuleSites`, go/ast, in the
   access package, the pattern of `TestPutPublicKeyCallers`): over the
   non-test files of `internal/core/...`, `internal/server/...`,
   `internal/slackbridge/...` and `internal/bus/local` (the server side;
   clients are excluded by design, question 3):
   - no `if`, `switch` case or `&&`/`||` operand mentions a selector
     named `Admin`, `Creator`, `Owner` or `Scope`, or compares a `Kind` to
     `store.IdentityHuman`/`IdentityAgent` or to `store.KindMemory`/
     `bus.KindMemory`, outside `internal/core/access`; the allowlist holds
     the sites that **copy** the flag or the scope (`sessions/identities.go
     Identities`, the converters, `channels.Create`'s `scope :=
     p.Scope`), the sites that **set** it (`identities/human.go
     signInHumanTx`, `identities/admin.go MakeAdmin`), the state rules that
     read a scope's own row (`scopes.RequireOpen`, `scopes/retire.go`,
     `identities/agent.go ensureAgent`, `slackbridge/create.go` "a bridge
     requires a scoped channel"), the task-list and memory **kind**
     validations that refuse `send` on a task list and `edit_memory` on a
     task list (`messaging/send.go`, `memories.go`, `tasks/*`: they judge
     the operation's shape, not a principal), and a `Kind` test in
     registration and sign-in (`register.go`, `human.go`, `agent.go`,
     question 5), all by function name. For the memory filter (Eric's
     answer to question 4) the `KindMemory` clause also walks
     `internal/mcpserver` and `internal/cli`: after Task 4 of the plan no
     client decides what an agent is delivered (`internal/agentview` is
     deleted); `internal/tui` is allowlisted (rail sections are
     presentation);
   - `tx.ACL(` is called only by `access`, `channels.Service.ACL` (the
     get-ACL display) and `slackbridge/archive.go Remove`,
     `slackbridge/create.go` step 4 (the two ACL mutations), named
     exactly like the PutPublicKey gate names `registerTx`;
   - no non-test file under `internal/store/sqlstore` contains the SQL
     fragments `is_admin = TRUE`, `can_read = TRUE`, `can_write = TRUE`,
     `can_admin = TRUE` or `creator = ?` (a grep in the same test over the
     string literals of the parsed files);
   - `access` itself is the only package that names `store.PermRead`,
     `PermWrite`, `PermAdmin` in a condition (the converters and
     `CheckBridgeACL`, which judges an ACL's shape, are allowlisted).
   The gate's allowlist is a literal in the test, so adding a site is a
   visible edit with a review question attached.
5. **Unit tests of `decide`**: a table over the rule of 3.2, including the
   implicit entries granting `admin` only, `Anonymous` denied everything,
   an undefined pair returning `internal`, and a reserved name never
   reaching `decide` (refused by `Resolve`).

### 3.8 Call sites after the change

| Site today | After |
| --- | --- |
| `channels.mutate`/`read` `RequireIdentity` | `access.Resolve`; the `Principal` is passed into `fn` so no site resolves twice (I5) |
| `Authorize(read\|write\|admin)` in channels, subscriptions, messaging, memories, tasks, search, sessions (24 sites) | `Access.Require(p, Channel{ID}, Read\|Write\|Admin)` |
| `Create`: creation `admins` (Decision 93), naming a scope (ADR 0009 d9) | `Access.Require(p, Server{}, CreateChannel)` unconditionally (the configuration is read inside the rule), then `Require(p, Server{}, Administer)` with the scope-naming text when a scope is named |
| `SetACL` on a bridged channel | `Access.Require(p, Server{}, Administer)` with its text; `CheckBridgeACL` unchanged |
| `Status`, `Usage`, scopes (4), identities `As` operations (4), `slackbridge.requireAdmin` | `Access.Require(p, Server{}, Administer)` |
| `health.go:68, :111` | `Access.Check(p, Server{}, Administer).Allowed` picks the tier; `Anonymous` is the probe tier |
| `ReadableChannelIDs` in `delivery.load` (Receive, Peek, Watch) | `Access.Channels(p, Deliver)` (readable, minus memory channels for an agent); the principal is `Resolve(identity)`, the caller's identity, resolved once per call before any session lookup (I5: a deleted identity is `not_registered`) |
| `ReadableChannelIDs` in `search` | `Access.Channels(p, Read)` (an agent still searches memories) |
| `channels.Subscribe` `Authorize(read)` | `Require(p, Channel{ID}, Deliver)`: an agent subscribing to a memory channel is refused by the server with today's `validation` text; the MCP and CLI pre-checks go |
| `internal/agentview` and its call sites (`mcpserver/server.go:545, 870`, `cli/channels.go:238`, `cli/stophook.go:148`, `cli/receive.go`, `cli/wait.go`) | deleted: Receive, Peek and Watch already exclude memory channels for an agent principal, so the Stop hook's count, `wait`'s wake and `receive -session` need no client filter (Eric's answer to question 4) |
| `sessions/identities.go Identities` | `Access.Identities(p)` (the scope gate; an unscoped principal sees every identity) |
| `sessions/service.go:77`, `open.go:82`, `close.go:30`, `watch.go:100`, `peek_counts.go:29` | `Access.Require(p, Session{ID}, Use)`: the rule compares the session row's identity to `p.Name` (`not_found` as built) |
| `slackbridge/inbound.go trust` (human + stored write) | `Access.Require(p, Channel{ID}, Post)`; the human-kind and write denials come back as two distinct `Decision` reasons so `dropReason` keeps its fixed texts (`reasonNotHuman`, `reasonNoWrite`); the Slack binding checks stay in `trust` (they are about the Slack account, not the bus) |
| `slackbridge/create.go writeHumans` | `Access.Holders(Channel{ID}, Post)`, then the email binding as today |
| `tasks/update.go ownerGuardBlocked`, `Force`, the `forced` revision | deleted (Eric's answers to questions 1 and 12; section 3.14 lists every trace) |
| `server/bridges.go notConfigured`, `bus/local/scopes.go errBridgeNotConfigured` | `unavailable`, not retryable, same text (Eric's answer to question 7); `problem.go` already maps `unavailable` to 503 |
| `tui/tasks.go taskEditable` and `ownedToast`, `tui/model.go statusCmd`'s admin gate | deleted; the TUI sends the patch and shows the server's answer, calls `Status` and treats `forbidden` as "not an administrator" (Eric's answer to question 3) |

### 3.9 Observable behavior changes

Every matrix row (its channels store the creator's `[read, write, admin]`
row, so retiring the implicit creator entry does not move it), every
`TestAuthEndToEnd` leaf and the e2e code table keep their codes. The
changes, each decided by Eric in section 4 or intended by section 3:

- **No implicit creator entry** (question 6; section 3.13): a creator
  without a stored `admin` row no longer administers its channel and
  lists it only through a stored row.
- **Scope enforced** (question 11; section 3.12): a scoped identity gets
  `not_found` outside its scope, worded as for a thing that does not
  exist (4.1 (d)), and its set forms (list, receive, search, tag matches,
  identities) show in-scope objects only; the create/rename collision is
  the open item 4.2. Today a scoped
  identity sees and reads `general` and every default-ACL channel.
  `channels.Service.List` moves to `Access.Channels(p, List)` in this
  plan so the scoped list is filtered now; for an unscoped principal the
  interim `List` clause keeps Decision 94 (every channel) until the
  visibility rebuild, so the matrix's `list channels` row is unchanged.
- **No task owner guard** (questions 1 and 12; section 3.14): any writer
  on a task list changes any task; `force` leaves the wire; the `forced`
  revision type is never written again.
- **Memory filtering on the server** (question 4): an agent principal's
  Receive, Peek and Watch never carry a memory channel; an agent's
  Subscribe to a memory channel is refused by the server (`validation`,
  today's text). The TUI's identity is human in both modes (local:
  `client/local.go:85-90` `RegisterLocalHuman` for the OS-login identity;
  server: the sign-in), so it is unaffected; a TUI run with `-identity`
  naming an agent identity on a local profile is an agent principal and is
  filtered like one (`RegisterLocal`, `local.go:91`).
- **Bridge not configured is `unavailable`** (question 7): HTTP 503 with
  `retryable: false` and the same text; `agentbus2 bridges ...` exits 1
  with `agentbus2: unavailable: the Slack bridge is not configured`.
- A session whose identity row was deleted (host `reset`, scope
  retirement) answers `not_registered` on Receive, Peek and Watch instead
  of being evaluated as an unregistered reader with stored rows (I5).
- `Authorize` on an undefined action was a panic; it is `internal` (I10).
- The malformed-name-as-`everyone` binding in the Store disappears; such a
  name never reaches the rule (`Resolve` refuses it).

### 3.10 Rebuilding the channel-visibility plan on this

The visibility spec's section 3.1 (`Tx.VisibleChannels`), its plan's Task
1 and Review Focus 1 are superseded. The rule "any ACL entry lists the
channel; administrators see everything" is the `List` action of section
3.2, where "administrators see everything" is the implicit right Eric gave
server administrators in his answer to question 6 (discover any channel,
change any channel's ACL) and "any ACL entry" means a stored row (no
creator clause: question 6 retired the implicit creator entry, so the
visibility spec's 3.7 "a creator lists a channel it created with
`acl: []`" no longer holds). `channels.Service.List` already calls
`Access.Channels(p, List)` after this plan (interim clause: every channel
for an unscoped principal, the scope's channels for a scoped one); the
rebuild replaces the interim clause in `decide` with the stored-row rule,
touches no Store query, and changes the matrix `list channels` row's
content as its plan already says. The
`capabilities` array, the bridged icon and the client tasks of that plan
stand; its discovery-capability item is replaced by a minor protocol
version bump (the Context section). The rebuilt plan is written after this one
lands, against the then-current tree.

### 3.11 Alternatives rejected

- Keeping `core.Authorize` and friends as forwarders to `access`: fewer
  diffs, but two names for the rule and an allowlist entry in the gate
  for each. Rejected (section 3.3).
- A `Bus`-level `Can(object, action)` operation so clients stop mirroring
  rules: a protocol addition (a minor bump under the protocol-versioning
  decision, Context section); not needed
  for this change, offered in question 3.
- Putting the checker in `internal/bus` so clients could run it: clients
  have no Store and must not; the Bus error code is their answer.

### 3.12 Scope enforcement by object kind (Eric's decision, question 11)

"A scoped identity gets no access to anything outside its scope; inside
it, access is governed by ACL entries; unscoped identities are
unaffected." Made precise, per object kind, as the scope gate of section
3.2 implements it:

| Object kind | In scope when | Outside: what happens |
| --- | --- | --- |
| Channel (`channels.scope`) | `channel.scope == p.Scope`; an unscoped channel (`""`), `general` included, is outside every scope | every channel action by name is `not_found` `channel %q does not exist` (Reason `out_of_scope`; Eric's decision 4.1 (d): a scoped identity cannot learn what exists outside its scope, which supersedes the visibility decision's "no name secrecy" for scoped identities only); `List`, `Channels(Read/Deliver)` and so Receive, Peek, Watch, Search and tag matching omit it; a create or rename onto an existing out-of-scope name is the open item of section 4.2 |
| Task, memory | their channel's scope | as the channel; `task_get`/`get_memory` and the other by-id operations on an out-of-scope channel are `not_found` with the unknown-id text |
| Server (status, usage, reset, reset-credential, scopes, identities create, bridges create/list) | never | `Administer` is `forbidden` `administrator privilege required` for a scoped principal whatever its `Admin` flag (Eric's decision 4.1 (a): it cannot be or act as an administrator; `MakeAdmin` and `admins add` refuse it); the answer is the one every non-administrator gets and reveals nothing |
| Bridge by channel (`bridges get/remove`) | its channel's scope | administrator-only, so `forbidden` as above; were a scoped principal ever an administrator it would be `not_found` `channel %q has no bridge` |
| Channel creation | always (the one exemption of the gate: the new channel takes `p.Scope`, ADR 0009 decision 8) | `CreateChannel` follows Decision 93; naming any scope in the request is `Administer` and so `forbidden`; `channel_creation: admins` denies a scoped principal (it cannot be an effective administrator) |
| Session | the caller's own | unchanged: another identity's session is `not_found` (question 2); a scoped identity's sessions are its own |
| Identity (`Identities` listing, the MCP `discover`; a task `owner`; an ACL identity named by a scoped granter) | `identity.scope == p.Scope` | omitted from the listing (`See`); as a task owner `not_found` `identity %q is not registered`; as an ACL identity the Decision 96 `validation` text (4.1 (b) and (d)) |
| Health | no object | the identity tier as today; a scoped administrator-flagged identity is tier `identity`, not `admin` (consistent with the Server row) |
| Bridge | server operation | `forbidden` |
| Human principals | always unscoped (`identities.scope` is set only by `CreateScopedIdentity`) | unaffected |
| `slack-bridge` | never a principal | n/a |

A done scope (ADR 0009 decision 7) is still its members' scope: `done`
stops joining, not acting; a retired scope has no members left (decision
19). ACL rows naming a scoped identity on an out-of-scope channel grant
nothing (the gate runs first); whether `SetACL` should refuse writing
such a row is section 4.1 (b).

### 3.13 Retiring the implicit creator entry (Eric's decision, question 6)

ADR 0008 decision 1 evaluated every ACL as if it contained `[{admins,
[admin]}, {<creator>, [admin]}]`. The creator half is retired. The
replacement: a server administrator implicitly may discover (`List`) any
channel and holds `admin` on it (set the ACL, and so grant itself `read`
and `write`); there is no implicit `read` or `write` for anyone.

Migration effects (no schema change; `channels.creator` stays as data for
`ChannelInfo.Creator` and `status`):

- A channel created without an ACL stores the Decision 88 default,
  `[{everyone,[read,write]}, {<creator>,[read,write,admin]},
  {admins,[admin]}]`: the creator keeps `admin` through its stored row.
  **Unaffected.** `general` has no creator and is managed through the
  implicit `admins` right. **Unaffected.**
- A channel created with an explicit ACL that omits its creator (`acl:
  []`, or a private ACL naming only others): the creator held `admin`
  only through the implicit entry and **loses it** at upgrade; only a
  stored `admin` holder or a server administrator can now manage, rename
  or delete it, and the creator no longer lists it unless a stored row
  names it. No release has shipped, so no deployment is upgraded by this
  change; the plan still has the records task write the rule into
  `docs/ops/upgrade.md` with the one-line SQL an operator can run to find
  such channels (`SELECT c.name FROM channels c WHERE c.creator <> '' AND
  NOT EXISTS (SELECT 1 FROM channel_acl a WHERE a.channel_id = c.id AND
  a.identity = c.creator AND a.can_admin)`), and have `channels create
  -acl` document that an ACL omitting the creator leaves it without
  `admin`.
- Tests that assert the implicit creator entry change meaning:
  `internal/conformance/bus/channels_test.go` `TestChannelCreateWithKindAndACL`
  (:82 "[] stores no entries; the creator still administers", :197
  "Removing the creator's rows leaves the creator able to edit"),
  `bridgeguard_test.go:132` ("the creator still sets the ACL of its own
  channel"), the store suite's `TestACLImplicitEntriesGrantAdmin` and
  `TestACLRemovingAdminsAndCreatorRowsKeepsImplicitAdmin` (whose
  `EffectivePermissions` halves go in any case, section 3.5), and
  `internal/core/authz_test.go TestAuthorizeUsesImplicitEntries`. Each is
  rewritten to assert the new rule (the creator is `forbidden`; a server
  administrator is allowed; a stored `admin` row is allowed).

### 3.14 Removing the task owner guard (Eric's decisions, questions 1 and 12)

Write access to a task list permits modifying any task on it; v1's owner
rule existed only for lack of an authorization model. Every trace, found
by `rg -n -i '\bforce\b|forced|ownerGuard' internal api docs` (excluding
`internal/gen`, the installer's "-harness ... to force one" and the Slack
"no force" invite comments, which are unrelated):

- Core: `internal/core/tasks/update.go:28-37` `ownerGuardBlocked`, `:52-59`
  rules 2 and 3, `:299-313, 336-337` the `forced` revision (the extra
  revision row naming the overrider and the `forced` revision type);
  `doc.go` comments; `doc_test.go` (2 mentions).
- Bus: `internal/bus/tasks.go:76` `TaskPatch.Force`.
- Wire: `api/openapi.yaml:1773` the `force` member of the task update
  body; `internal/bus/remote/tasks.go:57-58`; `internal/server/tasks.go:107`;
  `make generate` after the edit. **Protocol change**: a request member is
  removed. Under Eric's protocol-versioning decision a removal is
  major-shaped, but `info.version` is `2.0.0-dev` and no release has
  shipped, so it lands without a bump and is listed in the version's
  changelog; a server receiving `force` from an older client ignores an
  unknown member as the generated decoder does today.
- MCP: `internal/mcpserver/inputs.go:197` `Force`; `server.go:724` the
  `task_update` description ("While a task is in_progress only its owner
  may change or delete it; force=true overrides that for anyone and is
  recorded") and `:731`.
- CLI: `internal/cli/tasks.go:37, 67, 174` the `-force` flag;
  `cli/tasks_test.go`.
- Skill: `internal/cli/skills/using-agentbus2/SKILL.md:143` ("pick another
  task, do not force") and `:162` ("`force: true` overrides another
  owner and is recorded"); the m6 word check does not cover these words,
  so no count moves.
- TUI: `internal/tui/tasks.go:55-58` `ownedToast`, `:66-68` `taskEditable`,
  `:95`, `:193-197` (question 3 removes them with it).
- Tests: `internal/conformance/bus/tasks_test.go` (4 mentions),
  `internal/server/memories_tasks_test.go` (4), `internal/core/tasks/
  doc_test.go` (2), `internal/cli/tasks_test.go` (1): the assertions that
  a non-owner is `conflict` become assertions that it succeeds; the
  `forced` revision assertions go.
- Docs: `CLAUDE.md` (the `tasks update` line, if it names `-force`),
  `docs/` (none found beyond the skill; the records task greps again).

What stays: `owner` as data, `task_claim` (sets the owner to the caller),
`task_release`, "an `in_progress` task needs an owner", `completed` sets
the owner to the caller when empty, the abandoned-task reclaim
(`reclaim.go`: a dead owner's task returns to `pending`), and
`TestTaskOwnerRegistryCheck` (a new owner must be registered). If agents
misbehave without the guard, Eric revisits.

## 4. Eric's decisions on the open questions (2026-10-25)

Each is **Eric's decision 2026-10-25**, answering the twelve questions the
first draft of this document put to him (the option letters refer to that
draft, commit 69e2c64; each item restates the question in its answer).

1. **No task owner guard in v2.** Write access to a task list permits
   modifying any task on it; v1's owner rule existed only for lack of an
   authorization model. `ownerGuardBlocked`, `Force` and the `forced`
   revision go, every trace of them (section 3.14), the wire member
   `force` included (a protocol change, noted there). If agents misbehave,
   Eric revisits. (Neither option; the rule is removed.)
2. **A session that is not the caller's stays `not_found`.** (Option A.)
3. **The TUI's two client-side access decisions are removed**
   (`taskEditable`, `statusCmd`'s admin gate); the TUI reacts to the
   server's codes. No `Can()` operation now. (Option A.)
4. **The agent memory filter moves into the access check** (the `Deliver`
   action: an agent principal is not delivered a memory channel through
   receive, wait, watch or the Stop hook's count), not a separate
   presentation filter; the gate covers it; `internal/agentview` goes.
   (Option B.)
5. **Registration, sign-in and admission rules stay in
   `internal/core/identities`.** (Option A.)
6. **The creator-implied-owner rule is retired** (ADR 0008's implicit
   creator entry). Replacement: server administrators implicitly may
   discover (list) any channel and change any channel's ACL; no implicit
   `read` (they grant themselves `read`). ADR 0008 changes; the records
   task records it there; section 3.13 calls out the migration effects.
   (Neither option as drafted.)
7. **Bridge not configured answers `unavailable`**, not `forbidden`.
   (Option B.)
8. **`Check` returns a `Decision` value `{Allowed, Code, Text, Reason}`;
   `Require` converts a denial to a `bus.Error`.** (Option A.)
9. **`EffectivePermissions` and `ReadableChannels` are deleted from the
   Store contract**; `ACLRowsFor` facts only. (Option A.)
10. **The gate anchor moves** to `internal/core/access/decide.go`
    `if !p.Admin {` and `check-gates-selftest.sh` block 15 is updated.
    (Approved.)
11. **Scope is enforced by the access check now**: a scoped identity gets
    no access to anything outside its scope; inside it, access is governed
    by ACL entries; unscoped identities are unaffected. Section 3.12
    defines "outside its scope" per object kind; section 4.1 lists what
    is still ambiguous. (Neither option as drafted.)
12. **Moot with 1**: there is no `Force` to grant.

### 4.1 Eric's decisions on the edges of "outside its scope" (2026-10-25, closed)

Each is **Eric's decision 2026-10-25**, ruling on the six sub-items the
first version of this section raised and on the two choices the review
made (section 3.9 and 3.10):

- (a) **A scoped identity cannot be or act as a server administrator.**
  `Administer` is denied to a scoped principal whatever its `Admin` flag;
  `MakeAdmin` and `admins add` refuse a scoped identity (`validation`, "a
  scoped identity cannot be an administrator"). (Accepted.)
- (b) **An ACL row naming a scoped identity on a channel outside its
  scope is accepted but inert**: the scope gate runs first, so the row
  grants nothing; `SetACL` does not refuse it. (Accepted.) One
  refinement follows from (d): when the *granter* is scoped and names an
  identity it cannot `See`, `SetACL` and `CreateChannel` answer the
  Decision 96 text `ACL identity %q is not a registered identity`
  (`validation`), exactly as for an unregistered name, so the granter
  learns nothing.
- (c) **A scoped identity lists only the identities of its own scope**
  (`Identities`, the MCP `discover`). (Accepted.)
- (d) **An out-of-scope denial answers `not_found`, not `forbidden`**: a
  scoped identity cannot learn what exists outside its scope. (The
  draft's `forbidden` reading is rejected.) Applied consistently, with
  the text the operation uses for a thing that does not exist, so the two
  cases are indistinguishable:
  - channel by name (`history`, `subscribe`, `unsubscribe`, `send`,
    `search` with a channel, `get_acl`, `set_acl`, `rename`, `delete`,
    `task_list`, `task_create`, `bridges get/remove`): `not_found`
    `channel %q does not exist` (`core.ChannelByName`'s text);
  - task or memory by id on an out-of-scope channel (`task_get`,
    `task_update`, `task_claim`, `task_release`, `get_memory`,
    `edit_memory`, `delete_memory`, `memory revisions`): `not_found`
    `memory %d does not exist` / the task operations' not-found text
    (`taskChannelOf`);
  - identity: omitted from `Identities`; as a task `owner` it is
    `not_found` `identity %q is not registered` (the rule 6 text);
    as an ACL identity named by a scoped granter it is the Decision 96
    `validation` text (item (b));
  - session: already `not_found` (question 2);
  - set forms (`List`, Receive, Peek, Watch, Search without a channel,
    tag matching, `Identities`): the object is absent;
  - server-level operations (status, usage, reset, reset-credential,
    scopes, identities create, bridges create/list): a scoped principal
    gets exactly what every non-administrator gets, `forbidden`
    `administrator privilege required` (item (a)); that reveals nothing,
    since the answer does not depend on what exists.
  The scope gate's `Decision` therefore carries Reason `out_of_scope`
  with Code `not_found` and the object kind's not-found text; `Require`
  emits it unchanged. Section 4.2 is the one place where `not_found`
  cannot be produced without a further decision.
- (e) **`general` is outside every scope.** (Accepted.)
- (f) **The administrators' implicit right on every channel is the full
  channel-admin set** of Decision 80: the ACL, rename, delete.
  (Accepted.)
- (review 1) **Scoped identities may create channels in their own scope**:
  the gate exempts `CreateChannel`, and the channel takes `p.Scope`.
  (Accepted.)
- (review 2) **Interim `List`**: an unscoped principal's list is unfiltered
  until the channel-visibility rebuild; a scoped principal's list is its
  scope's channels now. (Accepted.)

### 4.2 The one item needing Eric: a create or rename that collides with an out-of-scope name

Channel names are globally unique (`channels.name TEXT NOT NULL UNIQUE`,
`internal/store/sqlite/schema/0001_initial.sql:22`, the same on Postgres),
not unique per scope; identity names likewise (`identities.name` is the
primary key). So when a scoped identity creates (or renames onto) a name
that an out-of-scope channel already holds, the Store answers `conflict`
`channel %q` and the caller learns that the name exists outside its
scope, against decision (d). No other operation leaks: every other answer
of 4.1 (d) is indistinguishable from "does not exist", and identity
creation is administrator-only.

Options:

- **A. Scope-prefixed namespaces (recommended).** A scoped identity may
  create or rename to names of the form `<scope>-<rest>` only
  (`validation`, "a channel of scope %q is named %q-<name>"); nobody may
  create or rename to a name starting with `<s>-` for an existing scope
  `s` (open, done, or retired and not yet purged) unless the channel is
  given that scope (`validation`, "names starting with %q- belong to scope
  %q"); `CreateScope` refuses a name `s` while any channel of another or
  no scope is named `s-*` (`conflict`). Then every channel named
  `<scope>-*` is in that scope, so a scoped creator's `conflict` always
  names a channel it can list: no leak. Identities already follow the
  convention (ADR 0009 ratified call: "a bridged agent posts under its
  short name, the scope name and a following `-` removed"); an
  administrator naming a scoped channel without the prefix stays allowed
  (`k-chat` in `TestScopedChannels`), since the rule only has to make
  prefixed names exclusive. Cost: one name rule at create, rename and
  scope creation, no schema change, a `validation` for unscoped callers
  who pick a scope-prefixed name.
- **B. Per-scope uniqueness** (`UNIQUE (scope, name)`): changes name
  resolution on the wire for every unscoped caller (which `job1-chat`?)
  and every client; a schema migration. Rejected unless Eric prefers it.
- **C. Answer `conflict` without a text** for a scoped creator whose name
  collides outside its scope: the code itself is the leak. Rejected.
- **D. Accept the leak** for this one operation as a name-level
  disclosure: contradicts (d) as stated.

Until Eric rules, the plan implements (d) everywhere else and leaves the
create and rename collision answering `conflict` (today's behavior),
marked in Task 2 and in Task 11, which implements option A when he
accepts it.

## 5. Documents that change

- New ADR 0011 "One access check" (status: accepted, Eric 2026-10-25, for
  sections 1, 4 and 4.1; section 4.2 recorded as the open item with
  option A proposed): the table of 3.2 with the scope gate, the denial codes, the
  gate, the anchor move, the removal of the task owner guard and of the
  `force` wire member; the protocol-versioning decision (Context) is
  recorded there too, marked as context for the visibility rebuild.
- ADR 0004: addendum for the Store contract change (3.5).
- ADR 0008: a new section "Decisions of 2026-10-25 (Eric)": the implicit
  creator entry is retired; the implicit administrator right is
  discover-and-`admin`; the rule is evaluated in `internal/core/access`
  and the SQL serves facts; "superseded in part" markers on decision 1's
  creator sentences and on the Decision 88 wording "the creator's `admin`
  is always present through the implicit entry" (it is present through
  the stored default row).
- ADR 0009: a note on `Post` and `Holders` replacing the stored-row scans
  (decision 11 unchanged), on the scope gate making decision 8's scope an
  access boundary, and on `unavailable` for an unconfigured bridge.
- `docs/ops/upgrade.md`: the implicit-creator change and the operator SQL
  of 3.13. `docs/ops/slack-bridge.md:3` and `admin-bootstrap.md:80`:
  `forbidden` becomes `unavailable`. `CLAUDE.md:24, 56`: the same two
  words; the Design bullets (below).
- `internal/cli/skills/using-agentbus2/SKILL.md:143, 162`: the `force`
  sentences go; one sentence says any writer may change any task.
- `api/openapi.yaml`: the `force` member goes (3.14); `make generate`.
- `CLAUDE.md` Design bullets: "Authorize" and "`internal/core/authz.go`"
  become `internal/core/access`; the gate anchor's new home; the Store
  bullet drops `EffectivePermissions`/`ReadableChannels`.
- `docs/superpowers/plans/2026-10-25-health.md` and
  `2026-10-25-channel-visibility.md`: a one-line "superseded by" note at
  the top of each affected section (the anchor constraint; Task 1).
- The design spec's section 4.6.1: one paragraph naming the access check
  as where the table is evaluated.

## Appendix A. Inventory (file:line at commit 0333662)

Kind: **R** computes an access decision; **r** restates one (a mirror);
**S** sets or copies the principal's attributes; **N** not access (listed
for the gate's scope). Dup: the inconsistency of 2.2 it belongs to.

### Store

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| S1 | `internal/store/channels.go:68-74` | Contract: `EffectivePermissions`, `ReadableChannels` | R | I1 |
| S2 | `internal/store/sqlstore/acl.go:123-134` `isAdmin` | administrator flag by SQL; malformed name is not admin | R | I2 |
| S3 | `sqlstore/acl.go:139-172` `EffectivePermissions` | Decision 80 union; `admins` row only for an administrator; implicit `admin` for administrator and creator; malformed name bound to `everyone` | R | I1, I5, I6 |
| S4 | `sqlstore/acl.go:178-190` `ReadableChannels` | the `read` projection of S3 as one statement with an `EXISTS is_admin` subquery | R | I1, I2 |
| S5 | `sqlstore/acl.go:25-45` `validateACL` | Decision 96: entries name registered identities; `everyone`, `admins` exempt | N (ACL validity) | |
| S6 | `internal/store/acl.go:56-63` `DefaultACL` | Decision 88 default entries | N (data) | |
| S7 | `sqlstore/channels.go:199-210` `EmptyChannelsBefore` | ADR 0009 d20: a `slack-bridge` subscription keeps no channel | N (retention) | |
| S8 | `sqlstore/identities.go:171-173` | `slack-bridge` takes no key | N (reserved name) | |
| S9 | planned `Tx.VisibleChannels` (visibility plan Task 1) | any entry or creator or administrator | R | I1, I6 |

### Core, root

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C1 | `internal/core/authz.go:18-49` `Authorize` | permission on a channel via S3; reserved names `not_registered`; unknown permission panics | R | I5, I10 |
| C2 | `authz.go:53-71` `reservedIdentity`, `RequireIdentity` | registered and not `""`/`everyone`/`admins`/`slack-bridge` | R (principal) | I5 |
| C3 | `authz.go:75-84` `RequireAdmin` (anchor `:80 if !id.Admin {`) | Decision 73 administrator flag | R | I2 |
| C4 | `authz.go:100-106` `ReadableChannelIDs` | set form via S4 | R | I1 |

### Core, channels

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C5 | `core/channels/channels.go:20-33` `List` | Decision 94: no permission (changes under visibility decision 1) | R | |
| C6 | `channels.go:64-68` `Create` | Decision 93: `channel_creation: admins` needs the administrator flag | R | I7 |
| C7 | `channels.go:72-80` `Create` | ADR 0009 d9: naming a scope needs the administrator flag; text rewritten | R | I7 |
| C8 | `channels.go:82-86` `Create` | scope must be open (`scopes.RequireOpen`) | N (state) | |
| C9 | `channels.go:105-114` `adminChannel` | channel `admin` for ACL get/set, rename, delete (Decisions 80, 89) | R | |
| C10 | `channels.go:119-137` `liveBridge`, `refuseBridged` | a creating or live bridge pins the channel (`conflict`) | N (state) | |
| C11 | `channels.go:139-144` `protected` | `general` is never deleted or renamed | N (validation) | |
| C12 | `channels.go:243-253` `SetACL` | bridged channel: server administrator only, text rewritten; then `CheckBridgeACL` | R | I7 |
| C13 | `channels/subscriptions.go:30-33` `Subscribe`; `:46-57` `Unsubscribe`; `SubscribeTags` | `read` to subscribe (Decision 80); none to unsubscribe or to add a tag set (Decision 95) | R | |
| C14 | `channels/admin.go:22-27` `Status` | administrator only (Decision 73) | R | |
| C15 | `channels/usage.go:14-19` `Usage` | administrator only | R | |
| C16 | `channels/service.go:56-74` `mutate`, `read` | principal resolution before every operation | R (principal) | I5 |

### Core, scopes

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C17 | `core/scopes/service.go:57, 81, 104, 148` | scopes create, list, get, done: administrator only (ADR 0009 d9) | R | |
| C18 | `scopes/acl.go:16-33` `CheckBridgeACL` | Decision 12: a bridged ACL grants `read`/`write` to named identities only; `slack-bridge` `read` pinned | N (ACL validity) | |
| C19 | `scopes/retire.go` `Retire` (`RetireIdentity`) | ADR 0009 d19: retirement deletes the identity's ACL rows | N (mutation) | |

### Core, tasks, memories, search, messaging

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C20 | `core/tasks/create.go:59` (`write`), `read.go:34, 89` (`read`), `update.go:268` (`write`) | Decision 95 task rows | R | |
| C21 | `tasks/update.go:32-37, 52-59` `ownerGuardBlocked` | owner guard: in progress or owned by another is `conflict`; `Force` bypasses | R | I4, I9 |
| C22 | `tasks/service.go:76-85` `ownerKnown` | a reserved name is never an owner | N (validation) | |
| C23 | `core/memories/memories.go:30, 67` (`read`), `:137, 237` (`write`) | Decision 95 memory rows | R | |
| C24 | `core/search/service.go:63` (`read`), `:68` (set form) | Decision 80 search | R | I1 |
| C25 | `core/messaging/send.go:106` (`write`), `history.go:29` (`read`) | Decision 80 | R | |

### Core, delivery and sessions

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C26 | `core/delivery/receive.go:54-61, 70-100` `load` | readable set gates direct delivery (Decision 80) and tag matching (Decision 95); serves `Receive`, `peek.go:32` (`Watch`), `peek_counts.go:64` (`Peek`, the Stop hook) | R | I1, I5 |
| C27 | `delivery/watch.go:100`, `peek_counts.go:29`, `sessions/service.go:77` `Require`, `sessions/open.go:82` `resume`, `sessions/close.go:30` | the session belongs to the caller, else `not_found` | R | I4 (five copies) |
| C28 | `sessions/open.go:187-193` (offered cursors), `:221-226` (pending) | `read` on the channel (Decisions 44, 95) | R | |
| C29 | `sessions/identities.go:17, 32` `Identities` | every registered identity sees every identity's `Admin` and `Kind` | S (copy) | feeds C46 |

### Core, identities and health

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C30 | `core/identities/admin.go:114` `ResetCredentialAs`, `:181` `Reset` | administrator only (Decisions 58, 73) | R | |
| C31 | `identities/human.go:107` `CreateIdentityByEmailAs`, `agent.go:72` `CreateAgentIdentityAs` | administrator only (ADR 0009 d9) | R | |
| C32 | host variants `admin.go:38` `MakeAdmin`, `:58` `ResetCredential`, `human.go:35`, `agent.go:22` | no principal: host access is root | N (host) | |
| C33 | `identities/human.go:253, 287-296` `signInHumanTx` | `users` list admits (`forbidden` otherwise); `admins` list sets the flag | S (set) | question 5 |
| C34 | `identities/register.go:97-98, 111-117, 121-122, 127-170` | a human has no key; a scoped identity needs its bound token (d17); a keyless administrator needs a token; enrollment mode | N (authn policy) | question 5 |
| C35 | `identities/admin.go:218` `Reset` | reserved identities survive a reset | N | |
| C36 | `core/health/health.go:60-70, 111` | tier `admin` when `id.Admin`, else `identity`; `""` is the probe tier | R | I2, I8 |

### Slack bridge

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C37 | `internal/slackbridge/service.go:159-175` `requireAdmin`, `guard` | every bridge operation: administrator only | R | |
| C38 | `slackbridge/inbound.go:199-241` `trust` (`:211` kind, `:214-222` stored `write` row) | d11: a human with an explicit `write` entry may post; d18: Slack binding | R | I3 |
| C39 | `slackbridge/create.go:224-233, 252-275` `writeHumans` | d11: humans with an explicit `write` entry are invited | R | I3 |
| C40 | `slackbridge/create.go:423-440`, `archive.go:117-128` | pin and unpin the `slack-bridge` `read` entry | N (mutation) | |

### Server and Bus implementations

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C41 | `internal/server/problem.go:25` | `forbidden`, `not_registered` are 403 | N (mapping) | |
| C42 | `server/bridges.go:32`, `internal/bus/local/scopes.go:40` | bridge not configured: `forbidden` for everyone | N (feature) | question 7 |
| C43 | `server/health.go:20`, `server/auth.go:66-70` `IdentityFrom` | `""` is the anonymous principal | S (principal) | |
| C44 | `internal/bus/remote/...`, `internal/bus/local/...` | no decisions (verified: no `Admin`, `Creator`, `Owner` conditions) | | |

### Clients

| # | Site | Rule encoded | Kind | Dup |
| --- | --- | --- | --- | --- |
| C45 | `internal/mcpserver/server.go:545-549, 868-875`; `internal/cli/channels.go:238-242`; `cli/stophook.go:148-152`; `internal/agentview/kinds.go` | decision 20: agents never subscribe to or are woken by memory channels | N (presentation) | question 4 |
| C46 | `internal/tui/model.go:1363-1394` `statusCmd` | call `Status` only when the listing says `Admin` | r | I2, I9 |
| C47 | `tui/tasks.go:66-68` `taskEditable`, `:193-197` | unowned or own task is editable; no `Force` | r | I9 |
| C48 | `tui/client.go:146-147`, `tui/model.go:1409-1411`, `cli/status.go:45-55` | react to `forbidden` (`(no access)`, silent status) | N (reaction) | |

Totals: 33 rows of kind R compute an access decision on the server side
(S1 to S4, S9; C1 to C7, C9, C12 to C17; C20, C21, C23 to C28; C30, C31,
C36; C37 to C39). Of those, 24 go through the four `authz.go` entry points
and 9 decide on their own (S2, S3, S4, S9, C21, C27 counted as one row for
its five copies, C36, C38, C39). Two client mirrors (C46, C47, kind r).
The remaining rows are listed as the gate's boundary.

After Eric's answers of section 4: C21 is deleted rather than moved
(question 1); C45 (the agent memory filter) becomes a rule of the access
check (question 4) and its row's kind is R, not N; C42's code becomes
`unavailable` (question 7) and the row stays outside the gate; C46 and
C47 are deleted (question 3); S3's creator clause is retired, not moved
(question 6).
