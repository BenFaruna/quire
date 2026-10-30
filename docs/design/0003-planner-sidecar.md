# Post Planner Implementation Plan

**Goal:** Turn the note-and-publish app into a post planner: every note can be scheduled to one or more platforms, and the user works from a queue and a calendar instead of only a document list.

**Architecture:** Scheduling intent lives in a new per-note sidecar, `<id>.plan.json`, written atomically beside the note — the same pattern as the existing `<id>.publish.json`. A new `PlannerService` owns that sidecar, derives a single display status per note from plan + publish state, and answers queue/backlog queries by scanning the notes directory. The frontend gains a `Notes | Queue | Calendar` switch; all scheduling logic stays in Go so it is testable without a webview.

**Tech Stack:** Go 1.26, Wails v3.0.0 (`application.Service`, `app.Event`), vanilla TypeScript + Vite, goldmark (existing, reused for rendition text).

**Spec:** this document. Sections 1–8 are the specification; Section 9 is the implementation order.

## Global Constraints

- Note files stay pure portable Markdown: `<id>.md` with a leading `# Title`. Never store planner state inside the `.md` file.
- Every write to disk goes through `writeFileAtomic` (`noteservice.go:290`) — no bare `os.WriteFile`.
- Note IDs are validated with `validateID`/`safeID` (`noteservice.go:345` / `noteservice.go:19`) before being used in a path. Slot IDs use the same regex.
- A missing sidecar is not an error; a corrupt sidecar is renamed to `.bak` and treated as empty. This is the established contract of `readPublishState` and the new reader must match it exactly.
- All timestamps on disk are Unix milliseconds (UTC), matching `Modified`, `Updated`, `SyncedAt`, `PublishedAt`.
- The frontend never computes scheduling state; it renders what Go returns. Timezone formatting is the only date logic allowed in TypeScript.
- New exported Go service methods must be followed by `wails3 generate bindings -ts`; `frontend/bindings/**` is generated and never hand-edited.
- Do not break `PublishService`: the existing publish dialog keeps working throughout, and `PublishState` keeps its current meaning (a record of what happened remotely).

## Review Focus

1. **Timezones and DST.** A slot is stored as an absolute instant, so "9:00" scheduled in summer reads as "10:00" after a DST shift. That is the intended behavior, but the UI must always render absolute local time and never a stored day label. Go tests pin that the stored value is untouched by formatting; the rendered-label check is a manual step in Phase 5, because **this project has no frontend test runner** (`frontend/package.json` has only `vite` and `typescript`; `tsc --noEmit` is the only automated frontend check). If a JS test runner is added, this becomes an assertion.
2. **Slots that came due while the app was closed.** Nothing should silently disappear; an unsent past slot must surface as overdue on the first load. Test: a slot 3 days in the past appears in `Queue` and reports `overdue`.
3. **Notes edited outside the app.** A `.md` changed on disk after a rendition was saved must mark the slot stale rather than quietly posting outdated text. Test: `SaveRendition`, modify the note, expect `QueueItem.Stale == true`.
4. **Hand-edited or corrupt plan JSON.** Unknown platform strings, negative timestamps, and truncated JSON must degrade to an empty plan with the file preserved as `.bak`, and must never make `Queue` fail for every other note. Tests cover each.
5. **Orphaned sidecars.** A note deleted outside the app leaves `<id>.plan.json` behind; `Queue` must skip it without error, and the plan file must not resurrect a phantom row. Test: plan sidecar with no matching `.md`.
6. **The exact-due boundary.** A slot at exactly `now` counts as due. Tests pin `<=`, not `<`, and pin that `NotifiedAt` stops a second notification.

---

## 1. Problem and outcomes

Today `NotesService` lists documents and `PublishService` turns one into a draft or a remote post. There is no notion of *when* something should go out, no cross-note view, and no record of intent. The app answers "what did I write?" but not "what am I posting this week?".

After this plan the app answers:

- What am I posting today, and what did I miss? (Queue)
- What is the week/month shaped like? (Calendar)
- What is still just an idea with no slot? (Backlog)
- Remind me when a slot comes due, since X and LinkedIn have no free write API.

### Non-goals

- Auto-posting to X or LinkedIn. Neither has a free write API (X v2 write is paid, LinkedIn needs partner approval). Reminders + clipboard remain the mechanism, and that is a deliberate product decision, not a gap.
- Multi-account support.
- A kanban board with arbitrary columns. The status set is fixed and derived (Section 4).
- Syncing planner state between machines. Files stay the transport: copy the notes directory, get the plan with it.

