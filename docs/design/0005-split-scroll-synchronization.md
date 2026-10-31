# Split Scroll Synchronization

**Goal:** In Split view, scrolling the Markdown source or the rendered preview
keeps the other pane at the corresponding position. The two panes have
different heights, fonts, padding, images, and diagram sizes, so the mapping is
proportional rather than line-based.

**Architecture:** A single `syncScroll(from, to)` function in
`frontend/src/main.ts` maps the source pane's normalized scroll position onto
the destination pane's scrollable range. Both panes listen for scroll events
passively. A short-lived `scrollSource` guard and a 100 ms timer prevent the
programmatic assignment from being echoed back as a second scroll event. The
feature is entirely frontend; Go, storage, and rendering are unchanged.

**Tech Stack:** vanilla TypeScript, DOM `scrollTop` / `scrollHeight` /
`clientHeight`, passive scroll listeners. No scroll library and no anchor
mapping are introduced.

**Spec:** this document. It is retrospective: it records the behavior shipped
in `4f1dcf3` and the invariants the later preview decorators (`0006`) and
external-change reload (`0008`) must preserve.

## Global Constraints

- **Sync only in Split mode.** `syncScroll` returns immediately unless
  `app.dataset.mode === "split"`. Write and Read modes have one visible pane and
  must not receive programmatic scroll changes.
- **Both directions.** Editor → preview and preview → editor use the same
  function. There is no privileged source.
- **Proportional, not line-anchored.** The mapping is
  `ratio = from.scrollTop / (from.scrollHeight - from.clientHeight)`, then
  `to.scrollTop = ratio * (to.scrollHeight - to.clientHeight)`. This stays
  correct when the panes have different content heights.
- **No feedback loop.** The destination's scroll event caused by setting
  `scrollTop` must not bounce back. `scrollSource` records the pane currently
  driving; events from the other pane are ignored until the timer clears.
- **No layout reads during the event beyond what is required.** The function
  reads four geometry values and performs one write. It does not query the DOM
  or recompute the preview.
- **Degenerate panes are no-ops.** If either pane has no scrollable overflow
  (`max <= 0`), the function returns without touching the other pane.
- **No persisted scroll position.** Scroll sync is a live interaction, not
  per-note state. Switching notes starts both panes at the top unless the note
  load/reload path says otherwise.
- **The preview may be re-rendered at any time.** Mermaid rendering and preview
  debouncing can change `previewPane.scrollHeight` after HTML arrives. The
  code resets the guard before re-syncing after a render.

## Review Focus

1. **Render-time resync.** `renderPreview()` assigns `preview.innerHTML`, then
   calls `decoratePreview()` (which may asynchronously replace Mermaid code
   blocks with SVGs), and then, if still in Split mode, clears `scrollSource`
   and calls `syncScroll(editor, previewPane)`. Without the reset, a render that
   happens during a scroll gesture could be ignored by the guard.
2. **Async decoration and stale tokens.** `decoratePreview` and
   `renderMermaidBlocks` receive a sequence token and stop if a newer preview
   started. Scroll sync does not depend on decoration completion; it re-syncs
   against whatever height exists when rendering settles.
3. **Mode flips.** Entering Split calls `applyViewMode()`, which sets the grid
   and then syncs once from the editor. Leaving Split does not need to restore
   anything; the hidden pane keeps its own scroll position for the next entry.
4. **Very short documents.** When both panes fit without overflow, scroll
   events do not fire usefully and `syncScroll` returns early. There is nothing
   to synchronize.
5. **Small fractional differences.** The mapping is not exact pixel-for-pixel;
   it is a proportion. A code block rendered much larger than its source will
   occupy more destination distance. This is the accepted tradeoff for not
   maintaining a source-line ↔ rendered-node map.
6. **Programmatic scrolls from the app.** Selecting a note, reloading after an
   external change, or toggling modes may set `scrollTop` or replace HTML. Only
   the explicit render path re-syncs; unrelated programmatic scrolls do not
   start a feedback loop because they happen outside the scroll listeners or
   are covered by the guard.

---

## 1. Problem and outcomes

Split view puts the textarea and the rendered article side by side. Without
synchronization, writing near the end of a long note meant manually scrolling
the preview to compare output. A line-based mapping is not viable: the preview
has headings, margins, tables, syntax-highlighted blocks, KaTeX formulas, and
Mermaid SVGs that do not correspond one-to-one with source lines.

The chosen behavior is intentionally simple:

- Drag either scrollbar or use the wheel/trackpad over either pane.
- The other pane follows proportionally.
- There is no jitter from the other pane echoing the programmatic scroll.
- When the preview re-renders, it re-aligns to the editor once, then resumes
  following the user.

### Non-goals

- Exact line/heading alignment or a table of contents that tracks the viewport.
- Scroll-position persistence per note or across app restarts.
- Synchronizing the planner, sidebar, or dialogs.
- A JS test harness. This is verified manually in Split view.

## 2. Algorithm

