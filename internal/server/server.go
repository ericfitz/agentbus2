package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/gen/spec"
)

// Options configure New.
type Options struct {
	Auth    Authenticator // nil: RefuseAll
	Version string        // "": version.Version
	Issuers []string      // external issuers for discover; nil until Milestone 5
	Logger  *slog.Logger  // nil: discard
}

var (
	specOnce sync.Once
	specDoc  *openapi3.T
)

// Spec is the embedded OpenAPI document, loaded once with Servers cleared so
// the validator matches any host. It panics on a load error: that is a build
// defect, not a runtime condition.
func Spec() *openapi3.T {
	specOnce.Do(func() {
		doc, err := spec.GetSpec()
		if err != nil {
			panic(fmt.Sprintf("server: embedded OpenAPI document: %v", err))
		}
		doc.Servers = nil
		specDoc = doc
	})
	return specDoc
}

// New builds the handler. Chain, outermost first: limitBody, auth, validator
// (security and request validation), then the generated mux over the strict
// handler.
func New(c *core.Core, o Options) http.Handler {
	if o.Auth == nil {
		o.Auth = RefuseAll{}
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	a := newAPI(c, o)

	strict := gen.NewStrictHandlerWithOptions(a, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteProblem(w, r, http.StatusBadRequest, bus.CodeValidation, err.Error(), false)
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if _, _, write := ProblemFor(err); !write {
				log.Debug("client gone; nothing written", "path", r.URL.Path, "err", err)
				return
			}
			responseError(w, r, err)
		},
	})
	mux := gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter: http.NewServeMux(),
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteProblem(w, r, http.StatusBadRequest, bus.CodeValidation, err.Error(), false)
		},
	})

	validator := nethttpmiddleware.OapiRequestValidatorWithOptions(Spec(), &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: func(ctx context.Context, _ *openapi3filter.AuthenticationInput) error {
				if IdentityFrom(ctx) == "" {
					return errors.New("no identity")
				}
				return nil
			},
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, r *http.Request, opts nethttpmiddleware.ErrorHandlerOpts) {
			WriteProblem(w, r, opts.StatusCode, validatorCode(opts.StatusCode), err.Error(), false)
		},
	})

	limit := int64(c.Cfg.MaxMessageKiB)*1024 + 1<<20
	return limitBody(authenticate(o.Auth, validator(mux)), limit)
}

// authenticate always writes the identity key (possibly "") after calling the
// Authenticator and records the request's base URL for discovery. An
// Authenticate error is a 401 problem.
func authenticate(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Authenticate(r)
		if err != nil {
			WriteProblem(w, r, http.StatusUnauthorized, bus.CodeUnauthenticated, err.Error(), false)
			return
		}
		ctx := WithIdentity(r.Context(), id)
		ctx = context.WithValue(ctx, baseURLKey{}, baseURL(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// limitBody reads the body ahead (choice 11): never http.MaxBytesReader and
// never r.ContentLength before reading, so a chunked body is judged too, and
// the validator and handlers see a bounded, replayable body.
func limitBody(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
			_ = r.Body.Close()
			if err != nil {
				WriteProblem(w, r, http.StatusBadRequest, bus.CodeValidation, "reading the request body: "+err.Error(), false)
				return
			}
			if int64(len(b)) > limit {
				WriteProblem(w, r, http.StatusRequestEntityTooLarge, bus.CodeValidation,
					fmt.Sprintf("request body exceeds %d bytes", limit), false)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
		}
		next.ServeHTTP(w, r)
	})
}
