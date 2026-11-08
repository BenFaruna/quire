package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Categories are the user's own buckets for notes. They live in a single
// sidecar next to the notes, `.categories.json`, so the Markdown files stay
// portable: move a note file and it is just an uncategorised note, while the
// category list and its purpose descriptions travel with the notes directory.
//
// The sidecar holds both the definitions and the note-to-category assignments.
// A single file keeps rename and delete cheap: there is no fan-out over every
// note, and the cross-process lock in lock.go keeps the GUI and the MCP server
// from clobbering each other's writes.
const (
	categoryStateVersion = 1
	categoryStateName    = ".categories.json"

	categoryKindNotes   = "notes"
	categoryKindJournal = "journal"

	maxCategoryNameRunes        = 60
	maxCategoryDescriptionRunes = 400
	maxCategoryIDLength         = 48
)

// Category is a user-defined bucket. Description records what the user intends
// to do with the category, which is what makes it useful to a model: an agent
// can read the description before deciding what to write there.
type Category struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"` // notes | journal
	Created     int64  `json:"created"`
}

// categoryState is the whole `.categories.json` document.
type categoryState struct {
	Version     int               `json:"version"`
	Updated     int64             `json:"updated"`
	Categories  []Category        `json:"categories"`
	Assignments map[string]string `json:"assignments"` // note id -> category id
}

func emptyCategoryState() categoryState {
	return categoryState{
		Version:     categoryStateVersion,
		Categories:  []Category{},
		Assignments: map[string]string{},
	}
}

func categoryStatePath(root string) string {
	return filepath.Join(root, categoryStateName)
}

// readCategoryState loads the sidecar. A missing file is an empty state, and a
// corrupt file is moved aside like the other sidecars rather than breaking the
// note list. Structurally valid JSON is normalised: entries with an unusable id
// or a blank name are dropped, and assignments pointing at a missing category
// are forgotten.
func readCategoryState(root string) (categoryState, error) {
	state := emptyCategoryState()
	path := categoryStatePath(root)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read categories: %w", err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		_ = os.Rename(path, path+".bak")
		return emptyCategoryState(), nil
	}
	normalizeCategoryState(&state)
	return state, nil
}

func writeCategoryState(root string, state categoryState) error {
	normalizeCategoryState(&state)
	state.Updated = time.Now().UnixMilli()

	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode categories: %w", err)
	}
	raw = append(raw, '\n')

	if err := writeFileAtomic(categoryStatePath(root), 0o644, func(w io.Writer) error {
		_, err := w.Write(raw)
		return err
	}); err != nil {
		return fmt.Errorf("write categories: %w", err)
	}
	return nil
}

func normalizeCategoryState(state *categoryState) {
	if state.Categories == nil {
		state.Categories = []Category{}
	}
	if state.Assignments == nil {
		state.Assignments = map[string]string{}
	}

	seen := make(map[string]bool, len(state.Categories))
	kept := state.Categories[:0]
	for _, category := range state.Categories {
		category.ID = strings.TrimSpace(category.ID)
		category.Name = strings.TrimSpace(category.Name)
		if !safeID.MatchString(category.ID) || category.Name == "" || seen[category.ID] {
			continue
		}
		if category.Kind != categoryKindJournal {
			category.Kind = categoryKindNotes
		}
		category.Description = strings.TrimSpace(category.Description)
		seen[category.ID] = true
		kept = append(kept, category)
	}
	state.Categories = kept

	for noteID, categoryID := range state.Assignments {
		if validateID(noteID) != nil || !seen[categoryID] {
			delete(state.Assignments, noteID)
		}
	}
	state.Version = categoryStateVersion
}

// updateCategoryState runs a read-modify-write cycle under the cross-process
// notes lock, so the app and the MCP server cannot lose each other's category
// changes.
func updateCategoryState(root string, update func(*categoryState) error) error {
	unlock, err := lockNotesDir(root)
	if err != nil {
		return fmt.Errorf("lock categories: %w", err)
	}
	defer unlock()

	state, err := readCategoryState(root)
	if err != nil {
		return err
	}
	if err := update(&state); err != nil {
		return err
	}
	return writeCategoryState(root, state)
}

// ensureCategoryDefaults seeds the two categories the app ships with on first
// use. It is deliberately a no-op once the sidecar exists, so deleting or
// renaming them sticks.
func ensureCategoryDefaults(root string) error {
	if _, err := os.Stat(categoryStatePath(root)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect categories: %w", err)
	}

	return updateCategoryState(root, func(state *categoryState) error {
		if len(state.Categories) > 0 {
			return nil
		}
		now := time.Now().UnixMilli()
		state.Categories = []Category{
			{
				ID:          "daily-log",
				Name:        "Daily log",
				Description: "A day-by-day record of what happened, what you learned and what comes next. Entries are dated so a model can read a week or a month and write a track record.",
				Kind:        categoryKindJournal,
				Created:     now,
			},
			{
				ID:          "track-record",
				Name:        "Track record",
				Description: "Model-written summaries of daily logs over a period: progress, themes and what changed.",
				Kind:        categoryKindNotes,
				Created:     now + 1,
			},
		}
		return nil
	})
}

