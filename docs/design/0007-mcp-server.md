# MCP Server

**Goal:** Let an MCP-capable agent read and write the same notes, planner state,
drafts, and publish records the desktop app uses, without copy/paste and without
reimplementing scheduling or publishing rules. The server runs from the same
binary as `post-planner mcp` over stdio and works whether or not the GUI is
open.

**Architecture:** `main.go` branches on the first argument before constructing
the Wails application. `runMCP()` builds the normal `NotesService`,
`PublishService`, and `PlannerService`, wraps them in an MCP server from
`github.com/modelcontextprotocol/go-sdk`, and serves it over
`mcp.StdioTransport`. The tools are thin adapters over the existing services,
so derived status, backend dedupe, draft shaping, and publish tracking come from
the same code as the UI. The server exposes 21 tools, three resources, and two
prompts.

**Tech Stack:** Go 1.26, `github.com/modelcontextprotocol/go-sdk v1.8.0`, the
existing note/publish/planner services, stdio JSON-RPC. No HTTP listener, no
embedded server in the GUI, and no second storage path.

**Spec:** this document. It is retrospective: it records the server shipped
alongside the watcher and lock work. Publishing semantics remain owned by
`0002`; planner semantics by `0003`; cross-process coordination by `0008`.

## Global Constraints

- **One binary, two modes.** `post-planner` starts the GUI; `post-planner mcp`
  starts the server. There is no separate executable to find, package, or keep
  in sync.
- **stdio is the transport.** stdout is reserved for protocol frames; all
  diagnostics go to stderr via `log.SetOutput(os.Stderr)`. A stray print to
  stdout corrupts the session.
- **The GUI does not need to be running.** The server opens the same notes
  directory lazily. Any MCP client can launch it as a subprocess.
- **Services are shared, not reimplemented.** Tool handlers call
  `NotesService`, `PlannerService`, and `PublishService`. No tool writes JSON
  directly and no tool derives a status in the adapter.
- **The storage contract does not change.** Notes stay `<id>.md`; planner state
  stays `<id>.plan.json`; publish tracking stays `<id>.publish.json`.
- **Publishing is two-step and opt-in.** `publish_note` with `confirm=false`
  returns a prepared payload and sends nothing. Only `confirm=true` reaches a
  provider. New remote posts default to draft.
- **X and LinkedIn are copy-ready only.** They have no direct write API in this
  app, so the tool returns `mode: "prepared"` and a `Draft` instead of claiming
  to publish.
- **AGENTS.md-style instructions are part of the interface.** The server
  instructions tell a client to search first, prefer append over replace, pass
  `expected_modified`, and confirm before publishing.
- **No delete tool.** Deleting a note is a deliberate UI action with a
  confirmation dialog. An agent cannot delete notes or history through MCP.
- **Errors are tool errors, not protocol errors.** The SDK's typed handler maps
  returned errors into `CallToolResult.IsError` content, which clients can show
  to the user and retry from.

## Review Focus

1. **Concurrent writes with the GUI.** The MCP process and the GUI are separate
   processes with separate mutexes. Planner mutations therefore take the
   `.notes.lock` file lock (`0008`), and note-body writes use atomic renames.
   An MCP schedule must not erase a schedule made in the app, and vice versa.
2. **Stale full rewrites.** `create_note` and `append_note` are safe by
   construction (new file, append). `replace_note` is destructive, so it accepts
   `expected_modified` from `get_note`; if the note changed, the service rejects
   the write with a "read it again and retry" error. `expected_modified <= 0`
   explicitly opts out of the check.
3. **Publishing cannot be accidental.** `confirm` is a separate boolean from
   `draft`. A dry run returns the `Draft`; a confirmed create uses
   `Draft: true` unless the caller passes `draft=false`. An existing remote
   target is refused with "use update_published instead" rather than creating a
   duplicate.
4. **Updating an existing post keeps it live by default.** `update_published`
   defaults `asDraft=false`, because an already-published post should not be
   silently unpublished. A caller can still pass `draft=true` explicitly.
5. **Time parsing is strict.** Scheduling accepts RFC3339 or Unix
   milliseconds. Empty means "no bound" only for windows; a scheduling call gets
   a clear error instead of silently scheduling at epoch zero.
