# Categories and a daily log

**Status:** implemented

## Context

Notes were a single flat list with an optional publishing planner. That works
while the app is a scratchpad, but not when the user wants different kinds of
writing to live apart: client notes, reading notes, a log of the day. The user
also wants a model to be able to add to a specific bucket, pull from it, and
summarise what is in it over MCP.

The request also asked for an opening page that navigates the application and
"dictates how I intend to make use of the note". That purpose belongs on the
bucket itself, not in a global settings page, because it has to be visible to an
agent at the moment it decides where to write.

## Decision

Add user-defined **categories** and a seeded **Daily log** category, plus a
Home screen that acts as the map of the app.

### Storage

Categories live in a single sidecar, `<notes dir>/.categories.json`:

```json
{
  "version": 1,
  "updated": 1762600000000,
  "categories": [
    {
      "id": "daily-log",
      "name": "Daily log",
      "description": "A day-by-day record of what happened...",
      "kind": "journal",
      "created": 1762600000000
    }
  ],
  "assignments": { "20261108-132301-a1b2": "daily-log" }
}
```

Why one file rather than a sidecar per note:

- Rename and delete are cheap: no fan-out over every note.
- One read answers "what is in this category", which is exactly the MCP query.
- The existing `.notes.lock` already serialises cross-process writes. Category
  updates join that protocol with no new locking mechanism.

The trade-off is a hot file on every assignment. Writes are small, atomic
(`writeFileAtomic`) and only happen on a user action or an agent call, so the
cost is negligible for a personal notes directory. Markdown files remain the
canonical note content; losing `.categories.json` degrades every note to Inbox
rather than losing content.

Normalisation is deliberately forgiving because agents and humans both edit the
file: invalid ids and blank names are dropped, unknown kinds fall back to
`notes`, and assignments that point at a missing category are forgotten. A
corrupt file is renamed to `.bak`, matching the planner and publish sidecars.

### Categories

- `Category.Kind` is `notes` or `journal`. The kind is metadata, not a separate
  storage shape; it lets the UI and MCP find the daily log without hard-coding a
  name the user may rename.
- `Description` is the user's statement of intent. It is shown on Home, in the
  category dialog, and in `list_categories` for agents.
- Ids are slugged from the name with a random suffix, so `"Client research"`
  becomes `client-research-1a2b`. Name resolution is case-insensitive, so an
  agent can say `"daily log"` without listing ids first.
- Deleting a category deletes the definition and its assignments, never notes.
  Notes return to Inbox.

### Daily log

A daily log entry is an ordinary note titled `YYYY-MM-DD` in the category whose
kind is `journal`, created from:

```markdown
## Log

- 

## Learned

- 

## Tomorrow

- 
```

`JournalEntry(date)` finds the entry by title and category, or creates it. The
title is the date so entries sort lexically and `list_journal` can honour a date
range without a new index. If the journal category has been deleted, it is
recreated rather than leaving the Today action permanently broken.

The app never calls a model. A track record is just a note an agent writes into
the Track record category after reading `list_journal`.

### Home screen

Home is a screen, not a modal, so the sidebar and its category chips stay
available. It shows:

- a greeting and Today's date, with New note and Today's log actions;
- one card per category with its purpose, note count, Open / New note / Today
  and Edit actions, plus a dashed New category card;
- shortcuts to the queue, calendar and all notes;
- the four most recently edited notes.

The last open note is still restored, so switching to Notes resumes where the
user left off.

### MCP

Category tools mirror the UI service methods: `list_categories`,
`create_category`, `list_category_notes`, `set_note_category`.
`create_note` accepts an optional `category`. The journal gets
`get_journal_entry`, `append_journal` and `list_journal`.

`list_category_notes` returns full note bodies because its job is to give a
model source material for a summary; `search_notes` stays a lightweight index.
Prompts (`summarize_category`, `track_record`) describe the workflow but do not
write anything themselves — the model still has to call `create_note`.

## Testing

`categories_test.go` covers seeding, create/rename/delete, validation,
assignment by id and name, note deletion cleanup, journal find-or-create,
journal recovery when the category is deleted, date ranges, category reads,
corrupt-file handling and normalisation of hand-edited state.

The GUI is not exercised by the test suite; `tsc` and the Vite build catch
type and bundling errors, but the Home layout and dialog were not
visually verified in this change.