func slugifyCategoryName(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= maxCategoryIDLength {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "category"
	}
	return slug
}

func newCategoryID(name string, existing map[string]bool) string {
	base := slugifyCategoryName(name)
	for {
		suffix := make([]byte, 2)
		_, _ = rand.Read(suffix)
		id := base + "-" + hex.EncodeToString(suffix)
		if !existing[id] {
			return id
		}
	}
}

func validateCategoryName(name string) error {
	if name == "" {
		return errors.New("category name is required")
	}
	if utf8.RuneCountInString(name) > maxCategoryNameRunes {
		return fmt.Errorf("category name is too long (maximum %d characters)", maxCategoryNameRunes)
	}
	return nil
}

func normalizeCategoryDescription(description string) (string, error) {
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > maxCategoryDescriptionRunes {
		return "", fmt.Errorf("category description is too long (maximum %d characters)", maxCategoryDescriptionRunes)
	}
	return description, nil
}

// findCategory resolves a category by id or, failing that, by name. Matching by
// name is case-insensitive so an agent can say "daily log" without first
// listing ids.
func findCategory(state *categoryState, ref string) (Category, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Category{}, false
	}
	for _, category := range state.Categories {
		if category.ID == ref {
			return category, true
		}
	}
	for _, category := range state.Categories {
		if strings.EqualFold(category.Name, ref) {
			return category, true
		}
	}
	return Category{}, false
}

func categoryByKind(state *categoryState, kind string) (Category, bool) {
	for _, category := range state.Categories {
		if category.Kind == kind {
			return category, true
		}
	}
	return Category{}, false
}

func isUncategorisedRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref == "" || strings.EqualFold(ref, "uncategorized") || strings.EqualFold(ref, "uncategorised")
}

// assignCategoryForNote is the lookup used when building note summaries and
// documents.
func assignCategoryForNote(state *categoryState, noteID string) string {
	if state.Assignments == nil {
		return ""
	}
	return state.Assignments[noteID]
}

// --- service methods --------------------------------------------------------

// Categories returns every category in creation order.
func (s *NotesService) Categories() ([]Category, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return nil, err
	}
	state, err := readCategoryState(s.root)
	if err != nil {
		return nil, err
	}
	out := make([]Category, len(state.Categories))
	copy(out, state.Categories)
	return out, nil
}

// CreateCategory adds a category. Its description is the user's statement of
// intent for the category and is shown to agents by the MCP server.
func (s *NotesService) CreateCategory(name, description string) (Category, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Category{}, err
	}

	name = strings.TrimSpace(name)
	if err := validateCategoryName(name); err != nil {
		return Category{}, err
	}
	description, err := normalizeCategoryDescription(description)
	if err != nil {
		return Category{}, err
	}

	var created Category
	err = updateCategoryState(s.root, func(state *categoryState) error {
		for _, category := range state.Categories {
			if strings.EqualFold(category.Name, name) {
				return fmt.Errorf("a category named %q already exists", category.Name)
			}
		}
		existing := make(map[string]bool, len(state.Categories))
		for _, category := range state.Categories {
			existing[category.ID] = true
		}
		created = Category{
			ID:          newCategoryID(name, existing),
			Name:        name,
			Description: description,
			Kind:        categoryKindNotes,
			Created:     time.Now().UnixMilli(),
		}
		state.Categories = append(state.Categories, created)
		return nil
	})
	if err != nil {
		return Category{}, err
	}
	s.markCategoryWrite()
	return created, nil
}

// UpdateCategory renames a category or changes its purpose description. The
// journal kind is preserved.
func (s *NotesService) UpdateCategory(id, name, description string) (Category, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Category{}, err
	}

	name = strings.TrimSpace(name)
	if err := validateCategoryName(name); err != nil {
		return Category{}, err
	}
	description, err := normalizeCategoryDescription(description)
	if err != nil {
		return Category{}, err
	}

	var updated Category
	err = updateCategoryState(s.root, func(state *categoryState) error {
		for i := range state.Categories {
			category := &state.Categories[i]
			if category.ID != id {
				continue
			}
			for _, other := range state.Categories {
				if other.ID != id && strings.EqualFold(other.Name, name) {
					return fmt.Errorf("a category named %q already exists", other.Name)
				}
			}
			category.Name = name
			category.Description = description
			updated = *category
			return nil
		}
		return fmt.Errorf("category %q not found", id)
	})
	if err != nil {
		return Category{}, err
	}
	s.markCategoryWrite()
	return updated, nil
}

