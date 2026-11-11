# Quire Implementation Plan

**Goal:** Turn the note-and-publish app into a post planner: every note can be scheduled to one or more platforms, the sidebar groups notes by derived status, and the user can inspect the history of posts that were marked done or skipped.

**Architecture:** Scheduling intent lives in a new per-note sidecar, `<id>.plan.json`, written atomically beside the note — the same pattern as the existing `<id>.publish.json`. A new `PlannerService` owns that sidecar, derives a single display status per note from plan + publish state, and answers queue, history, backlog, and status queries by scanning the notes directory. Markdown remains the only note format; planner metadata stays in JSON sidecars. The frontend gains a `Notes | Queue | Calendar` switch, a collapsible status-grouped note list, and an `Upcoming | History` switch inside Queue. All scheduling logic stays in Go so it is testable without a webview.

**Tech Stack:** Go 1.26, Wails v3.0.0 (`application.Service`, `app.Event`), vanilla TypeScript + Vite, goldmark (existing, reused for rendition text). No database is introduced.

**Spec:** this document. Global Constraints and Sections 1–8 are the specification; Section 9 is the implementation order.

## Global Constraints

- Note files stay pure portable Markdown: `<id>.md` with a leading `# Title`. Never store planner state inside the `.md` file.
- Planner metadata stays in JSON sidecars for now. This design deliberately does **not** introduce a database: the notes directory remains the transport, a corrupt sidecar affects one note, and copying the folder remains a complete backup story.
- Every write to disk goes through `writeFileAtomic` (`noteservice.go:290`) — no bare `os.WriteFile`.
- Note IDs are validated with `validateID`/`safeID` (`noteservice.go:345` / `noteservice.go:19`) before being used in a path. Slot IDs use the same regex.
- A missing sidecar is not an error; a corrupt sidecar is renamed to `.bak` and treated as empty. This is the established contract of `readPublishState` and the new reader must match it exactly. Structurally valid JSON with bad *slot* fields is different: the bad slot is skipped in memory, the rest of the file still loads, and the file is left untouched.
- All timestamps on disk are Unix milliseconds (UTC), matching `Modified`, `Updated`, `SyncedAt`, `PublishedAt`.
- The frontend never computes scheduling state; it renders what Go returns. Local wall-clock ↔ absolute instant conversion is allowed in TypeScript because only the browser knows the user's timezone. Derived labels (`overdue`, `scheduled`, `published`, `ready`, `drafting`, `idea`) are always computed in Go.
- History is retained in the plan sidecar until the note is deleted. `NotesService.Delete` removes the plan sidecar, so deleting a note also deletes its done/skipped history.
- New exported Go service methods must be followed by `wails3 generate bindings -ts`; `frontend/bindings/**` is generated and never hand-edited. Generated slice types are `T[] | null`; every new call site must normalise with `?? []`.
- All note, slot, platform, and rendition text rendered into the DOM uses `textContent` or attributes, never `innerHTML`. Remote or hand-edited strings are data, not markup.
- Do not break `PublishService`: the existing publish dialog keeps working throughout, and `PublishState` keeps its current meaning (a record of what happened remotely).
- No storage indirection layer is required for this iteration. `planstate.go` is already the seam if a SQLite metadata store is ever justified; until then, files are the source of truth.

## Review Focus

1. **Timezones and DST.** A slot is stored as an absolute instant, so "9:00" scheduled in summer reads as "10:00" after a DST shift. That is the intended behavior, but the UI must always render absolute local time and never a stored day label. Go tests pin that the stored value is untouched by formatting; the rendered-label check is a manual step in Phase 5, because **this project has no frontend test runner** (`frontend/package.json` has only `vite` and `typescript`; `tsc --noEmit` is the only automated frontend check). If a JS test runner is added, this becomes an assertion.
2. **Slots that came due while the app was closed.** Nothing should silently disappear; an unsent past slot must surface as overdue on the first load. Test: a slot 3 days in the past appears in `Queue` and reports `overdue`.
3. **Notes edited outside the app.** A `.md` changed on disk after a rendition was saved must mark the slot stale rather than quietly posting outdated text. Test: `SaveRendition`, modify the note, expect `QueueItem.Stale == true`.
4. **Hand-edited or corrupt plan JSON.** These are two different failure modes. Truncated or invalid JSON quarantines the whole file as `.plan.json.bak` and returns an empty plan. Structurally valid JSON with an unknown platform, unknown status, invalid slot ID, duplicate slot ID, or non-positive timestamp keeps the file and skips only the affected slot in `Queue`, `History`, `Due`, and `DerivedStatus`. No bad slot may make a query fail for every other note.
5. **Orphaned sidecars.** A note deleted outside the app leaves `<id>.plan.json` behind; `Queue`, `History`, `Backlog`, and `Statuses` must skip it without error, and the plan file must not resurrect a phantom row. A note deleted through the app removes its sidecar and therefore its history too.
6. **The exact-due boundary.** A slot at exactly `now` counts as due. Tests pin `<=`, not `<`, and pin that `NotifiedAt` stops a second notification.
7. **History and status grouping stay consistent.** `Queue` shows only scheduled/overdue slots; `History` shows only done/skipped slots. `Statuses()` and `DerivedStatus` use the same slot-usability predicate as `Queue`, so a note cannot read `overdue` in the sidebar while its slot is hidden from the queue.
8. **Concurrent writes.** `Schedule`, `Reschedule`, `Unschedule`, `MarkSlot`, `SaveRendition`, and the reminder tick all perform read-modify-write on the same sidecar. `PlannerService` serialises them with a mutex, and `markNotified` revalidates the slot's `ScheduledAt` so a reminder cannot stamp a time the user just changed.
9. **Generated bindings are nullable.** Every Go slice return is `T[] | null` in TypeScript. `Queue`, `History`, `Backlog`, `PlanState.Slots`, and `SlotText.Parts` all need explicit empty-array handling; `Statuses` is a map and may also be null. The baseline `tsc --noEmit` must be green before Phase 1; it currently is not.
10. **Reminder copy.** The in-app banner offers **Copy** and **Open**. Copy uses the saved rendition when present; otherwise `SlotText` generates the platform-shaped text from the note through the existing `Prepare` path. Open selects the note and opens the publish dialog with the slot context.

---

## 1. Problem and outcomes

Today `NotesService` lists documents and `PublishService` turns one into a draft or a remote post. There is no notion of *when* something should go out, no cross-note view, and no record of intent. The app answers "what did I write?" but not "what am I posting this week?".

After this plan the app answers:

- What am I posting today, and what did I miss? (Queue → Upcoming)
- What did I already post or deliberately skip? (Queue → History)
- What is the week/month shaped like? (Calendar)
- What is still just an idea with no slot? (Backlog, excluding published notes)
- How are all my notes distributed across states? (collapsible, status-grouped sidebar)
- Remind me when a slot comes due, and let me copy the post or open the publish dialog, since X and LinkedIn have no free write API.

### Non-goals

- Auto-posting to X or LinkedIn. Neither has a free write API (X v2 write is paid, LinkedIn needs partner approval). Reminders + clipboard remain the mechanism, and that is a deliberate product decision, not a gap.
- Multi-account support.
- A kanban board with arbitrary columns. The status set is fixed and derived (Section 4); the sidebar groups by that fixed set.
- Syncing planner state between machines. Files stay the transport: copy the notes directory, get the plan with it.
- A database. Metadata stays in JSON sidecars. If note counts, history analytics, or cross-device sync later make a SQLite metadata store worthwhile, that is a separate design; the store seam is `planstate.go`.
- Retaining history after a note is deleted. Delete removes the note, its plan, and its history together.

## 2. Constraints discovered in the existing code

