-- Schema version 1: the whole schema. The migrations of the pre-release
-- builds (their versions 1 to 6) were flattened into this step before the
-- first release (Eric's decision 2026-10-25, ADR 0004 addendum); a database
-- created by one of those builds is refused, not converted.
--
-- State: runStep locks, re-reads the version and runs the step in one
-- transaction, so a second open skips it, two overlapping opens run it once,
-- and a failed run rolls back to version 0 (DDL is transactional on both
-- engines) and the next open reruns it from clean.
CREATE TABLE seq_counter (id INTEGER PRIMARY KEY CHECK (id = 1), v BIGINT NOT NULL);
INSERT INTO seq_counter (id, v) VALUES (1, 0);
CREATE TABLE leases (name TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at BIGINT NOT NULL);
INSERT INTO leases (name, owner, expires_at) VALUES ('maintenance', '', 0);
CREATE TABLE notices (kind TEXT PRIMARY KEY, message TEXT NOT NULL, set_at BIGINT NOT NULL);
CREATE TABLE scopes (
  name TEXT PRIMARY KEY,
  state TEXT NOT NULL CHECK (state IN ('open', 'done', 'retired')),
  retire_mode TEXT NOT NULL DEFAULT '' CHECK (retire_mode IN ('', 'with_bridge', 'normal')),
  created_at BIGINT NOT NULL,
  done_at BIGINT NOT NULL DEFAULT 0,
  retired_at BIGINT NOT NULL DEFAULT 0,
  last_activity BIGINT NOT NULL
);
CREATE TABLE identities (
  name TEXT PRIMARY KEY,
  is_admin BOOLEAN NOT NULL DEFAULT FALSE,
  tags_acked_seq BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'human')),
  scope TEXT NULL REFERENCES scopes(name)
);
CREATE TABLE identity_keys (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  kid TEXT NOT NULL,
  algorithm TEXT NOT NULL CHECK (algorithm IN ('EdDSA', 'ES256')),
  public_key BYTEA NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (identity, kid)
);
CREATE TABLE channels (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('ordinary', 'memory', 'tasks')),
  creator TEXT NOT NULL DEFAULT '',
  created_seq BIGINT NOT NULL,
  evicted_before_seq BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  last_read_at BIGINT NOT NULL DEFAULT 0,
  scope TEXT NULL REFERENCES scopes(name),
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(name) + octet_length(kind) + octet_length(creator) + coalesce(octet_length(scope), 0)) STORED
);
CREATE TABLE channel_acl (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  identity TEXT NOT NULL,
  can_read BOOLEAN NOT NULL,
  can_write BOOLEAN NOT NULL,
  can_admin BOOLEAN NOT NULL,
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(identity)) STORED,
  CHECK (can_read OR can_write OR can_admin)
);
CREATE INDEX channels_scope ON channels (scope);
CREATE INDEX identities_scope ON identities (scope);
CREATE INDEX channel_acl_channel_identity ON channel_acl(channel_id, identity);
CREATE TABLE subscriptions (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  acked_seq BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(identity)) STORED,
  PRIMARY KEY (identity, channel_id)
);
CREATE TABLE tag_subscriptions (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  tags_key TEXT NOT NULL,
  created_seq BIGINT NOT NULL,
  PRIMARY KEY (identity, tags_key)
);
CREATE TABLE tag_subscription_tags (
  identity TEXT NOT NULL,
  tags_key TEXT NOT NULL,
  tag TEXT NOT NULL,
  lo TEXT NOT NULL,
  hi TEXT NOT NULL,
  PRIMARY KEY (identity, tags_key, tag),
  FOREIGN KEY (identity, tags_key) REFERENCES tag_subscriptions(identity, tags_key) ON DELETE CASCADE
);
CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  display_name TEXT NOT NULL,
  hint TEXT NOT NULL DEFAULT '',
  resume_token_hash TEXT NOT NULL UNIQUE,
  opened_at BIGINT NOT NULL,
  lease_expires_at BIGINT NOT NULL,
  closed_at BIGINT NOT NULL DEFAULT 0,
  pending_token TEXT NOT NULL DEFAULT '',
  tags_cursor_seq BIGINT NOT NULL DEFAULT 0,
  tags_pending_end_seq BIGINT NOT NULL DEFAULT 0,
  watch_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_identity ON sessions(identity);
