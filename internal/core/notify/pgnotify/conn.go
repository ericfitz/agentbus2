package pgnotify

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/ericfitz/agentbus2/internal/store/postgres"
)

type connector interface {
	Connect(ctx context.Context, dsn string) (conn, error)
}

type conn interface {
	// Channel runs postgres.ChannelQuery.
	Channel(ctx context.Context) (string, error)
	Listen(ctx context.Context, channel string) error
	// Wait blocks for one notification and returns its payload; the
	// notification's channel is ignored because only ours is subscribed.
	Wait(ctx context.Context) (payload string, err error)
	Close(ctx context.Context) error
}

type pgxConnector struct{ applicationName string }

func (p pgxConnector) Connect(ctx context.Context, dsn string) (conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["application_name"] = p.applicationName
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pgxConn{c}, nil
}

type pgxConn struct{ c *pgx.Conn }

func (p pgxConn) Channel(ctx context.Context) (string, error) {
	var name string
	err := p.c.QueryRow(ctx, postgres.ChannelQuery).Scan(&name)
	return name, err
}

func (p pgxConn) Listen(ctx context.Context, channel string) error {
	_, err := p.c.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize())
	return err
}

func (p pgxConn) Wait(ctx context.Context) (string, error) {
	n, err := p.c.WaitForNotification(ctx)
	if err != nil {
		return "", err
	}
	return n.Payload, nil
}

func (p pgxConn) Close(ctx context.Context) error { return p.c.Close(ctx) }
