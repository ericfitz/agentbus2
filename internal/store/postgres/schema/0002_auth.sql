CREATE TABLE enrollment_tokens (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  identity TEXT NOT NULL DEFAULT '',
  expires_at BIGINT NOT NULL,
  uses_left INTEGER NOT NULL CHECK (uses_left >= 0),
  created_at BIGINT NOT NULL
);
CREATE TABLE signing_keys (
  kid TEXT PRIMARY KEY,
  singleton INTEGER NOT NULL DEFAULT 1 UNIQUE CHECK (singleton = 1),
  algorithm TEXT NOT NULL CHECK (algorithm IN ('EdDSA', 'ES256')),
  private_key_pem TEXT NOT NULL,
  created_at BIGINT NOT NULL
);
