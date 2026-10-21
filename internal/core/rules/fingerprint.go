package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Fingerprint hashes the canonical JSON of payload. encoding/json sorts map
// keys, so equivalent inputs with different key order hash the same.
func Fingerprint(payload any) (string, error) {
	j, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(j)
	return hex.EncodeToString(h[:]), nil
}
