package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// safeID guards against path traversal: note IDs are used as file names.
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// maxImportSize caps how large an imported Markdown file may be.
const maxImportSize = 5 << 20 // 5 MB

// NoteSummary is the lightweight representation used to populate the note list.
type NoteSummary struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Preview    string `json:"preview"`
	Modified   int64  `json:"modified"` // Unix milliseconds
	CategoryID string `json:"categoryId"`
}

// Note is a full note document.
type Note struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Modified   int64  `json:"modified"` // Unix milliseconds
	CategoryID string `json:"categoryId"`
}

// NotesService stores notes as plain Markdown files on disk, one file per note,
// so the user's notes stay portable and readable outside the app.
type NotesService struct {
	mu   sync.Mutex
	root string
	// writes lets the GUI watcher tell its own saves apart from edits made by
	// another process (the MCP server or an external editor).
	writes *writeTracker
}

func NewNotesService() *NotesService {
	return &NotesService{}
}

// init creates the notes directory on first use and seeds a welcome note.
func (s *NotesService) init() error {
	if s.root != "" {
		return nil
	}
	appDir, err := appConfigDir()
	if err != nil {
		return fmt.Errorf("locate config directory: %w", err)
	}
	root := filepath.Join(appDir, "notes")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create notes directory: %w", err)
	}
	s.root = root

	if err := ensureCategoryDefaults(root); err != nil {
		return err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read notes directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
	}
	// Empty notes directory: add a welcome document that exercises the renderer.
	_, err = s.createWith("Welcome to Quire", welcomeNote)
	return err
}

// List returns every note, newest first.
func (s *NotesService) List() ([]NoteSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read notes: %w", err)
	}
	state, err := readCategoryState(s.root)
	if err != nil {
		return nil, err
	}

	notes := make([]NoteSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		path := filepath.Join(s.root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		title, body := splitDocument(string(raw))
		notes = append(notes, NoteSummary{
			ID:         id,
			Title:      title,
			Preview:    previewText(body),
			Modified:   info.ModTime().UnixMilli(),
			CategoryID: assignCategoryForNote(&state, id),
		})
	}

	sort.Slice(notes, func(i, j int) bool {
		return notes[i].Modified > notes[j].Modified
	})
	return notes, nil
}

// Get loads a single note.
func (s *NotesService) Get(id string) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Note{}, err
	}
	return s.readNoteLocked(id)
}

// Create adds a new note and returns it.
func (s *NotesService) Create() (Note, error) {
	return s.CreateIn("")
}

// CreateIn adds a note straight into a category. An empty category id creates
// an uncategorised note, which the UI shows under Inbox.
func (s *NotesService) CreateIn(categoryID string) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Note{}, err
	}
	categoryID = strings.TrimSpace(categoryID)
	if categoryID != "" {
		state, err := readCategoryState(s.root)
		if err != nil {
			return Note{}, err
		}
		category, ok := findCategory(&state, categoryID)
		if !ok {
			return Note{}, fmt.Errorf("category %q not found", categoryID)
		}
		categoryID = category.ID
	}
	return s.createWithCategory("Untitled note", "", categoryID)
}

func (s *NotesService) createWith(title, body string) (Note, error) {
	return s.createWithCategory(title, body, "")
}

// createWithCategory creates a note and files it in a category. The caller must
// hold s.mu, or be inside init, which already runs under it.
func (s *NotesService) createWithCategory(title, body, categoryID string) (Note, error) {
	id := newNoteID(s.root)
	now := time.Now()
	doc := composeDocument(title, body)
	path := filepath.Join(s.root, id+".md")
	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, err := io.WriteString(w, doc)
		return err
	}); err != nil {
		return Note{}, fmt.Errorf("create note: %w", err)
	}
	if s.writes != nil {
		s.writes.mark(path)
	}
	if categoryID != "" {
		if err := s.setCategoryAssignmentLocked(id, categoryID); err != nil {
			// Do not leave a note behind that claims a category it never got.
			_ = os.Remove(path)
			return Note{}, err
		}
	}
	return Note{ID: id, Title: title, Body: body, Modified: now.UnixMilli(), CategoryID: categoryID}, nil
}

