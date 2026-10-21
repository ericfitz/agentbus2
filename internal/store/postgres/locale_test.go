package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/store"
)

func TestClassifyLocale(t *testing.T) {
	for _, c := range []struct {
		name                      string
		ctype, encoding, provider string
		wantErr                   bool
	}{
		{"en_US.utf8", "en_US.utf8", "UTF8", "c", false},
		{"C.UTF-8", "C.UTF-8", "UTF8", "c", false},
		{"en_US.UTF-8", "en_US.UTF-8", "UTF8", "c", false},
		{"C", "C", "UTF8", "c", true},
		{"POSIX", "POSIX", "UTF8", "c", true},
		{"ISO8859-1", "en_US.ISO8859-1", "LATIN1", "c", true},
		{"empty", "", "UTF8", "c", true},
		// ICU and builtin providers: datctype is irrelevant, the encoding decides.
		{"icu with C ctype, UTF8", "C", "UTF8", "i", false},
		{"icu, LATIN1", "en_US.ISO8859-1", "LATIN1", "i", true},
		{"builtin, UTF8", "C", "UTF8", "b", false},
	} {
		err := classifyLocale(c.ctype, c.encoding, c.provider)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: classifyLocale err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if err != nil {
			if !errors.Is(err, store.ErrSchemaInvalid) {
				t.Errorf("%s: err = %v, want ErrSchemaInvalid", c.name, err)
			}
			if !strings.Contains(err.Error(), "UTF") {
				t.Errorf("%s: message %q does not say a UTF-8 setting is required", c.name, err)
			}
		}
	}
}

func TestClassifyLocaleMessageNamesCtype(t *testing.T) {
	err := classifyLocale("POSIX", "UTF8", "c")
	if err == nil || !strings.Contains(err.Error(), `"POSIX"`) || !strings.Contains(err.Error(), "text-search normalization") {
		t.Fatalf("err = %v", err)
	}
}
