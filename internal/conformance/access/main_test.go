package access

import (
	"os"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }
