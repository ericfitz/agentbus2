package remote

import (
	"context"
	"errors"
	"fmt"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

// ErrNotImplemented marks the Admin methods Milestone 5 Task 7 replaces.
var ErrNotImplemented = errors.New("remote: not implemented until Milestone 5 Task 7")

// Reset is a stub replaced by Milestone 5 Task 7.
func (b *Bus) Reset(context.Context, ab2bus.ResetInput) error {
	return fmt.Errorf("remote.Reset: %w", ErrNotImplemented)
}

// ResetCredential is a stub replaced by Milestone 5 Task 7.
func (b *Bus) ResetCredential(context.Context, ab2bus.ResetCredentialInput) (ab2bus.ResetCredentialResult, error) {
	return ab2bus.ResetCredentialResult{}, fmt.Errorf("remote.ResetCredential: %w", ErrNotImplemented)
}
