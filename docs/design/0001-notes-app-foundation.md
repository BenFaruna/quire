# Notes App Foundation

**Goal:** Replace the Wails starter screen with a focused, local-first Markdown
notes app: a searchable note list, a distraction-free editor, a live rendered
preview, safe export/import, and crash-safe saves. Every note stays a plain
`.md` file the user can read, copy, or edit outside the app.

**Architecture:** A single Go service, `NotesService`, owns the notes directory
and acts as the only writer. It stores one Markdown file per note, renders
Markdown to HTML in Go with goldmark, and exposes CRUD, import, export, and
print-support methods to a vanilla TypeScript frontend through generated Wails
bindings. The frontend is a thin shell: it holds UI state, debounces saves, and
renders the HTML that Go returns. No note content is rendered by a client-side
Markdown parser, and no note content is ever written by the webview directly.

**Tech Stack:** Go 1.26, Wails v3.0.0 (`application.Service`, embedded frontend
assets), vanilla TypeScript + Vite, goldmark (GFM, footnotes, typographer,
Chroma highlighting), plain CSS.

**Spec:** this document. It is retrospective: it records the foundation as built
so later documents (`0003-planner-sidecar.md` and the publish work) have a
baseline. Sections 1–10 describe the design; Section 11 marks the boundary.

## Global Constraints

- **The `.md` file is the source of truth.** Format: an optional leading
  `# Title` line, a blank line, then the body. No database, no index file, no
  proprietary wrapper. If the app disappears, the notes still open in any editor.
- **One file per note, named by ID.** IDs are generated, never derived from the
  title, so renaming a note never moves a file and duplicate titles are safe.
- **All writes go through `writeFileAtomic`.** Save, create, import, and export
  share one temp-file → `fsync` → `chmod` → `rename` path. No bare
  `os.WriteFile` on note content.
- **All IDs are validated before they are used in a path.** `safeID`
  (`^[A-Za-z0-9_-]+$`) and `validateID` block traversal (`..`, `/`, absolute
  paths) at every public entry point that accepts an ID.
- **Raw HTML in notes is never executed.** The goldmark instance deliberately
  omits `WithUnsafe`; raw HTML is replaced with a comment, so imported or
  pasted markup cannot inject scripts into the webview.
- **The frontend never invents content.** Titles, bodies, previews, export
  files, and rendered HTML all come from Go. TypeScript handles layout,
  keyboard shortcuts, timers, and display-only formatting (relative times,
  default filenames).
- **Generated bindings are not hand-edited.** Changing an exported service
  method or model requires `wails3 generate bindings -ts`.

## Review Focus

1. **Save races.** Autosave is debounced, but switching notes, creating,
   deleting, importing, exporting, and printing must flush pending edits first.
   Two guards make this safe: a monotonically increasing save sequence ignores
   stale responses, and a selection sequence ignores a note load that finished
   after the user already moved on. Tests cannot cover the webview timers; the
   Go side guarantees that each `Save` is all-or-nothing.
2. **Crash safety.** A process kill during a save must leave the previous note
   intact, not a truncated file. `TestAtomicWriteKeepsOriginalOnFailure` and
   `TestSaveIsAtomicAndLeavesNoTempFiles` pin this, including temp-file cleanup.
3. **Untrusted titles at the filesystem boundary.** Titles become export file
   names. They may contain `/`, `..`, control characters, emoji, or 500
   characters of text. `safeExportFilename` strips path separators, collapses
   whitespace, trims leading/trailing dots and dashes, caps the stem at 80
   runes, and falls back to `note`.
4. **Notes edited outside the app.** `List` and `Get` always read from disk;
   there is no in-memory cache to go stale. The file's modification time is the
   only timestamp shown.
5. **Markdown that the app did not author.** Imported files may have CRLF, a
   UTF-8 BOM, no H1, or an H1 that is really a paragraph. Import normalises
   line endings, strips invalid UTF-8, and falls back to the filename as the
   title when no H1 is present.
6. **The webview is a renderer, not a filesystem client.** File pickers and
   save dialogs are native Wails dialogs; the frontend passes paths to Go.
   Paths supplied by the UI are still validated where they address a note ID.

---

## 1. Problem and outcomes