| Fact | Consequence for this design |
| --- | --- |
| `publishRoot()` (`noteservice.go:276`) exposes the notes dir to `PublishService` | `PlannerService` uses the same accessor; sidecars sit next to notes and travel with them |
| `NotesService.Delete` already calls `deletePublishState` (`noteservice.go:199`) | The same hook must also delete the plan sidecar; history disappears with the note by design |
| `List()` filters on `filepath.Ext(name) == ".md"` | `<id>.plan.json` is invisible to the note list; no filtering change needed |
| `readPublishState` renames corrupt files to `.bak` and returns an empty state | The plan reader copies this contract verbatim for invalid JSON |
| `publishContentHash(title, body)` (`publishstore.go:123`) already fingerprints a note | Reused as `Rendition.SourceHash` to detect stale renditions |
| `Prepare()` (`publish.go:101`) derives a `Draft` from a note on demand | Renditions store a *snapshot* of that draft; `Prepare` stays the default generator and the fallback for `SlotText` |
| `app.Event` is an `*EventManager` with `Emit(name string, data ...any) bool` | Due reminders push `planner:due` to the frontend without new IPC types; `eventAlert` reaches it through `application.Get().Event` |
| `notifications.New()` returns the platform notifier in `pkg/services/notifications` | Native reminders are possible, but `application.NewService(notifications.New())` must be registered; the in-app path remains the required, testable one |
| `frontend/src/main.ts` is one 978-line file with module-level state | Queue/calendar code goes in **new files**. Grouping the existing note list is a targeted `main.ts` change; `planner.ts` receives callbacks rather than importing `main.ts` |
| Generated bindings type every Go slice as `T[] \| null` | All new TS call sites normalise with `?? []`; `PlanState.Slots` is initialised to an empty slice on the Go side |
| Baseline `npx tsc --noEmit` currently fails at `publish.ts:222` (`Connections()` possibly null) | Phase 0 fixes this before any planner code depends on a green typecheck |
| `go test ./...` passes; `go build ./...` fails on the mobile scaffold | Use `go test ./...`, `go vet .`, and `wails3 build`; do not use `go build ./...` as an acceptance check |

## 3. Data model

### 3.1 File

`<notes-dir>/<id>.plan.json`, one per scheduled note, written atomically, mode `0644`, trailing newline — identical mechanics to `publishstore.go`.

It is a **separate file from `.publish.json`** on purpose:

- `.publish.json` is a record of what already happened remotely, written only after a network success, and owned by `PublishService`.
- `.plan.json` is user intent, written offline, owned by `PlannerService`.

Merging them would make every calendar drag rewrite publish history (and risk clobbering it), and would make a failed publish corrupt the plan. Two files, two lifecycles.

The plan file also holds history. A slot is never removed when it becomes `done` or `skipped`; it stays in `Slots` with a `CompletedAt` timestamp. `Unschedule` can remove a slot only when it has no rendition and no notes. Deleting the note deletes the whole sidecar, so history does not outlive its note.

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
	statusPosted     = "posted"
	statusDraft      = "drafting"
	statusIdea       = "idea"
)

// PlanSlot is one intended post: a platform plus the moment it should go out.
type PlanSlot struct {
	ID          string     `json:"id"`
	Platform    string     `json:"platform"`
	ScheduledAt int64      `json:"scheduledAt"` // UTC ms; 0 means unscheduled
	Status      string     `json:"status"`      // scheduled | done | skipped
	NotifiedAt  int64      `json:"notifiedAt,omitempty"`
	CompletedAt int64      `json:"completedAt,omitempty"` // set for done/skipped
	Rendition   *Rendition `json:"rendition,omitempty"`
	Notes       string     `json:"notes,omitempty"`
}

