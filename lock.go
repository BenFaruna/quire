package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// lockNotesDir serialises read-modify-write updates to the notes directory
// across processes. The GUI and the MCP server are separate processes, so an
// in-process mutex alone would let them both read the same sidecar and write
// back competing versions. The lock file lives next to the notes and is
// ignored by the note listing and the file watcher.
func lockNotesDir(root string) (func(), error) {
	fileLock := flock.New(filepath.Join(root, ".notes.lock"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	locked, err := fileLock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return func() {}, err
	}
	if !locked {
		return func() {}, context.DeadlineExceeded
	}

	return func() {
		_ = fileLock.Unlock()
		_ = fileLock.Close()
	}, nil
}
