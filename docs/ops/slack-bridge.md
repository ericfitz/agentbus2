# The Slack bridge

The Slack bridge mirrors one bus channel into one private Slack channel, so people can follow and join a job's conversation from Slack. It is off until both Slack token files are configured; without them every `bridges` action answers `forbidden` ("the Slack bridge is not configured"). The design is the Slack bridge spec (`docs/superpowers/specs/2026-10-23-slack-bridge-design.md`) and ADR 0009.

## Create the Slack app

The app is internal to one workspace and is never distributed publicly (a Socket Mode app cannot be listed in the Slack Marketplace anyway).

1. Create an app in your workspace and enable **Socket Mode**.
2. Create an **app-level token** (`xapp-...`) with the scope `connections:write`.
3. Add these **bot token scopes**: `groups:write`, `groups:write.invites`, `groups:history`, `groups:read`, `users:read`, `users:read.email`, `chat:write` and `chat:write.customize`.
4. Subscribe to these **bot events**: `message.groups` and `member_left_channel`.
5. Install the app to the workspace and copy the **bot token** (`xoxb-...`).
6. Write each token to its own file readable only by the server user (mode 0600), for example `/etc/agentbus2/slack-app-token` and `/etc/agentbus2/slack-bot-token`. The files hold the token alone; leading and trailing whitespace is trimmed.
7. Set `slack_app_token_file` and `slack_bot_token_file` (see [configuration.md](configuration.md)); both or neither. The server reads the files once at start. A missing file, an app token that does not start with `xapp-` or a bot token that does not start with `xoxb-` stops the start with exit 2, naming the setting and never the token. Slack answering `invalid_auth` to the bot token also stops the start. Any other failure to reach Slack at start (network, a 5xx, a rate limit) does not: the server starts, bridge calls answer `unavailable` ("the Slack bridge is not connected to Slack yet; retry", retryable) and the bridge retries `auth.test` until Slack answers.

Two more settings tune it: `slack_archive_after_done_hours` (default 168) and `slack_max_message_chars` (default 3000; a longer message is cut by characters and ends with a note pointing to agentbus2). Every replica needs both token files to serve bridge calls, and the same settings.

## What a job looks like