// Rendition is the human-edited content for a slot. It is a snapshot:
// SourceHash records the note it was generated from so later edits can be
// detected. Social slots use Text/Parts; blog slots use Title/Body/Tags.
type Rendition struct {
	Title      string   `json:"title,omitempty"`
	Text       string   `json:"text,omitempty"`
	Parts      []string `json:"parts,omitempty"`
	Body       string   `json:"body,omitempty"`
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

`Stage` is an *optional override*. Empty means "derive from the note body", which keeps the common case free of redundant writes and avoids two sources of truth going out of sync. `CompletedAt` is what History sorts and groups by; a hand-edited history slot with `CompletedAt == 0` falls back to `ScheduledAt` for display and filtering.

`emptyPlanState` and `readPlanState` initialise `Slots` to an empty slice, never nil, so generated TypeScript sees `[]` rather than `null` for an empty plan. `Rendition.Parts` and `Rendition.Tags` may still be nil on the wire; the frontend normalises those with `?? []`.

### 3.3 Rendition field mapping

| Platform lane | `Title` | `Text` | `Parts` | `Body` | `Tags` | `Canonical` |
| --- | --- | --- | --- | --- | --- | --- |
| X | note title | single-post text | thread parts | — | — | optional link, already appended to `Text`/last part |
| LinkedIn | note title | post text | — | — | — | optional link, already appended to `Text` |
| dev.to | edited title | — | — | Markdown body with `Tags:` line removed | normalised tags | optional canonical URL |
| Hashnode | edited title | — | — | Markdown body with `Tags:` line removed | normalised tags | optional canonical URL |

`Text`/`Parts` and `Body` are mutually exclusive by lane. `SaveRendition` stores the form the dialog is currently showing; it does not try to keep the other lane's fields in sync.

### 3.4 Example

```json
{
  "version": 1,
  "noteId": "20261028-153000-a1b2",
  "stage": "ready",
  "updated": 1761740000000,
  "slots": [
    {
      "id": "9f2c41a0d3e5",
      "platform": "x",
      "scheduledAt": 1761838800000,
      "status": "scheduled",
      "notifiedAt": 0,
      "rendition": {
        "text": "We shipped a planner. Here is what changed: https://example.com/planner",
        "parts": ["We shipped a planner.", "Here is what changed:"],
        "canonical": "https://example.com/planner",
        "sourceHash": "sha256:9f2c8e…",
        "editedAt": 1761740000000
      }
    },
    {
      "id": "1b7d03c9a4f2",
      "platform": "devto",
      "scheduledAt": 1762011600000,
      "status": "scheduled",
      "rendition": {
        "title": "Building a post planner",
        "body": "We shipped a planner...\n\n## What changed\n",
        "tags": ["go", "wails", "markdown"],
        "canonical": "https://example.com/planner",
        "sourceHash": "sha256:9f2c8e…",
        "editedAt": 1761740000000
      }
    },
    {
      "id": "45a1b2c3d4e5",
      "platform": "linkedin",
      "scheduledAt": 1761700000000,
      "status": "done",
      "notifiedAt": 1761700000000,
      "completedAt": 1761700600000,
      "rendition": {
        "text": "We shipped a planner. Here is what changed:",
        "sourceHash": "sha256:9f2c8e…",
        "editedAt": 1761740000000
      }
    }
  ]
}
```

### 3.5 Functions (`planstate.go`)

```go
func emptyPlanState(noteID string) PlanState
func planStatePath(root, noteID string) string
func readPlanState(root, noteID string) (PlanState, bool, error)   // found=false when absent
func writePlanState(root string, state PlanState) error            // sets Version + Updated
func deletePlanState(root, noteID string) error                    // missing file is not an error
func validateSlotID(id string) error                               // safeID regex + non-empty
func newSlotID() string                                            // 6 random bytes, 12 hex chars
func findSlot(state *PlanState, slotID string) (*PlanSlot, bool)
func normalizePlatform(p string) (string, error)                   // allowlist: x, linkedin, devto, hashnode
func isUsableScheduledSlot(slot PlanSlot) bool                     // known platform, status scheduled, ScheduledAt > 0
func effectiveCompletedAt(slot PlanSlot) int64                     // CompletedAt, or ScheduledAt for legacy files
```

`readPlanState` rejects malformed documents by renaming to `.plan.json.bak` and returning `(emptyPlanState(id), false, nil)` — identical to `readPublishState`, so a hand-edited file can never block the queue.

`readPlanState` also normalises without dropping data: it forces `NoteID` to the requested ID, upgrades `Version == 0` to `planStateVersion`, and turns a nil `Slots` into an empty slice. Structurally valid but unusable slots stay in the file and are ignored by queries via `isUsableScheduledSlot`. This is deliberate: a future platform or a hand-edited slot must not be silently erased by the next `Schedule` write.

`writePlanState` validates only the owning note ID. It does not reject a file because one hand-edited slot is odd; otherwise a single bad slot would make every planner mutation fail. Service methods validate the slots they create.

`newSlotID` returns 12 lowercase hex characters. Examples and tests use the same shape. Duplicate slot IDs are treated as a hand-edited error: `findSlot` returns the first match, and `Queue`/`History` keep the first occurrence of each ID and drop later duplicates from the result set.

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
| 1 | Any slot with `isUsableScheduledSlot(slot) && ScheduledAt <= now` | `overdue` |
| 2 | Any slot with `isUsableScheduledSlot(slot) && ScheduledAt > now` | `scheduled` |
| 3 | Any `publish.Targets[*].Status == "published"` | `published` |
| 4 | Any valid `done` slot (`isHistorySlot(slot) && effectiveCompletedAt(slot) > 0`) | `posted` |
| 5 | `state.Stage` is one of `idea`, `drafting`, `ready` | that stage |
| 6 | `strings.TrimSpace(body) != ""` | `drafting` |
| 7 | otherwise | `idea` |

`isUsableScheduledSlot` requires a valid slot ID, a known platform, `Status == scheduled`, and `ScheduledAt > 0`. It is the same predicate used by `Queue`, `Due`, `Backlog`, and `Statuses`, so the sidebar and the queue cannot disagree about whether a slot exists.

An unknown `Stage` value from a hand-edited file is ignored and falls through to the body check. `SetStage` only accepts `""`, `idea`, `drafting`, and `ready`; it rejects derived labels such as `scheduled` or `published`.

Ordering is a product decision, stated so it is not re-litigated per task: **a missed post outranks a future one**, and a note that is published *and* has a future slot still reads `scheduled`, because it is back in the queue. A valid `done` slot with no remaining usable scheduled slot reads `posted`, even if the note has an older explicit stage; that is what keeps "mark posted" from dropping a Ready note back to Ready. `skipped` slots do not imply `posted`; a note whose only slots were skipped stays at its stage or body-derived label. `published` outranks `posted` because a recorded remote post is the stronger fact.

`Statuses()` returns this label for every note the planner can read. The sidebar uses it to group notes; the label is never recomputed in TypeScript.

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
                                              posted, unless the platform
                                              recorded a remote publish
                                              (then published)
```

Rules the service enforces:

- At most **one `scheduled` slot per (note, platform)**. `Schedule` updates the existing scheduled slot for that platform instead of appending a duplicate.
- If a hand-edited file contains multiple `scheduled` slots for one platform, `Schedule` keeps the earliest one, applies the new `ScheduledAt` to it, and removes the other `scheduled` duplicates. It never removes done/skipped history.
- `Schedule` requires an existing note and `at > 0`; it rejects unknown platforms. New slots start as `scheduled`, `NotifiedAt = 0`, `CompletedAt = 0`.
- `Reschedule` requires an existing note, a known slot, `Status == scheduled`, and `at > 0`. It preserves the rendition and notes, resets `NotifiedAt` to `0`, and refuses if another scheduled slot already exists for the same platform.
- `Unschedule` requires a scheduled slot. It removes the slot when it has neither a rendition nor notes; otherwise it marks the slot `skipped` and stamps `CompletedAt = now`, so the draft is not lost and the action appears in History.
- `MarkSlot` accepts only `done` or `skipped`. It stamps `CompletedAt = now`, leaves `NotifiedAt` as historical data, and never changes `.publish.json`.
- A slot can be `done` or `skipped` without ever being published remotely; for X/LinkedIn that is the normal end state ("I posted it by hand").
- `MarkSlot(..., done)` on a blog platform does not touch `.publish.json`; only `PublishService` writes that.
- `SetStage` accepts only `""`, `idea`, `drafting`, `ready`. An empty value clears the override and falls back to the body.
- `SaveRendition` requires an existing note and slot. It stores the rendition on any slot status, fills `SourceHash` from the current note, and stamps `EditedAt = now`; the caller cannot forge either field.
- `Plan` returns the sidecar (or an empty plan) even if the note file is gone; mutations require the note to exist. Queries skip orphan sidecars.
- Deleting a note deletes its plan and therefore its history.

## 5. Service API (`planner.go`)

```go
type PlannerService struct {
	notes   *NotesService
	publish *PublishService // used only by SlotText's Prepare fallback
	mu      sync.Mutex      // serialises read-modify-write sidecar updates
	now     func() time.Time
}

func NewPlannerService(notes *NotesService, publish *PublishService) *PlannerService

// Plan returns a note's plan, or an empty plan when the note has no sidecar.
func (s *PlannerService) Plan(noteID string) (PlanState, error)

// SetStage sets the explicit stage, or clears it when stage is "".
func (s *PlannerService) SetStage(noteID, stage string) (PlanState, error)

// Schedule upserts the scheduled slot for platform at the given instant.
func (s *PlannerService) Schedule(noteID, platform string, at int64) (PlanState, error)

// Reschedule moves an existing scheduled slot, keeping its rendition and
// resetting notifiedAt.
func (s *PlannerService) Reschedule(noteID, slotID string, at int64) (PlanState, error)

// Unschedule removes a slot that has no rendition or notes; otherwise marks it
// skipped so it appears in History.
func (s *PlannerService) Unschedule(noteID, slotID string) (PlanState, error)

// MarkSlot sets a slot to done or skipped and stamps CompletedAt.
func (s *PlannerService) MarkSlot(noteID, slotID, status string) (PlanState, error)

// SaveRendition stores edited content for a slot. SourceHash and EditedAt are
// filled in by the service from the current note and clock.
func (s *PlannerService) SaveRendition(noteID, slotID string, r Rendition) (PlanState, error)

// Queue returns scheduled slots in [from, to), oldest first. to == 0 means no
// upper bound. Done and skipped slots are excluded; use History for those.
func (s *PlannerService) Queue(from, to int64) ([]QueueItem, error)

// History returns done and skipped slots in [from, to) by effective completion
// time, newest first. to == 0 means no upper bound.
func (s *PlannerService) History(from, to int64) ([]QueueItem, error)

// Backlog returns unpublished notes with no scheduled slot, newest modified
// first. limit <= 0 means no limit.
func (s *PlannerService) Backlog(limit int) ([]BacklogItem, error)

// Statuses returns the derived status for every note the planner can read.
func (s *PlannerService) Statuses() (map[string]string, error)

// SlotText returns the text a reminder should copy: the saved rendition when
// present, otherwise a freshly prepared draft.
func (s *PlannerService) SlotText(noteID, slotID string) (SlotText, error)

// Due returns usable scheduled slots at or before now that have not been
// notified yet, oldest first. Internal to ReminderService.
//wails:ignore
func (s *PlannerService) Due() ([]QueueItem, error)

// markNotified stamps NotifiedAt only if the slot still has the same status and
// ScheduledAt that produced the reminder.
func (s *PlannerService) markNotified(noteID, slotID string, scheduledAt, at int64) error
```

```go
type QueueItem struct {
	NoteID      string `json:"noteId"`
	SlotID      string `json:"slotId"`
	Title       string `json:"title"`
	Platform    string `json:"platform"`
	Status      string `json:"status"` // queue: scheduled|overdue; history: done|skipped
	ScheduledAt int64  `json:"scheduledAt"`
	CompletedAt int64  `json:"completedAt,omitempty"`
	Preview     string `json:"preview"`
	Stale       bool   `json:"stale"`
}

type BacklogItem struct {
	NoteID   string `json:"noteId"`
	Title    string `json:"title"`
	Status   string `json:"status"` // derived status
	Modified int64  `json:"modified"`
}

type SlotText struct {
	Title     string   `json:"title"`
	Text      string   `json:"text,omitempty"`
	Parts     []string `json:"parts,omitempty"`
	Body      string   `json:"body,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Canonical string   `json:"canonical,omitempty"`
}
```

`Queue`, `History`, `Backlog`, `Statuses`, and `Due` scan the notes directory on every call, exactly as `List()` does. Reading a few hundred small JSON files is single-digit milliseconds and the codebase already re-reads every `.md` per listing; adding a cache would be the first invalidation bug. Each query isolates per-note errors: an unreadable note or plan is skipped, never returned as a failure for the whole query.

`QueueItem.Status` is the **slot's** status, not the note's derived status: a stored `scheduled` slot whose `ScheduledAt <= now` reports `overdue`. `History` returns `done` and `skipped` instead. This keeps the two views unambiguous and mirrors `DerivedStatus` rule 1.

`QueueItem.Title` is `Rendition.Title` when set, otherwise the note title. That matters for blog slots, where the user may have edited the title in the publish dialog.

`Preview` is the first 140 runes of `Rendition.Text`, the joined `Parts`, `Rendition.Body`, or the flattened note body, in that order — computed in Go so the UI does no text shaping. Truncation is rune-safe and appends `…`.

`Stale` is `slot.Rendition != nil && slot.Rendition.SourceHash != publishContentHash(title, body)`.

`History` groups by `effectiveCompletedAt(slot)`: `CompletedAt` when set, otherwise `ScheduledAt` for a hand-edited legacy slot. A history slot with both zero is skipped. `Queue` ignores done/skipped slots entirely; the History view calls `History`.

`Backlog` scans notes and excludes any note whose derived status is `published`, `posted`, `scheduled`, or `overdue`. A note with only skipped slots, or with no slots, is eligible. The query returns `Status` so the UI can show the status chip without a second call.

`Statuses` returns a map keyed by note ID. The sidebar fetches it after `List()` and after any mutation that can change a status (save, delete, schedule, mark, unschedule, publish). A missing entry means the note was unreadable; the UI falls back to an ungrouped list rather than inventing a status.

`SlotText` is used by the reminder banner's **Copy** action and as the canonical fallback for the publish dialog's slot prefill. With a rendition it returns the saved fields. Without one it calls `publish.Prepare(title, body, platform, PrepareOptions{})` and maps `Draft` to `SlotText`: social slots get `Text`/`Parts`, blog slots get `Title`/`Body`/`Tags`. `Draft` has no canonical field, so a generated `SlotText.Canonical` is empty; only a saved rendition carries one.

Register in `main.go` after the notes and publish services:

```go
publish := NewPublishService(notes)
planner := NewPlannerService(notes, publish)
Services: []application.Service{
    application.NewService(notes),
    application.NewService(publish),
    application.NewService(planner),
}
```

## 6. Reminders

`ReminderService` (own file `reminder.go`) owns a 30-second `time.Ticker` started in `ServiceStartup`. It runs once immediately at startup, then on each tick. `runOnce()` is the testable unit:

1. Call `PlannerService.Due()`.
2. Send one alert per item through the required `eventAlert`.
3. Best-effort send the native alert (if registered).
4. If the event alert succeeded, call `markNotified(noteID, slotID, scheduledAt, now)`.
5. If a sender fails, leave `NotifiedAt` unstamped so the next tick retries. A native failure alone never prevents the in-app alert or the stamp.

Delivery is behind a seam so the scheduling logic is testable and the platform path can be added without rewriting it:

```go
type alertSender interface {
	send(item QueueItem) error
}

type ReminderService struct {
	planner *PlannerService
	alert   alertSender // eventAlert, required
	native  alertSender // nativeAlert, optional
	now     func() time.Time
	stop    chan struct{}
}

func NewReminderService(planner *PlannerService, alert, native alertSender) *ReminderService
func (s *ReminderService) runOnce()
```

Implementations:

- `eventAlert` — required. Emits `planner:due` with the item via `application.Get().Event.Emit("planner:due", item)`. The frontend shows a persistent banner with **Copy** and **Open**. Works on every platform with no permissions.
- `nativeAlert` — enhancement. Uses `notifications.New().SendNotification(notifications.NotificationOptions{ID: item.NoteID + ":" + item.SlotID, Title: "Time to post", Body: ...})`. `pkg/services/notifications` is not auto-registered by `application.New` in v3.0.0, so `main.go` must call `application.NewService(notifications.New())` before the reminder service. Request notification authorization best-effort on startup; on Linux the D-Bus notifier needs no permission prompt.

`ServiceShutdown` closes `stop` and waits for the ticker goroutine, so a reload does not leave duplicate tickers. The startup context is also watched with `select`.

Do **not** use `NotificationSchedule.At` for deferral: per the Wails source, its Linux/Windows implementation is an in-process `time.AfterFunc` that does not survive an app exit. Recompute due slots from disk on each tick and once at startup instead.

`markNotified` re-reads the sidecar under the planner mutex and stamps only when the slot is still `scheduled`, has the same `ScheduledAt` that produced the reminder, and still has `NotifiedAt == 0`. If the user rescheduled between `Due()` and the stamp, the write is a no-op rather than stamping the new time as already notified.

A crash between alert delivery and the `NotifiedAt` write can produce one duplicate reminder after restart. That is accepted: the alternative is silently losing a reminder.

The frontend banner, its Copy/Open/Dismiss actions, and deduplication rules are specified in Section 7.5. **Copy** calls `SlotText(noteID, slotID)`; **Open** calls `host.openPublishForSlot(noteID, slotID)`.

## 7. Screens

Navigation: a `.segmented` control in the sidebar header, above the search field — `Notes | Queue | Calendar`. The current workspace stays untouched in `Notes` mode; the other two modes swap the workspace body and reuse the sidebar footer. The sidebar itself (search, grouped note list, footer) stays visible in every mode.

New files, so `main.ts` does not grow further:

- `frontend/src/planner.ts` — queue, history, calendar, scheduling popover, reminder banner.
- `frontend/src/plan.css` — styles for the planner surfaces; imported by `planner.ts`.
- `frontend/src/platforms.ts` — canonical platform list and display labels/abbreviations shared by the popover and queue badges.
- `main.ts` gains `type Screen = "notes" | "queue" | "calendar"`, `setScreen()`, and the grouped sidebar renderer. It keeps ownership of note selection and exposes callbacks through a host object.

`planner.ts` never imports `main.ts`. Instead `main.ts` calls:

```ts
export interface PlannerHost {
    showToast(message: string): void;
    openNote(id: string): Promise<void>;
    openPublishForSlot(noteID: string, slotID: string): Promise<void>;
    flushSave(): Promise<void>;
    confirm(title: string, message: string, action?: string): Promise<boolean>;
}

export function initPlanner(host: PlannerHost): void;
export async function renderPlannerScreen(screen: "queue" | "calendar"): Promise<void>;
```

`main.ts` calls `flushSave()` before leaving Notes mode and before opening a note from a planner surface.

### 7.1 Notes sidebar: grouped and collapsible

The existing note list becomes a set of sections in this fixed order:

`Overdue`, `Scheduled`, `Ready`, `Drafting`, `Idea`, `Posted`, `Published`.

- `main.ts` calls `NotesService.List()` and `PlannerService.Statuses()` together. Every note is placed in the section for its derived status. If `Statuses()` fails, the list falls back to the current flat list and shows one toast; it never invents statuses.
- Each section header is a `<button>` with `aria-expanded`, the label, and a count. Clicking it collapses/expands that section. Collapsed state persists in `localStorage` under `notes.collapsedStatuses` as a JSON array of status names.
- Within a section, notes keep the existing newest-modified-first order and the existing row markup/active highlight.
- Search filters notes first; sections with no matches are hidden. Counts reflect the filtered set while a search is active.
- Empty state remains the existing "No notes match your search." message.
- Status changes refresh `Statuses()` after save, delete, schedule, reschedule, unschedule, mark, or publish. No optimistic status is computed in TypeScript.
- Published notes remain visible in the sidebar but are excluded from Backlog.

### 7.2 Queue

Queue has a `.segmented` control with `Upcoming | History`, defaulting to `Upcoming`.

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

**Upcoming**

- Data: `Queue(0, 0)` returns every usable scheduled slot; the UI groups by local day. If a day filter came from the calendar, call `Queue(dayStartMs, dayEndMs)` instead and show a **Clear day filter** chip.
- Grouped by local day, ascending, with `Overdue` pinned as its own first group even if its day is in the past.
- Day headers read `Today`, `Tomorrow`, then `Mon 27 Oct`.
- A row shows local `HH:MM`, a platform badge, the title, and a stale dot. Clicking opens the note and switches to `Notes`.
- The trailing `…` menu contains **Reschedule**, **Mark posted**, **Skip**, and **Unschedule**. Reschedule opens the shared scheduling popover with the slot's platform and time. Mark posted calls `MarkSlot(..., "done")`. Skip calls `MarkSlot(..., "skipped")`. Unschedule calls `Unschedule`; when the slot has a rendition it becomes a skipped history row, otherwise it is removed.
- **Unscheduled** section at the bottom, from `Backlog(20)`: title + status chip, click to open. Published notes never appear here.
- Empty state: "Nothing scheduled yet. Open a note and use **Schedule**."
- Every mutation re-fetches the queue, backlog, and statuses; there is no optimistic local scheduling state.

**History**

- Data: `History(0, 0)`, newest effective completion first, grouped by local completion day.
- Rows show local `HH:MM`, platform badge, title, preview, and a `Done` or `Skipped` badge. A stale dot is still shown.
- Filter chips: `All`, `Done`, `Skipped` (client-side filtering of the returned rows).
- Clicking opens the note. No mutation actions are offered; history is a record. A deleted note removes its history.
- Empty state: "No completed posts yet. Mark a queue item as posted or skipped."

- Keyboard: `j`/`k` move between rows, `Enter` opens, `m` marks posted. Shortcuts are disabled while a text field, a modal, or the row menu has focus.

### 7.3 Calendar

- Month grid, Monday-first, 6 rows fixed height so the layout never jumps between months.
- Header: `‹ October 2026 ›` plus a `Today` button.
- Each cell: day number, then at most **2** chips in the form `09:00 X` (time + platform abbreviation, full title in `title` and `aria-label`). When a day has more than 2 slots, the cell also renders a `+N more` button where `N = slots - 2`.
- Clicking `+N more` and clicking the day both switch to **Queue → Upcoming filtered to that day**. `state.queueDay` is `{ start: number; end: number; label: string } | null`; `null` means no day filter.
- Clicking a chip opens the note directly.
- Today's cell gets an accent-tinted day number; the selected cell gets `--bg-active`; days from adjacent months are `--text-dim`.
- Keyboard: arrows move selection by day/week, `Enter` opens that day's queue, `PageUp`/`PageDown` change month, `t` jumps to today. Guard these shortcuts the same way as queue shortcuts.
- Accessibility: the grid is `role="grid"` with `role="row"` wrappers, `role="gridcell"`, and `aria-selected`; each chip is a `<button>` with the accessible name `Post "Title" to X at 09:00, 30 October`.
- Respect `prefers-reduced-motion` for the month-transition fade.
- The month's chips come from one `Queue(monthStartMs, monthEndMs)` call. Month bounds are computed as local `new Date(year, month, 1)` and `new Date(year, month + 1, 1)` and converted to UTC milliseconds; **no day strings are stored anywhere**.

### 7.4 Scheduling entry point

In `Notes` mode, the topbar gains a calendar icon next to the publish button, disabled when no note is open.

The same popover is used from the queue's **Reschedule** action. Extract it as:

```ts
openSchedulePopover({
    noteId,
    slotId?,       // present for Reschedule; platform is fixed
    platform?,     // default "x"
    at?,           // default today, next half hour
    anchor,        // element to position against and return focus to
    onSaved?,      // refresh queue/statuses
})
```

- Fields: platform segmented control (from `platforms.ts`), date input, time input, and a primary button.
- New slot: button reads **Schedule**; if a scheduled slot already exists for that platform, the popover prefills its time and the button reads **Update** (calls `Schedule`, which upserts).
- Existing slot: button reads **Reschedule** (calls `Reschedule`); the platform control is disabled.
- Defaults: today, next half hour, platform `x`.
- Empty/invalid date or time shows an inline error and does not call Go.
- The local date/time is converted to UTC milliseconds in TypeScript. If the chosen wall-clock time falls in a DST spring-forward gap, show a warning with the resolved local time before saving. A fall-back overlap uses the first occurrence.
- Escape closes and returns focus to the anchor. Enter saves when focus is inside the popover.

### 7.5 Reminder banner

`planner.ts` subscribes to `planner:due` with `Events.On("planner:due", ...)`. The banner is persistent, not the existing toast, and is available on every screen.

- Rows are keyed by `noteID:slotID`, so a duplicate event does not duplicate a row.
- **Copy** calls `SlotText` and copies `Text`, or joins `Parts` with blank-line separators, or copies `Body`; if all are empty it shows a toast.
- **Open** calls `host.openPublishForSlot(noteID, slotID)`, which selects the note and opens the publish dialog with the slot context.
- **Dismiss** removes one row; **Dismiss all** clears the banner.
- The banner is not cleared by switching screens or by the toast timer.

## 8. Failure handling

| Situation | Behavior |
| --- | --- |
| No `<id>.plan.json` | Empty plan; `Plan` returns success |
| Corrupt JSON | Rename to `.plan.json.bak`, return empty plan, log nothing user-facing |
| Unknown platform in a slot | Slot is skipped by `Queue`, `History`, `Due`, `Backlog`, and `DerivedStatus`; the rest of the file still loads and is preserved on the next write |
| Unknown slot status | Slot is skipped by `Queue`, `History`, and `Due`; `DerivedStatus` ignores it |
| `ScheduledAt <= 0` on a `scheduled` slot | Treated as unscheduled; never appears in `Queue` or `Due` and does not affect `DerivedStatus` |
| Duplicate slot IDs | First occurrence wins in query results; `findSlot` returns the first match |
| Plan exists, `.md` gone | Row omitted from every query; sidecar left in place (the user may restore the note) |
| Note edited after `SaveRendition` | `Stale = true`; UI shows the dot and "Note changed since this draft" |
| Two slots for the same platform both `scheduled` (hand-edited file) | `Queue` shows both until the next `Schedule`, which keeps the earliest slot, applies the new time, and removes the duplicate scheduled slots |
| Done/skipped slot with `CompletedAt == 0` (legacy or hand-edited) | History falls back to `ScheduledAt` for display and filtering; if both are zero the slot is skipped |
| App closed over a slot | Surfaces as overdue on next launch and as due on the first reminder tick |
| Note deleted through the app | Note, `.plan.json`, and all done/skipped history are removed together |
| One unreadable note or plan | That note is skipped; `Queue`, `History`, `Backlog`, and `Statuses` still return every other note |
| `Statuses()` fails | Sidebar falls back to the ungrouped list and shows one toast; statuses are never invented in TypeScript |
| Reminder event send fails | `NotifiedAt` is not stamped; the next tick retries |
| Reminder native send fails | Logged; the in-app event already delivered and `NotifiedAt` is stamped |
| Reminder `NotifiedAt` write fails | Logged; a duplicate reminder is possible on the next tick or after restart |
| Copy with no rendition | `SlotText` generates the draft through `Prepare`; Copy still works |
| Published note with no future slot | Visible in the sidebar under `Published`; excluded from Backlog |

## 9. Implementation order

Phases 1–2 are backend and fully tested before any UI exists; Phase 0 clears an existing typecheck failure first. Each phase leaves the app working.

### Phase 0 — Baseline hygiene

**Files:**
- Modify: `frontend/src/publish.ts:222` (nullable `Connections()`)
- Modify: `frontend/package.json` (add `"typecheck": "tsc --noEmit"` if not present)

- [ ] **Step 1:** Fix the existing `Connections()` nullability so `npx tsc --noEmit` passes before planner code lands.
- [ ] **Step 2:** Confirm the baseline: `go test ./...`, `go vet .`, `npx tsc --noEmit`, `npm run build`. `go build ./...` is not a valid check because the mobile scaffold has no root `main`.
- [ ] **Step 3:** Note the binding convention for later phases: every generated slice result is normalised with `?? []` at the call site.

### Phase 1 — Plan store (Go, no UI)

**Files:**
- Create: `planstate.go`, `planstate_test.go`

**Interfaces produced:** `PlanState`, `PlanSlot`, `Rendition`, `emptyPlanState`, `planStatePath`, `readPlanState`, `writePlanState`, `deletePlanState`, `validateSlotID`, `newSlotID`, `findSlot`, `normalizePlatform`, `isUsableScheduledSlot`, `effectiveCompletedAt`.

- [ ] **Step 1: Write failing tests** in `planstate_test.go`

```go
func TestPlanStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	in := emptyPlanState("n1")
	in.Stage = stageReady
	in.Slots = []PlanSlot{{
		ID: "abc123def456", Platform: platformX, ScheduledAt: 1000, Status: slotScheduled,
		CompletedAt: 900, Rendition: &Rendition{Text: "hello", Body: "blog body"},
	}}
	if err := writePlanState(root, in); err != nil { t.Fatal(err) }
	out, found, err := readPlanState(root, "n1")
	if err != nil || !found { t.Fatalf("found=%v err=%v", found, err) }
	if out.Version != planStateVersion || out.Slots[0].ID != "abc123def456" { t.Fatalf("bad round trip: %+v", out) }
	if out.Slots[0].CompletedAt != 900 || out.Slots[0].Rendition.Body != "blog body" { t.Fatalf("lost fields: %+v", out.Slots[0]) }
	if out.Updated == 0 { t.Fatal("Updated not stamped") }
}

func TestReadPlanStateMissingIsNotAnError(t *testing.T) {
	state, found, err := readPlanState(t.TempDir(), "nope")
	if err != nil || found || state.NoteID != "nope" || len(state.Slots) != 0 { t.Fatalf("state=%+v found=%v err=%v", state, found, err) }
}

func TestReadPlanStateNormalisesVersionAndNoteID(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(planStatePath(root, "n1"), []byte(`{"noteId":"other","slots":null}`), 0o644)
	state, found, err := readPlanState(root, "n1")
	if err != nil || !found { t.Fatalf("found=%v err=%v", found, err) }
	if state.NoteID != "n1" || state.Slots == nil || state.Version != planStateVersion { t.Fatalf("not normalised: %+v", state) }
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

- [ ] **Step 3: Implement `planstate.go`** following Section 3.5, reusing `writeFileAtomic` and `validateID`. Initialise slices, normalise `Version`/`NoteID`, and add `CompletedAt`, `Rendition.Body`, `normalizePlatform`, `isUsableScheduledSlot`, and `effectiveCompletedAt`.

- [ ] **Step 4: Run and confirm pass**

Run: `go test -run 'TestPlanState|TestReadPlanState' -v .`
Expected: PASS, including a `.bak` file present in the temp dir. Also run `go vet .` and expect no output.

### Phase 2 — Derived status and queries

**Files:**
- Create: `planner.go`, `planner_test.go`
- Modify: `noteservice.go:199` (delete cascade)

**Interfaces:** consumes Phase 1 types; produces `DerivedStatus`, `PlannerService`, `QueueItem`, `History`, `BacklogItem`, `Statuses`, `SlotText`, `markNotified` per Section 5.

Test helpers live at the top of `planner_test.go` and are used by every test below:

```go
// newTestPlanner returns a service whose notes root is a temp dir, so
// publishRoot() resolves there and no test touches the real config directory.
// It also builds a PublishService over the same notes root for SlotText.
func newTestPlanner(t *testing.T) (*PlannerService, string)
// mustNote writes <id>.md with the given title/body via composeDocument.
func mustNote(t *testing.T, root, id, title, body string)
// mustPlan writes a plan directly, for hand-edited and corrupt-file cases.
func mustPlan(t *testing.T, root string, state PlanState)
```

- [ ] **Step 1: Write failing tests**

```go
func TestDerivedStatus(t *testing.T) {
	now := int64(1000)
	future := PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: 2000, Status: slotScheduled}}}
	past := PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: 500, Status: slotScheduled}}}
	atNow := PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: now, Status: slotScheduled}}}
	done := PlanState{Slots: []PlanSlot{{ID: "d1", Platform: platformX, ScheduledAt: 500, Status: slotDone, CompletedAt: 600}}}
	published := emptyPublishState("n1")
	published.Targets[platformDevto] = PublishTarget{Status: publishStatusPublished}

	cases := []struct{ name, body string; plan PlanState; pub PublishState; want string }{
		{"overdue beats scheduled", "text", PlanState{Slots: append(future.Slots, past.Slots...)}, emptyPublishState("n1"), statusOverdue},
		{"scheduled beats published", "text", future, published, statusScheduled},
		{"published beats stage", "text", emptyPlanState("n1"), published, statusPublished},
		{"posted beats explicit stage", "text", PlanState{Stage: stageReady, Slots: done.Slots}, emptyPublishState("n1"), statusPosted},
		{"explicit stage wins over body", "text", PlanState{Stage: stageReady}, emptyPublishState("n1"), stageReady},
		{"body implies drafting", "text", emptyPlanState("n1"), emptyPublishState("n1"), statusDraft},
		{"empty body is an idea", "   ", emptyPlanState("n1"), emptyPublishState("n1"), statusIdea},
		{"done slot is posted", "text", done, emptyPublishState("n1"), statusPosted},
		{"slot exactly at now is due", "text", atNow, emptyPublishState("n1"), statusOverdue},
		{"unknown platform is ignored", "text", PlanState{Slots: []PlanSlot{{Platform: "myspace", ScheduledAt: 500, Status: slotScheduled}}}, emptyPublishState("n1"), statusDraft},
		{"zero scheduled time is ignored", "text", PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: 0, Status: slotScheduled}}}, emptyPublishState("n1"), statusDraft},
		{"unknown stage falls through", "text", PlanState{Stage: "archived"}, emptyPublishState("n1"), statusDraft},
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

