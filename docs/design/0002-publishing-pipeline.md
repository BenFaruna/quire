# Publishing Pipeline

**Goal:** Turn any note into something the user can post. Two targets stay
local — X and LinkedIn, where the app shapes the text and the user pastes it —
and two publish directly over an API — dev.to and Hashnode. Every successful
remote write is recorded next to the note in `<id>.publish.json`, so the app can
later say "this is already live, here is where" without the user remembering a
remote ID.

**Architecture:** A new `PublishService` wraps `NotesService`. For social
targets it is a pure function: Markdown in, platform-shaped `Draft` out, no
network access, no credentials, no disk writes. For blog targets it adds a small
provider interface (`Validate`, `Publish`, `Update`) with one implementation per
platform, a credential store that prefers the OS keychain, and an atomically
written sidecar that records what happened remotely. The frontend dialog holds
no publishing logic of its own: it displays drafts, counts characters, and calls
Go.

**Tech Stack:** Go 1.26, Wails v3.0.0 (`application.Service`), vanilla
TypeScript + Vite, goldmark (the foundation's parser, reused for flattening),
`zalando/go-keyring` for secrets.

**Spec:** this document. It is retrospective, like `0001-notes-app-foundation.md`:
it records the publishing layer as built. It sits between the foundation
(`0001`) and the planner (`0003`), and it depends on both the note file contract
and the sidecar conventions the foundation established. Sections 1–10 describe
the design; Section 11 marks the boundary.

## Global Constraints

- **Two lanes, one dialog.** X and LinkedIn are local-only: nothing leaves the
  machine until the user pastes. dev.to and Hashnode are remote: the app sends
  the post itself. A platform's lane is fixed by whether it has a usable write
  API, not by a user setting.
- **Secrets never cross the binding boundary.** `ProviderStatus` carries the
  account name, publication, and storage backend — never the key. All HTTP
  happens in Go. `Connections()` returning a secret is a bug, and a test pins it.
- **The note file is never a publish record.** Publishing does not add
  front-matter, IDs, or status to `<id>.md`. Everything learned from the network
  lives in `<id>.publish.json`.
- **The sidecar records what happened, not what is intended.** It is written
  only after a provider call succeeds. Scheduling intent belongs to the planner
  layer (`0003`), which keeps a separate `<id>.plan.json`.
- **Every sidecar write is atomic and every sidecar ID is validated.**
  `writeFileAtomic` and `validateID` are reused unchanged; path traversal is
  blocked before any read or write.
- **A missing sidecar is not an error; a corrupt one is quarantined.** Corrupt
  JSON is renamed to `.publish.json.bak` and treated as "never published".
- **Publishing never mutates the note.** The dialog may edit the title, body,
  tags, and canonical URL that are sent; those edits are transient and do not
  change the `.md` file.
- **Errors are user-facing strings.** They name the platform and the fix
  ("reconnect dev.to", "choose a Hashnode publication first") so the dialog can
  show them directly.
- **Generated bindings are not hand-edited.** `PublishService` is registered in
  `main.go` and exposed through generated TypeScript bindings.

## Review Focus

1. **A successful post with failed tracking.** The remote write cannot be rolled
   back because the sidecar write failed. The service returns success with a
   `Warning` instead of an error, so the user does not retry a post that already
   exists. The residual risk is real: the in-memory state shows the target for
   this session, but after a restart a retry would create a duplicate remote
   post. The design accepts this because the alternative — pretending the post
   failed — guarantees the duplicate.
2. **The duplicate guard is content-equality, not identity.** `Publish` refuses
   a repeat only when the same platform already has the *same* content hash. A
   note edited after publishing will pass the guard and create a second remote
   post if `Publish` is called directly. The dialog avoids this by routing any
   existing target to `UpdatePublished`, but the service-level guarantee is
   narrower than it looks. This is the main API hazard in the layer.
3. **There is no unpublish.** dev.to and Hashnode both let the app create and
   edit, not take a live post down. `devtoProvider.Update` deliberately ignores
   a draft request for an already-published post, and the dialog hides "Save
   draft" once a target is live. The UI must never imply otherwise; unlinking
   only forgets the local record.
4. **Length accounting is platform-specific.** X counts every URL as 23
   characters regardless of length and allows numbering only when the user asks
   for it; the splitter reserves 8 characters for a `99/99 ` prefix. LinkedIn
   counts runes and trims at a word boundary. A canonical link appended to an
   over-limit X post survives in thread mode (it stays in the last part) but can
   be trimmed away in single-post mode, because truncation cuts from the end.
5. **Secrets on desktops without a keychain.** The fallback is a `0600` JSON
   file under the config directory, and the dialog says so. This is a deliberate
   availability-over-confidentiality tradeoff for minimal Linux sessions, not an
   oversight. The keychain always wins on read, and a successful keychain save
   removes a stale fallback file.
6. **Remote strings are data, not markup.** Post URLs, account names, and
   provider error text all originate outside the app. The dialog writes them with
   `textContent` and opens URLs through Wails `Browser.OpenURL`, so a hostile or
   malformed response cannot inject HTML into the webview.

---

## 1. Problem and outcomes

The foundation could write a note but not get it out. Export produced a file for
archiving; it did not produce a *post*. The gap had two very different halves:

- Social platforms have no free write API for this app (X's write API is paid;
  LinkedIn requires partner approval). The only honest product is to shape the
  text well and hand it to the composer.
- dev.to and Hashnode expose simple token-authenticated APIs. The app can
  publish there directly, but doing so creates a durable remote object that the
  user must be able to find again.

After this layer the app answers:

- What does this note look like as an X post, and does it fit? (Draft + counter)
- It is too long — thread or single trimmed post? (Chooser + thread parts)
- What will the opening lines look like before "see more"? (LinkedIn hook)
- Can I publish this to my blog without leaving the app? (dev.to / Hashnode)
- Where did it go, and is the local note newer than the remote post? (Sidecar +
  content hash)

### Non-goals

- No OAuth or per-user login flows. Blog targets use a pasted API key; social
  targets use no account at all.
- No scheduling in this layer. When a post should go out is planner state
  (`0003`), deliberately stored separately.
- No image uploads, cover images, or platform-specific rich embeds.
- No auto-posting to X or LinkedIn. That remains a copy/open-composer workflow.
- No Substack. It has no official publishing API; using its undocumented
  cookie-authenticated endpoint would be fragile and would make the app ask for
  a password it should never hold.
- No deletion of remote posts. Unlink is local bookkeeping only.

## 2. Architecture at a glance

```text
frontend/src/publish.ts
        │  generated bindings
        ▼
PublishService (publish.go, publishremote.go)
   ├── Prepare ──────────────► goldmark flattening ─► Draft
   ├── credentialStore ──────► OS keychain / 0600 fallback file
   ├── providers[devto] ─────► dev.to REST
   ├── providers[hashnode] ──► Hashnode GraphQL
   └── read/write sidecar ───► <notes-dir>/<id>.publish.json
                                   ▲
NotesService.publishRoot() ────────┘
```

`PublishService` holds a `*NotesService` rather than re-deriving the notes
directory. `publishRoot()` (added for this feature) is an unexported accessor
with the same `init` + mutex contract as the public methods, so sidecars land
next to their notes and travel with them when the folder is copied.

The service is constructed once in `main.go`:

```go
notes := NewNotesService()
Services: []application.Service{
    application.NewService(notes),
    application.NewService(NewPublishService(notes)),
}
```

Construction creates one shared `*http.Client` (45 s timeout) and one provider
per direct target. The client is injected into providers so tests can swap in an
`httptest` server without touching global state.

### Files

| File | Responsibility |
| --- | --- |
| `publish.go` | Platform constants, `Draft`, `Prepare`, Markdown flattening, thread splitting, character counting |
| `publishprovider.go` | The four-method provider interface and its request/response structs |
| `publishremote.go` | `PublishService` API: connections, connect/disconnect, publish, update, unlink, sidecar reads |
| `publishstore.go` | `<id>.publish.json`: read, write, delete, content hash |
| `publish_devto.go` | dev.to REST client |
| `publish_hashnode.go` | Hashnode GraphQL client |
| `credentials.go` | Keychain-first credential store and redacted `ProviderStatus` |
| `frontend/src/publish.ts` | The publish dialog (868 lines, separate from `main.ts`) |

`main.ts` keeps only the entry point: flush pending edits, then
`openPublishModal({id, title, body})`. The dialog owns its own state, so the
978-line editor module did not grow a fifth screen.

## 3. Draft generation for social targets

`Prepare(title, body, platform, opts)` is pure. It takes the values currently in
the editor (after `flushSave`), not a note ID, so the draft can reflect unsaved
intent without a second read. It returns a `Draft`:

```go
type Draft struct {
    Platform    string   `json:"platform"`
    Title       string   `json:"title"`
    Text        string   `json:"text"`        // single-post version
    CharLimit   int      `json:"charLimit"`
    OverLimit   bool     `json:"overLimit"`
    Truncated   bool     `json:"truncated"`
    Parts       []string `json:"parts"`       // X thread, one per post
    Hook        string   `json:"hook"`        // LinkedIn "see more" preview
    Body        string   `json:"body"`        // blog Markdown
    Tags        []string `json:"tags"`        // blog tags
    ContentHash string   `json:"contentHash"`
    Warnings    []string `json:"warnings"`
}
```

One struct covers both lanes. Social targets fill `Text`/`Parts`/`Hook` and
leave `Body` empty; blog targets fill `Body`/`Tags` and leave `Text` empty. That
keeps one binding type and one dialog state object, at the cost of a struct with
two disjoint halves — a trade accepted for a four-target feature.

### Flattening

Markdown is parsed with the foundation's shared `markdownRenderer`, not with
regular expressions:

| Markdown | Becomes |
| --- | --- |
| Heading / paragraph | Its inline text, as one block |
| Blockquote | Its children, flattened in place |
| List | One block per item, keeping `- ` or `1. ` markers |
| Nested list | Indented two spaces under its parent item |
| Task item | `[x] ` / `[ ] ` prefix |
| Link | `label (url)` for absolute http(s); label alone otherwise |
| Image | Alt text only |
| Table | Rows joined with ` | `, one row per line |
| Fenced/indented code | `Code snippet (go, 12 lines)` plus a warning |
| Raw HTML | Dropped |

Blocks are joined with blank lines, so thread splitting never cuts a list item
in half. Code is deliberately summarised rather than dumped: a code block rarely
survives a social post intact, and a warning is more honest than a wall of
truncated source.

An optional canonical link is normalised (bare hosts get `https://`) and
appended as its own paragraph before platform shaping. It is remembered in
`localStorage` between sessions.

### X

- Limit: 280. Counting uses `weightedLength`: every `http(s)://…` run is 23
  characters, everything else is one rune. That mirrors the platform's own
  counter and avoids the classic "it fits in the app but not on X" bug.
- A post that fits becomes `Parts = [text]` and is never split.
- An over-limit post sets `OverLimit`, packs paragraphs into as few parts as
  possible, and always also computes a single-post version: `Text` is trimmed at
  a word boundary and suffixed with `…`, with `Truncated = true`.
- Oversized paragraphs are split on sentence boundaries, then word boundaries,
  and finally by hard rune chunks for a single token longer than the limit (a
  URL with no spaces).
- Numbering (`1/5 `) is opt-in and applied only when the caller asks for thread
  mode. The splitter reserves 8 characters first, so numbering cannot push a
  part back over the limit.
- `opts.Mode` does not decide whether to thread — the splitter always produces
  parts when needed. Mode selects which result the dialog displays and whether
  numbering is applied.

### LinkedIn

- Limit: 3000 runes. Over-limit text is trimmed at a word boundary when a space
  exists past the halfway point, otherwise hard-cut, then suffixed with `…`.
  A warning records the original and final counts.
- `Hook` is the first 260 runes of the post. The dialog shows it only when the
  post is longer than 240 runes, because shorter posts do not get a "see more"
  cut on the platform.

### Platform normalisation

| Input | Canonical |
| --- | --- |
| `x`, `twitter` | `x` |
| `linkedin` | `linkedin` |
| `devto`, `dev.to`, `dev` | `devto` |
| `hashnode` | `hashnode` |
| anything else | lowercase trimmed value, then rejected by `Prepare` |

Normalisation happens at every public boundary (`Prepare`, `Connect`,
`Disconnect`, `SetPublication`, `Publish`, `UpdatePublished`, `Unlink`), so the
UI can be casual about labels without creating duplicate target keys.

## 4. Draft generation for blog targets

Blog platforms accept Markdown directly, so no flattening happens. `prepareBlog`
trims the body, extracts tags, and leaves the Markdown intact.

### Tag extraction

Two sources are supported, both of which can coexist:

1. A `Tags: go, wails, markdown` line (case-insensitive, first one only,
   outside code fences). The line is **removed** from the body that is sent.
2. Inline `#hashtags` outside code fences. Hashtags stay in the body.

Fence tracking ignores both sources inside fenced code, so a shell comment or a
`Tags:` line in a snippet is not stolen from the post.

### Normalisation

| Platform | Rule | Limit |
| --- | --- | --- |
| dev.to | lowercased, ASCII letters and digits only | 4 |
| Hashnode | whitespace collapsed, sent as `{name, slug}` with a slug derived from the name | 15 |

Duplicates are removed by normalised key. dev.to's 4-tag limit is a hard API
constraint, so extras are dropped and a warning is emitted. The warning is keyed
on the *raw* tag count, so five raw tags that collapse to three still warns; the
message says extras were dropped. That is slightly conservative, not wrong.

A blog draft with an empty body is still returned, with
"This note has no body text to publish." The dialog can show it, but
`Publish`/`UpdatePublished` reject an empty body before any network call.

## 5. Direct publishing

### Provider interface

```go
type publishProvider interface {
    ID() string
    Validate(ctx context.Context, secret string) (providerAccount, error)
    Publish(ctx context.Context, secret string, req publishRequest) (remotePost, error)
    Update(ctx context.Context, secret, remoteID, currentStatus string, req publishRequest) (remotePost, error)
}
```

Four methods are enough for every current target, and adding a platform does not
touch the service or the dialog beyond a tab. `currentStatus` is passed to
`Update` so a provider can decide the correct mutation without the service
knowing platform rules. `providerAccount` carries the account name plus owned
publications (Hashnode); `remotePost` carries the ID, URL, status, and canonical
URL back.

Provider calls are wrapped in a 90 s context; validation uses 30 s. The shared
HTTP client has a 45 s timeout and every response body is read through
`io.LimitReader(..., 1<<20)`, so a misbehaving endpoint cannot exhaust memory.

### dev.to

- Validate: `GET /api/users/me` with `api-key: <secret>` and the Forem v1 Accept
  header. The username, falling back to the display name, becomes the account.
- Publish: `POST /api/articles` with `title`, `body_markdown`, `published`,
  optional `tags` and `canonical_url`.
- Update: `PUT /api/articles/{id}`. Draft state is computed as
  `req.Draft && currentStatus != "published"` — an update must never silently
  unpublish a live post.
- Errors: 401/403 becomes "dev.to rejected the API key. Reconnect dev.to from
  the publish dialog."; other failures prefer the API's `error` message, then
  the raw body, then the status line.
- Response: numeric `id` becomes a string; if `url` is empty it is reconstructed
  as `https://dev.to/<slug>`; `published` maps to `draft`/`published`.

### Hashnode

- Validate: `me { username name publications(first: 20) { edges { node { id title url } } } }`
  against `https://gql-beta.hashnode.com/` with a Bearer token. Publications are
  cached in the credential blob so the dialog can offer a picker without another
  round trip.
- Publish: `createDraft`, then `publishDraft` when the post is not a draft. That
  is the API's supported flow; there is no single "publish" mutation. A draft
  keeps the draft ID and a `hashnode.com/draft/<id>` URL.
- Update: `updatePost` for a published target, `updateDraft` for a draft, and
  `publishDraft` afterwards if the user asked to publish now. If the draft ID
  changes, the new ID is stored.
- Tags are sent as `[{name, slug}]`; the canonical URL is
  `originalArticleURL`.
- Errors: HTTP 401/403 and GraphQL `FORBIDDEN` (or a message mentioning "pro
  plan") become a Pro-plan message. Hashnode's API is Pro-only since May 2026;
  the dialog explains this before the user pastes a token, so the error is a
  reminder rather than the first mention.
- GraphQL errors are surfaced from the `errors[0].message`; an empty `data` is
  treated as success with no fields, not as a crash.

Hashnode does not return a canonical URL, so `CanonicalURL` stays empty for that
target. The dialog shows "View post" only when a URL exists.

### Service-level flow

```text
Publish(platform, req)
  normalise platform + request (trim title, CRLF → LF, fallback title)
  require provider, note ID, non-empty body
  read sidecar
  if target exists with the same content hash → "use Update instead"
  require stored credentials
  provider.Publish(ctx 90s, secret, request)
  build/merge PublishTarget
  write sidecar
  return PublishResult (with Warning if the sidecar write failed)
```

`UpdatePublished` follows the same path but requires an existing target with a
`RemoteID`, and merges the response conservatively: empty fields in the provider
response never erase a known good value. `PublishedAt` is stamped the first time
a target reaches `published`, and is not reset by later edits.

`Unlink` removes one platform's target. If no targets remain, it deletes the
sidecar outright rather than leaving an empty shell; otherwise it rewrites the
remaining targets atomically. The remote post is untouched.

`PublishState(noteID)` returns the sidecar (or an empty state) for the dialog.
The sidecar path is derived from the notes root, so publishing state is
per-folder, exactly like the notes.

## 6. Credentials

Secrets are stored per platform under the service name
`post-planner-publishing`.

```text
get(platform):
  keychain hit ──► decode ──► return (keychain)
  otherwise ────► read <config>/post-planner/publishing/<platform>.json
save(platform, creds):
  keychain set ──► remove fallback file ──► return (keychain)
  on failure ───► write fallback file 0600 ──► return (file)
delete(platform):
  best-effort keychain delete, then remove fallback file
```

The stored blob is `{secret, account, publicationId, publicationName,
publications}`. Caching account metadata next to the secret means `Connections()`
can render "Connected as …" and the publication picker without a network call —
important because the dialog opens instantly and should not depend on the
network to show that it is connected.

Details that matter:

- **Keychain first.** Even if a fallback file exists, a keychain read wins; a
  successful keychain save deletes the file so there is never a stale second
  copy.
- **Silent fallback, loud status.** Any keychain error (no secret service, locked
  session) drops to the file. The returned `ProviderStatus.Storage` is `file`,
  and `Warning` explains the 0600 location. The dialog displays it.
- **Empty secrets are invalid.** `decodeCredentials` rejects a blob whose secret
  is empty, so a half-written or hand-edited file cannot masquerade as connected.
- **`forceFile` for tests.** Tests never touch the developer's keychain; the
  store takes an injectable directory and a flag that skips `go-keyring`
  entirely.
- **Never read back.** Nothing in the service returns `providerCredentials` to
  the frontend. `ProviderStatus` is a separate, redacted struct.

`Connect` validates the secret with the provider *before* storing it, so a typo
does not leave a broken connection that fails only at publish time. For
Hashnode, connecting also selects the first publication automatically; the user
can change it later with `SetPublication`, which only accepts a publication ID
that the stored account actually owns.

## 7. Tracking sidecar

`<notes-dir>/<id>.publish.json`, mode `0644`, indented JSON with a trailing
newline:

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

- **One file, many targets.** `Targets` is a map keyed by canonical platform, so
  a note can be on dev.to and Hashnode at once, each with its own remote ID and
  status.
- **`contentHash` is `sha256:" + sha256(composeDocument(title, body))`.** It is
  the same document shape used on disk, so a note changed only in whitespace at
  the end is still a change, while a change the app would not persist is not.
  The dialog compares the current draft hash to the target hash to show "Changed
  since last publish".
- **`syncedAt` is always now; `publishedAt` is first-published-only.** The dialog
  can say "Synced 3m ago" without losing the original publication date.
- **`updated` is rewritten on every save**, so an unchanged sidecar still has a
  meaningful mtime for debugging.
- **Reads are defensive.** A missing `targets` map is created; a zero `version`
  is upgraded; `noteId` is always set from the argument, so a hand-edited file
  cannot redirect writes to another note.
- **Corruption is contained.** Invalid JSON is renamed to `.bak` and treated as
  an empty state. The user keeps both the broken file and a working app.
- **Deletion is best-effort.** `NotesService.Delete` calls
  `deletePublishState` after removing the `.md`; failure to remove tracking data
  is ignored so it can never block deleting a note.

The sidecar is *not* synced by this layer. It travels with the notes folder when
the user copies it, which is the app's entire sync story.

## 8. The publish dialog

`frontend/src/publish.ts` is a self-contained module. It is opened from the
toolbar button or `Ctrl/Cmd + Shift + P`; `main.ts` flushes the pending save
first and passes the editor's current title and body. Keyboard shortcuts in
`main.ts` are disabled while the dialog is open, so the two surfaces never fight
for `Escape` or `Ctrl+S`.

Structure: four tabs (X, LinkedIn, dev.to, Hashnode), a shared sub-heading, a
per-platform panel, a warnings list, and a footer whose buttons change meaning:

| State | Footer |
| --- | --- |
| Social | Cancel · Copy / Copy thread · Open X / Open LinkedIn |
| Blog, not connected | Close · Connect (inline form) |
| Blog, new target | Close · Save draft · Publish |
| Blog, draft target | Close · Update draft · Publish now |
| Blog, published target | Close · Update post (Save draft hidden) |

### State and async safety

- Module-level state: selected platform, social mode, numbering, source note,
  current draft, publish state, connection map, and per-platform blog form
  drafts.
- `prepareSeq` guards every refresh. Each `await` is followed by a sequence
  check, so clicking through four tabs quickly cannot let an older `Prepare`
  response overwrite the newest panel.
- `refresh(true)` is used on open and tab changes to move focus into the editor;
  `refresh()` on link/mode changes does not steal focus.
- Switching tabs keeps in-progress blog edits in `blogForms[platform]`, so a
  title typed on the dev.to tab is still there after visiting Hashnode.
- `busy` disables Connect / Save / Publish / Disconnect / Unlink for the
  duration of a call, and button labels become "Publishing…", "Updating…",
  "Connecting…" so the user can see what is in flight.
- `localStorage` access is wrapped in try/catch. The link field
  (`notes.publishLink`) and canonical field (`notes.publishCanonical`) are
  conveniences; a webview with storage disabled simply loses the memory rather
  than breaking the dialog.

### Social panel

- Shows the editable single post with a live counter, or a list of editable
  thread parts each with its own counter and Copy button.
- The thread/single chooser appears only when the post is over limit on X; the
  numbering checkbox appears only in thread mode.
- `Copy` uses the clipboard API and falls back to a temporary textarea with
  `document.execCommand("copy")` for webviews without clipboard permission.
- `Open X` / `Open LinkedIn` launches the platform's intent URL with the first
  part (or the single post) prefilled. The rest of a thread is pasted by hand,
  which is the only option without a paid API.
- The LinkedIn hook preview appears above the editor when the text is long
  enough to be cut by "see more".

### Blog panel

- Not connected: platform-specific setup instructions, a password input, and a
  Connect button. Enter in the input connects. The Hashnode panel states the
  Pro-plan requirement up front. The secret is cleared from the DOM after a
  successful connect.
- Connected: account row with Disconnect, the storage warning when the fallback
  file is in use, a publication picker when the account owns more than one
  publication, and the editable title / Markdown body / tags / canonical fields.
- If a target exists, a status strip shows Draft or Published, "Changed since
  last publish" when hashes differ, the relative sync time, and View post /
  Unlink links.
- After a successful create/update, the dialog copies the returned target's hash
  into the local draft. Without that, a post edited in the dialog would show
  "Changed since last publish" immediately, because the draft's hash would still
  reflect the note as it was before the dialog edits.
- Unlink confirms nothing because it is local-only and reversible by publishing
  again; the notice explicitly says the remote post was not changed.

### Accessibility

The dialog is `role="dialog"` with `aria-modal="true"`, labelled by its heading;
the tablist uses `aria-selected`; the counter is `aria-live="polite"`. Escape
closes, clicking the backdrop closes, and focus returns to the element that
opened the dialog.

## 9. Failure handling

| Situation | Behaviour |
| --- | --- |
| Unsupported platform | Rejected before any work: `unsupported publish target "…"` |
| No note selected / empty body | Rejected before credentials or network: "no note selected", "this note has no body text to publish" |
| Target not connected | Rejected before network: "dev.to is not connected" |
| Invalid API key | Validation fails; nothing is stored, nothing is published |
| Hashnode without Pro | GraphQL `FORBIDDEN` becomes an explicit Pro-plan message |
| Note unchanged and already published | `Publish` refuses with "use Update instead" |
| Note changed and already published | Dialog calls `UpdatePublished`; direct `Publish` would create a new post (Review Focus 2) |
| Remote succeeds, sidecar write fails | Success with `Warning`; the dialog shows the warning as an error-style notice |
| Corrupt sidecar | Renamed to `.publish.json.bak`, treated as never published |
| Keychain unavailable | 0600 fallback file, `Storage: file`, warning shown in dialog |
| Network timeout / 5xx | Provider error surfaces as a toast in the dialog; the sidecar is untouched |
| Live post + "Save draft" | Prevented by the provider and hidden by the UI; published posts cannot be unpublished |
| Unlink | Local record removed; remote post deliberately left live |
| Note deleted | Sidecar removed best-effort; remote post left live |

Two invariants hold across all rows: **no local tracking is written unless the
remote call succeeded**, and **a failed local write never turns a successful
remote action into a user-visible failure**.

## 10. Testing

The publishing layer has 34 Go tests across three files. There is still no
frontend test runner; `tsc --noEmit` and `vite build` are the automated frontend
checks, and the dialog is verified manually. Remote providers are tested against
`httptest` servers, never the real APIs.

`newTestPublishService` builds a service whose notes root is `t.TempDir()`,
installs a `forceFile` credential store in another temp dir seeded with a fake
secret, and points the dev.to provider at the test server. Tests therefore never
read the real config directory or keychain.

| Test | What it pins |
| --- | --- |
| `TestPrepareXShortNote` | A fitting post is one part, not over limit, with the correct hash. |
| `TestPrepareXLongNoteOffersThreadAndSingle` | Over-limit posts expose both a thread and a trimmed single post. |
| `TestPrepareXThreadNumbering` | `1/5 ` prefixes are applied only on request and never exceed the limit. |
| `TestPrepareXShortNoteIsNeverSplit` | Short posts are not fragmented by the thread splitter. |
| `TestPrepareLinkedInTrimsToLimit` | 3000-rune trim, ellipsis, and warning counts. |
| `TestPrepareAppendsCanonicalLink` | Canonical links are appended and bare hosts are normalised. |
| `TestPrepareUnsupportedPlatform` | Unknown targets fail before shaping. |
| `TestParsePublishBlocksFlattening` | Headings, lists, tasks, tables, links, and code summaries flatten correctly. |
| `TestWeightedLengthCountsURLsAsFixed` | URLs count as 23 characters; Unicode counts as runes. |
| `TestSplitSentences` | Sentence splitting keeps punctuation and closing quotes. |
| `TestTruncateWeightedRespectsLimit` | Weighted truncation stays under the limit and marks truncation. |
| `TestPrepareEmptyNote` | Empty content produces an empty draft, not a crash. |
| `TestPrepareBlogStripsTagsLineAndNormalisesDevto` | `Tags:` removal and dev.to tag normalisation. |
| `TestPrepareBlogCollectsHashtags` | Inline hashtags are collected and left in the body. |
| `TestPrepareBlogHashnodeKeepsMultiWordTags` | Hashnode tags keep words and produce slugs. |
| `TestPrepareBlogEmptyBody` | Empty blog drafts carry a warning. |
| `TestPublishContentHash` | Hash is stable, document-based, and changes with title or body. |
| `TestNormalizePublishPlatform` | Aliases collapse to canonical platform keys. |
| `TestPublishStateRoundTrip` | Sidecar write/read preserves targets, stamps `updated`, stays human-readable. |
| `TestReadPublishStateMissingAndCorrupt` | Missing is empty; corrupt becomes `.bak`. |
| `TestDeleteNoteRemovesSidecar` | Deleting a note removes its publish record. |
| `TestPublishStateRejectsTraversal` | A `../` note ID is rejected before path use. |
| `TestDevtoValidate` | `/api/users/me` parsing and account fallback. |
| `TestDevtoValidateRejectsBadKey` | 401 becomes a reconnect message. |
| `TestDevtoPublishDraftPayload` | `published:false`, tags, canonical, and draft status. |
| `TestDevtoUpdateKeepsPublishedPostLive` | Updating with draft intent does not unpublish a live post. |
| `TestHashnodeValidateListsPublications` | GraphQL viewer query and publication list. |
| `TestHashnodePublishCreatesThenPublishes` | Draft creation followed by publication, IDs and URLs. |
| `TestHashnodeProGate` | `FORBIDDEN` and Pro-plan messages become the explicit error. |
| `TestPublishCreatesSidecarAndGuardsDuplicates` | Successful create writes tracking; repeat is refused; unlink removes the empty sidecar. |
| `TestUpdatePublishedRefreshesHash` | Update stores the new URL, hash, and first `publishedAt`. |
| `TestUpdatePublishedRequiresTrackedPost` | Update without a remote ID fails before network. |
| `TestPublishRequiresConnection` | Publish fails before network when not connected. |
| `TestConnectionsNeverLeaksSecret` | `ProviderStatus` exposes account/storage but never the secret; fallback warning is present. |

The tests deliberately use the same `composeDocument` and `writeFileAtomic` code
as production, so a round-trip failure would indicate a real contract break
rather than a test-only fixture drift.

## 11. Boundaries and downstream work

This document stops after publishing. It leaves the note, the sidecar, and the
provider interface as the seams the planner builds on:

- **The planner reuses the sidecar pattern.** `<id>.plan.json` has its own
  lifecycle (intent, written offline) and must not be merged with
  `.publish.json` (history, written after network success). `0003` keeps them
  separate for exactly that reason.
- **The planner reuses the content hash.** `publishContentHash` becomes the
  basis for detecting stale renditions, so "the note changed since this draft"
  means the same thing in the scheduling UI as in the publish dialog.
- **The planner reuses `Prepare`.** A rendition starts as a `Draft` snapshot;
  scheduling does not invent a second Markdown flattener.
- **Provider additions stay local.** A new direct target implements four methods
  and gets a tab. It must not add platform branches to `PublishService`.
- **No layer may write publish state into the `.md` file.** If a future feature
  needs to remember something about a note, it gets a sidecar, not front-matter.

What stays true after the planner lands:

1. Social targets never touch the network or credentials; the user is always the
   one who posts.
2. Secrets live in the keychain when possible and in a 0600 file with a visible
   warning when not; the frontend never sees them.
3. Remote success is recorded atomically next to the note, and remote failure
   leaves tracking untouched.
4. Publishing never modifies the note it publishes.
5. Preview, export, print, social drafts, and planner renditions all derive from
   the same goldmark pipeline, so the app never shows two different readings of
   one note.
