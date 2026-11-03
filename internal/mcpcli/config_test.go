package mcpcli

import (
	"testing"
)

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"a-b_c.d/e:f": "a-b_c.d/e:f",
		"my key":      "'my key'",
		"it's":        `'it'\''s'`,
		"":            "''",
		"a$b":         "'a$b'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// commandLine quotes for the shell of the OS: POSIX on Unix, PowerShell on
// Windows (its default shell; cmd.exe does not read single quotes). Pure, so
// it runs on every OS.
func TestCommandLine(t *testing.T) {
	args := []string{"serve", "-server", "http://h:1", "-identity", "pat", "-credential", `C:\Users\First Last\k.pem`}
	for _, c := range []struct{ goos, exe, want string }{
		{"linux", "/opt/bin/x", `/opt/bin/x serve -server http://h:1 -identity pat -credential '` + `C:\Users\First Last\k.pem'`},
		{"darwin", "/Users/a b/x", `'/Users/a b/x' serve -server http://h:1 -identity pat -credential '` + `C:\Users\First Last\k.pem'`},
		{"linux", "/opt/it's here/x", `'/opt/it'\''s here/x' serve -server http://h:1 -identity pat -credential '` + `C:\Users\First Last\k.pem'`},
		{"windows", `C:\bin\x.exe`, `C:\bin\x.exe serve -server http://h:1 -identity pat -credential 'C:\Users\First Last\k.pem'`},
		{"windows", `C:\Users\First Last\x.exe`, `& 'C:\Users\First Last\x.exe' serve -server http://h:1 -identity pat -credential 'C:\Users\First Last\k.pem'`},
		{"windows", `C:\it's\x.exe`, `& 'C:\it''s\x.exe' serve -server http://h:1 -identity pat -credential 'C:\Users\First Last\k.pem'`},
	} {
		if got := commandLine(c.goos, c.exe, args); got != c.want {
			t.Errorf("commandLine(%s, %q) = %q, want %q", c.goos, c.exe, got, c.want)
		}
	}
}

func TestPowershellQuote(t *testing.T) {
	for in, want := range map[string]string{
		`C:\a\b.exe`:     `C:\a\b.exe`,
		"a@b":            "a@b",
		"@file":          "'@file'",
		"a,b":            "'a,b'",
		"100%":           "'100%'",
		"it's":           "'it''s'",
		"\u2018x\u2019":  "'\u2018\u2018x\u2019\u2019'",
		"a\u201a\u201bb": "'a\u201a\u201a\u201b\u201bb'",
		"a$b":            "'a$b'",
		"":               "''",
	} {
		if got := powershellQuote(in); got != want {
			t.Errorf("powershellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
