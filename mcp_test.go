package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestNotes(t *testing.T) (*NotesService, *PlannerService, *PublishService) {
	t.Helper()
	notes := &NotesService{root: t.TempDir()}
	publish := NewPublishService(notes)
	return notes, NewPlannerService(notes, publish), publish
}

func connectTestMCP(t *testing.T, notes *NotesService, planner *PlannerService, publish *PublishService) *mcp.ClientSession {
	t.Helper()
	return connectTestMCPWithLogger(t, notes, planner, publish, nil)
}

func connectTestMCPWithLogger(t *testing.T, notes *NotesService, planner *PlannerService, publish *PublishService, logger *slog.Logger) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := newMCPServer(notes, planner, publish, logger)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func callTool[T any](t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (T, *mcp.CallToolResult) {
	t.Helper()
	var zero T
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.IsError {
		return zero, result
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("%s: marshal structured content: %v", name, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: unmarshal structured content: %v\n%s", name, err, raw)
	}
	return out, result
}

func TestMCPCreateSearchAppend(t *testing.T) {
	notes, planner, publish := newTestNotes(t)
	session := connectTestMCP(t, notes, planner, publish)

	created, _ := callTool[Note](t, session, "create_note", map[string]any{
		"title": "Launch plan",
		"body":  "Ship the new planner.",
	})
	if created.ID == "" {
		t.Fatal("create_note returned no id")
	}

	found, _ := callTool[searchNotesOutput](t, session, "search_notes", map[string]any{"query": "launch"})
	if len(found.Notes) != 1 || found.Notes[0].ID != created.ID {
		t.Fatalf("search_notes = %+v", found.Notes)
	}

	appended, _ := callTool[Note](t, session, "append_note", map[string]any{
		"id":       created.ID,
		"markdown": "## Review\n\nLooks good.",
	})
	if appended.Body == "" || !strings.Contains(appended.Body, "Ship the new planner.") || !strings.Contains(appended.Body, "Looks good.") {
		t.Fatalf("append_note body = %q", appended.Body)
	}

	// A stale expected timestamp must be rejected rather than overwriting.
	_, stale := callTool[Note](t, session, "replace_note", map[string]any{
		"id":                created.ID,
		"title":             "Clobbered",
		"body":              "nope",
		"expected_modified": 1,
	})
	if !stale.IsError {
		t.Fatal("replace_note with a stale expected_modified succeeded")
	}

	current, _ := callTool[getNoteOutput](t, session, "get_note", map[string]any{"id": created.ID})
	replaced, _ := callTool[Note](t, session, "replace_note", map[string]any{
		"id":                created.ID,
		"title":             "Launch plan v2",
		"body":              "Updated body.",
		"expected_modified": current.Note.Modified,
	})
	if replaced.Title != "Launch plan v2" || replaced.Body != "Updated body." {
		t.Fatalf("replace_note = %+v", replaced)
	}
}

func TestMCPPlanTools(t *testing.T) {
	notes, planner, publish := newTestNotes(t)
	session := connectTestMCP(t, notes, planner, publish)

	created, _ := callTool[Note](t, session, "create_note", map[string]any{
		"title": "Scheduled post",
		"body":  "Body",
	})
	other, _ := callTool[Note](t, session, "create_note", map[string]any{
		"title": "Backlog post",
		"body":  "Body",
	})

	plan, _ := callTool[PlanState](t, session, "schedule_note", map[string]any{
		"note_id":  created.ID,
		"platform": "devto",
		"at":       "2030-01-02T09:00:00Z",
	})
	if len(plan.Slots) != 1 || plan.Slots[0].ScheduledAt != 1893574800000 {
		t.Fatalf("schedule_note slots = %+v", plan.Slots)
	}

	got, _ := callTool[getPlanOutput](t, session, "get_plan", map[string]any{"note_id": created.ID})
	if got.Status != statusScheduled {
		t.Fatalf("status = %q, want %q", got.Status, statusScheduled)
	}

	backlog, _ := callTool[[]BacklogItem](t, session, "list_backlog", map[string]any{})
	if len(backlog) != 1 || backlog[0].NoteID != other.ID {
		t.Fatalf("backlog = %+v", backlog)
	}

	queue, _ := callTool[queueOutput](t, session, "list_queue", map[string]any{})
	if len(queue.Items) != 1 || queue.Items[0].NoteID != created.ID {
		t.Fatalf("queue = %+v", queue.Items)
	}

	marked, _ := callTool[PlanState](t, session, "mark_slot", map[string]any{
		"note_id": created.ID,
		"slot_id": plan.Slots[0].ID,
		"status":  "done",
	})
	if marked.Slots[0].Status != slotDone {
		t.Fatalf("mark_slot = %+v", marked.Slots)
	}

	posted, _ := callTool[getPlanOutput](t, session, "get_plan", map[string]any{"note_id": created.ID})
	if posted.Status != statusPosted {
		t.Fatalf("status after done = %q, want %q", posted.Status, statusPosted)
	}
}

func TestMCPPublishRequiresConfirm(t *testing.T) {
	notes, planner, publish := newTestNotes(t)
	session := connectTestMCP(t, notes, planner, publish)

	created, _ := callTool[Note](t, session, "create_note", map[string]any{
		"title": "Blog post",
		"body":  "Hello world",
	})

	out, _ := callTool[publishOutput](t, session, "publish_note", map[string]any{
		"note_id":  created.ID,
		"platform": "devto",
		"confirm":  false,
	})
	if out.Mode != "dry-run" || out.Draft == nil {
		t.Fatalf("dry run = %+v", out)
	}
	if out.Confirmed {
		t.Fatal("dry run reported confirmed")
	}

	// X has no direct publishing provider: the tool returns copy-ready text.
	social, _ := callTool[publishOutput](t, session, "publish_note", map[string]any{
		"note_id":  created.ID,
		"platform": "x",
		"confirm":  true,
	})
	if social.Mode != "prepared" || social.Draft == nil {
		t.Fatalf("x publish = %+v", social)
	}
}

// syncBuffer collects log output from handlers that may run on another
// goroutine while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMCPLogsRequests(t *testing.T) {
	notes, planner, publish := newTestNotes(t)
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	session := connectTestMCPWithLogger(t, notes, planner, publish, logger)

	callTool[searchNotesOutput](t, session, "search_notes", map[string]any{"query": "anything"})

	output := logs.String()
	if !strings.Contains(output, "method=tools/call") || !strings.Contains(output, "tool=search_notes") {
		t.Fatalf("logs did not record the tool call:\n%s", output)
	}
	// The request line must carry the duration so a slow call is obvious.
	if !strings.Contains(output, "duration=") {
		t.Fatalf("logs did not record request durations:\n%s", output)
	}
}

func TestMCPResources(t *testing.T) {
	notes, planner, publish := newTestNotes(t)
	session := connectTestMCP(t, notes, planner, publish)

	created, _ := callTool[Note](t, session, "create_note", map[string]any{
		"title": "Resource note",
		"body":  "Body text",
	})

	res, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "notes://" + created.ID,
	})
	if err != nil {
		t.Fatalf("read note resource: %v", err)
	}
	if len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, "Resource note") || !strings.Contains(res.Contents[0].Text, "Body text") {
		t.Fatalf("resource contents = %+v", res.Contents)
	}

	if _, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{
		URI: "notes://missing-note",
	}); err == nil {
		t.Fatal("missing note resource did not error")
	}
}

