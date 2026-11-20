# Quire

<p align="center">
  <img src="build/appicon.png" alt="Quire icon" width="128" height="128">
</p>

A local-first Markdown notebook built with [Wails v3](https://v3.wails.io/), Go,
and TypeScript.

Notes are stored as ordinary `.md` files — one file per note, with the title as the
document's leading `# Heading` — so they remain portable and readable outside the app.

## Features

- **Live Markdown preview** rendered server-side by [goldmark](https://github.com/yuin/goldmark)
  with GitHub-flavoured Markdown: tables, strikethrough, task lists, autolinks,
  footnotes, and smart typography.
- **Syntax highlighting** for fenced code blocks (Chroma, GitHub Dark theme) with
  one-click copy buttons.
- **Safe rendering** — raw HTML in notes is never injected into the webview.
- **Auto-save** (debounced) plus explicit save; write/read/split view modes.
- **Export** — the export dialog preselects the open note, offers a “Select all”
  toggle, and exports any subset of notes as Markdown or self-contained HTML
  (a single file, or a `.zip` for multiple notes), or sends them to the system
  print dialog to save as PDF. HTML exports use the same goldmark pipeline as
  the preview.
- **Import existing Markdown** — pick one or more `.md` files; each becomes a note.
  Files without an `# H1` take their title from the filename.
- **Publish** — turn a note into an X post/thread or a LinkedIn post, or publish
  directly to dev.to and Hashnode. Social targets stay local (copy/intent URLs)
  and need no account; blog targets use an API key stored in the OS keychain.
  Per-note `<id>.publish.json` tracking lets you update or unlink a post later.
  Drafts shaped for each platform are saved in the note's `<id>.plan.json`,
  reopen with your edits intact, and can be read in a roomy **Preview** view
  instead of the editor.
- **Atomic saves** — notes are written to a temp file, fsynced, then renamed, so
  a crash mid-write cannot truncate a note.
- **Home screen** — the app opens on a map of your categories, with quick
  actions for a new note and today's log, plus shortcuts to the queue and
  calendar.
- **Categories** — create your own buckets, each with a purpose description,
  filter the note list by them, and file the open note from the status bar.
- **Daily log** — a dated entry (`YYYY-MM-DD`) in the seeded Daily log category,
  created from a small template. An agent can read a run of entries over MCP
  and write a track record back into the notes.
- **Search**, relative timestamps, and a live note list.
- **Interactive task lists** — tick a checkbox in the preview to update the source.
- **Keyboard shortcuts**:

  | Shortcut | Action |
  | --- | --- |
  | `Ctrl/Cmd + N` | New note |
  | `Ctrl/Cmd + S` | Save now |
  | `Ctrl/Cmd + E` | Cycle Write → Split → Read |
  | `Ctrl/Cmd + Shift + E` | Export or print notes |
  | `Ctrl/Cmd + Shift + P` | Publish this note |
  | `Ctrl/Cmd + F` | Focus search |
  | `Ctrl/Cmd + B` / `I` / `K` | Bold / italic / link |

## Home, categories and the daily log

The app opens on the **Home** screen. It is the map of the app: New note and
Today's log actions, a card for every category, shortcuts to the queue and
calendar, and the most recent notes.

**Categories** are your own buckets for notes. Each has a name and a purpose —
a sentence saying what belongs there. That purpose is what an agent reads
before deciding what to write, so it is worth being specific. Create, rename
and delete categories from Home or from the `+` chip above the note list;
deleting a category keeps its notes and moves them to **Inbox**.

The chips above the note list filter by category, and the status bar under the
editor sets the open note's category. The list itself groups by planner status
or by category through the **Group by** switch above it, and the app remembers
which one you last used. Creating a note while a category is
selected files it there, and importing Markdown from that state files the
imported notes there too.

**Daily log.** The seeded Daily log category makes "log my day" a single dated
note: Home → Today's log opens the entry for today, or creates it titled
`YYYY-MM-DD` with `Log`, `Learned` and `Tomorrow` sections. Entries are
ordinary Markdown notes in a category, so the MCP server can read a week or a
month and
write a **track record** into the seeded Track record category. The app itself
does not call a model.

Categories and assignments live in `.categories.json` in the notes directory,
next to the Markdown files. A corrupt file is moved to `.bak` and treated as
empty, and hand-edited assignments that point at a missing category are
dropped. The file is updated under the same cross-process lock as the planner
sidecars, so the app and the MCP server cannot lose each other's changes.

## Publishing

The paper-plane button in the toolbar (or `Ctrl/Cmd + Shift + P`) opens the
publish dialog. It has four targets: **X / Twitter**, **LinkedIn**, **dev.to**
and **Hashnode**.

### Social posts — X and LinkedIn

These stay **local-first**: the app prepares and shapes the text, then hands it
to your clipboard or the platform's own composer. No account, API key, or
network request is involved, and nothing is written to disk.

- **X / Twitter** — plain text with a 280-character limit. Links count as the
  platform's fixed 23 characters.
- **LinkedIn** — plain text with a 3000-character limit, plus a preview of the
  opening lines that appear before “see more”.

Markdown is flattened through the same goldmark pipeline used by the preview:
headings and paragraphs become text, links become `label (url)`, lists and task
items stay readable, tables flatten into rows, and fenced code blocks become a
short `Code snippet (go, 12 lines)` summary with a warning.

When a note is too long for X, the dialog asks each time whether to post it as a
**thread** or as a **single trimmed post**. Thread parts are editable, each has
its own counter and copy button, and can optionally be numbered (`1/5`).
LinkedIn posts are trimmed at a word boundary and shown with a warning.

The optional **Link to include** field appends a canonical URL to the draft and
is remembered for next time. Use **Copy** / **Copy thread** to paste manually, or
**Open X** / **Open LinkedIn** to launch the platform composer with the first
part prefilled.

### Blog posts — dev.to and Hashnode

The dev.to and Hashnode tabs publish the note directly. The note is sent as
Markdown with its title, tags and optional canonical URL; no social flattening
is applied.

**Credentials.** Keys are validated by the provider, then stored in the OS
keychain (`zalando/go-keyring`). If the desktop has no secret service available
(for example a minimal Linux session), the app falls back to a `0600` file under
`os.UserConfigDir()/quire/publishing` and says so in the dialog. The frontend
never receives the key; all HTTP happens in Go.

**Connecting.**

- **dev.to** — create a key in *Settings → Extensions → DEV Community API
  Keys*, paste it, and press Connect. Drafts are saved with `published:false`;
  publishing is a separate click.
- **Hashnode** — create a Personal Access Token in *Account settings →
  Developer*. Hashnode's API is **Pro-only since 13 May 2026**; the tab explains
  this up front. If the publication is not on Pro, the API returns `FORBIDDEN`
  and the app shows a Pro-plan message instead of a generic error. The endpoint
  is Hashnode's current GraphQL host, `https://gql-beta.hashnode.com/`.

**Tags.** Add a `Tags: go, wails, markdown` line anywhere in the note, or use
inline `#hashtags`. The `Tags:` line is removed from the published body while
hashtags stay in place. Tags are normalised per platform (dev.to: lowercase
alphanumeric, max 4; Hashnode: `{name, slug}`, max 15). You can also edit the
tag field in the dialog before publishing.

**Publishing.** Use **Save draft** to create a private draft, or **Publish** to
make it live. For Hashnode the app creates a draft and then publishes it, since
that is the API's supported flow.

**Tracking.** Every successful create/update writes `<id>.publish.json` next to
the note:

```json
{
  "version": 1,
  "noteId": "20261028-153000-a1b2",
  "updated": 1761740000000,
  "targets": {
    "devto": {
      "remoteId": "1234567",
      "url": "https://dev.to/user/slug",
      "status": "published",
      "canonicalUrl": "https://example.com/post",
      "publishedAt": 1761740000000,
      "syncedAt": 1761740000000,
      "contentHash": "sha256:9f2c…"
    }
  }
}
```

The sidecar is written atomically. If it is corrupt it is renamed to
`.publish.json.bak` and treated as empty, and deleting a note removes its
sidecar. The dialog uses it to show a **Draft** or **Published** badge, warn when
the note changed after publishing (`contentHash` mismatch), and offer **View
post**, **Update** and **Unlink**. Unlink only forgets the local record; the
remote post is never deleted. Published posts cannot be unpublished through
either API — “Save draft” is hidden for them so the button never lies.

**Substack.** Substack has no official publishing API (only an undocumented,
cookie-authenticated internal one), so the app does not publish there. Use the
X/LinkedIn-style **Copy** flow and paste into the Substack editor.

## MCP server

The same binary can run as a local [Model Context Protocol](https://modelcontextprotocol.io)
server, so an agent can search and write notes, update the planner, and prepare
or publish posts without copy/paste. It speaks stdio and works with any MCP
client; the desktop app does not need to be running.

```bash
bin/quire mcp
```

The server reads and writes the same Markdown files and `.plan.json` /
`.publish.json` sidecars as the app. It reuses the real services, so schedule
dedupe, `posted` derivation, backlog exclusions, drafts and publish tracking
behave exactly as they do in the UI.

### Tools

| Tool | Purpose |
| --- | --- |
| `search_notes` | Case-insensitive title/body search with derived status |
| `get_note` | Note body plus planner and publish sidecars |
| `create_note`, `append_note` | Capture new notes (optionally in a category), or append reviews/summaries |
| `replace_note` | Full rewrite guarded by `expected_modified` |
| `list_categories`, `create_category` | Read categories with their purpose and note count, or add one |
| `list_category_notes`, `set_note_category` | Pull a category's notes with full bodies; file or unfil a note |
| `get_journal_entry`, `append_journal`, `list_journal` | Read or extend a dated daily log entry; list entries over a range |
| `get_plan`, `list_statuses` | Read stages, slots and derived status |
| `list_backlog`, `list_queue`, `list_history` | Planner views |
| `set_stage`, `schedule_note`, `reschedule_slot`, `unschedule_note`, `mark_slot` | Planner mutations |
| `prepare_draft`, `save_rendition` | Platform drafts and edited rendition snapshots |
| `get_publish_state`, `list_connections` | Publish tracking and connected accounts |
| `publish_note`, `update_published` | Two-step publish: `confirm=false` returns the payload, `confirm=true` sends it |

Notes are also exposed as `notes://{id}` resources, plans as `plan://{id}`, and
there are `notes://index` and `categories://index` resources. Optional prompts
(`review_note`, `draft_post`, `summarize_category`, `track_record`) are
provided for clients that support prompts.

The category tools are how an agent can add to a specific category, pull from
it and summarise it: `list_categories` shows the user's stated purpose for each
bucket, `list_category_notes` returns the source notes with full bodies, and
`create_note` with a `category` (or `set_note_category`) writes the result back
into the right place. `track_record` reads daily logs over a date range and
writes a summary into the Track record category.

### Safety

- `publish_note` never sends anything unless `confirm=true`; new posts are
  remote drafts unless `draft=false` is also passed. An existing remote post is
  never re-created: use `update_published` instead.
- X and LinkedIn have no publishing API integration. Those calls return the
  copy-ready draft text instead of pretending to post.
- Planner sidecar updates take a cross-process file lock (`.notes.lock`), so the
  app and the MCP server cannot lose each other's schedule changes.
- There is no delete tool. Deleting notes remains a UI action with a
  confirmation dialog.

### Client configuration

Most clients use the `mcpServers` shape:

```json
{
  "mcpServers": {
    "notes": {
      "command": "/absolute/path/to/bin/quire",
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
      "command": ["/absolute/path/to/bin/quire", "mcp"],
      "enabled": true
    }
  }
}
```

On macOS, point `command` at the binary inside the bundle, for example
`/Applications/Quire.app/Contents/MacOS/quire`.

### Logging

Diagnostics go to stderr and are also appended to
`<config dir>/quire/mcp.log` (`~/.config/quire/mcp.log` on Linux,
`~/Library/Application Support/quire/mcp.log` on macOS). The file exists
because many clients hide their child process's stderr. It rotates to
`mcp.log.1` at 1 MiB.

Each launch logs its version, PID, executable path, working directory and notes
directory, then every protocol method it handles — the handshake (`initialize`,
or `server/discover` for newer clients), `tools/list`, `tools/call` with the
tool name, `resources/read`, and so on — each with a duration and its outcome.
Tool arguments are not logged because they carry note bodies.

If the client reports that it cannot connect and the log has no `server
starting` line, the client never launched the binary: check that `command` is an
absolute path to the executable itself. If the log shows `server starting` but
no `initialize`, the process started and the failure is in the handshake; the
logged error says why.

Set `QUIRE_MCP_LOG` to move the file, or to `off` to keep diagnostics on
stderr only.

### Watching for changes

While the app is open, it watches the notes directory with `fsnotify` (kernel
events, no polling). A note created or edited by the MCP server or an external
editor appears in the sidebar immediately. If the open note changed, the app
reloads it when the editor is clean; if there are unsaved edits it shows a
**Reload / Keep mine** banner instead of overwriting your text.

## Development

```bash
wails3 dev        # run with hot reload
wails3 build      # production build → bin/quire
wails3 package    # native package/installer for the current OS
go test ./...     # backend tests

cd frontend
npm run typecheck # type-check the UI
npm test          # frontend unit tests
npm run build     # rebuild embedded frontend assets
```

After changing an exported Go service or model, regenerate the typed bindings
with `wails3 generate bindings -ts -i` from the repository root, matching the
`generate:bindings` task.

The `wails3` CLI must be built with the same Go toolchain as the `go` command it
invokes. If binding generation prints warnings about source-processing packages,
unknown stdlib fields, or “is not a type”, rebuild the CLI with the local Go:

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
```

## Releases

Pushing a `v*` tag runs the release workflow, which packages the app on Linux,
macOS and Windows and attaches the results to a draft GitHub Release:

- `quire-<version>-linux-amd64.AppImage`, `.deb`, `.rpm`
- `quire-<version>-darwin-arm64.app.zip`
- `quire-<version>-windows-amd64-installer.exe`

The version comes from the tag without its leading `v`. Manual workflow runs
build `0.0.0.dev.<sha>` assets without creating a release. Review the draft
release before publishing it.

## Icons and packaging art

The icon and DMG artwork is committed as build output, so a change starts at one
of the SVGs in `build/` and ends in the binary files beside it.

| Source | Render | Consumed by |
| --- | --- | --- |
| `build/appicon.svg` | `build/appicon.png` | `build/darwin/icons.icns` (the app icon, and the mounted DMG volume), `build/windows/icon.ico`, the Linux packages, the Android launcher mipmaps |
| `build/darwin/dmg-file-icon.svg` | `build/darwin/dmg-file-icon.png` | `build/darwin/dmg-file-icon.icns` — the icon of the `.dmg` file itself in Finder |
| `build/darwin/dmg-background.svg` | `build/darwin/dmg-background.png` | the DMG window background (540×380) |
| `build/appicon.icon/` | `build/darwin/Assets.car` | the macOS 26 icon bundle (see below) |

The two DMG slots are wired in `build/darwin/Taskfile.yml`: `--volume-icon` is
`build/darwin/icons.icns`, which is what the mounted drive shows, and
`--file-icon` is `build/darwin/dmg-file-icon.icns`, which is what the downloaded
disk image shows. Both are defaults and can be overridden per run.

### Rendering the PNGs

`wails3 task icons` runs every step on this page, in order, from the SVGs to the
committed binaries. The commands are spelled out below so a single step can be
re-run by hand when one of them fails.

`appicon.svg` and `dmg-file-icon.svg` share the same Q geometry; the DMG icon
adds the graph grid, the glow and the volume ribbon. Render them with
ImageMagick's librsvg backend:

```bash
magick -background none build/appicon.svg -resize 1024x1024 -depth 8 -strip build/appicon.png
magick -background none build/darwin/dmg-file-icon.svg -resize 1254x1254 -depth 8 -strip PNG32:build/darwin/dmg-file-icon.png
magick -background '#efe7d6' build/darwin/dmg-background.svg -flatten -resize 540x380 -depth 8 -strip PNG24:build/darwin/dmg-background.png
```

Then let Wails build the container formats. Run it from `build/`, because the
tool resolves its relative filenames against the working directory, and park the
Windows `.ico` of the DMG icon somewhere throwaway so it cannot overwrite the
app's:

```bash
cd build
wails3 generate icons -input appicon.png -macfilename darwin/icons.icns -windowsfilename windows/icon.ico
wails3 generate icons -input darwin/dmg-file-icon.png -macfilename darwin/dmg-file-icon.icns -windowsfilename /tmp/discard.ico
```

Android launcher icons come from the same `appicon.png` at five densities, the
`_round` variants clipped by an ImageMagick circle mask of exactly the tile size
(`circle N/2,N/2 N/2,0`, no inset). Keep `--` out of the SVG header comments: a
double hyphen is illegal inside an XML comment, and librsvg then refuses the
whole document.

### The macOS `actool` step

`build/darwin/Assets.car` is the macOS 26 icon bundle — layered art with dark,
tinted and clear variants plus the specular highlight — and it is compiled from
the Icon Composer document in `build/appicon.icon/` by Apple's `actool`. Only
Xcode ships `actool`, so this is the one icon artefact that cannot be built on
Linux or Windows.

`task darwin:package` reaches it through `common:generate:icons` in
`build/Taskfile.yml`, which passes `-iconcomposerinput appicon.icon
-macassetdir darwin`. `wails3 task icons` makes the same call, so on a Mac a single run
rebuilds `Assets.car` along with everything else. Anywhere else `wails3 generate
icons` skips that half and refreshes only the `.icns` and `.ico`.

When `Assets.car` sits next to `icons.icns` in `Contents/Resources`, macOS 26
prefers it. The bundle sets no `CFBundleIconName`, and the system falls back to
`appicon` when the file is present (see the commented option in
`build/config.yml`). The two therefore have to be regenerated together: a
committed `Assets.car` that predates an icon change wins over the newer `.icns`,
which puts the old artwork back in the Dock. That is true of the copy in this
repository, which predates the Quire mark.

Deleting it is safe. Both copy steps in `build/darwin/Taskfile.yml` are guarded
with `if [ -f … ]`, so a bundle without `Assets.car` simply falls back to
`icons.icns`. Rebuild it on a Mac with `task darwin:package` after
`task common:generate:icons`, or delete it before packaging. The DMG itself is
macOS-only too: `wails3 tool package --format dmg` needs `hdiutil`.

## Layout

```
main.go             # app bootstrap, window, service registration
config.go           # app identity and per-user paths
noteservice.go      # note CRUD, file storage, seeding, Markdown → HTML
export.go           # Markdown/HTML export + zip bundling
planner.go          # planner stages, slots and derived status
planstate.go        # <id>.plan.json sidecar format and locking
reminder.go         # due-reminder timers and notifications
publish.go          # note → platform drafts, blog tags, content hash
publishremote.go    # PublishService: connect, publish, update, unlink
publish_devto.go    # dev.to REST client
publish_hashnode.go # Hashnode GraphQL client
publishprovider.go  # provider interface and request/response types
publishstore.go     # <id>.publish.json sidecar
credentials.go      # keychain-backed credentials (+ 0600 file fallback)
markdown.go         # shared goldmark pipeline
mcp.go              # stdio MCP server: tools, resources, prompts, logging
watcher.go          # fsnotify watcher + external-change event
categories.go       # category store, assignments and daily log entries
lock.go             # cross-process notes-directory lock
signal_linux.go     # hand SIGUSR1 back to WebKit on Linux
signal_other.go     # signal handling stub for other platforms
*_test.go           # backend, planner and publishing tests
frontend/
  index.html
  src/main.ts       # UI: list, editor, preview, shortcuts
  src/home.ts       # home screen and category dialog
  src/home.css      # home screen and category dialog styles
  src/planner.ts    # queue, history, calendar and schedule picker
  src/plan.css      # planner surfaces and schedule picker styles
  src/platforms.ts  # publishing platform metadata and limits
  src/publish.ts    # publish dialog: social drafts, blog targets, tracking
  src/style.css
  src/*.test.ts     # frontend unit tests
  bindings/         # generated TypeScript bindings (do not edit)
```

## Where notes live

`os.UserConfigDir()/quire/notes` — on Linux that is `~/.config/quire/notes`.
Click the folder icon in the sidebar to copy the path to the clipboard. Category
definitions and assignments are in `.categories.json` there; notes themselves
stay ordinary `.md` files.
