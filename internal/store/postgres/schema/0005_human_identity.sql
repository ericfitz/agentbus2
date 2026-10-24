-- Federated identity (spec 6.2): an identity kind (agent or human), the
-- human bindings (claim type and value, plus the provider subject learned at
-- first sign-in), the users and admins reference lists, and the sender kind of
-- a message. Existing admin flags become {admins, identity, "", NAME} entries.
--
-- State: runStep locks, re-reads the version and runs the step in one
-- transaction, so a second open skips it, two overlapping opens run it once,
-- and a failed run rolls back to version 4 (DDL is transactional on both
-- engines) and the next open reruns it from clean.
ALTER TABLE identities ADD COLUMN kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'human'));
ALTER TABLE messages ADD COLUMN sender_kind TEXT NOT NULL DEFAULT 'agent' CHECK (sender_kind IN ('agent', 'human'));
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
INSERT INTO access_refs (ref_list, ref_kind, issuer, value, created_at)
  SELECT 'admins', 'identity', '', name, created_at FROM identities WHERE is_admin;