6. **Results are structured.** Typed output structs become
   `structuredContent`; clients do not need to scrape text. `get_note` returns
   note + plan + publish + derived status in one round trip.
7. **Tool names are stable.** Clients and prompts refer to them by name. Add a
   tool rather than renaming one; keep the input field names documented here.

---

## 1. Problem and outcomes

The app is a local-first notes and planning tool. Agents are useful for
summarizing, reviewing, drafting, and scheduling, but giving an agent raw access
to the notes directory invites it to invent its own JSON formats and scheduling
rules. Reading files is not enough: the planner has derived statuses, duplicate
slot rules, stale rendition detection, and publish tracking.

After this work:

- An MCP client can search notes, read a note with its sidecars, create notes,
  append reviews, and safely rewrite a note.
- An agent can inspect and mutate the planner using the same rules as the UI:
  stage, schedule, reschedule, unschedule, mark done/skipped.
- An agent can prepare platform drafts, save renditions, inspect publish state,
  and publish behind an explicit confirmation.
- The same resources are available for clients that prefer URI reads over tool
  calls, plus two ready-made prompts for review and drafting.
- The desktop app watches the directory and reflects agent changes without a
  restart (`0008`).

### Non-goals

- A remote or multi-user server. This is a local stdio process sharing the
  user's notes directory.
- Authentication, authorization, or per-tool permission prompts beyond the
  publish confirmation. The MCP client is trusted to run a local binary.
- A general filesystem tool. Only notes and their planner/publish data are
  exposed.
- A delete tool. Destructive note management stays in the UI.
- Replacing the GUI or exposing every service method. The tool set is task
  shaped, not a binding dump.

## 2. Process model

```text
post-planner                 → Wails GUI, watcher, reminders
post-planner mcp             → stdio MCP server

runMCP():
  notes   := NewNotesService()
  publish := NewPublishService(notes)
  planner := NewPlannerService(notes, publish)
  server  := newMCPServer(notes, planner, publish)
  signal.NotifyContext(SIGINT, SIGTERM)
  log.SetOutput(os.Stderr)
  server.Run(ctx, &mcp.StdioTransport{})
```

The branch is checked before `application.New`, so the MCP mode never creates a
window, requests notification authorization, or starts the webview. Both modes
resolve the same notes root through `NotesService.init`, which means they share
files without any IPC.

The server has no long-running background work of its own. Each tool call reads
or writes through the services. Shutdown is driven by the signal context or by
the client closing stdin, after which `Run` returns and the process exits.

## 3. Tools

Tools are registered with typed input and output structs. JSON field names use
snake_case in tool arguments; Go models keep their existing camelCase JSON for
output.

### Notes

| Tool | Input | Output / behavior |
| --- | --- | --- |
| `search_notes` | `query?`, `limit?` (default 20, max 100) | Case-insensitive title/body search, newest first, with preview and derived status. Empty query lists recent notes. |
| `get_note` | `id` | `{note, plan, publish, status}` in one call. |
| `create_note` | `title`, `body` | Creates a note with content via `createTitled`; returns the new `Note`. |
| `append_note` | `id`, `markdown` | Appends Markdown at the end, preserving existing text; returns the updated note. |
| `replace_note` | `id`, `title?`, `body`, `expected_modified?` | Full rewrite; rejects if the note mtime no longer matches. Empty title keeps the current one. |

`search_notes` and `notes://index` call the unexported `NotesService.search`,
which is intentionally not Wails-bound. It reads `.md` files, skips unreadable
entries, and sorts by modification time. `createTitled`, `appendTo`, and
`replaceIfUnchanged` are likewise MCP helpers rather than public bindings.

`append_note` is the preferred mutation for agent output. It cannot clobber
concurrent edits in the way a whole-document replacement can. It is still a
read-modify-write operation, so the watcher's conflict banner covers the narrow
race with a simultaneous GUI save (`0008`).

### Planner

