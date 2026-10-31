# Responsive Layout and Mobile Shell

**Goal:** Make the app usable on a small window and on a phone-sized webview
without maintaining a second UI. At 380 px wide the note list becomes a drawer,
the editor becomes the only pane, the topbar collapses secondary actions into a
menu, and the planner gets a compact calendar and its own way back to the note
list. Nothing about storage, rendering, or scheduling changes.

**Architecture:** The desktop layout is a two-column CSS grid (`.app`), and the
narrow layout is the same DOM with a media-query-driven drawer. TypeScript owns
only the state that CSS cannot express: whether the drawer is open, which
element should be inert, where focus should return, and whether Split mode is
allowed at the current width. The Wails window keeps a hard minimum of
`380×520`, which is the smallest size the compact layout is expected to
support.

**Tech Stack:** Wails v3.0.0 window options, vanilla TypeScript, plain CSS
(`@media`, `env(safe-area-inset-*)`, `inert`, `matchMedia`). No UI framework and
no mobile build target are introduced. The existing frontend is embedded in the
same binary.

**Spec:** this document. It is retrospective: it records the responsive work
shipped in `00c1bf0` and the small-screen behavior that later features must keep
working (the planner in `0003`, math in `0006`, publish dialogs in `0002`).

## Global Constraints

- **One DOM, two layouts.** There is no separate mobile template. The sidebar,
  editor, preview, planner, dialogs, and banners are the same nodes at every
  width. Responsive behavior is CSS plus a small amount of layout state.
- **The minimum window is a real target.** `main.go` sets `MinWidth: 380` and
  `MinHeight: 520`. A layout that needs 600 px is a bug, not an acceptable
  desktop-only choice.
- **The drawer must be keyboard- and screen-reader-safe.** When it is closed on
  mobile, the sidebar is `inert`; when it is open, the workspace is `inert`, the
  backdrop is visible, and focus moves into the drawer. Closing restores focus
  to the element that opened it.
- **Split view is desktop-only.** Below 760 px the Split option is hidden and a
  persisted `split` preference degrades to Write. The preference itself is not
  rewritten, so widening the window restores the user's chosen mode.
- **Secondary actions must remain reachable.** Export and Delete are removed
  from the topbar at narrow widths, not lost: they live in the overflow menu.
- **Safe areas are respected.** Topbar, planner toolbar, statusbar, and
  full-screen dialogs use `env(safe-area-inset-*)` so notches and gesture bars
  do not cover controls.
- **No page-level horizontal scrolling.** The sidebar becomes a fixed overlay;
  the editor, preview, calendar, and dialogs fit the viewport width.
- **Touch targets stay usable.** At mobile widths icon buttons are 38×38 px and
  note rows and queue rows get taller padding.

## Review Focus

1. **Drawer state after rotation or resize.** The media-query listener and the
   `resize` handler both call `syncLayout()`. Crossing back above 760 px closes
   the drawer without restoring focus and removes the `inert` attributes, so the
   desktop shell cannot be left partially disabled.
2. **Focus is not stolen from the editor.** The drawer and topbar menu are
   modal-ish surfaces only while open. Escape closes them before the global
   shortcuts run. The disk-conflict banner (`0008`) deliberately never takes
   focus, so an external edit cannot interrupt typing on a phone.
3. **Hidden is not the same as removed.** `.mobile-only` is `display: none`
   above 760 px and `display: inline-grid !important` below it. Buttons that
   exist only for the drawer (close, hamburger) are still in the DOM so event
   delegation and `aria-controls` keep working.
4. **The planner has its own hamburger.** The planner builds its toolbar in
   `planner.ts`, outside the static topbar. It renders a `.mobile-only`
   `data-sidebar-toggle` button with `aria-controls="sidebar"` so the same
   delegation in `main.ts` opens the drawer while the user is in Queue or
   Calendar.
5. **The calendar remains legible at 380 px.** Mobile cells show short colored
   bars instead of text chips, reduce row height, and keep the day number as the
   target that opens the day's queue. Full titles remain available through the
   desktop chips, `title`, and `aria-label`.
6. **No behavior depends on hover.** Row actions, menus, and dialogs are all
   click/tap driven. Hover styles are enhancement only.

---

## 1. Problem and outcomes

The foundation and planner were designed for a 1280×820 desktop window. At
around 700 px the fixed sidebar consumed most of the width and the preview
became unusable. At phone width the window could not be narrowed below
`860×560`, and the Wails mobile scaffold served the same frontend with no
responsive behavior at all.

After this work:

- The app can be resized to 380×520 without clipped controls.
- On a narrow screen, the note list opens over the editor as a drawer with a
  backdrop, and closes after selecting a note.