func TestScheduleCollapsesDuplicateScheduledSlots(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	mustPlan(t, root, PlanState{NoteID: "n1", Slots: []PlanSlot{
		{ID: "a1", Platform: platformX, ScheduledAt: 1000, Status: slotScheduled},
		{ID: "a2", Platform: platformX, ScheduledAt: 2000, Status: slotScheduled},
		{ID: "a3", Platform: platformX, ScheduledAt: 500, Status: slotDone, CompletedAt: 600},
	}})
	state, err := svc.Schedule("n1", platformX, 3000)
	if err != nil { t.Fatal(err) }
	if len(state.Slots) != 2 { t.Fatalf("got %d slots, want earliest scheduled + done: %+v", len(state.Slots), state.Slots) }
	if state.Slots[0].ScheduledAt != 3000 || state.Slots[0].Status != slotScheduled { t.Fatalf("earliest slot not updated: %+v", state.Slots[0]) }
}

func TestRescheduleResetsNotifiedAndKeepsRendition(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, _ := svc.Schedule("n1", platformX, 1000)
	slotID := state.Slots[0].ID
	svc.SaveRendition("n1", slotID, Rendition{Text: "draft"})
	svc.markNotified("n1", slotID, 1000, 900)
	state, err := svc.Reschedule("n1", slotID, 5000)
	if err != nil { t.Fatal(err) }
	if state.Slots[0].ScheduledAt != 5000 || state.Slots[0].NotifiedAt != 0 { t.Fatalf("reschedule did not reset: %+v", state.Slots[0]) }
	if state.Slots[0].Rendition == nil || state.Slots[0].Rendition.Text != "draft" { t.Fatal("rendition lost") }
}

