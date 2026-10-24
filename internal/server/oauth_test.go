package server

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
)

func TestOAuthFromBus(t *testing.T) {
	cases := []struct {
		code     bus.Code
		exchange bool
		status   int
		want     string
	}{
		{bus.CodeUnauthenticated, false, http.StatusUnauthorized, "invalid_client"},
		{bus.CodeValidation, false, http.StatusBadRequest, "invalid_request"},
		{bus.CodeForbidden, false, http.StatusInternalServerError, "server_error"},
		{bus.CodeConflict, false, http.StatusInternalServerError, "server_error"},
		{bus.CodeForbidden, true, http.StatusBadRequest, exchangeDeniedCode},
		{bus.CodeUnauthenticated, true, http.StatusBadRequest, "invalid_request"},
		{bus.CodeConflict, true, http.StatusBadRequest, "invalid_request"},
		{bus.CodeValidation, true, http.StatusBadRequest, "invalid_request"},
		{bus.CodeUnavailable, false, http.StatusServiceUnavailable, "temporarily_unavailable"},
		{bus.CodeUnavailable, true, http.StatusServiceUnavailable, "temporarily_unavailable"},
		{bus.CodeInternal, true, http.StatusInternalServerError, "server_error"},
		{bus.CodeNotFound, false, http.StatusInternalServerError, "server_error"},
	}
	for _, tc := range cases {
		got := oauthFromBus(core.E(tc.code, "detail"), tc.exchange)
		if got.Status != tc.status || got.Code != tc.want {
			t.Errorf("%s exchange=%v: got %d %s, want %d %s", tc.code, tc.exchange, got.Status, got.Code, tc.status, tc.want)
		}
	}
	// A server_error never carries the underlying text.
	if got := oauthFromBus(errors.New("db password=hunter2"), false); got.Code != "server_error" || got.Description != "internal error" {
		t.Errorf("plain error: %+v", got)
	}
}
