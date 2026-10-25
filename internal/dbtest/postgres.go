package dbtest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// postgresSource resolves the Postgres DSN once per test binary: from
// PostgresDSNEnv when set and non-empty, otherwise from a testcontainers
// container. A failed resolution is stored and fails every later request; it
// never turns into a skip.
type postgresSource struct {
	lookupEnv    func(string) (string, bool)
	start        func(ctx context.Context) (dsn string, stop func(), err error)
	startTimeout time.Duration // bound on start; 2*time.Minute in defaultPostgres

	once      sync.Once
	dsn, from string // from is PostgresDSNEnv or "testcontainers"
	stop      func()
	err       error
}

var defaultPostgres = &postgresSource{
	lookupEnv:    os.LookupEnv,
	start:        startPostgresContainer,
	startTimeout: 2 * time.Minute,
}

func (s *postgresSource) resolve(ctx context.Context) (dsn, from string, err error) {
	s.once.Do(func() {
		if v, ok := s.lookupEnv(PostgresDSNEnv); ok && v != "" {
			s.dsn, s.from = v, PostgresDSNEnv
			return
		}
		sctx, cancel := context.WithTimeout(ctx, s.startTimeout)
		defer cancel()
		d, stop, serr := s.start(sctx)
		if serr != nil {
			s.err = fmt.Errorf("postgres: %s is unset and testcontainers could not start %s (start Docker, or set the variable to a reachable server): %w",
				PostgresDSNEnv, PostgresImage, serr)
			return
		}
		s.dsn, s.from, s.stop = d, "testcontainers", stop
	})
	return s.dsn, s.from, s.err
}

func startPostgresContainer(ctx context.Context) (string, func(), error) {
	ctr, err := postgres.Run(ctx, PostgresImage,
		postgres.WithDatabase("agentbus2"),
		postgres.WithUsername("agentbus2"),
		postgres.WithPassword("agentbus2"),
		postgres.BasicWaitStrategies(),
		// The module's command starts with "postgres"; Chainguard's entrypoint already ends with it, so the
		// server would get "postgres postgres -c fsync=off" and exit. The bare script takes the command as is.
		testcontainers.WithEntrypoint("/usr/bin/docker-entrypoint.sh"),
	)
	if err != nil {
		// Run can return a created container together with the error.
		_ = testcontainers.TerminateContainer(ctr)
		return "", nil, err
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return "", nil, err
	}
	return dsn, func() { _ = ctr.Terminate(context.Background()) }, nil
}