The Wails v3 starter ships a demo window and a single "Greet" binding. That is
not an app. The foundation needed to answer four user questions:

- What notes do I have, and which one was I just editing? (List + restore)
- Can I write Markdown and see the result without leaving the window? (Preview)
- Can I get my notes out, and bring existing Markdown in? (Export / import)
- Will my writing survive a crash or a full disk? (Atomic saves)

After this work the app is a usable desktop notes client:

- Notes open and save as ordinary files in a known directory.
- The editor and preview stay in sync; write, split, and read modes cover
  different working styles.
- Notes can be imported in bulk, searched, exported individually or in a batch,
  printed, and deleted with confirmation.
- The visual shell is a dark, quiet workspace rather than the template demo.

### Non-goals at this layer

- No accounts, sync, cloud storage, or collaboration. The notes directory is
  the transport.
- No rich-text/WYSIWYG editing. The source is Markdown, edited as text.
- No database or full-text index. `List` scans the directory; a personal notes
  folder is small enough that this is simpler and more robust.
- No publishing, scheduling, or planner state. Those are downstream features
  and deliberately keep their state out of the `.md` file.

## 2. Process and window

`main.go` is deliberately small:

- `//go:embed all:frontend/dist` embeds the built frontend into the binary, so
  `bin/` is a single self-contained executable.
- `application.New` registers `NotesService` and configures
  `AssetFileServerFS(assets)` to serve the embedded build.
- One window is created: 1280×820 (minimum 860×560), title "Notes", dark
  background `rgb(14, 17, 22)` matching the CSS `--bg`, and a hidden-inset
  title bar on macOS.
- `ApplicationShouldTerminateAfterLastWindowClosed` makes closing the window
  quit the app, which is the expected behaviour for a single-window utility.

The window is the only entry point. There is no tray icon, menu bar surface, or
secondary window in the foundation.

## 3. Note model and file format

Two shapes cross the binding boundary:

```go
type NoteSummary struct {
    ID       string `json:"id"`
    Title    string `json:"title"`
    Preview  string `json:"preview"`
    Modified int64  `json:"modified"` // Unix milliseconds
}

type Note struct {
    ID       string `json:"id"`
    Title    string `json:"title"`
    Body     string `json:"body"`
    Modified int64  `json:"modified"` // Unix milliseconds
}
```

`NoteSummary` is what the sidebar needs: identity, display title, a one-line
snippet, and a sort key. `Note` is the full document. Splitting them keeps
`List` cheap and makes it obvious that the list is not the document.

### On disk

```markdown
# Groceries

- milk
- eggs
```

- The leading `# ` line is the title. `splitDocument` removes it and returns the
  rest as the body; `composeDocument` re-adds it on every write. The round trip
  is exact for app-authored notes: title, blank line, body, trailing newline.
- A note with no body is written as just `# Title\n`.
- A file without an H1 is still readable: `splitDocument` falls back to the
  first non-empty line as a title (truncated by `previewText`) and keeps the
  whole file as the body. `Import` instead uses the filename as the title.
- Bodies are normalised to LF on write; CRLF is accepted on read. Trailing
  spaces and newlines at the end of the body are trimmed by `composeDocument`.
- Empty titles are never persisted: `Save` and `composeDocument` fall back to
  `"Untitled note"`, and `splitDocument` applies the same fallback when reading.

Timestamps are Unix milliseconds, from `os.FileInfo.ModTime` on read and
`time.Now` on write. The UI converts them to relative labels; nothing else
interprets them.

## 4. Storage layout and lifecycle

The notes directory is resolved lazily on first use:

```text
os.UserConfigDir()/post-planner/notes
```

On Linux that is `~/.config/post-planner/notes`. The directory is created with
`0755` if missing. `NotesPath` exposes it to the UI so the folder button can
copy the location to the clipboard.

`init` is idempotent and guarded by `s.mu`:

1. Resolve and create the root if `s.root` is empty.
2. Scan the directory for any `.md` file (case-insensitive extension).
3. If none exists, seed `Welcome to Notes` from the `welcomeNote` constant.

