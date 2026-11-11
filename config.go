package main

import (
	"os"
	"path/filepath"
)

// The app's identity. Every name shown to the user or used on disk derives
// from these constants.
const (
	// appName is the product name: window title, app bundle, installers.
	appName = "Quire"
	// appSlug names the binary, the bundle identifiers and the per-user
	// config directory.
	appSlug = "quire"
)

// appConfigDir returns the app's per-user config directory, where the notes
// directory, publishing credentials and the MCP log live.
func appConfigDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, appSlug), nil
}
