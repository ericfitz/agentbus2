-- Scopes and the Slack bridge (Slack bridge spec 4, 5.3, 8): the scopes table,
-- a scope column on channels and identities, retired identity names, the
-- reserved slack-bridge identity, and the bridge, message-map, Slack-user and
-- dropped-message tables.
--
-- State: runStep locks, re-reads the version and runs the step in one
-- transaction, so a second open skips it, two overlapping opens run it once,
-- and a failed run rolls back to version 5 (DDL is transactional on both
-- engines) and the next open reruns it from clean.
CREATE TABLE scopes (
  name TEXT PRIMARY KEY,
  state TEXT NOT NULL CHECK (state IN ('open', 'done', 'retired')),
  retire_mode TEXT NOT NULL DEFAULT '' CHECK (retire_mode IN ('', 'with_bridge', 'normal')),
  created_at INTEGER NOT NULL,
  done_at INTEGER NOT NULL DEFAULT 0,
  retired_at INTEGER NOT NULL DEFAULT 0,
  last_activity INTEGER NOT NULL
);
ALTER TABLE channels ADD COLUMN scope TEXT NULL REFERENCES scopes(name);
ALTER TABLE identities ADD COLUMN scope TEXT NULL REFERENCES scopes(name);
CREATE INDEX channels_scope ON channels (scope);
CREATE INDEX identities_scope ON identities (scope);
CREATE TABLE retired_identities (
  name TEXT PRIMARY KEY,
  scope TEXT NOT NULL,
  retired_at INTEGER NOT NULL
);
-- No foreign key to channels: a bridge outlives a channel removed behind its
-- back (spec 6.1b); the runner cleans it up.
CREATE TABLE bridges (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL UNIQUE,
  channel_name TEXT NOT NULL,
  scope TEXT NOT NULL,
  slack_channel_id TEXT NOT NULL DEFAULT '',
  slack_name TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('creating', 'live', 'archived')),
  cursor_ts TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  archived_at INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX bridges_slack_channel ON bridges (slack_channel_id) WHERE slack_channel_id <> '';
CREATE TABLE bridge_messages (
  bridge_id INTEGER NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  slack_ts TEXT NOT NULL,
  root_ts TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (bridge_id, seq),
  UNIQUE (bridge_id, slack_ts)
);
CREATE INDEX bridge_messages_root ON bridge_messages (bridge_id, root_ts);
CREATE TABLE slack_users (
  identity TEXT PRIMARY KEY REFERENCES identities(name) ON DELETE CASCADE,
  team_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE (team_id, user_id)
);
-- Slack messages dropped as unauthorized; a drop is final and goes with its
-- bridge, so with the scope (Decision 15).
CREATE TABLE bridge_dropped (
  bridge_id INTEGER NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
  slack_ts TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (bridge_id, slack_ts)
);
INSERT INTO identities (name, is_admin, tags_acked_seq, created_at)
  VALUES ('slack-bridge', FALSE, 0, CAST(strftime('%s', 'now') AS INTEGER) * 1000)
  ON CONFLICT (name) DO NOTHING;
-- A pre-existing row of that name (plan choice, Slack bridge plan Task 1) is
-- made the reserved shape: a non-admin agent with no keys, sessions,
-- subscriptions, tokens, binding or access reference.
UPDATE identities SET is_admin = FALSE, kind = 'agent', scope = NULL,
  created_at = CASE WHEN created_at > 0 THEN created_at ELSE CAST(strftime('%s', 'now') AS INTEGER) * 1000 END
  WHERE name = 'slack-bridge';
DELETE FROM identity_keys WHERE identity = 'slack-bridge';
DELETE FROM sessions WHERE identity = 'slack-bridge';
DELETE FROM subscriptions WHERE identity = 'slack-bridge';
DELETE FROM tag_subscriptions WHERE identity = 'slack-bridge';
DELETE FROM enrollment_tokens WHERE identity = 'slack-bridge';
DELETE FROM human_bindings WHERE identity = 'slack-bridge';
DELETE FROM access_refs WHERE ref_kind = 'identity' AND value = 'slack-bridge';
