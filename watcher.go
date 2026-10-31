package main

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ExternalChange tells the frontend that files changed behind its back. NoteIDs
// lets it decide whether the open note needs reloading; Kinds says which part
// of the sidecar data is stale.
type ExternalChange struct {
	NoteIDs []string `json:"noteIds"`
	Kinds   []string `json:"kinds"`
}

const (
	changeKindBody    = "body"
	changeKindPlan    = "plan"
	changeKindPublish = "publish"
)

// writeTracker remembers files this process just wrote so the watcher can
// ignore its own echo. Without it every autosave would look like an external
// edit and could raise a conflict banner while the user is still typing.
type writeTracker struct {
	mu     sync.Mutex
	recent map[string]time.Time
	now    func() time.Time
}

func newWriteTracker() *writeTracker {
	return &writeTracker{recent: make(map[string]time.Time), now: time.Now}
}

func (t *writeTracker) mark(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.recent[path] = now
	for p, at := range t.recent {
		if now.Sub(at) > 5*time.Second {
			delete(t.recent, p)
		}
	}
}

func (t *writeTracker) recentWrite(path string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.recent[path]
	if !ok {
		return false
	}
	return t.now().Sub(at) < 2*time.Second
}

// NotesWatcher watches the notes directory and emits notes:external-change when
// another process (the MCP server, an editor, a sync tool) modifies a note or a
// sidecar. It uses inotify/FSEvents/ReadDirectoryChangesW rather than polling,
// so an idle app costs one file descriptor and no wakeups.
type NotesWatcher struct {
	notes   *NotesService
	tracker *writeTracker
	// emit is a field so tests can observe changes without a running Wails app.
	emit func(ExternalChange)

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
}

func NewNotesWatcher(notes *NotesService, tracker *writeTracker) *NotesWatcher {
	return &NotesWatcher{
		notes:   notes,
		tracker: tracker,
		emit:    emitExternalChange,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func emitExternalChange(change ExternalChange) {
	if app := application.Get(); app != nil {
		app.Event.Emit("notes:external-change", change)
	}
}

func (w *NotesWatcher) ServiceName() string { return "NotesWatcher" }

func (w *NotesWatcher) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return nil
	}
	w.started = true
	w.mu.Unlock()

	// Watching is an enhancement, not a requirement: if the filesystem does
	// not support notifications (some network mounts), the app still runs and
	// falls back to refreshing the list when it regains focus.
	root, err := w.notes.NotesPath()
	if err != nil {
		return w.disable(err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return w.disable(err)
	}
	if err := watcher.Add(root); err != nil {
		_ = watcher.Close()
		return w.disable(err)
	}

	go w.loop(ctx, watcher)
	return nil
}

func (w *NotesWatcher) disable(err error) error {
	log.Printf("notes watcher disabled: %v", err)
	w.mu.Lock()
	w.started = false
	w.mu.Unlock()
	return nil
}

func (w *NotesWatcher) ServiceShutdown() error {
	w.mu.Lock()
	started := w.started
	w.started = false
	w.mu.Unlock()
	if !started {
		return nil
	}
	close(w.stop)
	<-w.done
	return nil
}

func (w *NotesWatcher) loop(ctx context.Context, watcher *fsnotify.Watcher) {
	defer close(w.done)
	defer watcher.Close()

	const debounce = 250 * time.Millisecond
	batch := make(map[string]string) // path -> kind
	var timer *time.Timer
	var timerC <-chan time.Time

	flush := func() {
		if len(batch) == 0 {
			return
		}
		change := summarizeChanges(batch)
		batch = make(map[string]string)
		if len(change.NoteIDs) == 0 {
			return
		}
		w.emit(change)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			kind, ok := changeKindFor(event.Name)
			if !ok {
				continue
			}
			if kind == changeKindBody && w.tracker != nil && w.tracker.recentWrite(event.Name) {
				continue
			}
			batch[event.Name] = kind
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			if err != nil {
				log.Printf("notes watcher: %v", err)
			}
		case <-timerC:
			timerC = nil
			flush()
		}
	}
}

// changeKindFor classifies a watched path, ignoring temporary, backup and lock
// files. WriteFileAtomic writes a temporary file and renames it into place, so
// only the final name matters.
func changeKindFor(path string) (string, bool) {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || strings.HasSuffix(base, ".bak") {
		return "", false
	}
	switch {
	case strings.HasSuffix(strings.ToLower(base), ".md"):
		return changeKindBody, true
	case strings.HasSuffix(strings.ToLower(base), ".plan.json"):
		return changeKindPlan, true
	case strings.HasSuffix(strings.ToLower(base), ".publish.json"):
		return changeKindPublish, true
	}
	return "", false
}

// noteIDForChange derives the note id from a note or sidecar file name.
func noteIDForChange(path string) string {
	base := filepath.Base(path)
	for _, suffix := range []string{".plan.json", ".publish.json", ".md"} {
		if strings.HasSuffix(strings.ToLower(base), suffix) {
			return base[:len(base)-len(suffix)]
		}
	}
	return ""
}

func summarizeChanges(batch map[string]string) ExternalChange {
	kindSet := make(map[string]bool)
	idSet := make(map[string]bool)
	for path, kind := range batch {
		kindSet[kind] = true
		if id := noteIDForChange(path); validateID(id) == nil {
			idSet[id] = true
		}
	}
	kinds := make([]string, 0, len(kindSet))
	for kind := range kindSet {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ExternalChange{NoteIDs: ids, Kinds: kinds}
}