## 2. Constraints discovered in the existing code

| Fact | Consequence for this design |
| --- | --- |
| `publishRoot()` (`noteservice.go:276`) exposes the notes dir to `PublishService` | `PlannerService` uses the same accessor; sidecars sit next to notes and travel with them |
| `NotesService.Delete` already calls `deletePublishState` (`noteservice.go:199`) | The same hook must also delete the plan sidecar |
| `List()` filters on `filepath.Ext(name) == ".md"` | `<id>.plan.json` is invisible to the note list; no filtering change needed |
| `readPublishState` renames corrupt files to `.bak` and returns an empty state | The plan reader copies this contract verbatim |
| `publishContentHash(title, body)` (`publishstore.go:123`) already fingerprints a note | Reused as `Rendition.SourceHash` to detect stale renditions |
| `Prepare()` (`publish.go:101`) derives a `Draft` from a note on demand | Renditions store a *snapshot* of that draft; `Prepare` stays the default generator |
| `app.Event` is an `*EventManager` with `Emit(name string, data ...any) bool` | Due reminders push `planner:due` to the frontend without new IPC types |
| `notifications.NotificationService.SendNotification(NotificationOptions{ID, Title, Body})` exists in `pkg/services/notifications` | Native reminders are possible, but the service must be registered as an app service; the in-app path is the required, testable one |
| `frontend/src/main.ts` is one 978-line file with module-level state | New screens go in **new files**, not into `main.ts` (Section 7) |

## 3. Data model

### 3.1 File

`<notes-dir>/<id>.plan.json`, one per scheduled note, written atomically, mode `0644`, trailing newline — identical mechanics to `publishstore.go`.

It is a **separate file from `.publish.json`** on purpose:

- `.publish.json` is a record of what already happened remotely, written only after a network success, and owned by `PublishService`.
- `.plan.json` is user intent, written offline, owned by `PlannerService`.

Merging them would make every calendar drag rewrite publish history (and risk clobbering it), and would make a failed publish corrupt the plan. Two files, two lifecycles.

### 3.2 Structs (`planstate.go`)

```go
const (
	planStateVersion = 1

	stageIdea     = "idea"
	stageDrafting = "drafting"
	stageReady    = "ready"

	slotScheduled = "scheduled"
	slotDone      = "done"
	slotSkipped   = "skipped"

	// Derived display statuses (never stored).
	statusOverdue    = "overdue"
	statusScheduled  = "scheduled"
	statusPublished  = "published"
	statusDraft      = "drafting"
	statusIdea       = "idea"
)

// PlanSlot is one intended post: a platform plus the moment it should go out.
type PlanSlot struct {
	ID          string     `json:"id"`
	Platform    string     `json:"platform"`
	ScheduledAt int64      `json:"scheduledAt"`       // UTC ms, 0 means unscheduled
	Status      string     `json:"status"`            // scheduled | done | skipped
	NotifiedAt  int64      `json:"notifiedAt,omitempty"`
	Rendition   *Rendition `json:"rendition,omitempty"`
	Notes       string     `json:"notes,omitempty"`
}

// Rendition is the human-edited text for a slot. It is a snapshot: SourceHash
// records the note it was generated from so later edits can be detected.
type Rendition struct {
	Title      string   `json:"title,omitempty"`
	Text       string   `json:"text,omitempty"`
	Parts      []string `json:"parts,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Canonical  string   `json:"canonical,omitempty"`
	SourceHash string   `json:"sourceHash"`
	EditedAt   int64    `json:"editedAt"`
}

