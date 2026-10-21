// Package sqlstore is the one implementation of the store contract, written
// once in portable SQL over SQLite and Postgres (Decision 25).
package sqlstore

import _ "github.com/jmoiron/sqlx" // settles go.mod; the framework arrives in Task 2
