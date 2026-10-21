CREATE TABLE assertion_jti (
  jti TEXT PRIMARY KEY,
  expires_at BIGINT NOT NULL
);
CREATE INDEX assertion_jti_expires ON assertion_jti (expires_at);