// PlanState is the whole sidecar document.
type PlanState struct {
	Version int        `json:"version"`
	NoteID  string     `json:"noteId"`
	Stage   string     `json:"stage,omitempty"` // idea | drafting | ready; empty means derive
	Updated int64      `json:"updated"`
	Slots   []PlanSlot `json:"slots"`
}
```

`Stage` is an *optional override*. Empty means "derive from the note body", which keeps the common case free of redundant writes and avoids two sources of truth going out of sync.

### 3.3 Example

```json
{
  "version": 1,
  "noteId": "20261028-153000-a1b2",
  "stage": "ready",
  "updated": 1761740000000,
  "slots": [
    {
      "id": "9f2c41a0",
      "platform": "x",
      "scheduledAt": 1761838800000,
      "status": "scheduled",
      "notifiedAt": 0,
      "rendition": {
        "text": "We shipped a planner. Here is what changed:",
        "parts": ["We shipped a planner.", "Here is what changed:"],
        "sourceHash": "sha256:9f2c8e…",
        "editedAt": 1761740000000
      }
    },
    {
      "id": "1b7d03c9",
      "platform": "devto",
      "scheduledAt": 1762011600000,
      "status": "scheduled",
      "rendition": {
        "title": "Building a post planner",
        "tags": ["go", "wails", "markdown"],
        "canonical": "https://example.com/planner",
        "sourceHash": "sha256:9f2c8e…",
        "editedAt": 1761740000000
      }
    }
  ]
}
```

### 3.4 Functions (`planstate.go`)

```go
func emptyPlanState(noteID string) PlanState
func planStatePath(root, noteID string) string
func readPlanState(root, noteID string) (PlanState, bool, error)   // found=false when absent
func writePlanState(root string, state PlanState) error            // sets Version + Updated
func deletePlanState(root, noteID string) error                    // missing file is not an error
func validateSlotID(id string) error                               // safeID regex + non-empty
func newSlotID() string                                            // 6 random bytes, hex
func findSlot(state *PlanState, slotID string) (*PlanSlot, bool)
func normalizePlatform(p string) (string, error)                   // reuses normalizePublishPlatform
```

`readPlanState` rejects malformed documents by renaming to `.plan.json.bak` and returning `(emptyPlanState(id), false, nil)` — identical to `readPublishState`, so a hand-edited file can never block the queue.

## 4. Planner states

Two things are stored, one thing is derived.

**Stored:**

- `PlanState.Stage` ∈ `idea | drafting | ready` (optional).
- `PlanSlot.Status` ∈ `scheduled | done | skipped`.

**Derived** — one label per note, computed by `DerivedStatus`:

```go
func DerivedStatus(body string, state PlanState, publish PublishState, now int64) string
```

Evaluated top to bottom, first match wins:

| # | Condition | Result |
| --- | --- | --- |
| 1 | Any slot with `Status == scheduled && ScheduledAt <= now` | `overdue` |
| 2 | Any slot with `Status == scheduled && ScheduledAt > now` | `scheduled` |
| 3 | Any `publish.Targets[*].Status == "published"` | `published` |
| 4 | `state.Stage != ""` | that stage |
| 5 | `strings.TrimSpace(body) != ""` | `drafting` |
| 6 | otherwise | `idea` |

Ordering is a product decision, stated so it is not re-litigated per task: **a missed post outranks a future one**, and a note that is published *and* has a future slot still reads `scheduled`, because it is back in the queue.

### 4.1 Transitions

```
        (new note, empty body)
                │
                ▼
   idea ──────────────────────────► drafting
    │        (user writes)             │
    │                                  │ user sets stage, or
    └── stage=idea ────────────────────┤ schedules it
                                       ▼
                              ready ───────────────► scheduled
                                       ▲                 │
                                       │  unschedule     │ slot time passes
                                       │  (label falls   │
                                       │   back to stage │
                                       │   or drafting)  │
                                       └─────────────────┤
                                                         ▼
                                                     overdue
                                                         │ mark done
                                                         ▼
                                              done (slot) → published if
                                              the platform recorded a post
```

Rules the service enforces:

- At most **one `scheduled` slot per (note, platform)**. `Schedule` upserts an existing scheduled slot for that platform instead of appending a duplicate.
- A slot can be `done` or `skipped` without ever being published remotely; for X/LinkedIn that is the normal end state ("I posted it by hand").
- `MarkSlot(..., done)` on a blog platform does not touch `.publish.json`; only `PublishService` writes that.
- Deleting a note deletes its plan.

## 5. Service API (`planner.go`)

```go
type PlannerService struct {
	notes *NotesService
	now   func() time.Time // injectable for tests
}

func NewPlannerService(notes *NotesService) *PlannerService

// Plan returns a note's plan, or an empty plan when the note has no sidecar.
func (s *PlannerService) Plan(noteID string) (PlanState, error)

// SetStage sets the explicit stage, or clears it when stage is "".
func (s *PlannerService) SetStage(noteID, stage string) (PlanState, error)

// Schedule upserts a scheduled slot for platform at the given instant.
func (s *PlannerService) Schedule(noteID, platform string, at int64) (PlanState, error)

// Reschedule moves an existing slot, keeping its rendition and notifiedAt reset.
func (s *PlannerService) Reschedule(noteID, slotID string, at int64) (PlanState, error)