// DeleteCategory removes a category and clears its assignments. The notes
// themselves are kept; they become uncategorised.
func (s *NotesService) DeleteCategory(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return err
	}

	err := updateCategoryState(s.root, func(state *categoryState) error {
		index := -1
		for i, category := range state.Categories {
			if category.ID == id {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("category %q not found", id)
		}
		state.Categories = append(state.Categories[:index], state.Categories[index+1:]...)
		for noteID, categoryID := range state.Assignments {
			if categoryID == id {
				delete(state.Assignments, noteID)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.markCategoryWrite()
	return nil
}

// AssignCategory moves a note into a category. An empty category id clears the
// assignment.
func (s *NotesService) AssignCategory(noteID, categoryID string) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Note{}, err
	}
	if err := validateID(noteID); err != nil {
		return Note{}, err
	}
	note, err := s.readNoteLocked(noteID)
	if err != nil {
		return Note{}, err
	}

	categoryID = strings.TrimSpace(categoryID)
	if categoryID != "" {
		// Accept a name as well as an id, and report the canonical id so the
		// caller does not have to reload just to learn it.
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
	if err := s.setCategoryAssignmentLocked(noteID, categoryID); err != nil {
		return Note{}, err
	}
	note.CategoryID = categoryID
	return note, nil
}

// setCategoryAssignmentLocked writes one assignment. An empty category id
// clears it.
func (s *NotesService) setCategoryAssignmentLocked(noteID, categoryID string) error {
	err := updateCategoryState(s.root, func(state *categoryState) error {
		if categoryID == "" {
			delete(state.Assignments, noteID)
			return nil
		}
		category, ok := findCategory(state, categoryID)
		if !ok {
			return fmt.Errorf("category %q not found", categoryID)
		}
		state.Assignments[noteID] = category.ID
		return nil
	})
	if err != nil {
		return err
	}
	s.markCategoryWrite()
	return nil
}

// resolveCategoryID turns a category id or name into the canonical category.
// An empty reference means "uncategorised".
func (s *NotesService) resolveCategoryID(ref string) (Category, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Category{}, err
	}
	state, err := readCategoryState(s.root)
	if err != nil {
		return Category{}, err
	}
	ref = strings.TrimSpace(ref)
	if isUncategorisedRef(ref) {
		return Category{}, nil
	}
	category, ok := findCategory(&state, ref)
	if !ok {
		return Category{}, fmt.Errorf("category %q not found; call list_categories first", ref)
	}
	return category, nil
}

func (s *NotesService) markCategoryWrite() {
	if s.writes != nil {
		s.writes.mark(categoryStatePath(s.root))
	}
}

// JournalEntry returns the daily log entry for a date, creating it from a
// template when it does not exist yet. An empty date means today. Dates are
// local calendar days in the form YYYY-MM-DD, which is also the note title, so
// entries sort naturally and an agent can find a period by title.
func (s *NotesService) JournalEntry(date string) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Note{}, err
	}

	date = strings.TrimSpace(date)
	if date == "" {
		date = time.Now().Format(dateLayout)
	}
	if _, err := time.Parse(dateLayout, date); err != nil {
		return Note{}, fmt.Errorf("date must be YYYY-MM-DD: %w", err)
	}
	return s.journalEntryLocked(date)
}

func (s *NotesService) journalEntryLocked(date string) (Note, error) {
	state, err := readCategoryState(s.root)
	if err != nil {
		return Note{}, err
	}
	category, ok := categoryByKind(&state, categoryKindJournal)
	if !ok {
		// The user deleted the journal category; recreate it rather than
		// leaving the Today entry permanently broken.
		category = Category{
			ID:          "daily-log",
			Name:        "Daily log",
			Description: "A day-by-day record of what happened, what you learned and what comes next.",
			Kind:        categoryKindJournal,
			Created:     time.Now().UnixMilli(),
		}
		if err := updateCategoryState(s.root, func(next *categoryState) error {
			if _, exists := findCategory(next, category.ID); exists {
				return nil
			}
			next.Categories = append(next.Categories, category)
			return nil
		}); err != nil {
			return Note{}, err
		}
		s.markCategoryWrite()
		state, err = readCategoryState(s.root)
		if err != nil {
			return Note{}, err
		}
		if existing, found := findCategory(&state, category.ID); found {
			category = existing
		}
	}

	var newest *Note
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return Note{}, fmt.Errorf("read notes: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if validateID(id) != nil || assignCategoryForNote(&state, id) != category.ID {
			continue
		}
		note, err := s.readNoteLocked(id)
		if err != nil {
			continue
		}
		if strings.TrimSpace(note.Title) != date {
			continue
		}
		if newest == nil || note.Modified > newest.Modified {
			copied := note
			newest = &copied
		}
	}
	if newest != nil {
		return *newest, nil
	}

	return s.createWithCategory(date, journalEntryTemplate, category.ID)
}

