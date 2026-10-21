package sqlite

import "testing"

func TestOpenSetsIncrementalAutoVacuum(t *testing.T) {
	t.Run("fresh file", func(t *testing.T) {
		path := newPath(t)
		mustOpen(t, path, Options{})
		if got := scalar[int](t, rawDB(t, path), "PRAGMA auto_vacuum"); got != 2 {
			t.Fatalf("auto_vacuum = %d, want 2", got)
		}
	})
	// A file holding a foreign table is refused by Open before any pragma
	// (application_id check), so the conversion path is exercised on an
	// agentbus2 file put back in mode 0 with an extra table whose rows must
	// survive the VACUUM.
	t.Run("converts a mode 0 file, second Open does not VACUUM", func(t *testing.T) {
		path := newPath(t)
		s := mustOpen(t, path, Options{})
		_ = s.Close()
		db := rawDB(t, path)
		execAll(t, db, "CREATE TABLE keep (x INTEGER)", "INSERT INTO keep VALUES (7)", "PRAGMA auto_vacuum = 0", "VACUUM")
		if got := scalar[int](t, db, "PRAGMA auto_vacuum"); got != 0 {
			t.Fatalf("precondition: auto_vacuum = %d, want 0", got)
		}
		s2 := mustOpen(t, path, Options{})
		if got := scalar[int](t, rawDB(t, path), "PRAGMA auto_vacuum"); got != 2 {
			t.Fatalf("auto_vacuum after Open = %d, want 2", got)
		}
		if got := scalar[int](t, db, "SELECT x FROM keep"); got != 7 {
			t.Fatalf("keep.x = %d, want 7", got)
		}
		if got := scalar[int](t, db, "SELECT count(*) FROM schema_meta"); got != 1 {
			t.Fatalf("schema_meta rows = %d", got)
		}
		before := scalar[int](t, db, "PRAGMA data_version")
		_ = s2.Close()
		mustOpen(t, path, Options{})
		if got := scalar[int](t, rawDB(t, path), "PRAGMA auto_vacuum"); got != 2 {
			t.Fatalf("auto_vacuum after second Open = %d, want 2", got)
		}
		if after := scalar[int](t, db, "PRAGMA data_version"); after != before {
			t.Fatalf("data_version %d -> %d: the second Open wrote (VACUUM?)", before, after)
		}
	})
}