// Unschedule removes a slot that has no rendition; otherwise marks it skipped.
func (s *PlannerService) Unschedule(noteID, slotID string) (PlanState, error)

// MarkSlot sets a slot to done or skipped.
func (s *PlannerService) MarkSlot(noteID, slotID, status string) (PlanState, error)

// SaveRendition stores edited text for a slot. SourceHash is filled in by the
// service from the current note, so the caller cannot forge it.
func (s *PlannerService) SaveRendition(noteID, slotID string, r Rendition) (PlanState, error)

// Queue returns scheduled slots in [from, to), oldest first. to == 0 means no
// upper bound. Slots whose note file is gone are skipped.
func (s *PlannerService) Queue(from, to int64) ([]QueueItem, error)

// Backlog returns notes with no scheduled slot, most recently modified first.
func (s *PlannerService) Backlog(limit int) ([]BacklogItem, error)

// Due returns slots at or before now that have not been notified yet.
func (s *PlannerService) Due() ([]QueueItem, error)
```

```go
type QueueItem struct {
	NoteID      string `json:"noteId"`
	SlotID      string `json:"slotId"`
	Title       string `json:"title"`
	Platform    string `json:"platform"`
	Status      string `json:"status"` // scheduled | overdue | done | skipped
	ScheduledAt int64  `json:"scheduledAt"`
	Preview     string `json:"preview"`
	Stale       bool   `json:"stale"`
}