The welcome note is not decoration. It exercises the renderer end to end —
headings, emphasis, strikethrough, inline code, links, blockquotes, nested
lists, task lists, tables, fenced code in two languages, and footnotes — so a
broken goldmark extension is visible on first launch. It is created through the
same `createWith` path as a user note, so seeding also proves the write path.

Note IDs are generated by `newNoteID`:

```text
YYYYMMDD-HHMMSS-<4 hex chars>
```

The timestamp makes IDs sortable and human-recognisable; the random suffix
avoids collisions when two notes are created in the same second. The generator
stats the candidate path and retries on collision. IDs are opaque to the user
except in filenames.

Deleting a note removes `<id>.md` and tolerates a missing file. The foundation
has no sidecars; later features hook cleanup into `Delete` (publish tracking
first, then planner state). That hook is best-effort by design: failure to
remove tracking data must not block deleting the note itself.

## 5. `NotesService` API

All methods take `s.mu` for the duration of their work. The service is a single
writer, and serialising operations keeps list/read/write consistent without a
more elaborate locking scheme.

| Method | Behaviour |
| --- | --- |
| `List() ([]NoteSummary, error)` | Scans root, reads each `.md`, returns summaries newest-first. Unreadable entries are skipped rather than failing the whole list. |
| `Get(id) (Note, error)` | Validates the ID, reads the file, returns title/body/mtime. A missing file becomes `note "<id>" not found`. |
| `Create() (Note, error)` | Generates an ID and writes `# Untitled note`. |
| `Save(id, title, body) (Note, error)` | Validates the ID, normalises title/body, atomically rewrites the file, returns the saved shape and a new mtime. |
| `Delete(id) error` | Validates the ID, removes `<id>.md`, tolerates `ErrNotExist`, then runs sidecar cleanup. |
| `Import(paths []string) ([]Note, error)` | Validates each source file and copies it into a new note (see Section 8). |
| `Render(markdown) (string, error)` | Runs the shared goldmark pipeline and returns sanitised HTML. |
| `Export(ids, format, destPath) (int, error)` | Writes selected notes as Markdown or HTML, zipping multiple notes (see Section 8). |
| `NotesPath() (string, error)` | Returns the resolved notes directory for display. |

Two conventions matter across the API:

- **Errors are user-facing strings.** They name the operation and the file
  (`read note: …`, `save note: …`, `export notes: …`) so the toast can show
  them without a second translation layer.
- **A missing note is an error; a missing sidecar is not.** Only `.md` content
  is required to exist. This becomes important when the publish and planner
  layers add optional `<id>.*.json` files.

`publishRoot()` is an unexported accessor added later so `PublishService` can
place sidecars next to notes without duplicating path resolution. It uses the
same `init` + mutex contract as the public methods.

## 6. Markdown rendering pipeline

One shared `goldmark.Markdown` instance serves the preview, exported HTML, and
print view:

```go
goldmark.New(
    goldmark.WithExtensions(
        extension.GFM,         // tables, strikethrough, task lists, autolinks
        extension.Footnote,
        extension.Typographer, // curly quotes, em/en dashes, ellipses
        highlighting.NewHighlighting(
            highlighting.WithStyle("github-dark"),
        ),
    ),
    goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)
```

Decisions embedded here:

- **GFM, not "Markdown plus".** Users coming from GitHub, Obsidian, or a README
  get the syntax they expect: tables, `~~strike~~`, `- [ ]` tasks, bare URLs,
  and `[^1]` footnotes.
- **Typographer is on.** Straight quotes and `--` become typographic characters
  in the preview. This is display behaviour; the source file is unchanged.
- **Chroma uses `github-dark`.** Highlighting is emitted as inline styles/classes
  in the HTML, so exported and printed HTML look the same without shipping a
  separate stylesheet for code.
- **Auto heading IDs** make long documents linkable and give the preview stable
  anchors; they also make repeated renders idempotent.
- **Raw HTML is off.** Goldmark replaces it with `<!-- raw HTML omitted -->`.
  This is the security boundary, not a formatting preference. Exported HTML
  reuses the rendered body inside a `template.HTML` wrapper only because the
  body is already known to be goldmark output.

The instance is safe for concurrent use after construction; `renderMarkdown`
only owns a local `bytes.Buffer`. `Render` is therefore a pure service call with
no note-directory access and no lock contention with saves beyond the service
mutex.