// Save writes a note to disk. An empty title falls back to "Untitled note".
func (s *NotesService) Save(id, title, body string) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Note{}, err
	}
	if err := validateID(id); err != nil {
		return Note{}, err
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled note"
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")

	path := filepath.Join(s.root, id+".md")
	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, err := io.WriteString(w, composeDocument(title, body))
		return err
	}); err != nil {
		return Note{}, fmt.Errorf("save note: %w", err)
	}
	if s.writes != nil {
		s.writes.mark(path)
	}
	state, err := readCategoryState(s.root)
	if err != nil {
		return Note{}, err
	}
	return Note{ID: id, Title: title, Body: body, Modified: time.Now().UnixMilli(), CategoryID: assignCategoryForNote(&state, id)}, nil
}

// Delete removes a note from disk.
func (s *NotesService) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.root, id+".md")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete note: %w", err)
	}
	// Tracking and planner data are meaningless once the note is gone.
	// Best-effort: a failure here must not block deleting the note itself.
	_ = deletePublishState(s.root, id)
	_ = deletePlanState(s.root, id)
	_ = s.clearCategoryAssignment(id)
	return nil
}

// Import copies Markdown files from disk into the notes directory.
//
// Each imported file is normalised into the app's document format: if the file
// starts with an H1 that becomes the title, otherwise the file name is used and
// the whole file becomes the body.
func (s *NotesService) Import(paths []string) ([]Note, error) {
	return s.ImportInto(paths, "")
}

// ImportInto imports Markdown files into a category. An empty category id
// leaves the imported notes uncategorised.
func (s *NotesService) ImportInto(paths []string, categoryID string) ([]Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("no files selected")
	}
	categoryID = strings.TrimSpace(categoryID)
	if categoryID != "" {
		state, err := readCategoryState(s.root)
		if err != nil {
			return nil, err
		}
		category, ok := findCategory(&state, categoryID)
		if !ok {
			return nil, fmt.Errorf("category %q not found", categoryID)
		}
		categoryID = category.ID
	}

	imported := make([]Note, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return imported, fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}
		if info.IsDir() {
			return imported, fmt.Errorf("%s is a directory", filepath.Base(path))
		}
		if info.Size() > maxImportSize {
			return imported, fmt.Errorf("%s is too large (maximum %d MB)", filepath.Base(path), maxImportSize/(1<<20))
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return imported, fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}

		content := strings.TrimLeft(strings.ToValidUTF8(string(raw), ""), "\n\r\t ")
		content = strings.ReplaceAll(content, "\r\n", "\n")

		title, body := "", ""
		if strings.HasPrefix(content, "# ") {
			title, body = splitDocument(content)
		} else {
			title = strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
			body = strings.TrimSpace(content)
		}
		if title == "" {
			title = "Untitled note"
		}

		note, err := s.createWithCategory(title, body, categoryID)
		if err != nil {
			return imported, err
		}
		imported = append(imported, note)
	}
	return imported, nil
}

// Render converts Markdown to HTML using the shared goldmark pipeline.
func (s *NotesService) Render(markdown string) (string, error) {
	return renderMarkdown(markdown)
}

