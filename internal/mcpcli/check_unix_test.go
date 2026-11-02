//go:build !windows

package mcpcli

import (
	"strings"
	"testing"
)

func TestCheckCredentialModeFix(t *testing.T) {
	t.Chdir(t.TempDir())
	base := startServer(t)
	environ, _ := testEnviron(t)
	key := writeFreshKey(t, 0o644)
	code, out, errs := run([]string{"check", "-server", base, "-identity", "pat", "-credential", key}, environ)
	got := lines(out)
	if code != 1 || len(got) != 4 || !strings.HasPrefix(got[3], "FAIL credential: ") || !strings.HasSuffix(got[3], "fix: chmod 600 "+key) {
		t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
	}
}