func TestUnscheduleRemovesOrSkips(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, _ := svc.Schedule("n1", platformX, 1000)
	emptyID := state.Slots[0].ID
	state, _ = svc.Schedule("n1", platformLinkedIn, 2000)
	editedID := state.Slots[1].ID
	svc.SaveRendition("n1", editedID, Rendition{Text: "keep me"})

	state, err := svc.Unschedule("n1", emptyID)
	if err != nil { t.Fatal(err) }
	if len(state.Slots) != 1 { t.Fatalf("empty slot not removed: %+v", state.Slots) }

	svc.now = func() time.Time { return time.UnixMilli(4000) }
	state, err = svc.Unschedule("n1", editedID)
	if err != nil { t.Fatal(err) }
	if state.Slots[0].Status != slotSkipped || state.Slots[0].CompletedAt != 4000 { t.Fatalf("edited slot not moved to history: %+v", state.Slots[0]) }
}

func TestHistoryIncludesDoneAndSkipped(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	mustPlan(t, root, PlanState{NoteID: "n1", Slots: []PlanSlot{
		{ID: "a1", Platform: platformX, ScheduledAt: 1000, Status: slotDone, CompletedAt: 1200},
		{ID: "a2", Platform: platformDevto, ScheduledAt: 2000, Status: slotSkipped, CompletedAt: 2200},
		{ID: "a3", Platform: platformX, ScheduledAt: 3000, Status: slotScheduled},
	}})
	items, err := svc.History(0, 0)
	if err != nil { t.Fatal(err) }
	if len(items) != 2 || items[0].Status != slotSkipped || items[1].Status != slotDone { t.Fatalf("history = %+v", items) }
	if items[0].CompletedAt != 2200 { t.Fatalf("completedAt = %d", items[0].CompletedAt) }
}

