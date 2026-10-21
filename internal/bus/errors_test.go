package bus

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorJSON(t *testing.T) {
	e := Errorf(CodeConflict, false, "name %q taken", "x")
	var got map[string]any
	if err := json.Unmarshal([]byte(e.Error()), &got); err != nil {
		t.Fatalf("Error() is not JSON: %v: %s", err, e.Error())
	}
	if len(got) != 3 || got["code"] != "conflict" || got["message"] != `name "x" taken` || got["retryable"] != false {
		t.Fatalf("members: %v", got)
	}
}

func TestTruncateErrorMessage(t *testing.T) {
	at := strings.Repeat("a", MaxErrorMessageBytes)
	if got := TruncateErrorMessage(at); got != at {
		t.Fatal("a message at the bound must be unchanged")
	}
	over := at + "b"
	got := TruncateErrorMessage(over)
	if len(got) != MaxErrorMessageBytes || !strings.HasSuffix(got, "…") {
		t.Fatalf("len %d, suffix %q", len(got), got[len(got)-3:])
	}
	if e := Errorf(CodeValidation, false, "%s", over); len(e.Message) > MaxErrorMessageBytes {
		t.Fatalf("Errorf did not truncate: %d", len(e.Message))
	}
}

func TestErrorfRejectsUnknownCode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Errorf(bogus) did not panic")
		}
	}()
	_ = Errorf(Code("bogus"), false, "x")
}

func TestValidCode(t *testing.T) {
	for _, c := range []Code{CodeValidation, CodeNotFound, CodeConflict, CodeForbidden, CodeNotRegistered, CodeRateLimited, CodeReplaced, CodeSessionLapsed, CodeSessionClosed, CodeUnavailable, CodeInternal} {
		if !ValidCode(c) {
			t.Errorf("%q rejected", c)
		}
	}
	for _, c := range []Code{"", "bogus", "Validation"} {
		if ValidCode(c) {
			t.Errorf("%q accepted", c)
		}
	}
}

func TestInternal(t *testing.T) {
	if Internal(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	be := Errorf(CodeForbidden, false, "no")
	if Internal(be) != error(be) {
		t.Fatal("*Error must pass through")
	}
	got := Internal(errors.New("boom"))
	var e *Error
	if !errors.As(got, &e) || e.Code != CodeInternal || e.Message != "boom" || !e.Retryable {
		t.Fatalf("wrap: %#v", got)
	}
}

func TestCodeOf(t *testing.T) {
	if CodeOf(errors.New("x")) != "" || CodeOf(nil) != "" {
		t.Fatal("non-Error must give an empty code")
	}
	wrapped := fmt.Errorf("ctx: %w", Errorf(CodeNotFound, false, "gone"))
	if CodeOf(wrapped) != CodeNotFound {
		t.Fatalf("wrapped: %q", CodeOf(wrapped))
	}
}
