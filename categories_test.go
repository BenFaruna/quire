package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureCategoryDefaults(t *testing.T) {
	root := t.TempDir()
	if err := ensureCategoryDefaults(root); err != nil {
		t.Fatalf("ensure defaults: %v", err)
	}
	// A second call must not append a duplicate set.
	if err := ensureCategoryDefaults(root); err != nil {
		t.Fatalf("ensure defaults again: %v", err)
	}

	state, err := readCategoryState(root)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(state.Categories) != 2 {
		t.Fatalf("expected 2 seeded categories, got %d", len(state.Categories))
	}
	journal, ok := categoryByKind(&state, categoryKindJournal)
	if !ok {
		t.Fatal("expected a journal category")
	}
	if journal.Name != "Daily log" {
		t.Fatalf("expected Daily log, got %q", journal.Name)
	}
	if journal.Description == "" {
		t.Fatal("seeded journal category should explain its purpose")
	}
}

func TestCategoryCreateListAndAssign(t *testing.T) {
	root := t.TempDir()
	service := &NotesService{root: root}

	client, err := service.CreateCategory("Client work", "Notes from client calls")
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	if client.ID == "" || client.Kind != categoryKindNotes {
		t.Fatalf("unexpected category: %+v", client)
	}

	categories, err := service.Categories()
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	if len(categories) != 1 || categories[0].Name != "Client work" {
		t.Fatalf("unexpected categories: %+v", categories)
	}

	note, err := service.CreateIn(client.ID)
	if err != nil {
		t.Fatalf("create note in category: %v", err)
	}
	if note.CategoryID != client.ID {
		t.Fatalf("note category = %q, want %q", note.CategoryID, client.ID)
	}

	notes, err := service.List()
	if err != nil {
		t.Fatalf("list notes: %v", err)
	}
	if len(notes) != 1 || notes[0].CategoryID != client.ID {
		t.Fatalf("list did not carry the category: %+v", notes)
	}

	// Reassigning through a category name resolves to the canonical id.
	other, err := service.CreateCategory("Ideas", "")
	if err != nil {
		t.Fatalf("create second category: %v", err)
	}
	moved, err := service.AssignCategory(note.ID, other.Name)
	if err != nil {
		t.Fatalf("assign by name: %v", err)
	}
	if moved.CategoryID != other.ID {
		t.Fatalf("moved category = %q, want %q", moved.CategoryID, other.ID)
	}

	cleared, err := service.AssignCategory(note.ID, "")
	if err != nil {
		t.Fatalf("clear assignment: %v", err)
	}
	if cleared.CategoryID != "" {
		t.Fatalf("expected no category, got %q", cleared.CategoryID)
	}
}

func TestCategoryValidationAndRename(t *testing.T) {
	service := &NotesService{root: t.TempDir()}

	if _, err := service.CreateCategory("   ", ""); err == nil {
		t.Fatal("expected an error for a blank name")
	}
	if _, err := service.CreateCategory(strings.Repeat("x", maxCategoryNameRunes+1), ""); err == nil {
		t.Fatal("expected an error for an overly long name")
	}
	if _, err := service.CreateCategory("Reading", strings.Repeat("y", maxCategoryDescriptionRunes+1)); err == nil {
		t.Fatal("expected an error for an overly long description")
	}

	first, err := service.CreateCategory("Reading", "Books and papers")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.CreateCategory("reading", ""); err == nil {
		t.Fatal("expected duplicate names to be rejected case-insensitively")
	}
	if _, err := service.CreateCategory("Publishing", ""); err != nil {
		t.Fatalf("create second: %v", err)
	}

	updated, err := service.UpdateCategory(first.ID, "Reading list", "Books to finish")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Reading list" || updated.Description != "Books to finish" {
		t.Fatalf("unexpected updated category: %+v", updated)
	}
	if _, err := service.UpdateCategory(first.ID, "publishing", ""); err == nil {
		t.Fatal("expected rename onto another category to fail")
	}
}