The frontend adds two behaviours on top of the HTML:

- External `http(s)` links get `target="_blank"` and
  `rel="noreferrer noopener"`, so a note link opens the system browser rather
  than navigating the webview away from the app.
- Task-list checkboxes are re-enabled (goldmark emits them disabled) and wired
  to rewrite the matching `- [ ]` / `- [x]` line in the source. This keeps the
  Markdown file authoritative while making the preview interactive.
- Every fenced code block is wrapped in a `.code-block` with a Copy button that
  copies the rendered code text, with a `document.execCommand` fallback for
  webviews where the clipboard API is unavailable.

## 7. Frontend shell and UX

`frontend/index.html` is a two-column application shell:

```text
┌───────────────┬──────────────────────────────────────────────┐
│ sidebar       │ topbar: title · save status · view · actions │
│  new/import   ├──────────────────────────────────────────────┤
│  search       │ editor pane        │ preview pane            │
│  note list    │ (Markdown textarea)│ (rendered article)      │
│  folder/count ├──────────────────────────────────────────────┤
│               │ statusbar: words · characters · Markdown     │
└───────────────┴──────────────────────────────────────────────┘
```

### Sidebar

- New note and import buttons in the header; the note list below.
- Search filters by title and preview text, case-insensitively. The clear
  button appears only when a query is present.
- Each row shows the title, a relative timestamp, and a flattened preview
  (`previewText` strips common Markdown punctuation and caps at 120 runes).
- `notes-count` reports the total, not the filtered count; the empty state
  distinguishes "no notes" from "no search matches".
- The folder button copies the notes path to the clipboard and shows a toast.

### Editor and preview

Three modes are toggled by the segmented control and persisted in
`localStorage` under `notes.viewMode`:

| Mode | Layout |
| --- | --- |
| Write | editor only |
| Split | editor and preview side by side |
| Read | preview only |

The last opened note is persisted under `notes.lastId` and restored on load if
it still exists; otherwise the newest note is selected. The title is a separate
single-line input above the body, but both are composed into the same document
on save.

- **Autosave** is debounced at 700 ms after the last keystroke. Status text
  moves through "Unsaved changes" → "Saving…" → "Saved just now" → relative
  time on reload. A failed save restores the dirty flag and shows a toast.
- **Preview** is debounced at 120 ms, separately from saving, so typing feels
  responsive without writing to disk on every keystroke.
- **Stats** show word and character counts for the body; the statusbar also
  carries a static "Markdown" badge.
- **Formatting toolbar** wraps the selection for bold, italic, code, links,
  headings, lists, quotes, and code blocks. It edits the textarea selection
  rather than injecting rendered HTML.
- **Shortcuts** cover the frequent paths: `Ctrl/Cmd+N` new, `S` save,
  `E` cycle view, `Shift+E` export/print, `Shift+P` publish (later), `F` focus
  search, and `B`/`I`/`K` for bold/italic/link.

### Modals and feedback

- Delete uses an in-app themed confirmation modal rather than the browser's
  `confirm()`, so the destructive action looks like the rest of the app. The
  confirm button is red at rest, not only on hover.
- The export dialog (Section 8) is also in-app.
- A single toast surface (`role="status"`, `aria-live="polite"`) reports
  successes and failures. `reportError` logs the original error and appends its
  message to the toast.
- `setBusy` disables editor actions while a load, save-critical transition,
  import, or print is in flight, preventing a second action from racing the
  first.

### Styling

`frontend/src/style.css` is a small design system built on CSS variables:
surface levels, `--text` / `--text-muted` / `--text-dim`, an amber accent, a
red danger colour, border and radius tokens, and a monospace stack used in both
the editor and code blocks. The palette is dark and low-contrast by default;
the window background is set to the same `#0e1116` in `main.go` so there is no
white flash before the webview paints. Layout is grid-based with two responsive
breakpoints (1040 px and 820 px) that narrow the sidebar and reduce preview
padding.

## 8. Import, export, and print

### Import

Native `Dialogs.OpenFile` allows multiple selection and accepts `.md`,
`.markdown`, `.mdown`, `.mkd`, and `.txt` (plus an all-files filter). The
frontend hands absolute paths to `NotesService.Import`; all reading happens in
Go.

