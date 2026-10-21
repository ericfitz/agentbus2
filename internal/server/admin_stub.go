package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/gen"
)

var errAdminNotImplemented = errors.New("not implemented until Milestone 5 Task 7")

// AdminReset is a stub replaced by Milestone 5 Task 7.
func (a *api) AdminReset(context.Context, gen.AdminResetRequestObject) (gen.AdminResetResponseObject, error) {
	return nil, fmt.Errorf("server.AdminReset: %w", errAdminNotImplemented)
}

// ResetCredential is a stub replaced by Milestone 5 Task 7.
func (a *api) ResetCredential(context.Context, gen.ResetCredentialRequestObject) (gen.ResetCredentialResponseObject, error) {
	return nil, fmt.Errorf("server.ResetCredential: %w", errAdminNotImplemented)
}
