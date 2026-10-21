// Package sqlstore is the one implementation of the store contract, written
// once in portable SQL over SQLite and Postgres (Decision 25). The engine
// packages (internal/store/sqlite, internal/store/postgres) supply a Dialect
// and one embedded migration set each.
package sqlstore
