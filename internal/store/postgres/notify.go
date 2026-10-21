package postgres

import (
	"context"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

// channelExpr names the LISTEN/NOTIFY channel of the current schema. A
// Postgres identifier is at most 63 bytes, so the name is a fixed 34 bytes
// ("agentbus2_" plus 24 hex digits of a SHA-256 of the schema name) however
// long the schema is; replicas on other schemas hear nothing (Decision 52).
const channelExpr = "'agentbus2_' || left(encode(sha256(convert_to(current_schema()::text, 'UTF8')), 'hex'), 24)"

// ChannelQuery yields the channel name; the listener runs it on its own
// connection and then LISTENs on the result.
const ChannelQuery = "SELECT " + channelExpr

// EncodePayload is the NOTIFY payload "<channelID>:<seq>"; (0, 0) is the
// control payload (a watch registration or an ACL change).
func EncodePayload(channelID, seq int64) string {
	return strconv.FormatInt(channelID, 10) + ":" + strconv.FormatInt(seq, 10)
}

// ParsePayload parses ^[0-9]+:[0-9]+$ with both parts fitting int64. Anything
// else reports ok false.
func ParsePayload(s string) (channelID, seq int64, ok bool) {
	a, b, found := strings.Cut(s, ":")
	if !found || !digits(a) || !digits(b) {
		return 0, 0, false
	}
	c, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	q, err := strconv.ParseInt(b, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return c, q, true
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// notifySQL sends one payload on the current schema's channel. pg_notify is
// transactional: the notification is delivered when the transaction commits
// and dropped when it rolls back (Decision 52).
const notifySQL = "SELECT pg_notify(" + channelExpr + ", $1)"

// NotifyAppend queues the append hint inside the append transaction.
func (dialect) NotifyAppend(ctx context.Context, tx *sqlx.Tx, channelID, seq int64) error {
	_, err := tx.ExecContext(ctx, notifySQL, EncodePayload(channelID, seq))
	return err
}

// NotifyControl queues the control hint (0, 0) that a watch registration
// sends, so an armed watch on another replica re-checks the Store.
func (dialect) NotifyControl(ctx context.Context, tx *sqlx.Tx) error {
	_, err := tx.ExecContext(ctx, notifySQL, EncodePayload(0, 0))
	return err
}
