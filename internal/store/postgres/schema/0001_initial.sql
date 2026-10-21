CREATE TABLE seq_counter (id INTEGER PRIMARY KEY CHECK (id = 1), v BIGINT NOT NULL);
INSERT INTO seq_counter (id, v) VALUES (1, 0);
CREATE TABLE leases (name TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at BIGINT NOT NULL);
INSERT INTO leases (name, owner, expires_at) VALUES ('maintenance', '', 0);
CREATE TABLE notices (kind TEXT PRIMARY KEY, message TEXT NOT NULL, set_at BIGINT NOT NULL);
CREATE TABLE identities (
  name TEXT PRIMARY KEY,
  is_admin BOOLEAN NOT NULL DEFAULT FALSE,
  tags_acked_seq BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL
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
  created_at BIGINT NOT NULL
);
CREATE TABLE channel_acl (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  identity TEXT NOT NULL,
  can_read BOOLEAN NOT NULL,
  can_write BOOLEAN NOT NULL,
  can_admin BOOLEAN NOT NULL,
  CHECK (can_read OR can_write OR can_admin)
);
CREATE INDEX channel_acl_channel_identity ON channel_acl(channel_id, identity);
CREATE TABLE subscriptions (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  acked_seq BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
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
  fts_content tsvector GENERATED ALWAYS AS (to_tsvector('simple', regexp_replace(content, '[^[:alnum:]]+', ' ', 'g'))) STORED
);
CREATE INDEX messages_channel_seq ON messages(channel_id, seq);
CREATE INDEX messages_memory ON messages(memory_id, tombstone);
CREATE INDEX messages_tombstone_at ON messages(tombstone_at);
CREATE INDEX messages_created_at ON messages(created_at);
CREATE INDEX messages_fts_subject ON messages USING GIN (fts_subject);
CREATE INDEX messages_fts_content ON messages USING GIN (fts_content);
CREATE TABLE message_tags (
  seq BIGINT NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  tag TEXT COLLATE "C" NOT NULL,
  PRIMARY KEY (seq, tag)
);
CREATE INDEX message_tags_tag_seq ON message_tags(tag, seq);
CREATE TABLE embeddings (
  seq BIGINT PRIMARY KEY REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  vector BYTEA NOT NULL
);
CREATE TABLE embed_failures (
  seq BIGINT NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  error TEXT NOT NULL,
  failed_at BIGINT NOT NULL,
  PRIMARY KEY (seq, model)
);
CREATE TABLE memory_access (memory_id BIGINT PRIMARY KEY, accessed_at BIGINT NOT NULL);
CREATE TABLE receipts (
  identity TEXT NOT NULL,
  key TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  result TEXT NOT NULL,
  expires_at BIGINT NOT NULL,
  PRIMARY KEY (identity, key)
);
CREATE INDEX receipts_expires ON receipts(expires_at);