type BacklogItem struct {
	NoteID   string `json:"noteId"`
	Title    string `json:"title"`
	Stage    string `json:"stage"`
	Modified int64  `json:"modified"`
}
```

`Queue` scans the notes directory on every call, exactly as `List()` does. Reading a few hundred small JSON files is single-digit milliseconds and the codebase already re-reads every `.md` per listing; adding a cache would be the first invalidation bug.

`QueueItem.Status` is the **slot's** status, not the note's derived status: a stored `scheduled` slot whose `ScheduledAt <= now` reports `overdue`; `done` and `skipped` pass through. This mirrors `DerivedStatus` rule 1 so the queue and the note list never disagree.

`Preview` is the first 140 runes of `Rendition.Text` (or the joined `Parts`, or the note body flattened) — computed in Go so the UI does no text shaping.

`Stale` is `slot.Rendition != nil && slot.Rendition.SourceHash != publishContentHash(title, body)`.

Register in `main.go` alongside the existing services:

```go
application.NewService(NewPlannerService(notes)),
```

## 6. Reminders

`ReminderService` (own file `reminder.go`) owns a 30-second `time.Ticker` started in `ServiceStartup`. On each tick it calls `Due()`, sends one alert per item, then writes `NotifiedAt = now` on that slot.

Delivery is behind a seam so the scheduling logic is testable and the platform path can be added without rewriting it:

```go
type alertSender interface {
	send(item QueueItem) error
}
```

Two implementations:

- `eventAlert` — required. Emits `planner:due` with the item; the frontend shows the existing toast and a persistent banner. Works on every platform with no permissions.
- `nativeAlert` — enhancement. Wraps the platform notifier with
  `notifications.NotificationOptions{ID: item.SlotID, Title: "Time to post", Body: ...}`.
  **Verified constraint:** `pkg/services/notifications` is not auto-registered by `application.New` in v3.0.0, so this requires registering the service and confirming the accessor at implementation time. If registration does not work on a platform, `nativeAlert` returns an error, the ticker logs it, and `eventAlert` has already delivered. Never make the ticker fail because a notification did not.

Do **not** use `NotificationSchedule.At` for deferral: per the Wails source, its Linux/Windows implementation is an in-process `time.AfterFunc` that does not survive an app exit. Recompute due slots from disk on each tick and on window focus instead.

## 7. Screens

Navigation: a `.segmented` control in the sidebar header, above the search field — `Notes | Queue | Calendar`. The current sidebar list and workspace stay untouched in `Notes` mode; the other two modes swap the workspace body and reuse the sidebar footer.

New files, so `main.ts` does not grow further:

- `frontend/src/planner.ts` — queue + calendar views, scheduling actions.
- `frontend/src/plan.css` — styles for both.
- `main.ts` gains a `type Screen = "notes" | "queue" | "calendar"` and a `setScreen()`; it keeps ownership of note selection.

### 7.1 Queue

```
┌ Queue ───────────────────────────────────── Overdue ─┐
│  Mon 27 Oct                                          │
│  ┌──────────────────────────────────────────────┐   │
│  │ 09:00  [X]     Building a post planner   ●   │   │  ● = stale
│  └──────────────────────────────────────────────┘   │
│  Today                                               │
│  ┌──────────────────────────────────────────────┐   │
│  │ 14:30  [dev.to] Building a post planner      │   │
│  └──────────────────────────────────────────────┘   │
│  Thu 30 Oct                                          │
│  …                                                   │
└──────────────────────────────────────────────────────┘
```

- Grouped by local day, ascending, with `Overdue` pinned as its own first group even if its day is in the past.
- Day headers read `Today`, `Tomorrow`, then `Mon 27 Oct`.
- A row shows local `HH:MM`, a platform badge, the title, and a stale dot. Clicking opens the note and switches to `Notes`; the row keeps a trailing `…` menu with **Reschedule**, **Mark posted**, **Skip**.
- **Unscheduled** section at the bottom, from `Backlog(20)`: title + stage chip, click to open.
- Empty state: "Nothing scheduled yet. Open a note and use **Schedule**."
- Keyboard: `j`/`k` move between rows, `Enter` opens, `m` marks posted.

### 7.2 Calendar

- Month grid, Monday-first, 6 rows fixed height so the layout never jumps between months.
- Header: `‹ October 2026 ›` plus a `Today` button.
- Each cell: day number, then up to 3 chips in the form `09:00 X` (time + platform abbreviation, truncated title on hover via `title` and `aria-label`). A fourth slot renders `+2 more`.
- Today's cell gets an accent-tinted day number; the selected cell gets `--bg-active`; days from adjacent months are `--text-dim`.
- Clicking a day switches to **Queue filtered to that day** (`state.queueDay`), which reuses the queue renderer instead of building a second detail panel.
- Clicking a chip opens the note directly.
- Keyboard: arrows move selection by day/week, `Enter` opens that day's queue, `PageUp`/`PageDown` change month, `t` jumps to today.
- Accessibility: the grid is `role="grid"` with `role="gridcell"` and `aria-selected`; each chip is a `<button>` with the accessible name `Post "Title" to X at 09:00, 30 October`.
- Respect `prefers-reduced-motion` for the month-transition fade.
- The month's chips come from one `Queue(monthStartMs, monthEndMs)` call computed in local time; **no day strings are stored anywhere**.

### 7.3 Scheduling entry point

In `Notes` mode, the topbar gains a calendar icon next to the publish button, disabled when no note is open. It opens a small popover: platform segmented control (reusing the publish dialog's platform list), a date input, a time input, and **Schedule**. Defaults: today, next half hour, platform `x`. If a scheduled slot already exists for that platform, the popover shows its time and the button reads **Update**.

## 8. Failure handling

| Situation | Behavior |
| --- | --- |
| No `<id>.plan.json` | Empty plan; `Plan` returns success |
| Corrupt JSON | Rename to `.plan.json.bak`, return empty plan, log nothing user-facing |
| Unknown platform in a slot | Slot is skipped by `Queue` and `DerivedStatus`; the rest of the file still loads |
| `ScheduledAt <= 0` on a `scheduled` slot | Treated as unscheduled; never appears in the queue |
| Plan exists, `.md` gone | Row omitted; sidecar left in place (the user may restore the note) |
| Note edited after `SaveRendition` | `Stale = true`; UI shows the dot and "Note changed since this draft" |
| Two slots for the same platform both `scheduled` (hand-edited file) | `Queue` shows both; `Schedule` collapses to the earliest on the next write |
| App closed over a slot | Surfaces as overdue on next launch |

## 9. Implementation order

Phase 1–2 are backend and fully tested before any UI exists. Each phase leaves the app working.

### Phase 1 — Plan store (Go, no UI)

**Files:**
- Create: `planstate.go`, `planstate_test.go`

**Interfaces produced:** `PlanState`, `PlanSlot`, `Rendition`, `emptyPlanState`, `planStatePath`, `readPlanState`, `writePlanState`, `deletePlanState`, `validateSlotID`, `newSlotID`, `findSlot`.

- [ ] **Step 1: Write failing tests** in `planstate_test.go`

```go
func TestPlanStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	in := emptyPlanState("n1")
	in.Stage = stageReady
	in.Slots = []PlanSlot{{ID: "abc123", Platform: platformX, ScheduledAt: 1000, Status: slotScheduled}}
	if err := writePlanState(root, in); err != nil { t.Fatal(err) }
	out, found, err := readPlanState(root, "n1")
	if err != nil || !found { t.Fatalf("found=%v err=%v", found, err) }
	if out.Version != planStateVersion || out.Slots[0].ID != "abc123" { t.Fatalf("bad round trip: %+v", out) }
	if out.Updated == 0 { t.Fatal("Updated not stamped") }
}