func TestMCPCategoryAndJournalTools(t *testing.T) {
	notes, planner, publish := newTestNotes(t)
	session := connectTestMCP(t, notes, planner, publish)

	category, _ := callTool[Category](t, session, "create_category", map[string]any{
		"name":        "Client research",
		"description": "Calls, constraints and follow-ups",
	})
	if category.ID == "" {
		t.Fatal("create_category returned no id")
	}

	created, _ := callTool[Note](t, session, "create_note", map[string]any{
		"title":    "Call notes",
		"body":     "Decision: ship in November.",
		"category": "client research",
	})
	if created.CategoryID != category.ID {
		t.Fatalf("note category = %q, want %q", created.CategoryID, category.ID)
	}

	listed, _ := callTool[listCategoriesOutput](t, session, "list_categories", nil)
	if len(listed.Categories) != 1 || listed.Categories[0].Count != 1 {
		t.Fatalf("list_categories = %+v", listed.Categories)
	}

	read, _ := callTool[categoryNotesOutput](t, session, "list_category_notes", map[string]any{
		"category": "Client research",
	})
	if len(read.Notes) != 1 || read.Notes[0].ID != created.ID || !strings.Contains(read.Notes[0].Body, "November") {
		t.Fatalf("list_category_notes = %+v", read.Notes)
	}

	moved, _ := callTool[Note](t, session, "set_note_category", map[string]any{
		"note_id":  created.ID,
		"category": "",
	})
	if moved.CategoryID != "" {
		t.Fatalf("set_note_category did not clear the category: %q", moved.CategoryID)
	}

	entry, _ := callTool[Note](t, session, "get_journal_entry", map[string]any{"date": "2026-11-08"})
	if entry.Title != "2026-11-08" || entry.CategoryID == "" {
		t.Fatalf("get_journal_entry = %+v", entry)
	}
	appended, _ := callTool[Note](t, session, "append_journal", map[string]any{
		"date":     "2026-11-08",
		"markdown": "- Shipped the category feature.",
	})
	if !strings.Contains(appended.Body, "Shipped the category feature") {
		t.Fatalf("append_journal body = %q", appended.Body)
	}

	journal, _ := callTool[journalListOutput](t, session, "list_journal", map[string]any{
		"from": "2026-11-01",
		"to":   "2026-11-30",
	})
	if len(journal.Entries) != 1 || journal.Entries[0].ID != entry.ID {
		t.Fatalf("list_journal = %+v", journal.Entries)
	}

	// A duplicate name must fail rather than silently creating a second bucket.
	_, duplicate := callTool[Category](t, session, "create_category", map[string]any{"name": "Client research"})
	if !duplicate.IsError {
		t.Fatal("duplicate category name was accepted")
	}
}
