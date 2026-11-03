//go:build !windows

package profile

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDir is $AGENTMESH_CONFIG_DIR, else $XDG_CONFIG_HOME/agentmesh, else
// $HOME/.config/agentmesh. HOME comes from environ, then os.UserHomeDir; never
// os.UserConfigDir, which on macOS is ~/Library/Application Support.
func ConfigDir(environ []string) (string, error) {
	if d, ok := Getenv(environ, "AGENTMESH_CONFIG_DIR"); ok {
		return d, nil
	}
	if d, ok := Getenv(environ, "XDG_CONFIG_HOME"); ok {
		return filepath.Join(d, "agentmesh"), nil
	}
	home, err := homeDir(environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agentmesh"), nil
}

// DataDir is $AGENTMESH_DATA_DIR, else $XDG_DATA_HOME/agentmesh, else
// $HOME/.local/share/agentmesh.
func DataDir(environ []string) (string, error) {
	if d, ok := Getenv(environ, "AGENTMESH_DATA_DIR"); ok {
		return d, nil
	}
	if d, ok := Getenv(environ, "XDG_DATA_HOME"); ok {
		return filepath.Join(d, "agentmesh"), nil
	}
	home, err := homeDir(environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "agentmesh"), nil
}

func homeDir(environ []string) (string, error) {
	if h, ok := Getenv(environ, "HOME"); ok {
		return h, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return h, nil
}
