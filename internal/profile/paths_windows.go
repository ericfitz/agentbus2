//go:build windows

package profile

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDir is %AGENTBUS2_CONFIG_DIR%, else %APPDATA%\agentbus2 (roaming),
// else <home>\AppData\Roaming\agentbus2.
func ConfigDir(environ []string) (string, error) {
	if d, ok := Getenv(environ, "AGENTBUS2_CONFIG_DIR"); ok {
		return d, nil
	}
	if d, ok := Getenv(environ, "APPDATA"); ok {
		return filepath.Join(d, "agentbus2"), nil
	}
	home, err := homeDir(environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "AppData", "Roaming", "agentbus2"), nil
}

// DataDir is %AGENTBUS2_DATA_DIR%, else %LOCALAPPDATA%\agentbus2 (Local, not
// Roaming: the SQLite database must not roam with a domain profile), else
// <home>\AppData\Local\agentbus2.
func DataDir(environ []string) (string, error) {
	if d, ok := Getenv(environ, "AGENTBUS2_DATA_DIR"); ok {
		return d, nil
	}
	if d, ok := Getenv(environ, "LOCALAPPDATA"); ok {
		return filepath.Join(d, "agentbus2"), nil
	}
	home, err := homeDir(environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "AppData", "Local", "agentbus2"), nil
}

func homeDir(environ []string) (string, error) {
	if h, ok := Getenv(environ, "USERPROFILE"); ok {
		return h, nil
	}
	if h, ok := Getenv(environ, "HOME"); ok {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return h, nil
}