- The topbar keeps the title, view control, and primary actions; Export and
  Delete move into a `⋯` menu.
- Split mode is not offered where it cannot work, and Read/Write remain.
- Queue and Calendar are usable on a phone: compact rows, wrapping filters, a
  compact month grid, and a same-width schedule popover.
- Dialogs fill the screen on small phones instead of overflowing it.

### Non-goals

- A native mobile application or a separate mobile build. The Wails mobile
  scaffold may compile the frontend, but this design targets the desktop window
  and a phone-sized webview.
- A responsive navigation paradigm beyond the drawer: no bottom tab bar, no
  separate tablet layout, no gesture navigation.
- Changing the storage, sync, renderer, or planner model. This is a layout
  layer only.
- A frontend test runner. The responsive behavior is checked by typecheck,
  production build, and a manual viewport matrix.

## 2. Breakpoints

The stylesheet uses four breakpoints, each with a distinct job:

| Width | Change |
| --- | --- |
| `≤ 1040px` | Sidebar narrows from 280 px to 244 px; preview padding tightens. |
| `≤ 820px` | Sidebar narrows to 210 px; save status hides; preview font drops to 14 px. |
| `≤ 760px` | Mobile shell: one column, sidebar becomes a drawer, topbar wraps, Split is removed, planner compacts, Export/Delete move to the menu. |
| `≤ 560px` | Dialogs become full-width/full-height sheets; modal actions wrap; publish tabs tighten. |

The TypeScript breakpoints mirror the CSS:

```ts
const MOBILE_QUERY = "(max-width: 760px)";
const SPLIT_QUERY = "(min-width: 761px)";
```

`MOBILE_QUERY` drives drawer state and `SPLIT_QUERY` decides whether a persisted
Split preference is effective. Keeping the two queries adjacent to the CSS
breakpoint is intentional; a third source of truth would drift.

## 3. Shell and drawer

At desktop width `.app` is a two-column grid:

```text
┌───────────────┬──────────────────────────────────────────────┐
│ sidebar       │ workspace: topbar / editor / statusbar       │
└───────────────┴──────────────────────────────────────────────┘
```

At `≤ 760px` it becomes one column and the sidebar is taken out of flow:

```css
.app { grid-template-columns: minmax(0, 1fr); }

.sidebar {
    position: fixed;
    inset: 0 auto 0 0;
    z-index: 45;
    width: min(84vw, 320px);
    transform: translateX(-100%);
    visibility: hidden;
    transition: transform 0.22s ease, visibility 0.22s;
}
.app.is-sidebar-open .sidebar { transform: none; visibility: visible; }
```

The open state is a class on `#app`, not a separate component. `main.ts` keeps
the rest of the contract in sync:

- `openSidebar()` records `document.activeElement`, closes the topbar menu, adds
  `.is-sidebar-open`, shows `#sidebar-backdrop`, sets `workspace.inert`, updates
  every toggle's `aria-expanded`, removes `inert` from the sidebar, and focuses
  `#sidebar-close`.
- `closeSidebar(restoreFocus)` reverses all of that. Selecting a note calls
  `closeSidebar()` without focus restoration because the editor is the next
  destination; Escape and the backdrop call it with restoration.
- `syncSidebarInert()` keeps the closed sidebar inert on mobile and removes
  `inert` entirely on desktop. An inert closed drawer is why `Tab` never walks
  into hidden rows.
- `syncLayout()` runs on media-query changes and on `resize`; above the
  breakpoint it force-closes the drawer and clears the backdrop.

The hamburger in the static topbar and the close button in the sidebar are
`.mobile-only`. The planner toolbar renders its own hamburger with the same
`data-sidebar-toggle` attribute; a single delegated click handler in `main.ts`
serves all of them.

## 4. Topbar and overflow menu

The topbar is a flex row: hamburger (mobile), title input, save status, view
segmented control, schedule/publish actions, and the `⋯` menu.

At mobile width it wraps into two rows. The first row carries the menu button
and title; the second carries actions. The layout intentionally keeps the title
on its own line because the Wails window can be only 380 px wide.

Actions that do not fit or that are destructive move into `#topbar-more`:

- Export and Print
- Delete

`#topbar-more` toggles `#topbar-menu`, sets `aria-expanded`, and moves focus to
the first enabled item. The menu closes on selection, on outside click, on
Escape, and whenever the drawer opens. Export and Delete buttons remain in the
DOM for desktop and are hidden only by CSS at `≤ 760px`.

The menu is deliberately not focus-trapped: it is a small popup, not a modal.
Escape and outside-click are the required exit paths; both return focus to the
`⋯` button.

## 5. Editor, preview, and view modes

View mode is stored in `localStorage` under `notes.viewMode` as `edit`, `split`,
or `read`. The stored value is the user's preference; the effective mode can be
narrower:

