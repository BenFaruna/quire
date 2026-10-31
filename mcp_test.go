package main

import (
	"context"
	"encoding/json"
	"strings"
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
	ctx := context.Background()
	server := newMCPServer(notes, planner, publish)
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