Per file:

1. `os.Stat` rejects directories and files larger than `maxImportSize` (5 MB).
2. The file is read, invalid UTF-8 is replaced, leading whitespace/newlines are
   trimmed, and CRLF becomes LF.
3. If the content starts with `# `, `splitDocument` extracts the title and body.
   Otherwise the filename (without extension) becomes the title and the whole
   content becomes the body.
4. The note is created with `createWith`, so it gets a fresh generated ID and
   the normal atomic write. The original file is not modified or moved.

Import is additive and non-destructive. If a later file fails, the error names
that file and the notes already imported in the same batch remain. The UI
reloads the list, selects the first imported note, and reports the count.

### Export

`Export(ids, format, destPath)` is the single backend entry point. The frontend
dialog builds the selection, chooses a format, asks for a destination, and calls
it:

| Selection | Format | Output |
| --- | --- | --- |
| one note | Markdown | one `.md` file, exactly the on-disk document |
| one note | HTML | one self-contained `.html` file |
| several | Markdown | `.zip`, one `.md` per note |
| several | HTML | `.zip`, one `.html` per note |

Details that keep exports trustworthy:

- **Markdown is the source.** A Markdown export is `composeDocument(title, body)`
  — the same text the app would write — not a re-serialisation of the HTML.
- **HTML reuses the preview pipeline.** `renderMarkdown` produces the body, then
  `renderExportHTML` wraps it in a document with an embedded dark theme and a
  print stylesheet. Export and preview cannot drift because they share the
  renderer.
- **HTML is escaped at the template boundary.** The title goes through
  `html/template` normally; only the already-sanitised goldmark body is marked
  `template.HTML`.
- **Names are portable and unique.** `safeExportFilename` sanitises the title;
  `uniqueExportName` lowercases for comparison and appends `-2`, `-3`, … so two
  notes titled "Same title" do not overwrite each other inside a zip.
- **Writes are atomic.** The single file or the whole zip is written through
  `writeFileAtomic`, so a failed export never leaves a half-written archive at
  the destination.
- **Validation is explicit.** Empty selections, unknown formats, and blank
  destinations return errors before any file is touched.

The UI dialog:

- Lists every note with a checkbox; the open note is preselected (all notes if
  none is open).
- Has a Select-all checkbox with an indeterminate state and a live
  "N selected" count; the confirm button is disabled at zero and its label
  reflects the action ("Export 3" / "Print 2").
- Offers Markdown, HTML, and Print / PDF. Choosing print closes the dialog and
  prepares print pages instead of writing a file.
- Flushes pending edits before exporting or printing, so the output includes
  what is currently on screen.
- Suggests `<title>.md`, `<title>.html`, or `notes-export-YYYY-MM-DD.zip`, and
  appends the correct extension if the user removes it. The multi-note case
  uses `.zip`, never `.zip.md`.

### Print

Print is not a file format in Go. The frontend:

1. Loads each selected note via `Get`.
2. Renders `# Title\n\nBody` through `Render` so the print output matches the
   preview.
3. Appends an `<article class="print-note markdown-body">` per note to a hidden
   `#print-root`.
4. Calls `window.print()` after a short delay for layout, then clears the print
   root on `afterprint`.

`@media print` hides the app shell, toast, and modals, and shows only the print
root. It uses a light paper palette, keeps code blocks readable with
`print-color-adjust: exact`, and forces a page break between notes. Print/PDF is
therefore a browser-rendered artifact, not a second serializer that could drift
from the on-screen document.

## 9. Atomicity, validation, and failure handling

### `writeFileAtomic`

```text
create temp file in the destination directory
write via callback
fsync
close
chmod
rename over the destination
```

Each property matters:

- **Same directory** guarantees the final `rename` is atomic on a single
  filesystem. A temp directory elsewhere would degrade to a copy.
- **fsync before rename** means the bytes are durable before the name points at
  them. Without it, a power loss can leave the new name with empty content.
- **chmod after close** applies the intended `0644` even though `CreateTemp`
  starts at `0600`, so exported files and notes are readable and not
  accidentally restricted.
