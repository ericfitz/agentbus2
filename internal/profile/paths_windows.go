//go:build windows

package profile

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDir is %AGENTMESH_CONFIG_DIR%, else %APPDATA%\agentmesh (roaming),
// else <home>\AppData\Roaming\agentmesh.
func ConfigDir(environ []string) (string, error) {
	if d, ok := Getenv(environ, "AGENTMESH_CONFIG_DIR"); ok {
		return d, nil
	}
	if d, ok := Getenv(environ, "APPDATA"); ok {
		return filepath.Join(d, "agentmesh"), nil
	}
	home, err := homeDir(environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "AppData", "Roaming", "agentmesh"), nil
}

// DataDir is %AGENTMESH_DATA_DIR%, else %LOCALAPPDATA%\agentmesh (Local, not
// Roaming: the SQLite database must not roam with a domain profile), else
// <home>\AppData\Local\agentmesh.
func DataDir(environ []string) (string, error) {
	if d, ok := Getenv(environ, "AGENTMESH_DATA_DIR"); ok {
		return d, nil
	}
	if d, ok := Getenv(environ, "LOCALAPPDATA"); ok {
		return filepath.Join(d, "agentmesh"), nil
	}
	home, err := homeDir(environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "AppData", "Local", "agentmesh"), nil
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