// search performs a case-insensitive substring search over note titles and
// bodies, newest first. An empty query lists the most recent notes. It backs
// the MCP search tool; the desktop UI still filters the list client-side.
func (s *NotesService) search(query string, limit int) ([]NoteSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	needle := strings.ToLower(strings.TrimSpace(query))

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read notes: %w", err)
	}
	state, err := readCategoryState(s.root)
	if err != nil {
		return nil, err
	}
	matches := make([]NoteSummary, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		path := filepath.Join(s.root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		title, body := splitDocument(string(raw))
		if needle != "" &&
			!strings.Contains(strings.ToLower(title), needle) &&
			!strings.Contains(strings.ToLower(body), needle) {
			continue
		}
		matches = append(matches, NoteSummary{
			ID:         id,
			Title:      title,
			Preview:    previewText(body),
			Modified:   info.ModTime().UnixMilli(),
			CategoryID: assignCategoryForNote(&state, id),
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Modified > matches[j].Modified
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// createTitled creates a note with content. The MCP server uses it so an agent
// can save text without first creating an empty note and then saving it.
func (s *NotesService) createTitled(title, body string) (Note, error) {
	return s.createTitledIn(title, body, "")
}

// createTitledIn creates a note in a category given by id or name. An empty
// category leaves the note uncategorised.
func (s *NotesService) createTitledIn(title, body, categoryID string) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Note{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled note"
	}
	body = strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\n")

	categoryID = strings.TrimSpace(categoryID)
	if categoryID != "" {
		state, err := readCategoryState(s.root)
		if err != nil {
			return Note{}, err
		}
		category, ok := findCategory(&state, categoryID)
		if !ok {
			return Note{}, fmt.Errorf("category %q not found", categoryID)
		}
		categoryID = category.ID
	}
	return s.createWithCategory(title, body, categoryID)
}

// appendTo adds Markdown to the end of a note. Appending rather than replacing
// keeps agent summaries from clobbering edits made in the app.
func (s *NotesService) appendTo(id, markdown string) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Note{}, err
	}
	note, err := s.readNoteLocked(id)
	if err != nil {
		return Note{}, err
	}
	markdown = strings.TrimRight(strings.ReplaceAll(markdown, "\r\n", "\n"), " \t\n")
	if strings.TrimSpace(markdown) == "" {
		return Note{}, errors.New("nothing to append")
	}

	body := note.Body
	if strings.TrimSpace(body) != "" {
		body = strings.TrimRight(body, " \t\n") + "\n\n"
	}
	body += markdown

	path := filepath.Join(s.root, id+".md")
	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, err := io.WriteString(w, composeDocument(note.Title, body))
		return err
	}); err != nil {
		return Note{}, fmt.Errorf("append to note: %w", err)
	}
	if s.writes != nil {
		s.writes.mark(path)
	}
	state, err := readCategoryState(s.root)
	if err != nil {
		return Note{}, err
	}
	return Note{ID: id, Title: note.Title, Body: body, Modified: time.Now().UnixMilli(), CategoryID: assignCategoryForNote(&state, id)}, nil
}

// replaceIfUnchanged replaces a note body only when it still has the mtime the
// caller read. expectedModified <= 0 skips the check. This is the optimistic
// concurrency guard for agents that rewrite whole documents.
func (s *NotesService) replaceIfUnchanged(id, title, body string, expectedModified int64) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Note{}, err
	}
	note, err := s.readNoteLocked(id)
	if err != nil {
		return Note{}, err
	}
	if expectedModified > 0 && note.Modified != expectedModified {
		return Note{}, fmt.Errorf(
			"note %q changed on disk (modified %d, expected %d); read it again and retry",
			id, note.Modified, expectedModified,
		)
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = note.Title
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")

	path := filepath.Join(s.root, id+".md")
	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, err := io.WriteString(w, composeDocument(title, body))
		return err
	}); err != nil {
		return Note{}, fmt.Errorf("save note: %w", err)
	}
	if s.writes != nil {
		s.writes.mark(path)
	}
	state, err := readCategoryState(s.root)
	if err != nil {
		return Note{}, err
	}
	return Note{ID: id, Title: title, Body: body, Modified: time.Now().UnixMilli(), CategoryID: assignCategoryForNote(&state, id)}, nil
}

// NotesPath exposes the on-disk location for display in the UI.
func (s *NotesService) NotesPath() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return "", err
	}
	return s.root, nil
}

// publishRoot gives the publish service access to the notes directory so
// sidecar files sit next to their notes.
func (s *NotesService) publishRoot() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return "", err
	}
	return s.root, nil
}

// --- helpers ---------------------------------------------------------------

