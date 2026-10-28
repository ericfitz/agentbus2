package signingkeys

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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

// blockingProvider's Next blocks until its context ends and returns what it
// wraps (nil: a plain error that names no deadline).
type blockingProvider struct {
	fakeProvider
	plain bool
}

func (p *blockingProvider) Next(ctx context.Context) (keyprovider.Key, error) {
	<-ctx.Done()
	if p.plain {
		return keyprovider.Key{}, errors.New("provider gave up")
	}
	return keyprovider.Key{}, ctx.Err()
}

// Every Next runs under NextTimeout, whatever the backend: a provider that
// never answers fails the rotation, retryably, within the bound; a caller's own
// cancel is not turned into the bound.
func TestPrepareRotationBoundsNext(t *testing.T) {
	if NextTimeout != 30*time.Second {
		t.Fatalf("NextTimeout = %v, want 30s", NextTimeout)
	}
	old := nextTimeout
	nextTimeout = 150 * time.Millisecond
	t.Cleanup(func() { nextTimeout = old })
	for _, plain := range []bool{false, true} {
		svc, _, _ := newSvc(t)
		svc.Provider = &blockingProvider{fakeProvider: fakeProvider{name: "awskms"}, plain: plain}
		start := time.Now()
		_, err := svc.PrepareRotation(context.Background())
		var be *bus.Error
		if !errors.As(err, &be) || be.Code != bus.CodeUnavailable || !be.Retryable {
			t.Fatalf("plain=%v: err = %v", plain, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("plain=%v: took %v", plain, time.Since(start))
		}
		// The same bound through Rotate and EnsureActive (no rows).
		if _, err := svc.Rotate(context.Background(), RotateInput{}); !errors.As(err, &be) || be.Code != bus.CodeUnavailable {
			t.Fatalf("Rotate err = %v", err)
		}
		if _, _, err := svc.EnsureActive(context.Background()); !errors.As(err, &be) || be.Code != bus.CodeUnavailable {
			t.Fatalf("EnsureActive err = %v", err)
		}
	}
	// A caller that cancels first is not a timeout of ours: the plain error
	// stays internal.
	svc, _, _ := newSvc(t)
	svc.Provider = &blockingProvider{fakeProvider: fakeProvider{name: "awskms"}, plain: true}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := svc.PrepareRotation(ctx)
	var be *bus.Error
	if !errors.As(err, &be) || be.Code != bus.CodeInternal {
		t.Fatalf("caller cancel: err = %v", err)
	}
}