```ts
let scrollSource: HTMLElement | null = null;
let scrollSourceTimer: ReturnType<typeof setTimeout> | undefined;

function syncScroll(from: HTMLElement, to: HTMLElement) {
    if (app.dataset.mode !== "split") return;

    // Ignore the scroll event caused by assigning scrollTop on the other pane.
    if (scrollSource && scrollSource !== from) return;

    const fromMax = from.scrollHeight - from.clientHeight;
    const toMax = to.scrollHeight - to.clientHeight;
    if (fromMax <= 0 || toMax <= 0) return;

    const ratio = Math.min(1, Math.max(0, from.scrollTop / fromMax));
    scrollSource = from;
    to.scrollTop = ratio * toMax;

    clearTimeout(scrollSourceTimer);
    scrollSourceTimer = setTimeout(() => {
        scrollSource = null;
    }, 100);
}

editor.addEventListener("scroll", () => syncScroll(editor, previewPane), { passive: true });
previewPane.addEventListener("scroll", () => syncScroll(previewPane, editor), { passive: true });
```

Properties:

- **Clamping.** `ratio` is clamped to `[0, 1]` before multiplication, so a
  transient overscroll (rubber-banding) cannot push the other pane beyond its
  range.
- **Single writer.** While `scrollSource` is set to `from`, any event whose
  `from` is the other pane is ignored. This is what breaks the feedback loop.
- **Timer, not a boolean.** The guard must outlive the browser's asynchronous
  scroll event for the assignment. 100 ms is short enough that a human cannot
  notice the other pane being briefly deaf, and long enough for webview event
  delivery.
- **Overwrite semantics.** A new user scroll on the *same* source pane clears
  and restarts the timer, so continuous scrolling keeps the other pane
  following.
- **Passive listeners.** Neither handler calls `preventDefault`, so the
  browser can keep scrolling smooth.

## 3. Lifecycle and integration points

| Point | Behavior |
| --- | --- |
| Entering Split (`applyViewMode`) | Sets the grid, shows both panes, clears `scrollSource`, syncs once from the editor. |
| After `renderPreview()` replaces HTML | If still Split, clears `scrollSource` and syncs from the editor after decoration. |
| Mermaid blocks finish rendering | The preview height changes; the next render/resync uses the final height. |
| Selecting another note | `selectNote` loads the note and calls `applyViewMode`, which re-syncs. The panes are not individually persisted. |
| Reloading an externally changed note (`0008`) | `reloadActiveFromDisk` replaces title/body and renders the preview; the normal render path re-syncs if in Split. |
| Resizing the window | CSS grid changes pane widths and therefore heights. No explicit handler is required; the next scroll uses current geometry. |
| Leaving Split | `syncScroll` is a no-op because `data-mode` is no longer `split`. |
| Write/Read mode | Synchronization is disabled; only one pane is visible. |

The guard is module-level because the editor and preview are module-level
elements. There is one editor and one preview in the app, so there is no need
for a more general controller object.

## 4. Why not line mapping

A source-line-to-rendered-element map would require the renderer to emit source
positions and the frontend to track them through preview decoration. Goldmark
already emits heading IDs, but those are not enough: paragraphs, lists, tables,
and code blocks have no stable one-to-one line count, and nested blockquotes
shift line numbers. Maintaining a second mapping would be a source of drift with
the HTML that Go actually returns.

Proportional mapping has known limits:

- A document that is mostly one huge code block will not feel line-aligned.
- A formula or diagram that is much taller than its Markdown source shifts the
  destination proportionally rather than matching the nearest line.

Those limits are acceptable for the product: the panes are a writing aid, not a
synchronized source map. The implementation is small, has no dependency, and
survives renderer changes.

## 5. Failure handling

| Situation | Behavior |
| --- | --- |
| One pane has no overflow | No programmatic scroll; the other pane is untouched. |
| Browser emits a scroll for the assignment | Ignored while `scrollSource` is set. |
| User scrolls immediately after a sync | Same-source scroll restarts the timer; the destination follows. |
| Preview height changes after Mermaid renders | Next render/resync maps against the new height; a scroll already in flight may land slightly off until the next event. |
| Preview render fails | `reportError` shows a toast; the previous preview remains and no sync is attempted. |
| Mode is not Split | Function returns before reading geometry. |
| Overscroll/rubber-band | `ratio` is clamped; destination stays in range. |
| Rapid mode toggling | Each entrance re-syncs; the timer is harmless because later `syncScroll` calls check the mode first. |

## 6. Testing

There is no frontend test runner. Automated checks remain
`npm --prefix frontend run typecheck`, `npm --prefix frontend run build`, and
`wails3 build`. The behavior is verified manually:

1. Open a long note in Split mode and scroll to the middle from the editor; the
   preview follows and does not oscillate.
2. Scroll the preview to the middle; the editor follows.
3. Continue scrolling in the same pane for several seconds; the other pane
   keeps tracking (the timer must not latch).
4. Add enough text to overflow, wait for the debounced preview render, and
   confirm the panes re-align without a jump loop.
5. Include a code block, a table, a KaTeX display formula, and a Mermaid
   diagram; scroll past them and confirm the mapping remains usable.
6. Switch to Write and back to Split; the sync resumes and the preview is
   shown.
7. Resize the window while in Split; scrolling still maps to the new heights.
8. Reload an externally changed note while in Split (`0008`); the new content
   renders and scroll remains responsive.