func TestReadPlanStateMissingIsNotAnError(t *testing.T) {
	state, found, err := readPlanState(t.TempDir(), "nope")
	if err != nil || found || state.NoteID != "nope" || len(state.Slots) != 0 { t.Fatalf("state=%+v found=%v err=%v", state, found, err) }
}

func TestReadPlanStateCorruptRenamesToBak(t *testing.T) {
	root := t.TempDir()
	path := planStatePath(root, "n1")
	os.WriteFile(path, []byte("{not json"), 0o644)
	state, found, err := readPlanState(root, "n1")
	if err != nil || found || len(state.Slots) != 0 { t.Fatalf("state=%+v found=%v err=%v", state, found, err) }
	if _, err := os.Stat(path + ".bak"); err != nil { t.Fatal("corrupt file not preserved as .bak") }
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) { t.Fatal("corrupt file not moved out of the way") }
}

func TestReadPlanStateRejectsPathTraversal(t *testing.T) {
	if _, _, err := readPlanState(t.TempDir(), "../evil"); err == nil { t.Fatal("expected error for unsafe id") }
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test -run TestPlanState -v .`
Expected: FAIL, `undefined: writePlanState`

- [ ] **Step 3: Implement `planstate.go`** following Section 3.4, reusing `writeFileAtomic` and `validateID`.

- [ ] **Step 4: Run and confirm pass**

Run: `go test -run 'TestPlanState|TestReadPlanState' -v .`
Expected: PASS, including a `.bak` file present in the temp dir. Also run `go vet .` and expect no output.

### Phase 2 — Derived status and queries

**Files:**
- Create: `planner.go`, `planner_test.go`
- Modify: `noteservice.go:199` (delete cascade)

**Interfaces:** consumes Phase 1 types; produces `DerivedStatus`, `PlannerService`, `QueueItem`, `BacklogItem` per Section 5.

Test helpers live at the top of `planner_test.go` and are used by every test below:

```go
// newTestPlanner returns a service whose notes root is a temp dir, so
// publishRoot() resolves there and no test touches the real config directory.
func newTestPlanner(t *testing.T) (*PlannerService, string)
// mustNote writes <id>.md with the given title/body via composeDocument.
func mustNote(t *testing.T, root, id, title, body string)
```

- [ ] **Step 1: Write failing tests**

```go
func TestDerivedStatus(t *testing.T) {
	now := int64(1000)
	future := PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: 2000, Status: slotScheduled}}}
	past := PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: 500, Status: slotScheduled}}}
	atNow := PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: now, Status: slotScheduled}}}
	done := PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: 500, Status: slotDone}}}
	published := emptyPublishState("n1")
	published.Targets["devto"] = PublishTarget{Status: publishStatusPublished}

	cases := []struct{ name, body string; plan PlanState; pub PublishState; want string }{
		{"overdue beats scheduled", "text", PlanState{Slots: append(future.Slots, past.Slots...)}, emptyPublishState("n1"), statusOverdue},
		{"scheduled beats published", "text", future, published, statusScheduled},
		{"published beats stage", "text", emptyPlanState("n1"), published, statusPublished},
		{"explicit stage wins over body", "text", PlanState{Stage: stageReady}, emptyPublishState("n1"), stageReady},
		{"body implies drafting", "text", emptyPlanState("n1"), emptyPublishState("n1"), statusDraft},
		{"empty body is an idea", "   ", emptyPlanState("n1"), emptyPublishState("n1"), statusIdea},
		{"done slot is not scheduled", "text", done, emptyPublishState("n1"), statusDraft},
		{"slot exactly at now is due", "text", atNow, emptyPublishState("n1"), statusOverdue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DerivedStatus(c.body, c.plan, c.pub, now); got != c.want { t.Fatalf("got %q want %q", got, c.want) }
		})
	}
}
```

```go
func TestScheduleUpsertsOneSlotPerPlatform(t *testing.T) {
	svc, root := newTestPlanner(t) // temp dir + seeded note
	mustNote(t, root, "n1", "Title", "Body")
	svc.Schedule("n1", platformX, 2000)
	svc.Schedule("n1", platformX, 3000)
	state, _ := svc.Plan("n1")
	if len(state.Slots) != 1 { t.Fatalf("got %d slots, want 1", len(state.Slots)) }
	if state.Slots[0].ScheduledAt != 3000 { t.Fatalf("scheduledAt = %d, want 3000", state.Slots[0].ScheduledAt) }
}