CREATE INDEX sessions_lease ON sessions(lease_expires_at);
CREATE TABLE session_cursors (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  cursor_seq BIGINT NOT NULL,
  pending_end_seq BIGINT NOT NULL DEFAULT 0,
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(session_id)) STORED,
  PRIMARY KEY (session_id, channel_id)
);
CREATE TABLE messages (
  seq BIGINT PRIMARY KEY,
  channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  sender TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  reply_to BIGINT NULL,
  metadata TEXT NULL,
  memory_id BIGINT NULL,
  revision BIGINT NULL,
  tombstone BOOLEAN NOT NULL DEFAULT FALSE,
  tombstone_at BIGINT NULL,
  fts_subject tsvector GENERATED ALWAYS AS (to_tsvector('simple', regexp_replace(subject, '[^[:alnum:]]+', ' ', 'g'))) STORED,
  fts_content tsvector GENERATED ALWAYS AS (to_tsvector('simple', regexp_replace(content, '[^[:alnum:]]+', ' ', 'g'))) STORED,
  sender_kind TEXT NOT NULL DEFAULT 'agent' CHECK (sender_kind IN ('agent', 'human')),
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(sender) + octet_length(session_id) + octet_length(type) + octet_length(subject) + octet_length(content) + coalesce(octet_length(metadata), 0) + octet_length(sender_kind)) STORED
);
CREATE INDEX messages_channel_seq ON messages(channel_id, seq);
CREATE INDEX messages_memory ON messages(memory_id, tombstone);
CREATE INDEX messages_tombstone_at ON messages(tombstone_at);
CREATE INDEX messages_created_at ON messages(created_at);
-- size_bytes is STORED because Postgres 18 makes an unqualified generated column VIRTUAL, which cannot be indexed.
CREATE INDEX messages_size ON messages (seq) INCLUDE (size_bytes);
CREATE INDEX messages_fts_subject ON messages USING GIN (fts_subject);
CREATE INDEX messages_fts_content ON messages USING GIN (fts_content);
CREATE TABLE message_tags (
  seq BIGINT NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  tag TEXT COLLATE "C" NOT NULL,
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(tag)) STORED,
  PRIMARY KEY (seq, tag)
);
CREATE INDEX message_tags_tag_seq ON message_tags(tag, seq);
CREATE TABLE embeddings (
  seq BIGINT PRIMARY KEY REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  vector BYTEA NOT NULL,
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(model) + octet_length(vector)) STORED
);
CREATE TABLE embed_failures (
  seq BIGINT NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  error TEXT NOT NULL,
  failed_at BIGINT NOT NULL,
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(model) + octet_length(error)) STORED,
  PRIMARY KEY (seq, model)
);
CREATE TABLE memory_access (memory_id BIGINT PRIMARY KEY, accessed_at BIGINT NOT NULL, size_bytes BIGINT GENERATED ALWAYS AS (32) STORED);
CREATE TABLE receipts (
  identity TEXT NOT NULL,
  key TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  result TEXT NOT NULL,
  expires_at BIGINT NOT NULL,
  PRIMARY KEY (identity, key)
);
CREATE INDEX receipts_expires ON receipts(expires_at);
CREATE TABLE enrollment_tokens (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  identity TEXT NOT NULL DEFAULT '',
  expires_at BIGINT NOT NULL,
  uses_left INTEGER NOT NULL CHECK (uses_left >= 0),
  created_at BIGINT NOT NULL,
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(id) + octet_length(token_hash) + octet_length(identity)) STORED
);
CREATE TABLE signing_keys (
  kid TEXT PRIMARY KEY,
  state TEXT NOT NULL CHECK (state IN ('active', 'retiring')),
  provider TEXT NOT NULL CHECK (provider IN ('store', 'awskms', 'gcpkms', 'azurekms', 'hashivault')),
  provider_ref TEXT NOT NULL DEFAULT '',
  algorithm TEXT NOT NULL CHECK (algorithm IN ('EdDSA', 'ES256')),
  public_key BYTEA NOT NULL,
  private_key_pem TEXT,
  created_at BIGINT NOT NULL,
  retired_at BIGINT NOT NULL DEFAULT 0,
  remove_after BIGINT NOT NULL DEFAULT 0,
  CHECK ((provider = 'store') = (private_key_pem IS NOT NULL)),
  CHECK ((provider = 'store') = (provider_ref = '')),
  CHECK ((state = 'active') = (retired_at = 0))
);
CREATE UNIQUE INDEX signing_keys_one_active ON signing_keys (state) WHERE state = 'active';
CREATE TABLE human_bindings (
  identity TEXT PRIMARY KEY REFERENCES identities(name) ON DELETE CASCADE,
  issuer TEXT NOT NULL DEFAULT '',
  claim_type TEXT NOT NULL CHECK (claim_type IN ('email')),
  value TEXT NOT NULL,
  provider_subject TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  UNIQUE (claim_type, value)
);
CREATE UNIQUE INDEX human_bindings_subject ON human_bindings (issuer, provider_subject) WHERE provider_subject <> '';
CREATE TABLE access_refs (
  ref_list TEXT NOT NULL CHECK (ref_list IN ('users', 'admins')),
  ref_kind TEXT NOT NULL CHECK (ref_kind IN ('email', 'group', 'subject', 'identity')),
  issuer TEXT NOT NULL DEFAULT '',
  value TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (ref_list, ref_kind, issuer, value)
);
CREATE TABLE assertion_jti (
  identity TEXT NOT NULL,
  jti TEXT NOT NULL,
  expires_at BIGINT NOT NULL,
  PRIMARY KEY (identity, jti)
);
CREATE INDEX assertion_jti_expires ON assertion_jti (expires_at);
CREATE TABLE retired_identities (
  name TEXT PRIMARY KEY,
  scope TEXT NOT NULL,
  retired_at BIGINT NOT NULL
);
-- No foreign key to channels: a bridge outlives a channel removed behind its
-- back (spec 6.1b); the runner cleans it up.
CREATE TABLE bridges (
  id BIGSERIAL PRIMARY KEY,
  channel_id BIGINT NOT NULL UNIQUE,
  channel_name TEXT NOT NULL,
  scope TEXT NOT NULL,
  slack_channel_id TEXT NOT NULL DEFAULT '',
  slack_name TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('creating', 'live', 'archived')),
  cursor_ts TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  archived_at BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX bridges_slack_channel ON bridges (slack_channel_id) WHERE slack_channel_id <> '';
