package sqlstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/store"
)

func TestCheckText(t *testing.T) {
	if err := checkText("session", "id", "s1", "hint", ""); err != nil {
		t.Errorf("checkText(valid) = %v, want nil", err)
	}
	for _, bad := range []string{"h\xff", "h\x00"} {
		err := checkText("session", "id", "s1", "hint", bad, "watch id", "w\x00")
		if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "session hint ") {
			t.Errorf("checkText(hint %q) = %v, want ErrInvalid naming session hint", bad, err)
		}
	}
}

func TestBadText(t *testing.T) {
	if badText() || badText("a", "") {
		t.Error("badText(valid) = true")
	}
	if !badText("a", "b\xff") || !badText("b\x00") {
		t.Error("badText(non-UTF-8 or NUL) = false")
	}
}

func TestTextArgs(t *testing.T) {
	if err := textArgs([]any{int64(1), "ok", []byte{0xff, 0}, nil}); err != nil {
		t.Errorf("textArgs(valid) = %v, want nil (bytes are not text)", err)
	}
	for _, bad := range []string{"x\xff", "x\x00"} {
		err := textArgs([]any{int64(1), "ok", bad})
		if !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "query argument 3 ") {
			t.Errorf("textArgs(%q) = %v, want ErrInvalid naming argument 3", bad, err)
		}
	}
}