func TestQueueSkipsOrphanedSidecar(t *testing.T) {
	svc, root := newTestPlanner(t)
	writePlanState(root, PlanState{NoteID: "ghost", Slots: []PlanSlot{{ID: "a1", Platform: platformX, ScheduledAt: 5000, Status: slotScheduled}}})
	items, err := svc.Queue(0, 0)
	if err != nil { t.Fatal(err) }
	if len(items) != 0 { t.Fatalf("orphan surfaced in queue: %+v", items) }
}

func TestQueueSurfacesMissedSlot(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	svc.now = func() time.Time { return time.UnixMilli(1000) }
	state, _ := svc.Schedule("n1", platformX, 1000-3*24*60*60*1000) // three days ago
	items, err := svc.Queue(0, 0)
	if err != nil || len(items) != 1 { t.Fatalf("items=%+v err=%v", items, err) }
	if items[0].Status != statusOverdue { t.Fatalf("status = %q, want overdue", items[0].Status) }
	if len(state.Slots) != 1 { t.Fatal("slot not persisted") }
}

func TestQueueIgnoresUnknownPlatformAndUnscheduledTimestamps(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	writePlanState(root, PlanState{NoteID: "n1", Slots: []PlanSlot{
		{ID: "a1", Platform: "myspace", ScheduledAt: 5000, Status: slotScheduled},
		{ID: "a2", Platform: platformX, ScheduledAt: 0, Status: slotScheduled},
		{ID: "a3", Platform: platformX, ScheduledAt: -1, Status: slotScheduled},
	}})
	items, err := svc.Queue(0, 0)
	if err != nil { t.Fatal(err) }
	if len(items) != 0 { t.Fatalf("unusable slots reached the queue: %+v", items) }
}

func TestQueueMarksStaleRendition(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, _ := svc.Schedule("n1", platformX, 5000)
	slotID := state.Slots[0].ID
	svc.SaveRendition("n1", slotID, Rendition{Text: "draft"})
	if items, _ := svc.Queue(0, 0); items[0].Stale { t.Fatal("fresh rendition reported stale") }
	mustNote(t, root, "n1", "Title", "Edited body")
	if items, _ := svc.Queue(0, 0); !items[0].Stale { t.Fatal("edited note not reported stale") }
}

