package tui

import (
	"strings"
	"testing"
	"time"
)

// ADR 0018: the status bar says "failing" when the last embedding pass (in
// whichever process ran it) failed, not "ok" from the TUI's own queries. The
// embedding state is an administrator view (Decision 73): a non-admin's bar
// carries none of it. The health overlay's half of v1's test (the error, when
// it happened, the rejected count) moves to Task 7's health_test.go.
func TestEmbeddingFailureShownInStatusBar(t *testing.T) {
	f := newFixture(t)
	if bar := stripANSI(f.m.renderStatusBar()); strings.Contains(bar, "embed") {
		t.Fatalf("a non-admin's status bar shows embedding state: %q", bar)
	}
	setAdmin(t, f.core, "eric")
	f.run(f.m.statusCmd())
	if !f.m.statusOK {
		t.Fatalf("setup: statusOK false, err %v", f.m.statusErr)
	}
	if bar := stripANSI(f.m.renderStatusBar()); !strings.Contains(bar, "embed ok") {
		t.Fatalf("healthy status bar: %q", bar)
	}
	f.m.status.EmbeddingError = "embeddings endpoint returned 401 Unauthorized (no API key in this process)"
	f.m.status.EmbeddingErrorAt = time.Now().UnixMilli()
	f.m.status.EmbeddingRejected = 2
	f.m.width = 100
	if bar := stripANSI(f.m.renderStatusBar()); !strings.Contains(bar, "embed failing") {
		t.Fatalf("failing status bar: %q", bar)
	}
	f.m.search.semanticDown = true
	f.m.status.EmbeddingError = ""
	if bar := stripANSI(f.m.renderStatusBar()); !strings.Contains(bar, "embed unreachable") {
		t.Fatalf("unreachable status bar: %q", bar)
	}
}
