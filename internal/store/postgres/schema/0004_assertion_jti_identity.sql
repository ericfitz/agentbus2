-- Post-build decision 4: the replay key is (identity, jti), so one identity
-- cannot consume another's jti. Existing rows are dropped: each lives about
-- two minutes (assertion exp plus leeway plus one second) and only blocks a
-- replay of an assertion that has already expired or will within that window.
DROP TABLE assertion_jti;
CREATE TABLE assertion_jti (
  identity TEXT NOT NULL,
  jti TEXT NOT NULL,
  expires_at BIGINT NOT NULL,
  PRIMARY KEY (identity, jti)
);
CREATE INDEX assertion_jti_expires ON assertion_jti (expires_at);