| Tool | Input | Behavior |
| --- | --- | --- |
| `get_plan` | `note_id` | Plan + derived status. |
| `list_statuses` | — | Map of note ID to derived status. |
| `list_backlog` | `limit?` (0 = all) | Unpublished notes with no scheduled slot, newest first. |
| `list_queue` | `from?`, `to?` | Scheduled/overdue slots, oldest first. |
| `list_history` | `from?`, `to?` | Done/skipped slots, newest completion first. |
| `set_stage` | `note_id`, `stage` | Sets `idea`, `drafting`, `ready`, or clears with `""`. |
| `schedule_note` | `note_id`, `platform`, `at` | Upserts the scheduled slot for a platform. Duplicate scheduled slots collapse to the earliest. |
| `reschedule_slot` | `note_id`, `slot_id`, `at` | Moves a scheduled slot, keeping its rendition. |
| `unschedule_note` | `note_id`, `slot_id` | Removes an empty slot; a slot with a rendition or notes becomes skipped history. |
| `mark_slot` | `note_id`, `slot_id`, `status` | `done` or `skipped`; stamps completion. A done slot with no remaining scheduled slot makes the note posted. |
| `save_rendition` | `note_id`, `slot_id`, plus `title/text/parts/body/tags/canonical` | Stores edited slot content; the service fills `sourceHash` and `editedAt`. |

`from` and `to` are RFC3339 or Unix milliseconds; empty means unbounded. All
planner mutations travel through `PlannerService`, so they take the in-process
mutex and the cross-process `.notes.lock`.

### Publishing

| Tool | Input | Behavior |
| --- | --- | --- |
| `prepare_draft` | `note_id`, `platform`, `mode?`, `number_threads?`, `canonical?` | Shapes a note into a platform `Draft` without sending it. |
| `get_publish_state` | `note_id` | Publish tracking sidecar. |
| `list_connections` | — | dev.to / Hashnode connection status; secrets never cross the boundary. |
| `publish_note` | `note_id`, `platform`, `title?`, `body?`, `tags?`, `canonical?`, `draft?`, `confirm` | Two-step create. `confirm=false` returns a dry run; `confirm=true` creates a draft by default. |
| `update_published` | same shape | Updates an existing remote post. `confirm=false` is a dry run; an existing post stays live unless `draft=true` is explicit. |

Publishing details:

- `publish_note` requires `confirm=true` to send anything.
- New posts default to `draft=true`; `draft=false` with `confirm=true` publishes
  live.
- An existing target with a `remoteId` is refused: the message tells the caller
  to use `update_published`. This prevents accidental duplicate remote posts.
- `update_published` requires an existing target; without one it returns a clear
  error before network access.
- X and LinkedIn never call a provider. They return `mode: "prepared"` plus the
  copy-ready `Draft` text, even if `confirm=true`.
- The `publishOutput.mode` is one of `dry-run`, `prepared`, `draft`,
  `published`, or `updated`, and `confirmed` records whether a remote call was
  actually made.

## 4. Resources and prompts

Resources let a client fetch data by URI:

| URI | MIME type | Contents |
| --- | --- | --- |
| `notes://index` | `application/json` | Every note with ID, title, preview, modified time, and derived status. |
| `notes://{id}` | `text/markdown` | The composed note document (`# Title`, blank line, body). |
| `plan://{id}` | `application/json` | The planner sidecar for that note. |

A missing or invalid note/plan resource returns `ResourceNotFoundError`, which
clients render as a normal "not found" rather than a transport failure.

Prompts are templates, not tools:

| Prompt | Arguments | Purpose |
| --- | --- | --- |
| `review_note` | `note_id` | Ask the model to review structure, unsupported claims, missing context, and concrete edits. |
| `draft_post` | `note_id`, `platform` | Ask the model to call `prepare_draft`, improve within platform limits, preserve voice, and not publish. |

The prompt text reinforces the safety rules: do not invent facts, do not
publish, and call `publish_note` with `confirm=true` only after the user
confirms.

## 5. Safety and concurrency

The MCP server does not add a second write path; it inherits the guarantees of
the services:

- Note writes use `writeFileAtomic`, so a client or app crash cannot leave a
  truncated note.
