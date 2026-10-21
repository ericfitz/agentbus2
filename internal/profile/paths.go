//go:build !windows

package profile

import (
	"fmt"
	"os"
	"path/filepath"
)

// ConfigDir is $AGENTBUS2_CONFIG_DIR, else $XDG_CONFIG_HOME/agentbus2, else
// $HOME/.config/agentbus2. HOME comes from environ, then os.UserHomeDir; never
// os.UserConfigDir, which on macOS is ~/Library/Application Support.
func ConfigDir(environ []string) (string, error) {
	if d, ok := Getenv(environ, "AGENTBUS2_CONFIG_DIR"); ok {
		return d, nil
	}
	if d, ok := Getenv(environ, "XDG_CONFIG_HOME"); ok {
		return filepath.Join(d, "agentbus2"), nil
	}
	home, err := homeDir(environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "agentbus2"), nil
}

// DataDir is $AGENTBUS2_DATA_DIR, else $XDG_DATA_HOME/agentbus2, else
// $HOME/.local/share/agentbus2.
func DataDir(environ []string) (string, error) {
	if d, ok := Getenv(environ, "AGENTBUS2_DATA_DIR"); ok {
		return d, nil
	}
	if d, ok := Getenv(environ, "XDG_DATA_HOME"); ok {
		return filepath.Join(d, "agentbus2"), nil
	}
	home, err := homeDir(environ)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "agentbus2"), nil
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