Everything starts with a scope (the walk-through is in [admin-bootstrap.md](admin-bootstrap.md#walk-through-a-job-with-agents-people-and-a-slack-channel)):

1. An administrator creates the scope, its agents and its people (humans are identities named by their email address).
2. A channel in the scope gets a **named-only ACL**: read or write may be granted only to named identities, never to `everyone` or `admins` (administrators act through the implicit admin entry). The bridge refuses any other ACL, and while a bridge is creating or live only a server administrator may change the ACL.
3. `agentbus2 bridges create [-slack-name N] CHANNEL` bridges it (below).
4. `agentbus2 scopes done -retire with_bridge SCOPE` ends the job; the Slack channel is archived and the scope retired afterwards.

## Creating a bridge

`bridges create` requires an ordinary channel (not a memory or tasks channel) that belongs to an open scope, has no bridge and has a named-only ACL. It then:

1. Stores a bridge row in state `creating` with the Slack name: `-slack-name` (1 to 80 characters of `a-z`, `0-9`, `-` and `_`), or the channel name lowercased with every other character replaced by `-` and cut to 80 characters.
2. Creates a private Slack channel of that name. A name that is already taken is `conflict` ("Slack channel name ... is taken"; no suffix is added) and the `creating` row is deleted. Slack keeps the name of an archived channel, so use `-slack-name` to bridge again after a removal.
3. Invites every human who holds an explicit `write` entry: the email bound to the identity is looked up in Slack, and only a **full member of the workspace** is invited. Slack guests (restricted and ultra-restricted users), external users (strangers and members of another workspace), deleted users and bots are never invited, and neither is an email Slack does not know. Each is listed in `not_invited` with the reason `not found in Slack`, `Slack guest or external user` or `invite failed: <Slack code>`.
4. Makes the bridge `live`, adds `{slack-bridge, [read]}` to the channel's ACL and subscribes the bridge at the channel's current end (earlier messages are not posted).
5. When `not_invited` is not empty, posts a notice in the Slack channel and sends a `notice` message from `slack-bridge` on the bus channel, naming those identities and the reasons. Failing to post the notice is logged and does not fail the create.

Invitations happen once, here. Later ACL changes still decide who may write from Slack (the ACL is read for every Slack message, so a human whose `write` entry is removed is dropped from then on, though still in the Slack channel), but they do not invite or remove anyone in Slack.

### If a create fails

Any failure leaves the row `creating` and returns an error: a Slack rate limit (`rate_limited`, the answer names the wait Slack asked for), a Slack or network failure (`unavailable`), and so on. Run the same `bridges create` again; it resumes the row under its stored name (a different `-slack-name` is `conflict` and names the stored one). Resuming skips creating the Slack channel when the row already has one, and invites again; users already invited are not a problem because Slack's `already_in_channel` counts as success, so a rate limit in the middle of the invitations costs only the retry. When a crash happened after Slack created the channel but before the row recorded it, the retry finds the channel by name and adopts it. Only a `creating` row adopts a channel; a fresh create never takes over a channel that already exists. A second `bridges create` on a `live` or `archived` bridge is `conflict` ("already bridged"). Two overlapping creates share the one row and end with one Slack channel.

## Removing and archiving

`bridges remove CHANNEL` posts "This channel is archived; the record stays in agentbus2 until its scope is retired.", archives the Slack channel, deletes the bridge row with its message map, and removes the `slack-bridge` ACL entry and subscription. The scope and the bus channel stay. A second `bridges remove` is `not_found`.

The Slack channel is also archived, and the bridge stops in both directions, at the earliest of:

- `slack_archive_after_done_hours` after the scope is marked done;
- after the scope is done, when no human remains in the Slack channel: the members of the Slack channel minus the app, bots and deleted users (a human who never joined counts as not remaining). The check runs when a `member_left_channel` event arrives for a done scope, on the first lifecycle pass after the scope is marked done, and again after every Socket Mode reconnect, because Slack does not replay an event missed while disconnected;
- `scope_idle_hours` without traffic on the scope.

The runner applies these rules every minute (it also reads Slack's history for messages it missed, see below). The Slack channel gets the final line before it is archived. A bridge whose bus channel has disappeared is archived in Slack and its row deleted.

A channel someone archived by hand in Slack is treated the same way: the bridge is marked `archived` as soon as the runner next posts to it or checks its members (a channel nobody posts to and that is not done stays marked `live` until then). Archiving is idempotent: a second run, or an overlapping replica, finds the channel already archived and succeeds. Retiring a scope deletes its bridge rows. A scope that is idle or done but whose Slack settings were removed is held, not retired, until the settings return or the bridge is removed; the server logs a warning on every maintenance tick naming it, so a Slack channel with people in it is never orphaned.

## Running it

One replica runs the bridge. The replicas compete for a Store lock named `slack-bridge`, renewed every 10 seconds: on SQLite it is a row with a 30-second expiry, on Postgres an advisory lock held by a pinned connection. The holder keeps the single Socket Mode connection, reads Slack's history on every connect and every minute, and posts the bus messages the bridge has not yet acked (it is woken by new messages and also polls every 5 seconds). A replica that loses the lock, or cannot renew it, stops all of that before its next attempt. If the runner's event queue ever fills (1024 events), the overflow is dropped, a catch-up is requested, and a dropped `member_left_channel` makes every done scope's members be checked again on the next lifecycle pass. When the holder shuts down it releases the lock; if it dies, another replica takes the lock after the expiry (SQLite) or when Postgres drops the dead session, connects, catches up and posts each unacked message once. A replica that vanishes without closing its database connection (a network partition, a frozen host) can keep a Postgres advisory lock, the runner's or a `bridges create` lock, until the server notices the dead connection; the TCP keepalive settings in [deploy.md](deploy.md) bound that wait. Every replica still needs the token files, because any replica may serve a `bridges` call or archive a bridge during maintenance.

The crash repair reads message metadata back through `conversations.history` with `include_all_metadata`, which the fact check of the bot scopes did not cover: Slack names `message_metadata:read` only for events. Before relying on it, verify on a real workspace that the bot can read its own message metadata with the scopes above. Treat the repair as best effort. A failed or interrupted database write after a successful post is retried with a short backoff, outlives a shutdown, and is finished by the next drain from memory, so it never duplicates the post. The one remaining duplicate is a crash (or a lost lock plus a takeover by another replica) between a post and its map row when the repair cannot find the message: the next runner then posts that bus message again, one duplicate Slack post at most, never a bus message.

## Logging

No Slack token, bus message content or Slack message text is ever logged. A Slack message dropped as unauthorized is logged with the Slack user ID, the Slack channel ID and the reason only.
