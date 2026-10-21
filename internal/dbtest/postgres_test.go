package dbtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const startFailedPrefix = "postgres: AGENTBUS2_TEST_POSTGRES_DSN is unset and testcontainers could not start postgres:17 (start Docker, or set the variable to a reachable server): "

func fakeSource(env string, set bool, start func(ctx context.Context) (string, func(), error)) *postgresSource {
	return &postgresSource{
		lookupEnv:    func(string) (string, bool) { return env, set },
		start:        start,
		startTimeout: time.Minute,
	}
}

func TestResolveUsesVariableWhenSet(t *testing.T) {
	s := fakeSource("postgres://x", true, func(context.Context) (string, func(), error) {
		t.Fatal("start must not be called")
		return "", nil, nil
	})
	dsn, from, err := s.resolve(context.Background())
	if err != nil || dsn != "postgres://x" || from != PostgresDSNEnv {
		t.Fatalf("got %q %q %v", dsn, from, err)
	}
}

func TestResolveTreatsEmptyAsUnset(t *testing.T) {
	calls := 0
	s := fakeSource("", true, func(context.Context) (string, func(), error) {
		calls++
		return "fake", func() {}, nil
	})
	dsn, from, err := s.resolve(context.Background())
	if err != nil || dsn != "fake" || from != "testcontainers" || calls != 1 {
		t.Fatalf("got %q %q %v calls=%d", dsn, from, err, calls)
	}
}

func TestResolveStartsOnce(t *testing.T) {
	calls := 0
	s := fakeSource("", false, func(context.Context) (string, func(), error) {
		calls++
		return "fake", func() {}, nil
	})
	for i := range 3 {
		dsn, _, err := s.resolve(context.Background())
		if err != nil || dsn != "fake" {
			t.Fatalf("call %d: %q %v", i, dsn, err)
		}
	}
	if calls != 1 {
		t.Fatalf("start called %d times", calls)
	}
}

func TestResolveWrapsStartFailure(t *testing.T) {
	cause := errors.New("no docker")
	s := fakeSource("", false, func(context.Context) (string, func(), error) { return "", nil, cause })
	_, _, err := s.resolve(context.Background())
	if err == nil || err.Error() != startFailedPrefix+"no docker" {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause not wrapped")
	}
	// A failed start fails every later call too; it is never a skip.
	if _, _, err2 := s.resolve(context.Background()); err2 == nil {
		t.Fatal("second resolve succeeded")
	}
}

func TestResolveStartIsBounded(t *testing.T) {
	s := fakeSource("", false, func(ctx context.Context) (string, func(), error) {
		<-ctx.Done()
		return "", nil, ctx.Err()
	})
	s.startTimeout = 50 * time.Millisecond
	begin := time.Now()
	_, _, err := s.resolve(context.Background())
	if time.Since(begin) > time.Second {
		t.Fatal("resolve did not return promptly")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if !strings.HasPrefix(err.Error(), startFailedPrefix) {
		t.Fatalf("got %v", err)
	}
}

func TestDefaultStartTimeout(t *testing.T) {
	if defaultPostgres.startTimeout != 2*time.Minute {
		t.Fatalf("got %v", defaultPostgres.startTimeout)
	}
}
