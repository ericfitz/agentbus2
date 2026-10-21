package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/gen"
)

var errAuthNotImplemented = errors.New("not implemented until Milestone 5 Task 6")

// Token is a stub replaced by Milestone 5 Task 6.
func (a *api) Token(context.Context, gen.TokenRequestObject) (gen.TokenResponseObject, error) {
	return nil, fmt.Errorf("server.Token: %w", errAuthNotImplemented)
}

// Jwks is a stub replaced by Milestone 5 Task 6.
func (a *api) Jwks(context.Context, gen.JwksRequestObject) (gen.JwksResponseObject, error) {
	return nil, fmt.Errorf("server.Jwks: %w", errAuthNotImplemented)
}

// Register is a stub replaced by Milestone 5 Task 6.
func (a *api) Register(context.Context, gen.RegisterRequestObject) (gen.RegisterResponseObject, error) {
	return nil, fmt.Errorf("server.Register: %w", errAuthNotImplemented)
}
