package access

import (
	"os"
	"testing"

	"github.com/ericfitz/agentmesh/internal/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }
