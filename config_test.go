package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinConfigDir points the platform's per-user config directory at a temporary
// directory and returns the resolved base, so a test can exercise code that
// writes there without touching the developer's real notes.
func pinConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux and the other unixes
	t.Setenv("HOME", dir)            // macOS: $HOME/Library/Application Support
	t.Setenv("AppData", dir)         // Windows

	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("resolve config directory: %v", err)
	}
	// The override must have taken effect, otherwise the test would write into
	// the real config directory.
	if !strings.HasPrefix(filepath.Clean(base), filepath.Clean(dir)) {
		t.Fatalf("config directory %q is outside the test directory %q", base, dir)
	}
	return base
}

// The config path is part of the app's on-disk contract: renaming the app moves
// the notes, so that must be a deliberate change and not a silent one.
func TestAppConfigDirIsQuire(t *testing.T) {
	base := pinConfigDir(t)

	got, err := appConfigDir()
	if err != nil {
		t.Fatalf("appConfigDir: %v", err)
	}
	want := filepath.Join(base, "quire")
	if got != want {
		t.Fatalf("appConfigDir() = %q, want %q", got, want)
	}
}

func TestNotesDirectoryIsUnderQuireConfigPath(t *testing.T) {
	base := pinConfigDir(t)

	service := NewNotesService()
	if err := service.init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	want := filepath.Join(base, "quire", "notes")
	if service.root != want {
		t.Fatalf("notes root = %q, want %q", service.root, want)
	}
	if info, err := os.Stat(want); err != nil {
		t.Fatalf("notes directory missing at %q: %v", want, err)
	} else if !info.IsDir() {
		t.Fatalf("%q is not a directory", want)
	}

	// The path the UI copies to the clipboard must be the same one.
	path, err := service.NotesPath()
	if err != nil {
		t.Fatalf("NotesPath: %v", err)
	}
	if path != want {
		t.Fatalf("NotesPath() = %q, want %q", path, want)
	}
}