// writeFileAtomic writes data to path via a temporary file in the same
// directory, fsyncs it, then renames it into place. A crash or disk-full while
// writing therefore leaves the previous version of the file intact.
func writeFileAtomic(path string, perm os.FileMode, write func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := write(tmp); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("set file permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return nil
}

// readNoteLocked loads a note while the caller holds s.mu.
func (s *NotesService) readNoteLocked(id string) (Note, error) {
	if err := validateID(id); err != nil {
		return Note{}, err
	}
	path := filepath.Join(s.root, id+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Note{}, fmt.Errorf("note %q not found", id)
		}
		return Note{}, fmt.Errorf("read note: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Note{}, fmt.Errorf("stat note: %w", err)
	}
	title, body := splitDocument(string(raw))
	state, err := readCategoryState(s.root)
	if err != nil {
		return Note{}, err
	}
	return Note{ID: id, Title: title, Body: body, Modified: info.ModTime().UnixMilli(), CategoryID: assignCategoryForNote(&state, id)}, nil
}

func validateID(id string) error {
	if !safeID.MatchString(id) {
		return errors.New("invalid note id")
	}
	return nil
}

func newNoteID(root string) string {
	for {
		suffix := make([]byte, 2)
		_, _ = rand.Read(suffix)
		id := time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(suffix)
		if _, err := os.Stat(filepath.Join(root, id+".md")); errors.Is(err, os.ErrNotExist) {
			return id
		}
		time.Sleep(time.Millisecond)
	}
}

// splitDocument separates the leading `# Title` heading from the note body.
func splitDocument(raw string) (title, body string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	firstLine, rest, found := strings.Cut(raw, "\n")
	if found && strings.HasPrefix(firstLine, "# ") {
		title = strings.TrimSpace(strings.TrimPrefix(firstLine, "# "))
		body = strings.TrimLeft(rest, "\n")
		if title == "" {
			title = "Untitled note"
		}
		return title, body
	}

	// No H1: fall back to the file's first non-empty line as a title.
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return previewText(line), strings.TrimSpace(raw)
		}
	}
	return "Untitled note", strings.TrimSpace(raw)
}

func composeDocument(title, body string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled note"
	}
	body = strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), " \t\n")
	if body == "" {
		return "# " + title + "\n"
	}
	return "# " + title + "\n\n" + body + "\n"
}

// previewText flattens Markdown into a short plain-text snippet for the list.
func previewText(body string) string {
	replacer := strings.NewReplacer(
		"`", "", "*", "", "_", "", "#", "", ">", "",
		"[", "", "]", "", "(", " ", ")", " ",
	)
	flat := replacer.Replace(body)
	flat = strings.Join(strings.Fields(flat), " ")
	if len([]rune(flat)) > 120 {
		flat = string([]rune(flat)[:120]) + "…"
	}
	if flat == "" {
		return "No additional text"
	}
	return flat
}

// welcomeNote is seeded on first launch to demonstrate the renderer. "§" stands
// in for a backtick so the document can live in a raw string literal.
var welcomeNote = strings.ReplaceAll(`This note lives as a plain §.md§ file on your computer. Everything you type on the left is rendered on the right using **GitHub-flavoured Markdown**.

## Formatting

You can use **bold**, *italic*, ~~strikethrough~~, §inline code§, and [links](https://v3.wails.io).

> Blockquotes work too — useful for calling out something important.

## Lists

- Bullet lists
- Nested items
  - Like this one
1. Ordered lists
2. Keep going

### Task lists

- [x] Write a note
- [ ] Try the preview toggle
- [ ] Press §Ctrl/Cmd + N§ to create another note

## Tables

| Shortcut | Action |
| --- | --- |
| §Ctrl/Cmd + N§ | New note |
| §Ctrl/Cmd + S§ | Save now |
| §Ctrl/Cmd + E§ | Toggle preview |

## Code

§§§go
func main() {
    fmt.Println("Hello from Go")
}
§§§

§§§js
const render = (md) => NotesService.Render(md);
§§§

## Footnotes

Markdown is rendered by goldmark[^1] on the Go side.

[^1]: A CommonMark-compliant Markdown parser for Go.
`, "§", "`")
