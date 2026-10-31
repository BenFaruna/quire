package main

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderMarkdownExtensions(t *testing.T) {
	html, err := renderMarkdown("# Title\n\n| a | b |\n| - | - |\n| 1 | 2 |\n\n- [x] done\n\n~~old~~ and `code`\n")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"<h1", "<table>", `type="checkbox"`, "<del>", "<code>"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q:\n%s", want, html)
		}
	}
}

func TestRenderOmitsRawHTML(t *testing.T) {
	html, err := renderMarkdown("hello <script>alert(1)</script>")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Fatalf("raw HTML was not sanitised: %s", html)
	}
}

func TestSplitAndComposeDocument(t *testing.T) {
	title, body := splitDocument("# Hello\n\nWorld\n")
	if title != "Hello" || body != "World\n" {
		t.Fatalf("splitDocument = %q, %q", title, body)
	}

	doc := composeDocument("Hello", "World")
	if doc != "# Hello\n\nWorld\n" {
		t.Fatalf("composeDocument = %q", doc)
	}

	if title, _ := splitDocument("Just text\n"); title != "Just text" {
		t.Fatalf("fallback title = %q", title)
	}
}

func TestNotesServiceCRUD(t *testing.T) {
	service := &NotesService{root: t.TempDir()}

	created, err := service.Create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created note has no id")
	}

	saved, err := service.Save(created.ID, "Groceries", "- milk\n- eggs")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Title != "Groceries" {
		t.Fatalf("saved title = %q", saved.Title)
	}

	list, err := service.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Title != "Groceries" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if !strings.Contains(list[0].Preview, "milk") {
		t.Fatalf("preview = %q", list[0].Preview)
	}

	onDisk, err := os.ReadFile(filepath.Join(service.root, created.ID+".md"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.HasPrefix(string(onDisk), "# Groceries\n") {
		t.Fatalf("file content = %q", onDisk)
	}

	if err := service.Delete(created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ = service.List(); len(list) != 0 {
		t.Fatalf("note was not deleted: %+v", list)
	}
}

func TestNotesServiceRejectsTraversal(t *testing.T) {
	service := &NotesService{root: t.TempDir()}
	if err := service.Delete("../../etc/passwd"); err == nil {
		t.Fatal("expected invalid id error")
	}
	if _, err := service.Get("../secret"); err == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestEmptyTitleFallsBack(t *testing.T) {
	service := &NotesService{root: t.TempDir()}
	note, err := service.Create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	saved, err := service.Save(note.ID, "   ", "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Title != "Untitled note" {
		t.Fatalf("title = %q", saved.Title)
	}
}

func TestFirstRunSeedsWelcomeNote(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME is only honoured on Linux")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	service := NewNotesService()
	notes, err := service.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected 1 seeded note, got %d", len(notes))
	}
	if notes[0].Title != "Welcome to Notes" {
		t.Fatalf("seeded title = %q", notes[0].Title)
	}
}

func TestImportNotes(t *testing.T) {
	service := &NotesService{root: t.TempDir()}
	source := t.TempDir()

	withHeading := filepath.Join(source, "meeting-notes.md")
	if err := os.WriteFile(withHeading, []byte("# Weekly sync\n\n- ship it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noHeading := filepath.Join(source, "ideas.md")
	if err := os.WriteFile(noHeading, []byte("Just some ideas\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	imported, err := service.Import([]string{withHeading, noHeading})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(imported) != 2 {
		t.Fatalf("imported %d notes", len(imported))
	}
	if imported[0].Title != "Weekly sync" || !strings.Contains(imported[0].Body, "ship it") {
		t.Fatalf("unexpected first import: %+v", imported[0])
	}
	if imported[1].Title != "ideas" || !strings.Contains(imported[1].Body, "Just some ideas") {
		t.Fatalf("unexpected second import: %+v", imported[1])
	}

	// Importing the same file again must create a distinct note, not overwrite.
	again, err := service.Import([]string{withHeading})
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again[0].ID == imported[0].ID {
		t.Fatal("re-import overwrote an existing note")
	}

	if notes, _ := service.List(); len(notes) != 3 {
		t.Fatalf("expected 3 notes on disk, got %d", len(notes))
	}

	if _, err := service.Import(nil); err == nil {
		t.Fatal("expected error for empty import list")
	}
}

func TestExportSingleMarkdown(t *testing.T) {
	service := &NotesService{root: t.TempDir()}
	note, err := service.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Save(note.ID, "Groceries", "- milk\n- eggs"); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "groceries.md")
	count, err := service.Export([]string{note.ID}, exportFormatMarkdown, dest)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if count != 1 {
		t.Fatalf("exported %d notes", count)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# Groceries\n\n- milk\n- eggs\n" {
		t.Fatalf("unexpected export:\n%q", data)
	}
}

func TestExportMultipleNotesAsZip(t *testing.T) {
	service := &NotesService{root: t.TempDir()}
	first, _ := service.Create()
	if _, err := service.Save(first.ID, "Same title", "first body"); err != nil {
		t.Fatal(err)
	}
	second, _ := service.Create()
	if _, err := service.Save(second.ID, "Same title", "second body"); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "notes.zip")
	count, err := service.Export([]string{first.ID, second.ID}, exportFormatMarkdown, dest)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if count != 2 {
		t.Fatalf("exported %d notes", count)
	}

	reader, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()

	if len(reader.File) != 2 {
		t.Fatalf("zip has %d entries", len(reader.File))
	}
	names := map[string]bool{}
	for _, file := range reader.File {
		names[file.Name] = true
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		if !strings.HasPrefix(string(content), "# Same title") {
			t.Fatalf("entry %s has unexpected content %q", file.Name, content)
		}
	}
	if !names["Same title.md"] || !names["Same title-2.md"] {
		t.Fatalf("duplicate titles were not disambiguated: %v", names)
	}
}

func TestExportHTML(t *testing.T) {
	service := &NotesService{root: t.TempDir()}
	note, _ := service.Create()
	if _, err := service.Save(note.ID, "HTML <test>", "| a |\n| - |\n| 1 |\n"); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "note.html")
	if _, err := service.Export([]string{note.ID}, exportFormatHTML, dest); err != nil {
		t.Fatalf("export: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{"<!DOCTYPE html>", "<title>HTML &lt;test&gt;</title>", "<h1", "<table>"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML export missing %q", want)
		}
	}
	if strings.Contains(html, "<test>") {
		t.Error("exported title was not escaped")
	}
}

func TestSaveIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	service := &NotesService{root: root}
	note, err := service.Create()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := service.Save(note.ID, "Atomic", strings.Repeat("x", 1<<16)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}

	data, err := os.ReadFile(filepath.Join(root, note.ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# Atomic\n\n") || len(data) < 1<<16 {
		t.Fatalf("note was not written completely")
	}
}

func TestExportRejectsBadInput(t *testing.T) {
	service := &NotesService{root: t.TempDir()}
	note, _ := service.Create()
	dest := filepath.Join(t.TempDir(), "x.md")

	if _, err := service.Export(nil, exportFormatMarkdown, dest); err == nil {
		t.Error("expected error for empty selection")
	}
	if _, err := service.Export([]string{note.ID}, "pdf", dest); err == nil {
		t.Error("expected error for unsupported format")
	}
	if _, err := service.Export([]string{note.ID}, exportFormatMarkdown, "  "); err == nil {
		t.Error("expected error for empty destination")
	}
}

func TestSafeExportFilename(t *testing.T) {
	cases := map[string]string{
		"Hello World":      "Hello World",
		"a/b\\c:d":         "a-b-c-d",
		"  ..hidden..  ":   "hidden",
		"":                 "note",
		"Über café — 2026": "Über café - 2026",
	}
	for input, want := range cases {
		if got := safeExportFilename(input); got != want {
			t.Errorf("safeExportFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAtomicWriteKeepsOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial data")
		return errors.New("simulated failure")
	})
	if err == nil {
		t.Fatal("expected the write to fail")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("original file was modified: %q", data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestRenderMathWithKaTeX(t *testing.T) {
	html, err := renderMarkdown("Inline $E = mc^2$ works.\n\n$$\n\\frac{a}{b}\n$$\n")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{`class="katex"`, `class="katex-display"`, "<mfrac"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q:\n%s", want, html)
		}
	}
}

func TestRenderMermaidFenceStaysForClient(t *testing.T) {
	html, err := renderMarkdown("```mermaid\ngraph TD\n  A[Start] --> B[End]\n```\n")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{`class="language-mermaid"`, "graph TD"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q:\n%s", want, html)
		}
	}
}
