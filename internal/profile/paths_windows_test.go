//go:build windows

package profile

import "testing"

func TestConfigDirWindows(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    string
	}{
		{"explicit wins", []string{`APPDATA=C:\a`, `AGENTBUS2_CONFIG_DIR=C:\c`}, `C:\c`},
		{"appdata", []string{`APPDATA=C:\a`}, `C:\a\agentbus2`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConfigDir(tc.environ)
			if err != nil || got != tc.want {
				t.Fatalf("ConfigDir = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDataDirWindows(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    string
	}{
		{"explicit wins", []string{`LOCALAPPDATA=C:\l`, `AGENTBUS2_DATA_DIR=C:\d`}, `C:\d`},
		{"localappdata", []string{`LOCALAPPDATA=C:\l`}, `C:\l\agentbus2`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DataDir(tc.environ)
			if err != nil || got != tc.want {
				t.Fatalf("DataDir = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
