CREATE TABLE seq_counter (id INTEGER PRIMARY KEY CHECK (id = 1), v INTEGER NOT NULL);
INSERT INTO seq_counter (id, v) VALUES (1, 0);
CREATE TABLE leases (name TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at INTEGER NOT NULL);
INSERT INTO leases (name, owner, expires_at) VALUES ('maintenance', '', 0);
CREATE TABLE notices (kind TEXT PRIMARY KEY, message TEXT NOT NULL, set_at INTEGER NOT NULL);
CREATE TABLE identities (
  name TEXT PRIMARY KEY,
  is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
  tags_acked_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE identity_keys (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  kid TEXT NOT NULL,
  algorithm TEXT NOT NULL CHECK (algorithm IN ('EdDSA', 'ES256')),
  public_key BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (identity, kid)
);
CREATE TABLE channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('ordinary', 'memory', 'tasks')),
  creator TEXT NOT NULL DEFAULT '',
  created_seq INTEGER NOT NULL,
  evicted_before_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE channel_acl (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  identity TEXT NOT NULL,
  can_read INTEGER NOT NULL CHECK (can_read IN (0, 1)),
  can_write INTEGER NOT NULL CHECK (can_write IN (0, 1)),
  can_admin INTEGER NOT NULL CHECK (can_admin IN (0, 1)),
  CHECK (can_read = 1 OR can_write = 1 OR can_admin = 1)
);
CREATE INDEX channel_acl_channel_identity ON channel_acl(channel_id, identity);
CREATE TABLE subscriptions (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  acked_seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (identity, channel_id)
);
CREATE TABLE tag_subscriptions (
  identity TEXT NOT NULL REFERENCES identities(name) ON DELETE CASCADE,
  tags_key TEXT NOT NULL,
  created_seq INTEGER NOT NULL,
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
  opened_at INTEGER NOT NULL,
  lease_expires_at INTEGER NOT NULL,
  closed_at INTEGER NOT NULL DEFAULT 0,
  pending_token TEXT NOT NULL DEFAULT '',
  tags_cursor_seq INTEGER NOT NULL DEFAULT 0,
  tags_pending_end_seq INTEGER NOT NULL DEFAULT 0,
  watch_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_identity ON sessions(identity);
CREATE INDEX sessions_lease ON sessions(lease_expires_at);
CREATE TABLE session_cursors (
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  cursor_seq INTEGER NOT NULL,
  pending_end_seq INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (session_id, channel_id)
);
CREATE TABLE messages (
  seq INTEGER PRIMARY KEY,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  sender TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL,
  reply_to INTEGER NULL,
  metadata TEXT NULL,
  memory_id INTEGER NULL,
  revision INTEGER NULL,
  tombstone INTEGER NOT NULL DEFAULT 0 CHECK (tombstone IN (0, 1)),
  tombstone_at INTEGER NULL
);
CREATE INDEX messages_channel_seq ON messages(channel_id, seq);
CREATE INDEX messages_memory ON messages(memory_id, tombstone);
CREATE INDEX messages_tombstone_at ON messages(tombstone_at);
CREATE INDEX messages_created_at ON messages(created_at);
CREATE VIRTUAL TABLE messages_fts USING fts5(subject, content, content='messages', content_rowid='seq', tokenize='unicode61 remove_diacritics 0');
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, subject, content) VALUES (new.seq, new.subject, new.content);
END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, subject, content) VALUES ('delete', old.seq, old.subject, old.content);
END;
CREATE TABLE message_tags (
  seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  PRIMARY KEY (seq, tag)
);
CREATE INDEX message_tags_tag_seq ON message_tags(tag, seq);
CREATE TABLE embeddings (
  seq INTEGER PRIMARY KEY REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  vector BLOB NOT NULL
);
CREATE TABLE embed_failures (
  seq INTEGER NOT NULL REFERENCES messages(seq) ON DELETE CASCADE,
  model TEXT NOT NULL,
  error TEXT NOT NULL,
  failed_at INTEGER NOT NULL,
  PRIMARY KEY (seq, model)
);
CREATE TABLE memory_access (memory_id INTEGER PRIMARY KEY, accessed_at INTEGER NOT NULL);
CREATE TABLE receipts (
  identity TEXT NOT NULL,
  key TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  result TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (identity, key)
);
CREATE INDEX receipts_expires ON receipts(expires_at);
