//go:build !windows

package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDirPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    string
	}{
		{"explicit wins", []string{"HOME=/h", "XDG_CONFIG_HOME=/x", "AGENTBUS2_CONFIG_DIR=/c"}, "/c"},
		{"xdg next", []string{"HOME=/h", "XDG_CONFIG_HOME=/x"}, "/x/agentbus2"},
		{"home last", []string{"HOME=/h"}, "/h/.config/agentbus2"},
		{"empty values count as unset", []string{"HOME=/h", "XDG_CONFIG_HOME=", "AGENTBUS2_CONFIG_DIR="}, "/h/.config/agentbus2"},
		{"last duplicate wins", []string{"HOME=/a", "HOME=/h"}, "/h/.config/agentbus2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConfigDir(tc.environ)
			if err != nil || got != tc.want {
				t.Fatalf("ConfigDir = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	t.Run("no HOME falls back to the OS home", func(t *testing.T) {
		if _, err := os.UserHomeDir(); err != nil {
			t.Skip("no home directory in this environment")
		}
		got, err := ConfigDir(nil)
		if err != nil || got == "" || filepath.Base(got) != "agentbus2" {
			t.Fatalf("ConfigDir(nil) = %q, %v", got, err)
		}
	})
}

func TestDataDirPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    string
	}{
		{"explicit wins", []string{"HOME=/h", "XDG_DATA_HOME=/x", "AGENTBUS2_DATA_DIR=/d"}, "/d"},
		{"xdg next", []string{"HOME=/h", "XDG_DATA_HOME=/x"}, "/x/agentbus2"},
		{"home last", []string{"HOME=/h"}, "/h/.local/share/agentbus2"},
		{"empty values count as unset", []string{"HOME=/h", "XDG_DATA_HOME="}, "/h/.local/share/agentbus2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DataDir(tc.environ)
			if err != nil || got != tc.want {
				t.Fatalf("DataDir = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	t.Run("no HOME falls back to the OS home", func(t *testing.T) {
		if _, err := os.UserHomeDir(); err != nil {
			t.Skip("no home directory in this environment")
		}
		got, err := DataDir(nil)
		if err != nil || got == "" || filepath.Base(got) != "agentbus2" {
			t.Fatalf("DataDir(nil) = %q, %v", got, err)
		}
	})
}

func TestGetenvLastWins(t *testing.T) {
	v, ok := Getenv([]string{"A=1", "B=2", "A=3"}, "A")
	if !ok || v != "3" {
		t.Fatalf("Getenv = %q, %v", v, ok)
	}
	if _, ok := Getenv([]string{"AB=1"}, "A"); ok {
		t.Fatal("prefix must not match")
	}
}
