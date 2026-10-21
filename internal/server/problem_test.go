package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func TestStatusForEveryCode(t *testing.T) {
	want := map[bus.Code]int{
		bus.CodeValidation: 400, bus.CodeUnauthenticated: 401, bus.CodeForbidden: 403,
		bus.CodeNotRegistered: 403, bus.CodeNotFound: 404, bus.CodeConflict: 409,
		bus.CodeReplaced: 409, bus.CodeSessionLapsed: 410, bus.CodeSessionClosed: 410,
		bus.CodeRateLimited: 429, bus.CodeUnavailable: 503, bus.CodeInternal: 500,
	}
	if len(want) != 12 {
		t.Fatalf("table has %d codes, want 12", len(want))
	}
	for code, status := range want {
		if got := StatusFor(code); got != status {
			t.Errorf("StatusFor(%s) = %d, want %d", code, got, status)
		}
	}
	if got := StatusFor("bogus"); got != 500 {
		t.Errorf("StatusFor(unknown) = %d, want 500", got)
	}
}

func TestValidatorCode(t *testing.T) {
	for status, want := range map[int]bus.Code{
		400: bus.CodeValidation, 422: bus.CodeValidation, 401: bus.CodeUnauthenticated,
		404: bus.CodeNotFound, 500: bus.CodeInternal, 413: bus.CodeInternal, 418: bus.CodeInternal,
	} {
		if got := validatorCode(status); got != want {
			t.Errorf("validatorCode(%d) = %s, want %s", status, got, want)
		}
	}
}

func TestProblemForMapsBusErrors(t *testing.T) {
	status, p, write := ProblemFor(&bus.Error{Code: bus.CodeRateLimited, Message: "slow", Retryable: true})
	if !write || status != 429 || p.Code != gen.ProblemCode("rate_limited") || p.Detail == nil || *p.Detail != "slow" || !p.Retryable {
		t.Fatalf("got %d %+v write=%v", status, p, write)
	}
	status, p, write = ProblemFor(errors.New("disk"))
	if !write || status != 500 || p.Code != gen.ProblemCode("internal") || !p.Retryable || *p.Detail != "disk" {
		t.Fatalf("got %d %+v write=%v", status, p, write)
	}
}

func TestProblemForContextErrorsWriteNothing(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.Join(errors.New("x"), context.Canceled)} {
		if _, _, write := ProblemFor(err); write {
			t.Errorf("ProblemFor(%v) write = true", err)
		}
	}
}

func TestWriteProblemShape(t *testing.T) {
	detail := strings.Repeat("d", 2000)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/things", nil)
	WriteProblem(rec, r, 409, bus.CodeConflict, detail, false)
	if rec.Code != 409 {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "urn:agentbus2:problem:conflict", "title": "Conflict", "status": float64(409),
		"detail": detail, "instance": "/v1/things", "code": "conflict", "retryable": false,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("member %s = %v, want %v", k, m[k], v)
		}
	}
}