// journalEntryTemplate is the starting point for a daily log entry: enough
// structure to make logging quick without dictating what a day looks like.
const journalEntryTemplate = `## Log

- 

## Learned

- 

## Tomorrow

- 
`

// dateLayout is the calendar-day format used for journal titles and queries.
const dateLayout = "2006-01-02"

// categoryNotes returns the full documents assigned to a category, newest
// first. An empty reference means uncategorised notes. It backs the MCP
// "read this category" tool, which is how a model pulls source material for a
// summary.
func (s *NotesService) categoryNotes(ref string, limit int) (Category, []Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Category{}, nil, err
	}
	return s.categoryNotesLocked(ref, limit)
}

// categoryNotesLocked is categoryNotes for callers that already hold s.mu.
func (s *NotesService) categoryNotesLocked(ref string, limit int) (Category, []Note, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	state, err := readCategoryState(s.root)
	if err != nil {
		return Category{}, nil, err
	}

	ref = strings.TrimSpace(ref)
	uncategorised := isUncategorisedRef(ref)
	category := Category{Name: "Uncategorized"}
	if !uncategorised {
		found, ok := findCategory(&state, ref)
		if !ok {
			return Category{}, nil, fmt.Errorf("category %q not found; call list_categories first", ref)
		}
		category = found
	}

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return Category{}, nil, fmt.Errorf("read notes: %w", err)
	}
	notes := make([]Note, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if validateID(id) != nil {
			continue
		}
		assigned := assignCategoryForNote(&state, id)
		if uncategorised {
			if assigned != "" {
				continue
			}
		} else if assigned != category.ID {
			continue
		}
		note, err := s.readNoteLocked(id)
		if err != nil {
			continue
		}
		notes = append(notes, note)
	}
	sortNotesNewestFirst(notes)
	if len(notes) > limit {
		notes = notes[:limit]
	}
	return category, notes, nil
}

func sortNotesNewestFirst(notes []Note) {
	for i := 1; i < len(notes); i++ {
		for j := i; j > 0 && notes[j].Modified > notes[j-1].Modified; j-- {
			notes[j], notes[j-1] = notes[j-1], notes[j]
		}
	}
}

// clearCategoryAssignment forgets a note's category when the note is deleted.
// Best-effort: a failure must not block the delete.
func (s *NotesService) clearCategoryAssignment(noteID string) error {
	err := updateCategoryState(s.root, func(state *categoryState) error {
		delete(state.Assignments, noteID)
		return nil
	})
	if err == nil {
		s.markCategoryWrite()
	}
	return err
}

// journalNotes returns daily log entries in an inclusive date range, newest
// first. Empty bounds are open. Entries are matched by their YYYY-MM-DD title,
// which is how a model asks for "last week" without knowing note ids.
func (s *NotesService) journalNotes(from, to string) (Category, []Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return Category{}, nil, err
	}

	var err error
	if from, err = normalizeJournalBound(from, "from"); err != nil {
		return Category{}, nil, err
	}
	if to, err = normalizeJournalBound(to, "to"); err != nil {
		return Category{}, nil, err
	}
	if from != "" && to != "" && from > to {
		return Category{}, nil, errors.New("from must be on or before to")
	}

	state, err := readCategoryState(s.root)
	if err != nil {
		return Category{}, nil, err
	}
	category, ok := categoryByKind(&state, categoryKindJournal)
	if !ok {
		return Category{}, nil, errors.New("no daily log category exists yet; call get_journal_entry to create today's entry")
	}

	_, notes, err := s.categoryNotesLocked(category.ID, 200)
	if err != nil {
		return Category{}, nil, err
	}
	filtered := notes[:0]
	for _, note := range notes {
		date := strings.TrimSpace(note.Title)
		if _, err := time.Parse(dateLayout, date); err != nil {
			continue
		}
		if from != "" && date < from {
			continue
		}
		if to != "" && date > to {
			continue
		}
		filtered = append(filtered, note)
	}
	return category, filtered, nil
}

func normalizeJournalBound(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if _, err := time.Parse(dateLayout, value); err != nil {
		return "", fmt.Errorf("%s must be YYYY-MM-DD: %w", field, err)
	}
	return value, nil
}
