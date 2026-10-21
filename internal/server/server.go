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

	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/gen/spec"
)

// Options configure New.
type Options struct {
	Auth    Authenticator // nil: RefuseAll
	Version string        // "": version.Version
	Logger  *slog.Logger  // nil: discard

	// Registration is the mode the register handler enforces. "" is treated
	// as closed: registration is never open by omission.
	Registration identities.Mode
	// Issuer mints and publishes the server's own bearers. nil: the token
	// endpoint and JWKS answer 503 unavailable (local mode, Decision 36) and
	// openid-configuration falls back to the request's base URL.
	Issuer *token.Issuer
	// External are the configured external issuers (Decision 57); their
	// Issuer URLs are what discover lists.
	External []*token.External
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
				if err := authErrorFrom(ctx); err != nil {
					return err
				}
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

// authenticate never refuses by itself. It always writes the identity key
// (possibly "", and "" on error), stashes an Authenticate error under
// authErrorKey for the validator's AuthenticationFunc, and records the
// request's base URL for discovery. The 401 therefore comes from the
// validator and only for operations that require security: an open operation
// ignores a bad bearer.
func authenticate(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Authenticate(r)
		ctx := r.Context()
		if err != nil {
			id = ""
			ctx = context.WithValue(ctx, authErrorKey{}, err)
		}
		ctx = WithIdentity(ctx, id)
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