- Planner writes take `PlannerService.mu` and `.notes.lock`, so GUI and MCP
  schedule updates serialize across processes.
- Publish tracking is written atomically after a successful provider call.
- IDs are validated before path use; tool arguments cannot traverse out of the
  notes directory.
- No secrets are returned by `list_connections` or any tool output.
- There is no delete tool.

The remaining races are explicit:

| Race | Result |
| --- | --- |
| `append_note` while the GUI saves the same note | Last writer wins; the watcher reports an external change and the UI may show the conflict banner. |
| `replace_note` without `expected_modified` while the GUI edits | The MCP write can overwrite the newer GUI content; callers are instructed to pass the timestamp. |
| A GUI save while an agent appends | Same as above; the filesystem is the arbiter. |
| Two MCP clients mutate the same note | Each call is atomic, but read-modify-write sequences can interleave; `expected_modified` is the guard. |
| Publishing from MCP and the UI simultaneously | Each call is atomic, but there is no cross-process publish lock; duplicate guards are content/target based, not a transaction. |

These are documented limits, not silent behavior. The file lock is scoped to
planner sidecars because that is where lost updates were structural; note bodies
use optimistic timestamps and the watcher.

## 6. Client configuration

Most clients use the `mcpServers` shape:

```json
{
  "mcpServers": {
    "notes": {
      "command": "/absolute/path/to/bin/post-planner",
      "args": ["mcp"]
    }
  }
}
```

opencode uses:

```json
{
  "mcp": {
    "notes": {
      "type": "local",
      "command": ["/absolute/path/to/bin/post-planner", "mcp"],
      "enabled": true
    }
  }
}
```

On macOS, point at the binary inside the bundle
(`/Applications/Notes.app/Contents/MacOS/post-planner`). The server uses the
normal user config directory, so it sees the same notes as the GUI.

## 7. Failure handling

| Situation | Behavior |
| --- | --- |
| Missing note ID | Tool error: `note "<id>" not found`. |
| Invalid/traversal ID | Rejected by `validateID` before any path use. |
| Bad timestamp | Tool error explaining RFC3339 / Unix milliseconds; nothing scheduled. |
| Unknown platform/stage/status | Service-level error; no sidecar write. |
| `replace_note` with stale `expected_modified` | Rejected with the current/expected timestamps; caller re-reads. |
| `publish_note` without confirm | Dry run, no network, no tracking write. |
| Confirmed publish without credentials | Provider connection error before network. |
| Duplicate remote target | Refused with "use update_published instead". |
| `update_published` without a target | Error before network. |
| Sidecar write fails after remote success | Publish result carries a warning; the remote post exists. |
| stdio client disconnects / SIGINT | Context canceled; `Run` returns; process exits cleanly. |
| Malformed protocol input | Handled by the SDK; diagnostics go to stderr. |

## 8. Testing

`mcp_test.go` connects a real MCP client to the server over
`mcp.NewInMemoryTransports()` and drives typed tool calls. This exercises the
same registration, schema, and dispatch path as stdio without spawning a
process.

| Test | What it pins |
| --- | --- |
| `TestMCPCreateSearchAppend` | Create, search, append, stale-write rejection, and a successful guarded replace. |
| `TestMCPPlanTools` | Schedule with RFC3339, derived status, backlog/queue, mark done, and posted derivation. |
| `TestMCPPublishRequiresConfirm` | `confirm=false` returns a dry run and no confirmation; X returns prepared copy-ready text. |
| `TestMCPResources` | `notes://{id}` returns composed Markdown; a missing resource errors. |

Manual stdio probe (used during implementation):

1. Launch `bin/post-planner mcp` and send `initialize`.
2. List tools (21), resources, resource templates, and prompts.
3. Call `create_note`, `search_notes`, `resources/read`, and `schedule_note`.
4. Confirm the note and `.plan.json` appear in the notes directory and that
   `tools/call` results carry `structuredContent`.
5. Start the GUI against the same config directory and confirm the created note
   appears (watcher, `0008`).

Automated checks: `go test ./...`, `go test -race ./...`, `go vet .`,
`npm --prefix frontend run typecheck`, `npm --prefix frontend run build`,
`wails3 build`.
