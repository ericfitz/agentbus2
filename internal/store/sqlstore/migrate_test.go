package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

func steps(versions ...int) []Migration {
	var m []Migration
	for _, v := range versions {
		m = append(m, Migration{Version: v, Up: func(context.Context, *sqlx.Tx) error { return nil }})
	}
	return m
}

func TestValidateMigrations(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  []Migration
		ok   bool
	}{
		{"one", steps(1), true},
		{"three", steps(1, 2, 3), true},
		{"empty", nil, false},
		{"starts at 2", steps(2, 3), false},
		{"starts at 0", steps(0, 1), false},
		{"gap", steps(1, 3), false},
		{"duplicate", steps(1, 1, 2), false},
		{"out of order", steps(2, 1), false},
		{"nil Up", []Migration{{Version: 1}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMigrations(tc.set)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && !errors.Is(err, store.ErrSchemaInvalid) {
				t.Fatalf("err = %v, want ErrSchemaInvalid", err)
			}
		})
	}
}
