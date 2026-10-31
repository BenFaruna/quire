package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestChangeKindFor(t *testing.T) {
	cases := []struct {
		path string
		kind string
		ok   bool
	}{
		{"/notes/20260101-000000-abcd.md", changeKindBody, true},
		{"/notes/20260101-000000-abcd.plan.json", changeKindPlan, true},
		{"/notes/20260101-000000-abcd.publish.json", changeKindPublish, true},
		{"/notes/.notes.lock", "", false},
		{"/notes/.tmp-20260101-000000-abcd.md-123", "", false},
		{"/notes/20260101-000000-abcd.plan.json.bak", "", false},
		{"/notes/readme.txt", "", false},
	}
	for _, tc := range cases {
		kind, ok := changeKindFor(tc.path)
		if ok != tc.ok || kind != tc.kind {
			t.Errorf("changeKindFor(%q) = %q, %v; want %q, %v", tc.path, kind, ok, tc.kind, tc.ok)
		}
	}
}

func TestSummarizeChanges(t *testing.T) {
	batch := map[string]string{
		"/notes/a.md":           changeKindBody,
		"/notes/a.plan.json":    changeKindPlan,
		"/notes/b.publish.json": changeKindPublish,
		"/notes/.notes.lock":    changeKindBody,
	}
	change := summarizeChanges(batch)
	if len(change.NoteIDs) != 2 || change.NoteIDs[0] != "a" || change.NoteIDs[1] != "b" {
		t.Fatalf("NoteIDs = %v", change.NoteIDs)
	}
	if len(change.Kinds) != 3 {
		t.Fatalf("Kinds = %v", change.Kinds)
	}
}

func TestWatcherEmitsForExternalWritesAndIgnoresOwn(t *testing.T) {
	root := t.TempDir()
	notes := &NotesService{root: root}
	tracker := newWriteTracker()
	notes.writes = tracker

	path := filepath.Join(root, "20260101-000000-abcd.md")
	if err := os.WriteFile(path, []byte("# Original\n\nBody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	watcher := NewNotesWatcher(notes, tracker)
	events := make(chan ExternalChange, 4)
	watcher.emit = func(change ExternalChange) { events <- change }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := watcher.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		t.Fatalf("start watcher: %v", err)
	}
	defer func() { _ = watcher.ServiceShutdown() }()

	// An external process edits the note the same way the MCP server does:
	// write a temp file, then rename it over the note.
	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, err := io.WriteString(w, "# External\n\nChanged outside the app\n")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case change := <-events:
		if len(change.NoteIDs) != 1 || change.NoteIDs[0] != "20260101-000000-abcd" {
			t.Fatalf("external change = %+v", change)
		}
		if len(change.Kinds) != 1 || change.Kinds[0] != changeKindBody {
			t.Fatalf("external kinds = %v", change.Kinds)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not report an external edit")
	}

	// The app's own save must not come back as an external change.
	if _, err := notes.Save("20260101-000000-abcd", "Original", "Saved by the app"); err != nil {
		t.Fatal(err)
	}
	select {
	case change := <-events:
		t.Fatalf("own save was reported as external: %+v", change)
	case <-time.After(700 * time.Millisecond):
	}
}

func TestWriteTrackerIgnoresOwnWritesOnly(t *testing.T) {
	now := time.Now()
	tracker := newWriteTracker()
	tracker.now = func() time.Time { return now }

	tracker.mark("/notes/a.md")
	if !tracker.recentWrite("/notes/a.md") {
		t.Fatal("fresh write was not recognised")
	}
	if tracker.recentWrite("/notes/b.md") {
		t.Fatal("unrelated path was recognised as a write")
	}

	now = now.Add(3 * time.Second)
	if tracker.recentWrite("/notes/a.md") {
		t.Fatal("old write was still considered recent")
	}
}