func TestStatusesAndBacklogExcludePublished(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "idea", "Idea", "")
	mustNote(t, root, "draft", "Draft", "Body")
	mustNote(t, root, "live", "Live", "Body")
	mustPlan(t, root, PlanState{NoteID: "live", Slots: []PlanSlot{{ID: "a1", Platform: platformDevto, ScheduledAt: 1000, Status: slotDone, CompletedAt: 1100}}})
	pub := emptyPublishState("live")
	pub.Targets[platformDevto] = PublishTarget{Status: publishStatusPublished}
	writePublishState(root, pub)

	statuses, err := svc.Statuses()
	if err != nil { t.Fatal(err) }
	if statuses["idea"] != statusIdea || statuses["draft"] != statusDraft || statuses["live"] != statusPublished { t.Fatalf("statuses = %+v", statuses) }
	backlog, err := svc.Backlog(20)
	if err != nil { t.Fatal(err) }
	for _, item := range backlog { if item.NoteID == "live" { t.Fatal("published note leaked into backlog") } }
}

func TestQueueIsolatesCorruptPlan(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "bad", "Bad", "Body")
	mustNote(t, root, "good", "Good", "Body")
	os.WriteFile(planStatePath(root, "bad"), []byte("{not json"), 0o644)
	svc.Schedule("good", platformX, 5000)
	items, err := svc.Queue(0, 0)
	if err != nil { t.Fatal(err) }
	if len(items) != 1 || items[0].NoteID != "good" { t.Fatalf("corrupt plan blocked queue: %+v", items) }
}