func TestDeleteCategoryKeepsNotes(t *testing.T) {
	service := &NotesService{root: t.TempDir()}

	category, err := service.CreateCategory("Archive", "")
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	note, err := service.CreateIn(category.ID)
	if err != nil {
		t.Fatalf("create note: %v", err)
	}

	if err := service.DeleteCategory(category.ID); err != nil {
		t.Fatalf("delete category: %v", err)
	}

	if _, err := service.Get(note.ID); err != nil {
		t.Fatalf("note should survive category deletion: %v", err)
	}
	notes, err := service.List()
	if err != nil {
		t.Fatalf("list notes: %v", err)
	}
	if len(notes) != 1 || notes[0].CategoryID != "" {
		t.Fatalf("expected an uncategorised note, got %+v", notes)
	}
	categories, err := service.Categories()
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	if len(categories) != 0 {
		t.Fatalf("expected no categories, got %+v", categories)
	}
}

func TestDeletingNoteClearsAssignment(t *testing.T) {
	root := t.TempDir()
	service := &NotesService{root: root}

	category, err := service.CreateCategory("Temporary", "")
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	note, err := service.CreateIn(category.ID)
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	if err := service.Delete(note.ID); err != nil {
		t.Fatalf("delete note: %v", err)
	}

	state, err := readCategoryState(root)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if _, exists := state.Assignments[note.ID]; exists {
		t.Fatal("deleted note should not keep a category assignment")
	}
}

func TestJournalEntryCreatesThenFinds(t *testing.T) {
	service := &NotesService{root: t.TempDir()}
	date := "2026-11-08"

	entry, err := service.JournalEntry(date)
	if err != nil {
		t.Fatalf("journal entry: %v", err)
	}
	if entry.Title != date {
		t.Fatalf("title = %q, want %q", entry.Title, date)
	}
	if entry.CategoryID == "" {
		t.Fatal("journal entry should be filed in a category")
	}
	if !strings.Contains(entry.Body, "## Log") || !strings.Contains(entry.Body, "## Tomorrow") {
		t.Fatalf("entry should start from the template, got %q", entry.Body)
	}

	again, err := service.JournalEntry(date)
	if err != nil {
		t.Fatalf("journal entry again: %v", err)
	}
	if again.ID != entry.ID {
		t.Fatalf("expected the same entry, got %q and %q", entry.ID, again.ID)
	}

	state, err := readCategoryState(service.root)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	journal, ok := categoryByKind(&state, categoryKindJournal)
	if !ok || journal.ID != entry.CategoryID {
		t.Fatalf("entry should use the journal category, got %+v", state.Categories)
	}
}