- **deferred cleanup** closes and removes the temp path on every error path, so
  a failed save does not accumulate `.tmp-*` files.
- **callback-based writing** lets the same helper handle a note string and a
  streaming `zip.Writer` without buffering the archive in memory.

The failure contract is simple: if `writeFileAtomic` returns an error, the
previous destination is untouched. Tests simulate a mid-write failure and verify
both the original content and the absence of temp files.

### ID validation

Every public method that takes a note ID calls `validateID` before joining it to
a path. The regex allows only ASCII letters, digits, `_`, and `-`. This blocks
`../`, absolute paths, separators, NULs, and shell-significant characters in one
place. `newNoteID` produces IDs that satisfy the same grammar, so generated and
user-supplied IDs are indistinguishable to the validator. Sidecar IDs added
later reuse it.

### Error handling in the UI

- Load/save/create/delete/import/export/print failures each have a context
  string ("Could not save note") and surface the Go error in a toast.
- A failed load leaves the current editor content alone and re-enables it.
- Closing the window with pending edits is not special-cased in the foundation;
  autosave and explicit save keep the window short, but the durable guarantee is
  per-write, not per-session.
- `List` skips unreadable files instead of failing, so one bad file cannot hide
  every other note. `Get` does report a missing note, because the user asked for
  that specific document.

## 10. Testing

`noteservice_test.go` is the only automated suite; there is no frontend test
runner. The division is deliberate: rendering, storage, import/export, and
validation live in Go and are directly testable; the frontend is checked by
`tsc --noEmit`, `vite build`, and a manual smoke run.

| Test | What it pins |
| --- | --- |
| `TestRenderMarkdownExtensions` | GFM tables, task lists, strikethrough, footnotes, and highlighting are enabled. |
| `TestRenderOmitsRawHTML` | Raw HTML never reaches the output. |
| `TestSplitAndComposeDocument` | Title/body round trip, including the empty-body case. |
| `TestNotesServiceCRUD` | Create, list, get, save, delete, and ordering. |
| `TestNotesServiceRejectsTraversal` | IDs like `../x` are rejected by every path-taking method. |
| `TestEmptyTitleFallsBack` | Empty/whitespace titles become "Untitled note". |
| `TestFirstRunSeedsWelcomeNote` | First initialisation creates exactly one welcome note; later initialisation does not duplicate it. |
| `TestImportNotes` | H1 and filename title paths, CRLF handling, and bulk import. |
| `TestExportSingleMarkdown` | Exact `.md` output for one note. |
| `TestExportMultipleNotesAsZip` | Zip entry count, content prefix, and duplicate-title suffixes. |
| `TestExportHTML` | DOCTYPE, escaped title, rendered heading/table, no raw title HTML. |
| `TestSaveIsAtomicAndLeavesNoTempFiles` | Repeated large saves leave no `.tmp-*` files and produce a complete document. |
| `TestExportRejectsBadInput` | Empty selection, unsupported format, and blank destination fail. |
| `TestSafeExportFilename` | Path separators, Unicode, leading dots, empty titles, and length cap. |
| `TestAtomicWriteKeepsOriginalOnFailure` | A failed write leaves the old file and no temp file. |

Tests construct `&NotesService{root: t.TempDir()}` directly, bypassing
`os.UserConfigDir`, so they never touch real user data. This is also why the
service takes its root from `init` rather than a global.

## 11. Boundaries and downstream work

This document stops at the notes foundation. Two later layers build on it and
must keep its constraints:

- **Publishing** turns a note into a draft or a remote post and stores tracking
  in `<id>.publish.json` beside the note. It reuses `publishRoot`, atomic writes,
  and the goldmark pipeline for flattening text; it never writes publish state
  into the `.md` file.
- **Planner** adds `<id>.plan.json` and a queue/calendar UI, following the same
  sidecar contract and `Delete` cleanup hook. See
  `0003-planner-sidecar.md`.

What stays true after both:

1. The `.md` file remains a portable Markdown document with no app-specific
   metadata.
2. `NotesService` remains the owner of the notes directory and the only writer
   of note content.
3. Every disk write is atomic, and every ID is validated before use in a path.
4. All rendering, export, and print output derives from the same goldmark
   pipeline, so previews, exports, and printouts cannot disagree.