func TestSaveRenditionFillsHashAndEditedAt(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, _ := svc.Schedule("n1", platformDevto, 5000)
	state, err := svc.SaveRendition("n1", state.Slots[0].ID, Rendition{Body: "blog body"})
	if err != nil { t.Fatal(err) }
	r := state.Slots[0].Rendition
	if r == nil || r.SourceHash == "" || r.EditedAt == 0 { t.Fatalf("rendition not stamped: %+v", r) }
	if r.SourceHash != publishContentHash("Title", "Body") { t.Fatal("source hash does not match note") }
}

func TestSlotTextUsesRenditionOrPrepare(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body text")
	state, _ := svc.Schedule("n1", platformX, 5000)
	text, err := svc.SlotText("n1", state.Slots[0].ID)
	if err != nil || text.Text == "" { t.Fatalf("prepare fallback failed: %+v err=%v", text, err) }
	svc.SaveRendition("n1", state.Slots[0].ID, Rendition{Text: "edited"})
	text, err = svc.SlotText("n1", state.Slots[0].ID)
	if err != nil || text.Text != "edited" { t.Fatalf("rendition not used: %+v err=%v", text, err) }
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test -run 'TestDerivedStatus|TestQueue|TestSchedule|TestReschedule|TestUnschedule|TestHistory|TestStatuses|TestBacklog|TestSaveRendition|TestSlotText|TestDeleteRemovesPlan' -v .`
Expected: FAIL, `undefined: DerivedStatus`

- [ ] **Step 3: Implement `planner.go`** per Section 5, and add `_ = deletePlanState(s.root, id)` beside the existing `deletePublishState` call in `NotesService.Delete`.

- [ ] **Step 4: Run the full suite**

Run: `go test ./...`
Expected: all PASS, existing publish/note tests unchanged.

### Phase 3 — Service registration and bindings

**Files:**
- Modify: `main.go` (construct publish + planner, register both)
- Regenerate: `frontend/bindings/quire/plannerservice.ts`, `models.ts`, `index.ts`

- [ ] **Step 1:** Construct `publish := NewPublishService(notes)` and `planner := NewPlannerService(notes, publish)`; register both alongside `notes`.
- [ ] **Step 2:** `wails3 generate bindings -ts -clean` — expect `plannerservice.ts` and model exports for `PlanState`, `PlanSlot`, `Rendition`, `QueueItem`, `BacklogItem`, `SlotText`, and `CompletedAt`/`Body`.
- [ ] **Step 3:** Confirm generated slice types are nullable and add `?? []` at every new call site.
- [ ] **Step 4:** `cd frontend && npx tsc --noEmit && npm run build` — expect no errors. The Phase 0 baseline fix must already be in place.
- [ ] **Step 5:** `wails3 build` and confirm `bin/quire` rebuilds.
- [ ] **Step 6: Commit.**

### Phase 4 — Queue screen and grouped sidebar

**Files:**
- Create: `frontend/src/planner.ts`, `frontend/src/plan.css`, `frontend/src/platforms.ts`
- Modify: `frontend/index.html` (screen segmented control, planner container, reminder banner), `frontend/src/main.ts` (screen switch, host callbacks, grouped sidebar)

**Interfaces consumed:** `PlannerService.Queue`, `History`, `Backlog`, `Statuses`, `MarkSlot`, `Reschedule`, `Unschedule`, `SlotText`.

- [ ] **Step 1:** Add the `Notes | Queue | Calendar` segmented control, `#planner-view`, and the `#due-banner` container.
- [ ] **Step 2:** Add `PlannerHost` in `planner.ts`; `main.ts` passes `showToast`, `openNote`, `openPublishForSlot`, `flushSave`, and `confirm`. No `planner.ts` → `main.ts` import.
- [ ] **Step 3:** Group the existing sidebar by `Statuses()` in the fixed order from Section 7.1. Add collapsible section headers, counts, `aria-expanded`, and `notes.collapsedStatuses` persistence. Refresh statuses after every mutation.
- [ ] **Step 4:** Implement Queue → Upcoming with `Queue(0, 0)`, local-day grouping, pinned Overdue, and `Backlog(20)`. Every mutation re-fetches queue/backlog/statuses.
- [ ] **Step 5:** Implement Queue → History with `History(0, 0)`, completion-day grouping, status badges, and All/Done/Skipped filters.
- [ ] **Step 6:** Implement the row menu: Reschedule (shared popover), Mark posted, Skip, Unschedule. Implement keyboard `j`/`k`/`Enter`/`m` with the input/modal/menu guard.
- [ ] **Step 7:** Extract `openSchedulePopover` and use it from the Notes topbar and the queue row. Add the `platforms.ts` list.
- [ ] **Step 8:** Verify by hand: schedule two slots on different days, confirm grouping and Overdue; mark one done and one skipped and confirm History; unschedule a slot with and without a rendition; delete a note and confirm its rows and history disappear.
- [ ] **Step 9:** `npx tsc --noEmit`, `npm run build`, `wails3 build`, then click through. Commit.

### Phase 5 — Calendar screen

**Files:**
- Modify: `frontend/src/planner.ts`, `frontend/src/plan.css`

- [ ] **Step 1:** Implement the month grid per Section 7.3 from a single `Queue(monthStart, monthEnd)` call, converting local month bounds to UTC ms in TypeScript.
- [ ] **Step 2:** Render at most 2 chips per day plus `+N more` (`N = slots - 2`). Day click and `+N more` switch to Queue → Upcoming filtered to that day via `state.queueDay`; add a **Clear day filter** chip.
- [ ] **Step 3:** Implement keyboard navigation and `role="grid"`/`role="row"`/`role="gridcell"` accessibility, with the same input/modal guard.
- [ ] **Step 4:** Verify against a month containing a slot near midnight, a day with 3+ slots, and one across a DST boundary, confirming both chips and local labels.
- [ ] **Step 5:** `npx tsc --noEmit`, `npm run build`, `wails3 build`, manual check. Commit.

### Phase 6 — Reminders

**Files:**
- Create: `reminder.go`, `reminder_test.go`
- Modify: `main.go` (register `notifications.New()`, construct `ReminderService`), `frontend/src/planner.ts` (`planner:due` listener and banner)

**Interfaces produced:** `ReminderService`, `alertSender`, `eventAlert`, `nativeAlert`, `markNotified`.

- [ ] **Step 1: Write failing tests** with a fake sender and fake clock: a due slot notifies once and stamps `NotifiedAt`; a second `Due()` returns nothing; a future slot notifies nothing; a sender error leaves `NotifiedAt` unstamped; rescheduling between `Due()` and `markNotified` does not stamp the new time; `ServiceShutdown` stops the ticker.
- [ ] **Step 2:** Run, confirm failure, implement, confirm pass.
- [ ] **Step 3:** Register `application.NewService(notifications.New())` before the reminder service; keep `nativeAlert` best-effort and `eventAlert` required.
- [ ] **Step 4:** Wire `planner:due` to the persistent banner with **Copy** (`SlotText`), **Open** (`host.openPublishForSlot`), **Dismiss**, and **Dismiss all**.
- [ ] **Step 5:** Manual check: schedule a slot one minute out, confirm the banner appears once, Copy and Open both work, and a restart does not re-notify. Commit.

### Phase 7 — Renditions in the publish dialog

**Files:**
- Modify: `frontend/src/publish.ts` (slot context, prefill, save/regenerate), `frontend/src/planner.ts` (stale banner with **Regenerate**), `frontend/src/main.ts` (`openPublishForSlot`)

- [ ] **Step 1:** Extend `openPublishModal` with an optional `slot: { noteId, slotId, platform }` context. When present, select that platform and prefill from `slot.Rendition`; otherwise call `Prepare` as today.
- [ ] **Step 2:** Map rendition fields per Section 3.3: social uses `Text`/`Parts`/`Canonical`; blog uses `Title`/`Body`/`Tags`/`Canonical`. Do not re-append a canonical link that is already in `Text`, and do not re-extract a `Tags:` line from a body that was already cleaned.
- [ ] **Step 3:** Add an explicit **Save to slot** action (and save before Copy/Publish/Update). Closing with unsaved edits prompts; Cancel discards. Save calls `SaveRendition`; the service fills `SourceHash`/`EditedAt`.
- [ ] **Step 4:** On a stale rendition, offer **Regenerate from note**: confirm if dirty, call `Prepare`, replace the form, and save.
- [ ] **Step 5:** `npx tsc --noEmit`, `npm run build`, `wails3 build`, manual verification of the stale → regenerate → fresh path and the social/blog field mapping. Commit.

### Phase 8 — Later (not required for a usable planner)

- `.ics` export of the queue (one `VEVENT` per slot) so reminders can live in the user's real calendar.
- Pre-flight checklist on publish: hook length, link count, thread numbering, missing tags.
- `Bluesky` as a real `publishProvider` (free AT Protocol app passwords) — the first platform where auto-posting is actually possible.
- If history grows beyond what a per-note sidecar should carry, or history-after-deletion/cross-device sync becomes a real requirement, evaluate a SQLite metadata store behind the `planstate.go` seam. Do not move Markdown content into it.

## 10. Decisions

1. **Storage.** Markdown + per-note JSON sidecars. No database in this iteration. The sidecar is the transport; delete removes its history.
2. **One scheduled slot per (note, platform).** Done/skipped history is unlimited. `Schedule` collapses duplicate scheduled slots to the earliest.
3. **Queue vs. History.** `Queue` returns scheduled/overdue only; `History` returns done/skipped. A single `QueueItem` shape serves both.
4. **History retention.** History lives until the note is deleted. A note deleted through the app removes its plan and history together.
5. **Sidebar grouping.** Notes are grouped by derived status in the existing sidebar, in the fixed order Overdue, Scheduled, Ready, Drafting, Idea, Posted, Published. Sections are collapsible and persisted in `localStorage`.
6. **Backlog.** Published and posted notes are excluded; a note with only skipped slots or no slots is eligible.
7. **Calendar overflow.** At most 2 chips per day; a `+N more` button opens the day's queue. No third chip.
8. **Reminders.** Notify exactly at the slot time. The banner offers Copy (`SlotText`) and Open (publish dialog with slot context). Native notifications are an enhancement.
9. **Reschedule UX.** Reuse the Notes scheduling popover; the queue row passes the existing slot. No second scheduling UI.
10. **Unschedule.** Exposed in the queue row menu. It removes a slot only when it has no rendition or notes; otherwise it becomes skipped history.
11. **Week start.** Monday, hard-coded in Phase 5. Changing it needs a preference and an `Intl.Locale` lookup.
12. **Queue scanning.** Scan per call. Add a cache only after measuring a real notes directory.

## 11. Risks

- **The calendar is the visible deliverable but the sidecar is the durable one.** If Phase 1–2 slip, later phases encode scheduling logic in TypeScript and it will drift from the derived-status rules. Keep the phase order.
- **`frontend/src/main.ts` is already 978 lines.** Queue/calendar/history code goes in `planner.ts`; grouping the existing list is the only substantial `main.ts` addition. Use the `PlannerHost` callbacks, not an import cycle.
- **Nullable bindings.** Every Go slice is `T[] | null` in TypeScript. A missed `?? []` becomes a runtime crash in the queue, history, or sidebar.
- **Concurrent read-modify-write.** Without the planner mutex and the `markNotified` revalidation, a reminder can overwrite a user reschedule. Tests must cover the interleaving.
- **Native notifications are the one unverified integration.** `notifications.New()` is not auto-registered in v3.0.0; Phase 6 must land the in-app path first and treat the native path as an enhancement.
- **Sidecar history growth.** Done/skipped slots accumulate in `<id>.plan.json`. That is acceptable for a personal planner; if it becomes large, a compaction/export feature is a later addition, not a reason to switch storage now.
- **DST and local-time conversion.** The stored instant is correct; the UI must always render it in the user's current local zone. The spring-forward gap and fall-back overlap are UI warnings, not stored state.