func TestJournalEntryRecreatesMissingCategory(t *testing.T) {
	service := &NotesService{root: t.TempDir()}

	category, err := service.CreateCategory("Daily log", "")
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	// A plain category is not the journal; make it one by seeding the kind in
	// the sidecar, then delete it to exercise the recovery path.
	if err := updateCategoryState(service.root, func(state *categoryState) error {
		for i := range state.Categories {
			if state.Categories[i].ID == category.ID {
				state.Categories[i].Kind = categoryKindJournal
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("mark journal: %v", err)
	}
	if err := service.DeleteCategory(category.ID); err != nil {
		t.Fatalf("delete category: %v", err)
	}
	if _, ok := categoryByKind(mustReadCategories(t, service.root), categoryKindJournal); ok {
		t.Fatal("journal category should have been deleted")
	}

	entry, err := service.JournalEntry("2026-11-08")
	if err != nil {
		t.Fatalf("journal entry: %v", err)
	}
	if entry.CategoryID == "" {
		t.Fatal("expected the journal category to be recreated")
	}
}

func TestJournalNotesRange(t *testing.T) {
	service := &NotesService{root: t.TempDir()}

	for _, date := range []string{"2026-11-01", "2026-11-03", "2026-11-05"} {
		if _, err := service.JournalEntry(date); err != nil {
			t.Fatalf("create %s: %v", date, err)
		}
	}

	category, entries, err := service.journalNotes("2026-11-01", "2026-11-03")
	if err != nil {
		t.Fatalf("journal notes: %v", err)
	}
	if category.Kind != categoryKindJournal {
		t.Fatalf("unexpected category: %+v", category)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries in range, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.Title < "2026-11-01" || entry.Title > "2026-11-03" {
			t.Fatalf("entry %q is outside the range", entry.Title)
		}
	}

	if _, _, err := service.journalNotes("2026-11-05", "2026-11-01"); err == nil {
		t.Fatal("expected an inverted range to fail")
	}
	if _, _, err := service.journalNotes("yesterday", ""); err == nil {
		t.Fatal("expected a bad date to fail")
	}
}

func TestCategoryNotesSelectsCategory(t *testing.T) {
	service := &NotesService{root: t.TempDir()}

	work, err := service.CreateCategory("Work", "")
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	filed, err := service.CreateIn(work.ID)
	if err != nil {
		t.Fatalf("create filed note: %v", err)
	}
	loose, err := service.Create()
	if err != nil {
		t.Fatalf("create loose note: %v", err)
	}

	_, notes, err := service.categoryNotes(work.ID, 0)
	if err != nil {
		t.Fatalf("category notes: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != filed.ID {
		t.Fatalf("unexpected category notes: %+v", notes)
	}

	category, uncategorised, err := service.categoryNotes("", 0)
	if err != nil {
		t.Fatalf("uncategorised notes: %v", err)
	}
	if category.Name != "Uncategorized" {
		t.Fatalf("unexpected category name %q", category.Name)
	}
	if len(uncategorised) != 1 || uncategorised[0].ID != loose.ID {
		t.Fatalf("unexpected uncategorised notes: %+v", uncategorised)
	}

	if _, _, err := service.categoryNotes("missing", 0); err == nil {
		t.Fatal("expected an unknown category to fail")
	}
}

func TestCorruptCategoryStateIsMovedAside(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, categoryStateName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt state: %v", err)
	}

	state, err := readCategoryState(root)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(state.Categories) != 0 {
		t.Fatalf("expected an empty state, got %+v", state)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("expected the corrupt file to be moved aside: %v", err)
	}
}

func TestCategoryStateNormalisesHandEdits(t *testing.T) {
	root := t.TempDir()
	state := emptyCategoryState()
	state.Categories = []Category{
		{ID: "../escape", Name: "Bad", Created: 1},
		{ID: "good", Name: "  Good  ", Kind: "mystery", Created: 2},
	}
	state.Assignments = map[string]string{
		"note-1":  "good",
		"note-2":  "missing",
		"bad..id": "good",
	}
	if err := writeCategoryState(root, state); err != nil {
		t.Fatalf("write state: %v", err)
	}

	raw, err := readCategoryState(root)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(raw.Categories) != 1 || raw.Categories[0].Name != "Good" {
		t.Fatalf("unexpected categories: %+v", raw.Categories)
	}
	if raw.Categories[0].Kind != categoryKindNotes {
		t.Fatalf("unknown kind should fall back to notes, got %q", raw.Categories[0].Kind)
	}
	if len(raw.Assignments) != 1 || raw.Assignments["note-1"] != "good" {
		t.Fatalf("unexpected assignments: %+v", raw.Assignments)
	}
	if raw.Updated == 0 {
		t.Fatal("expected the state to be timestamped")
	}
	_ = time.Now()
}

func mustReadCategories(t *testing.T, root string) *categoryState {
	t.Helper()
	state, err := readCategoryState(root)
	if err != nil {
		t.Fatalf("read categories: %v", err)
	}
	return &state
}
