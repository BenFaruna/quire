# Notes

A Markdown notes app built with [Wails v3](https://v3.wails.io/), Go, and TypeScript.

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
- **Atomic saves** — notes are written to a temp file, fsynced, then renamed, so
  a crash mid-write cannot truncate a note.
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
`os.UserConfigDir()/post-planner/publishing` and says so in the dialog. The frontend
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

## Development

```bash
wails3 dev        # run with hot reload
wails3 build      # production build → bin/post-planner
go test .         # backend tests

cd frontend
npx tsc --noEmit  # type-check the UI
npm run build     # rebuild embedded frontend assets
```

After changing an exported Go service or model, regenerate the typed bindings
with `wails3 generate bindings -ts` from the repository root.

## Layout

```
main.go             # app bootstrap, window, service registration
noteservice.go      # note CRUD, file storage, seeding, Markdown → HTML
export.go           # Markdown/HTML export + zip bundling
publish.go          # note → platform drafts, blog tags, content hash
publishremote.go    # PublishService: connect, publish, update, unlink
publish_devto.go    # dev.to REST client
publish_hashnode.go # Hashnode GraphQL client
publishprovider.go  # provider interface and request/response types
publishstore.go     # <id>.publish.json sidecar
credentials.go      # keychain-backed credentials (+ 0600 file fallback)
markdown.go         # shared goldmark pipeline
noteservice_test.go
publish_test.go
publish_blog_test.go
publish_remote_test.go
frontend/
  index.html
  src/main.ts       # UI: list, editor, preview, shortcuts
  src/publish.ts    # publish dialog: social drafts, blog targets, tracking
  src/style.css
  bindings/         # generated TypeScript bindings (do not edit)
```

## Where notes live

`os.UserConfigDir()/post-planner/notes` — on Linux that is `~/.config/post-planner/notes`.
Click the folder icon in the sidebar to copy the path to the clipboard.
