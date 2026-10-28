package signingkeys

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
)

// outageProvider is an external provider whose Next fails with err.
type outageProvider struct {
	fakeProvider
	err error
}

func (p *outageProvider) Next(context.Context) (keyprovider.Key, error) {
	return keyprovider.Key{}, p.err
}

// Controller ruling 1: a provider that reports an outage (ErrUnavailable) or a
// canceled or expired context is retryable `unavailable`; anything else stays
// `internal`.
func TestPrepareRotationMapsOutageToUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bus.Code
	}{
		{"outage", fmt.Errorf("kms: %w", keyprovider.ErrUnavailable), bus.CodeUnavailable},
		{"canceled", context.Canceled, bus.CodeUnavailable},
		{"deadline", fmt.Errorf("sign: %w", context.DeadlineExceeded), bus.CodeUnavailable},
		{"bug", errors.New("nil pointer"), bus.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := newSvc(t)
			svc.Provider = &outageProvider{fakeProvider: fakeProvider{name: "awskms"}, err: tc.err}
			_, err := svc.PrepareRotation(context.Background())
			var be *bus.Error
			if !errors.As(err, &be) || be.Code != tc.want {
				t.Fatalf("err = %v, want code %s", err, tc.want)
			}
			if wantRetry := tc.want == bus.CodeUnavailable; wantRetry && !be.Retryable {
				t.Fatalf("%s: not retryable", be.Code)
			}
			// The same mapping through Rotate and EnsureActive (no rows).
			if _, err := svc.Rotate(context.Background(), RotateInput{}); !errors.As(err, &be) || be.Code != tc.want {
				t.Fatalf("Rotate err = %v", err)
			}
			if _, _, err := svc.EnsureActive(context.Background()); !errors.As(err, &be) || be.Code != tc.want {
				t.Fatalf("EnsureActive err = %v", err)
			}
		})
	}
}