func TestDeleteRemovesPlanSidecar(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	svc.Schedule("n1", platformX, 5000)
	svc.notes.Delete("n1")
	if _, err := os.Stat(planStatePath(root, "n1")); !errors.Is(err, os.ErrNotExist) { t.Fatal("plan sidecar survived note deletion") }
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test -run 'TestDerivedStatus|TestQueue|TestSchedule|TestDeleteRemovesPlan' -v .`
Expected: FAIL, `undefined: DerivedStatus`

- [ ] **Step 3: Implement `planner.go`** per Section 5, and add `_ = deletePlanState(s.root, id)` beside the existing `deletePublishState` call in `NotesService.Delete`.

- [ ] **Step 4: Run the full suite**

Run: `go test ./...`
Expected: all PASS, existing publish/note tests unchanged.

### Phase 3 — Service registration and bindings

**Files:**
- Modify: `main.go:15-19` (add the service)
- Regenerate: `frontend/bindings/post-planner/plannerservice.ts`, `models.ts`, `index.ts`

- [ ] **Step 1:** Add `application.NewService(NewPlannerService(notes))` to `Services`.
- [ ] **Step 2:** `wails3 generate bindings -ts` — expect a new `plannerservice.ts` and new `PlanState`/`PlanSlot`/`Rendition`/`QueueItem`/`BacklogItem` model exports.
- [ ] **Step 3:** `cd frontend && npx tsc --noEmit && npm run build` — expect no errors.
- [ ] **Step 4:** `wails3 build` and confirm `bin/post-planner` rebuilds.
- [ ] **Step 5: Commit.**

### Phase 4 — Queue screen

**Files:**
- Create: `frontend/src/planner.ts`, `frontend/src/plan.css`
- Modify: `frontend/index.html` (nav segmented control, queue container), `frontend/src/main.ts` (screen switch)

**Interfaces consumed:** `PlannerService.Queue(from, to)`, `PlannerService.Backlog(limit)`, `PlannerService.MarkSlot`, `Reschedule`, `Unschedule` from bindings.

- [ ] **Step 1:** Add the `Notes | Queue | Calendar` segmented control and a `#planner-view` container; wire `setScreen("queue")` to render groups from `Queue(0, 0)` and `Backlog(20)`.
- [ ] **Step 2:** Implement row actions and keyboard navigation; every mutation re-fetches the queue (no optimistic local state).
- [ ] **Step 3:** Verify by hand with a real build: schedule two slots on different days, confirm grouping and the overdue group; delete a note and confirm its row disappears.
- [ ] **Step 4:** `npx tsc --noEmit`, `npm run build`, `wails3 build`, then launch and click through. Commit.

### Phase 5 — Calendar screen

**Files:**
- Modify: `frontend/src/planner.ts`, `frontend/src/plan.css`

- [ ] **Step 1:** Implement the month grid per Section 7.2 from a single `Queue(monthStart, monthEnd)` call, converting local month bounds to UTC ms in TypeScript.
- [ ] **Step 2:** Day click → queue filtered to that day; chip click → open note; keyboard nav per Section 7.2.
- [ ] **Step 3:** Verify against a month containing a slot near midnight and one across a DST boundary, confirming both render at the intended local time.
- [ ] **Step 4:** `npx tsc --noEmit`, `npm run build`, `wails3 build`, manual check. Commit.

### Phase 6 — Reminders

**Files:**
- Create: `reminder.go`, `reminder_test.go`
- Modify: `main.go` (register + startup hook), `frontend/src/planner.ts` (`planner:due` listener)

**Interfaces produced:** `ReminderService`, `alertSender`, `eventAlert`.

- [ ] **Step 1: Write failing tests** against a fake sender and a fake clock: a due slot notifies once and stamps `NotifiedAt`; a second `Due()` returns nothing; a slot in the future notifies nothing; a sender error still leaves `NotifiedAt` unstamped so the next tick retries.
- [ ] **Step 2:** Run, confirm failure, implement, confirm pass.
- [ ] **Step 3:** Wire the frontend listener to a persistent banner with a **Copy** action.
- [ ] **Step 4:** Manual check: schedule a slot one minute out, confirm the banner appears once and not again after a restart. Commit.

### Phase 7 — Renditions in the publish dialog

**Files:**
- Modify: `frontend/src/publish.ts` (save edits on close for a scheduled slot), `frontend/src/planner.ts` (stale banner with **Regenerate**)

- [ ] **Step 1:** When the publish dialog is opened for a note that has a scheduled slot for the active platform, prefill from `slot.Rendition` if present, else from `Prepare`.
- [ ] **Step 2:** On edits, call `SaveRendition`. On opening a stale rendition, offer **Regenerate from note** which calls `Prepare` and saves the result.
- [ ] **Step 3:** `npx tsc --noEmit`, `npm run build`, `wails3 build`, manual verification of the stale → regenerate → fresh path. Commit.

### Phase 8 — Later (not required for a usable planner)

- `.ics` export of the queue (one `VEVENT` per slot) so reminders can live in the user's real calendar.
- Pre-flight checklist on publish: hook length, link count, thread numbering, missing tags.
- `Bluesky` as a real `publishProvider` (free AT Protocol app passwords) — the first platform where auto-posting is actually possible.

## 10. Open decisions

1. **One slot per platform per note, or many?** Recommended: one, as specified. Multiple slots per platform would let a thread and a follow-up post coexist, but it complicates the "what is this note's state" derivation and the popover UI. Revisit only if the queue feels artificially constrained in practice.
2. **`Queue` scanning per call vs. a cache.** Recommended: scan per call. Add a cache only if a real notes directory makes it measurably slow — measure first.
3. **Week start.** Recommended: Monday. Note it is hard-coded in Phase 5 and would need an `Intl.Locale` lookup plus a preference to change.
4. **Reminder lead time.** Recommended: notify exactly at the slot time. A "15 minutes before" preference is a later addition; it needs a second `NotifiedAt`-style field per slot.

## 11. Risks

- **The calendar is the visible deliverable but the sidecar is the durable one.** If Phase 1–2 slip, later phases encode scheduling logic in TypeScript and it will drift from the derived-status rules. Keep the phase order.
- **`frontend/src/main.ts` is already 978 lines.** Phases 4–7 must add `planner.ts` rather than more module-level state in `main.ts`; the screen switch is the only addition there.
- **Native notifications are the one unverified integration.** `pkg/services/notifications` is not auto-registered in v3.0.0, so Phase 6 must land the in-app path first and treat the native path as an enhancement.