```ts
const effective = preferredMode === "split" && !splitQuery.matches
    ? "edit"
    : preferredMode;
```

- `applyViewMode()` writes `app.dataset.mode = effective`.
- The Split button is hidden by CSS at mobile width.
- `setMode()` refuses to enter Split while `SPLIT_QUERY` does not match, so a
  keyboard shortcut cannot create a broken two-pane layout.
- Widening the window re-applies the preference and restores Split if it was
  stored.

The mobile editor shell is one column. Even if a stale `data-mode="split"` is
present, the mobile CSS hides the preview pane and gives the editor the full
width. This is a defensive fallback, not the normal path.

The formatting toolbar remains horizontally scrollable rather than wrapping into
multiple rows at 380 px. Math and Mermaid buttons (`0006`) participate in that
scroll.

## 6. Planner on small screens

`planner.ts` builds its screens at runtime, so the responsive rules live in
`plan.css` alongside the planner styles.

At `≤ 760px`:

- The planner toolbar gets the mobile hamburger, reduces padding, and uses
  safe-area top padding.
- `.planner-scroll` padding drops to 12 px.
- Queue rows tighten their gap and padding; the row menu button becomes 34×34.
- Queue filter chips wrap instead of overflowing.
- The calendar grid reduces row height, cell padding, weekday font size, and day
  number size.
- Calendar chips become 5 px colored bars with `color: transparent` and
  `font-size: 0`; the day number and the `+N more` control still open the day's
  queue. The full title remains in the element's accessible name.
- The schedule popover is capped at `min(300px, calc(100vw - 16px))`.
- The reminder banner is positioned to fit the viewport instead of hanging off
  the edge.

The planner is a screen inside the same workspace, so switching to Queue or
Calendar does not change the drawer mechanics. Leaving Notes mode still flushes
pending edits first.

## 7. Dialogs and feedback

At `≤ 560px`:

- `.modal-backdrop` uses 12 px padding.
- `.modal` fills the width, is capped by `100dvh`, and scrolls internally.
- `#publish-modal` stretches to the viewport.
- `.modal-publish` becomes a full-height sheet with no border radius and
  bottom safe-area padding.
- `.modal-actions` wraps and its buttons flex to share the row.
- Publish tabs reduce horizontal padding but stay tappable.

The export dialog, confirm dialog, and publish dialog all inherit this behavior
because they share the `.modal` classes. The toast and the disk-conflict banner
are positioned above the statusbar and never block the editor. The banner's
action pair (`Reload` / `Keep mine`) stays on one line at phone width.

## 8. Accessibility and failure handling

| Situation | Behavior |
| --- | --- |
| Drawer closed on mobile | Sidebar is `inert`; toggles report `aria-expanded="false"`. |
| Drawer open on mobile | Workspace is `inert`; focus lands on the close button. |
| Drawer opened from the planner | Planner's delegated hamburger opens the same drawer. |
| Escape with the drawer open | Drawer closes and focus returns to its opener. |
| Escape with the menu open | Menu closes and focus returns to `⋯`. |
| Window crosses 760 px | Drawer force-closes, backdrop hides, `inert` is removed. |
| Stored mode is Split on mobile | Effective mode is Write; the preference is preserved. |
| Webview misses a media-query event | The `resize` handler re-runs `syncLayout()` above the breakpoint. |
| No `addEventListener` on `MediaQueryList` | `onMediaChange` falls back to the deprecated `addListener`. |
| Long note title at 380 px | Title input flexes and truncates visually; it remains editable and scrolls horizontally. |
| Calendar day has many slots | Mobile shows colored bars and `+N more`; titles are not lost, just compacted. |
| Modal taller than the viewport | Internal scroll plus `100dvh` cap; the close/cancel action stays reachable. |

## 9. Testing

There is no frontend test runner. Automated checks are:

- `npm --prefix frontend run typecheck`
- `npm --prefix frontend run build`
- `wails3 build`

The layout itself is verified manually at these widths, in both Notes and
planner screens:

| Width | Checks |
| --- | --- |
| 1280 px | Two-column desktop shell unchanged; Split available. |
| 1040 px | Sidebar narrows; no overlap. |
| 820 px | Sidebar narrower; save status hidden. |
| 760 px | Drawer opens/closes, backdrop, Escape, focus return, planner hamburger. |
| 560 px | Dialogs are full-width sheets; actions wrap; no horizontal scroll. |
| 380 px | Minimum window: title, view toggle, menu, editor, statusbar all reachable; calendar bars legible; popover fits. |

Also check a touch-sized viewport with the on-screen keyboard open (the editor
must still scroll), and rotate/resize across 760 px while the drawer is open.
