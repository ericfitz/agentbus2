package fileacl

import (
	"strings"
	"testing"
)

// The Windows fix names both shells; %USERNAME% alone fails in PowerShell.
func TestWindowsFixNamesBothShells(t *testing.T) {
	got := WindowsFix(`C:\k\id`)
	for _, want := range []string{`"${env:USERNAME}:F"`, `"%USERNAME%:F"`, `icacls "C:\k\id" /inheritance:r /grant:r`, `/remove:g ACCOUNT`, "PowerShell", "cmd.exe", "/remove:g *S-1-1-0 *S-1-5-11 *S-1-5-32-545"} {
		if !strings.Contains(got, want) {
			t.Errorf("WindowsFix missing %q in %q", want, got)
		}
	}
}

// Every allow type that is not understood fails closed; unknown types too.
func TestClassifyACE(t *testing.T) {
	cases := map[uint8]aceKind{
		0x00: aceSIDGrant, 0x09: aceSIDGrant,
		0x04: aceOpaqueGrant, 0x05: aceOpaqueGrant, 0x0B: aceOpaqueGrant,
		0x01: aceIgnore, 0x02: aceIgnore, 0x06: aceIgnore, 0x0A: aceIgnore, 0x11: aceIgnore, 0x15: aceIgnore,
		0x16: aceUnknown, 0x7F: aceUnknown, 0xFF: aceUnknown,
	}
	for typ, want := range cases {
		if got := classifyACE(typ); got != want {
			t.Errorf("classifyACE(%#x) = %d, want %d", typ, got, want)
		}
	}
}
