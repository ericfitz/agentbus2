package bus

import (
	"context"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
)

func TestScaffold(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		db := e.Open(t)
		var one int
		if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("%s: SELECT 1 = %d, %v", e.Name, one, err)
		}
	})
}