CREATE TABLE bridge_messages (
  bridge_id BIGINT NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
  seq BIGINT NOT NULL,
  slack_ts TEXT NOT NULL,
  root_ts TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  size_bytes BIGINT GENERATED ALWAYS AS (32 + octet_length(slack_ts) + octet_length(root_ts)) STORED,
  PRIMARY KEY (bridge_id, seq),
  UNIQUE (bridge_id, slack_ts)
);
CREATE INDEX bridge_messages_root ON bridge_messages (bridge_id, root_ts);
CREATE TABLE slack_users (
  identity TEXT PRIMARY KEY REFERENCES identities(name) ON DELETE CASCADE,
  team_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  UNIQUE (team_id, user_id)
);
-- Slack messages dropped as unauthorized; a drop is final and goes with its
-- bridge, so with the scope (Decision 15).
CREATE TABLE bridge_dropped (
  bridge_id BIGINT NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
  slack_ts TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (bridge_id, slack_ts)
);
-- The reserved slack-bridge identity: a non-admin agent, no scope, no keys.
INSERT INTO identities (name, is_admin, tags_acked_seq, created_at)
  VALUES ('slack-bridge', FALSE, 0, (EXTRACT(EPOCH FROM now()) * 1000)::BIGINT)
  ON CONFLICT (name) DO NOTHING;
