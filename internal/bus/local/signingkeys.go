package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

// The signing key operations: local mode has no signing keys (it mints no
// bearers), so both answer unavailable.
func errSigningKeysNotConfigured() error {
	return ab2bus.Errorf(ab2bus.CodeUnavailable, false, "%s", ab2bus.SigningKeysNotConfigured)
}

// ListSigningKeys answers unavailable.
func (b *Bus) ListSigningKeys(context.Context) ([]ab2bus.SigningKey, error) {
	return nil, errSigningKeysNotConfigured()
}

// RotateSigningKey answers unavailable.
func (b *Bus) RotateSigningKey(context.Context, ab2bus.RotateSigningKeyInput) (ab2bus.SigningKeyRotation, error) {
	return ab2bus.SigningKeyRotation{}, errSigningKeysNotConfigured()
}
