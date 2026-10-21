// Package pgnotify is the Postgres form of the wake seam (Decisions 50 and
// 52): a Listener holds one native pgx connection that LISTENs on the
// per-schema channel and republishes each notification as a notify.Hint. Hints
// are only wake-ups: a lost notification costs latency, never a message, so
// the listener reconnects with backoff and publishes a catch-up Hint{} after
// every successful LISTEN. The server starts and serves without it.
package pgnotify
