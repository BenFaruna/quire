package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP server exposes the same services the desktop UI uses, so an agent
// gets the real scheduling rules, draft generation and publish tracking
// instead of reimplementing them against the sidecar files.
//
// It runs as `post-planner mcp` over stdin/stdout. The GUI branch in main() is
// unaffected, which keeps the app and the server on one binary and one storage
// path.

const mcpServerInstructions = `This server provides access to the user's Markdown notes and content planner.

Guidelines:
- Call search_notes first to find a note id; almost every other tool takes an id.
- Prefer append_note over replace_note. append_note cannot clobber concurrent edits.
- When using replace_note, pass the modified timestamp from get_note so a stale write is rejected.
- Publishing is two-step: call publish_note with confirm=false to inspect the payload, then call it again with confirm=true to send it. Direct publishing currently exists only for dev.to and Hashnode; X and LinkedIn return copy-ready text.
- Scheduling uses RFC3339 timestamps (or Unix milliseconds). The planner stores UTC milliseconds.
- Never invent a note id: use the ids returned by search_notes or create_note.`

func runMCP() error {
	notes := NewNotesService()
	publish := NewPublishService(notes)
	planner := NewPlannerService(notes, publish)
	server := newMCPServer(notes, planner, publish)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// stdout belongs to the protocol; anything diagnostic must go to stderr.
	log.SetOutput(os.Stderr)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

type mcpTools struct {
	notes   *NotesService
	planner *PlannerService
	publish *PublishService
}

func newMCPServer(notes *NotesService, planner *PlannerService, publish *PublishService) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "post-planner", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: mcpServerInstructions},
	)
	tools := &mcpTools{notes: notes, planner: planner, publish: publish}
	tools.register(server)
	registerMCPResources(server, tools)
	registerMCPPrompts(server)
	return server
}

// --- tool input/output types ------------------------------------------------

type emptyInput struct{}

type searchNotesInput struct {
	Query string `json:"query" jsonschema:"case-insensitive text to match in titles and bodies; empty lists recent notes"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum results (default 20, max 100)"`
}

type noteListItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Preview  string `json:"preview"`
	Modified int64  `json:"modified" jsonschema:"Unix milliseconds"`
	Status   string `json:"status,omitempty" jsonschema:"derived planner status"`
}

type searchNotesOutput struct {
	Notes []noteListItem `json:"notes"`
}

type noteInput struct {
	ID string `json:"id" jsonschema:"note id from search_notes"`
}

type getNoteOutput struct {
	Note    Note         `json:"note"`
	Plan    PlanState    `json:"plan"`
	Publish PublishState `json:"publish"`
	Status  string       `json:"status"`
}

type createNoteInput struct {
	Title string `json:"title" jsonschema:"note title"`
	Body  string `json:"body" jsonschema:"Markdown body; Mermaid and LaTeX are supported"`
}

type appendNoteInput struct {
	ID       string `json:"id" jsonschema:"note id"`
	Markdown string `json:"markdown" jsonschema:"Markdown to append at the end of the note"`
}

type replaceNoteInput struct {
	ID               string `json:"id" jsonschema:"note id"`
	Title            string `json:"title,omitempty" jsonschema:"new title; empty keeps the current title"`
	Body             string `json:"body" jsonschema:"complete new Markdown body"`
	ExpectedModified int64  `json:"expected_modified,omitempty" jsonschema:"modified timestamp read from get_note; rejects the write if the note changed"`
}

type planInput struct {
	NoteID string `json:"note_id" jsonschema:"note id"`
}

type getPlanOutput struct {
	Plan   PlanState `json:"plan"`
	Status string    `json:"status"`
}

type listStatusesOutput struct {
	Statuses map[string]string `json:"statuses" jsonschema:"note id to derived status"`
}

type listBacklogInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum items; 0 means all"`
}

type listWindowInput struct {
	From string `json:"from,omitempty" jsonschema:"RFC3339 start (inclusive); empty means the beginning of time"`
	To   string `json:"to,omitempty" jsonschema:"RFC3339 end (exclusive); empty means no upper bound"`
}

type queueOutput struct {
	Items []QueueItem `json:"items"`
}

type setStageInput struct {
	NoteID string `json:"note_id" jsonschema:"note id"`
	Stage  string `json:"stage" jsonschema:"idea, drafting, ready, or empty to derive the stage"`
}

type scheduleInput struct {
	NoteID   string `json:"note_id" jsonschema:"note id"`
	Platform string `json:"platform" jsonschema:"x, linkedin, devto or hashnode"`
	At       string `json:"at" jsonschema:"when to post, as RFC3339 (e.g. 2026-11-02T09:00:00+01:00) or Unix milliseconds"`
}

type rescheduleInput struct {
	NoteID string `json:"note_id" jsonschema:"note id"`
	SlotID string `json:"slot_id" jsonschema:"slot id from get_plan"`
	At     string `json:"at" jsonschema:"new time as RFC3339 or Unix milliseconds"`
}

type slotInput struct {
	NoteID string `json:"note_id" jsonschema:"note id"`
	SlotID string `json:"slot_id" jsonschema:"slot id from get_plan"`
}

type markSlotInput struct {
	NoteID string `json:"note_id" jsonschema:"note id"`
	SlotID string `json:"slot_id" jsonschema:"slot id from get_plan"`
	Status string `json:"status" jsonschema:"done or skipped"`
}

type saveRenditionInput struct {
	NoteID    string   `json:"note_id" jsonschema:"note id"`
	SlotID    string   `json:"slot_id" jsonschema:"slot id from get_plan"`
	Title     string   `json:"title,omitempty"`
	Text      string   `json:"text,omitempty" jsonschema:"single-post text for social platforms"`
	Parts     []string `json:"parts,omitempty" jsonschema:"thread parts for X"`
	Body      string   `json:"body,omitempty" jsonschema:"Markdown body for blog platforms"`
	Tags      []string `json:"tags,omitempty"`
	Canonical string   `json:"canonical,omitempty"`
}

type prepareDraftInput struct {
	NoteID        string `json:"note_id" jsonschema:"note id"`
	Platform      string `json:"platform" jsonschema:"x, linkedin, devto or hashnode"`
	Mode          string `json:"mode,omitempty" jsonschema:"auto, thread or single (X only; default auto)"`
	NumberThreads bool   `json:"number_threads,omitempty" jsonschema:"prefix X thread parts with 1/5 style counters"`
	Canonical     string `json:"canonical,omitempty" jsonschema:"optional canonical URL appended to the draft"`
}

type publishInput struct {
	NoteID    string   `json:"note_id" jsonschema:"note id"`
	Platform  string   `json:"platform" jsonschema:"devto or hashnode for direct publishing; x or linkedin return copy-ready text"`
	Title     string   `json:"title,omitempty" jsonschema:"override title; empty uses the note title"`
	Body      string   `json:"body,omitempty" jsonschema:"override body; empty uses the note body"`
	Tags      []string `json:"tags,omitempty"`
	Canonical string   `json:"canonical,omitempty"`
	Draft     *bool    `json:"draft,omitempty" jsonschema:"true saves a remote draft; false publishes live. Defaults to true"`
	Confirm   bool     `json:"confirm" jsonschema:"must be true to send anything to a remote service"`
}

type publishOutput struct {
	Platform  string         `json:"platform"`
	Mode      string         `json:"mode" jsonschema:"dry-run, prepared, draft, published or updated"`
	Confirmed bool           `json:"confirmed"`
	Message   string         `json:"message"`
	Draft     *Draft         `json:"draft,omitempty"`
	Result    *PublishResult `json:"result,omitempty"`
}

// --- registration -----------------------------------------------------------

func (t *mcpTools) register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_notes",
		Description: "Search notes by title and body. Returns ids, previews and derived planner status. An empty query lists recent notes.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchNotesInput) (*mcp.CallToolResult, searchNotesOutput, error) {
		matches, err := t.notes.search(in.Query, in.Limit)
		if err != nil {
			return nil, searchNotesOutput{}, err
		}
		statuses, _ := t.planner.Statuses()
		items := make([]noteListItem, 0, len(matches))
		for _, note := range matches {
			items = append(items, noteListItem{
				ID:       note.ID,
				Title:    note.Title,
				Preview:  note.Preview,
				Modified: note.Modified,
				Status:   statuses[note.ID],
			})
		}
		return nil, searchNotesOutput{Notes: items}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_note",
		Description: "Read a note's Markdown body together with its planner and publish sidecars.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in noteInput) (*mcp.CallToolResult, getNoteOutput, error) {
		note, err := t.notes.Get(in.ID)
		if err != nil {
			return nil, getNoteOutput{}, err
		}
		plan, err := t.planner.Plan(in.ID)
		if err != nil {
			return nil, getNoteOutput{}, err
		}
		publish, err := t.publish.PublishState(in.ID)
		if err != nil {
			return nil, getNoteOutput{}, err
		}
		status := DerivedStatus(note.Body, plan, publish, time.Now().UnixMilli())
		return nil, getNoteOutput{Note: note, Plan: plan, Publish: publish, Status: status}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_note",
		Description: "Create a new note from Markdown. Returns the new note id.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createNoteInput) (*mcp.CallToolResult, Note, error) {
		note, err := t.notes.createTitled(in.Title, in.Body)
		return nil, note, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "append_note",
		Description: "Append Markdown to the end of an existing note. This is the safe way to add summaries, review comments or decisions.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in appendNoteInput) (*mcp.CallToolResult, Note, error) {
		note, err := t.notes.appendTo(in.ID, in.Markdown)
		return nil, note, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "replace_note",
		Description: "Replace a note's title and body. Pass expected_modified from get_note to avoid overwriting a newer edit; the write fails if the note changed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in replaceNoteInput) (*mcp.CallToolResult, Note, error) {
		note, err := t.notes.replaceIfUnchanged(in.ID, in.Title, in.Body, in.ExpectedModified)
		return nil, note, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_plan",
		Description: "Read a note's stage, slots and derived planner status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in planInput) (*mcp.CallToolResult, getPlanOutput, error) {
		plan, err := t.planner.Plan(in.NoteID)
		if err != nil {
			return nil, getPlanOutput{}, err
		}
		note, err := t.notes.Get(in.NoteID)
		if err != nil {
			return nil, getPlanOutput{}, err
		}
		publish, err := t.publish.PublishState(in.NoteID)
		if err != nil {
			return nil, getPlanOutput{}, err
		}
		return nil, getPlanOutput{
			Plan:   plan,
			Status: DerivedStatus(note.Body, plan, publish, time.Now().UnixMilli()),
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_statuses",
		Description: "Return the derived status (overdue, scheduled, ready, drafting, idea, posted, published) for every note.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in emptyInput) (*mcp.CallToolResult, listStatusesOutput, error) {
		statuses, err := t.planner.Statuses()
		if err != nil {
			return nil, listStatusesOutput{}, err
		}
		return nil, listStatusesOutput{Statuses: statuses}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_backlog",
		Description: "List unpublished notes that have no scheduled slot, newest first. Published and posted notes are excluded.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listBacklogInput) (*mcp.CallToolResult, []BacklogItem, error) {
		items, err := t.planner.Backlog(in.Limit)
		return nil, items, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_queue",
		Description: "List scheduled and overdue slots, oldest first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listWindowInput) (*mcp.CallToolResult, queueOutput, error) {
		from, to, err := parseWindow(in.From, in.To)
		if err != nil {
			return nil, queueOutput{}, err
		}
		items, err := t.planner.Queue(from, to)
		return nil, queueOutput{Items: items}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_history",
		Description: "List done and skipped slots, newest completion first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listWindowInput) (*mcp.CallToolResult, queueOutput, error) {
		from, to, err := parseWindow(in.From, in.To)
		if err != nil {
			return nil, queueOutput{}, err
		}
		items, err := t.planner.History(from, to)
		return nil, queueOutput{Items: items}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_stage",
		Description: "Set or clear a note's explicit stage (idea, drafting, ready).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in setStageInput) (*mcp.CallToolResult, PlanState, error) {
		plan, err := t.planner.SetStage(in.NoteID, in.Stage)
		return nil, plan, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "schedule_note",
		Description: "Schedule a note for a platform. Duplicate scheduled slots for the same platform collapse to the earliest one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in scheduleInput) (*mcp.CallToolResult, PlanState, error) {
		at, err := parseMCPTime(in.At)
		if err != nil {
			return nil, PlanState{}, err
		}
		plan, err := t.planner.Schedule(in.NoteID, in.Platform, at)
		return nil, plan, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "reschedule_slot",
		Description: "Move an existing scheduled slot, keeping its rendition.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in rescheduleInput) (*mcp.CallToolResult, PlanState, error) {
		at, err := parseMCPTime(in.At)
		if err != nil {
			return nil, PlanState{}, err
		}
		plan, err := t.planner.Reschedule(in.NoteID, in.SlotID, at)
		return nil, plan, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "unschedule_note",
		Description: "Remove a scheduled slot. Slots with no rendition or notes are deleted; slots with content are marked skipped so they appear in history.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in slotInput) (*mcp.CallToolResult, PlanState, error) {
		plan, err := t.planner.Unschedule(in.NoteID, in.SlotID)
		return nil, plan, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "mark_slot",
		Description: "Mark a scheduled slot done or skipped. A done slot with no remaining scheduled slot makes the note posted.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in markSlotInput) (*mcp.CallToolResult, PlanState, error) {
		plan, err := t.planner.MarkSlot(in.NoteID, in.SlotID, in.Status)
		return nil, plan, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "save_rendition",
		Description: "Save edited post content for a slot. The source hash and edit time are filled in automatically.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in saveRenditionInput) (*mcp.CallToolResult, PlanState, error) {
		plan, err := t.planner.SaveRendition(in.NoteID, in.SlotID, Rendition{
			Title:     in.Title,
			Text:      in.Text,
			Parts:     in.Parts,
			Body:      in.Body,
			Tags:      in.Tags,
			Canonical: in.Canonical,
		})
		return nil, plan, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "prepare_draft",
		Description: "Turn a note into a platform-ready draft (thread splitting, character limits, blog tags) without publishing.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prepareDraftInput) (*mcp.CallToolResult, Draft, error) {
		note, err := t.notes.Get(in.NoteID)
		if err != nil {
			return nil, Draft{}, err
		}
		draft, err := t.publish.Prepare(note.Title, note.Body, in.Platform, PrepareOptions{
			Mode:          in.Mode,
			NumberThreads: in.NumberThreads,
			Canonical:     in.Canonical,
		})
		return nil, draft, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_publish_state",
		Description: "Read the publish tracking sidecar for a note.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in planInput) (*mcp.CallToolResult, PublishState, error) {
		state, err := t.publish.PublishState(in.NoteID)
		return nil, state, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_connections",
		Description: "List direct-publishing accounts (dev.to, Hashnode) and whether they are connected.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in emptyInput) (*mcp.CallToolResult, []ProviderStatus, error) {
		statuses, err := t.publish.Connections()
		return nil, statuses, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "publish_note",
		Description: "Two-step publish. With confirm=false nothing is sent: the prepared payload is returned for review. " +
			"With confirm=true the post is created (draft by default; draft=false publishes live). " +
			"X and LinkedIn have no API integration, so they always return copy-ready text.",
	}, t.handlePublish)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_published",
		Description: "Update a remote post that was previously published or drafted. Requires confirm=true; nothing is sent otherwise.",
	}, t.handleUpdatePublished)
}

func (t *mcpTools) handlePublish(ctx context.Context, req *mcp.CallToolRequest, in publishInput) (*mcp.CallToolResult, publishOutput, error) {
	platform := normalizePublishPlatform(in.Platform)
	note, err := t.notes.Get(in.NoteID)
	if err != nil {
		return nil, publishOutput{}, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = note.Title
	}
	body := in.Body
	if strings.TrimSpace(body) == "" {
		body = note.Body
	}

	// Social targets are intentionally copy-and-post only: there is no remote
	// provider for them, so pretending to publish would be misleading.
	if platform == platformX || platform == platformLinkedIn {
		draft, err := t.publish.Prepare(title, body, platform, PrepareOptions{Canonical: in.Canonical})
		if err != nil {
			return nil, publishOutput{}, err
		}
		return nil, publishOutput{
			Platform: platform,
			Mode:     "prepared",
			Message:  "This platform has no direct publishing API. Copy the draft text and post it from the app or the platform itself.",
			Draft:    &draft,
		}, nil
	}

	draft, err := t.publish.Prepare(title, body, platform, PrepareOptions{Canonical: in.Canonical})
	if err != nil {
		return nil, publishOutput{}, err
	}
	if !in.Confirm {
		return nil, publishOutput{
			Platform: platform,
			Mode:     "dry-run",
			Message:  "Nothing was sent. Review the draft, then call publish_note again with confirm=true.",
			Draft:    &draft,
		}, nil
	}

	state, err := t.publish.PublishState(in.NoteID)
	if err != nil {
		return nil, publishOutput{}, err
	}
	if target, exists := state.Targets[platform]; exists && target.RemoteID != "" {
		return nil, publishOutput{}, fmt.Errorf(
			"this note already has a %s post (%s); use update_published instead",
			platformLabel(platform), target.URL,
		)
	}

	asDraft := true
	if in.Draft != nil {
		asDraft = *in.Draft
	}
	result, err := t.publish.Publish(platform, PublishRequest{
		NoteID:    in.NoteID,
		Title:     title,
		Body:      body,
		Tags:      in.Tags,
		Canonical: in.Canonical,
		Draft:     asDraft,
	})
	if err != nil {
		return nil, publishOutput{}, err
	}
	mode := "published"
	if asDraft {
		mode = "draft"
	}
	return nil, publishOutput{
		Platform:  platform,
		Mode:      mode,
		Confirmed: true,
		Message:   fmt.Sprintf("Created %s %s at %s", platformLabel(platform), mode, result.URL),
		Result:    &result,
	}, nil
}

func (t *mcpTools) handleUpdatePublished(ctx context.Context, req *mcp.CallToolRequest, in publishInput) (*mcp.CallToolResult, publishOutput, error) {
	platform := normalizePublishPlatform(in.Platform)
	note, err := t.notes.Get(in.NoteID)
	if err != nil {
		return nil, publishOutput{}, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = note.Title
	}
	body := in.Body
	if strings.TrimSpace(body) == "" {
		body = note.Body
	}

	state, err := t.publish.PublishState(in.NoteID)
	if err != nil {
		return nil, publishOutput{}, err
	}
	if target, exists := state.Targets[platform]; !exists || target.RemoteID == "" {
		return nil, publishOutput{}, fmt.Errorf("this note has not been published to %s", platformLabel(platform))
	}
	if !in.Confirm {
		return nil, publishOutput{
			Platform: platform,
			Mode:     "dry-run",
			Message:  "Nothing was sent. Call update_published again with confirm=true to update the remote post.",
		}, nil
	}

	asDraft := false // an existing published post stays published
	if in.Draft != nil {
		asDraft = *in.Draft
	}
	result, err := t.publish.UpdatePublished(platform, PublishRequest{
		NoteID:    in.NoteID,
		Title:     title,
		Body:      body,
		Tags:      in.Tags,
		Canonical: in.Canonical,
		Draft:     asDraft,
	})
	if err != nil {
		return nil, publishOutput{}, err
	}
	return nil, publishOutput{
		Platform:  platform,
		Mode:      "updated",
		Confirmed: true,
		Message:   fmt.Sprintf("Updated %s post at %s", platformLabel(platform), result.URL),
		Result:    &result,
	}, nil
}

// --- resources --------------------------------------------------------------

func registerMCPResources(server *mcp.Server, tools *mcpTools) {
	server.AddResource(&mcp.Resource{
		URI:         "notes://index",
		Name:        "notes index",
		Description: "JSON list of every note with id, title, preview, modified time and derived status.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		matches, err := tools.notes.search("", 100)
		if err != nil {
			return nil, err
		}
		statuses, _ := tools.planner.Statuses()
		items := make([]noteListItem, 0, len(matches))
		for _, note := range matches {
			items = append(items, noteListItem{
				ID:       note.ID,
				Title:    note.Title,
				Preview:  note.Preview,
				Modified: note.Modified,
				Status:   statuses[note.ID],
			})
		}
		raw, err := json.MarshalIndent(searchNotesOutput{Notes: items}, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      "notes://index",
			MIMEType: "application/json",
			Text:     string(raw),
		}}}, nil
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "notes://{id}",
		Name:        "note",
		Description: "A note's Markdown document.",
		MIMEType:    "text/markdown",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id := strings.TrimPrefix(req.Params.URI, "notes://")
		note, err := tools.notes.Get(id)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "text/markdown",
			Text:     composeDocument(note.Title, note.Body),
		}}}, nil
	})

	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "plan://{id}",
		Name:        "note plan",
		Description: "A note's planner sidecar as JSON.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id := strings.TrimPrefix(req.Params.URI, "plan://")
		plan, err := tools.planner.Plan(id)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		raw, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(raw),
		}}}, nil
	})
}

// --- prompts ----------------------------------------------------------------

func registerMCPPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "review_note",
		Description: "Review a note for clarity, structure, accuracy and missing detail.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "note_id",
			Description: "id of the note to review",
			Required:    true,
		}},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		id := req.Params.Arguments["note_id"]
		return &mcp.GetPromptResult{
			Description: "Review a note",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: reviewNotePrompt(id)},
			}},
		}, nil
	})

	server.AddPrompt(&mcp.Prompt{
		Name:        "draft_post",
		Description: "Turn a note into a platform-ready post and review it before publishing.",
		Arguments: []*mcp.PromptArgument{
			{Name: "note_id", Description: "id of the note to turn into a post", Required: true},
			{Name: "platform", Description: "x, linkedin, devto or hashnode", Required: true},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		id := req.Params.Arguments["note_id"]
		platform := req.Params.Arguments["platform"]
		return &mcp.GetPromptResult{
			Description: "Draft a post",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: draftPostPrompt(id, platform)},
			}},
		}, nil
	})
}

func reviewNotePrompt(id string) string {
	return fmt.Sprintf(`Review note %q using get_note. Focus on:
1. Structure and flow.
2. Unclear or unsupported claims.
3. Missing context a reader would need.
4. Concrete edits, written as a short list.
Do not rewrite the note unless asked. If an edit is clearly useful, offer the exact replacement text.`, id)
}

func draftPostPrompt(id, platform string) string {
	return fmt.Sprintf(`Use prepare_draft on note %q for %s, then improve the draft:
- Keep the platform's character limit and formatting conventions.
- Preserve the user's voice; do not invent facts.
- Return the final copy-ready text, plus a one-line note about anything you changed.
Do not publish. If the user later confirms, call publish_note with confirm=true.`, id, platform)
}

// --- helpers ----------------------------------------------------------------

func parseWindow(from, to string) (int64, int64, error) {
	start, err := parseMCPTime(from)
	if err != nil {
		return 0, 0, fmt.Errorf("from: %w", err)
	}
	end, err := parseMCPTime(to)
	if err != nil {
		return 0, 0, fmt.Errorf("to: %w", err)
	}
	return start, end, nil
}

// parseMCPTime accepts RFC3339 or Unix milliseconds. Agents get a clear error
// rather than silently scheduling at epoch zero.
func parseMCPTime(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
		if ms <= 0 {
			return 0, errors.New("time must be positive")
		}
		return ms, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, fmt.Errorf("use RFC3339 (2026-11-02T09:00:00+01:00) or Unix milliseconds: %w", err)
	}
	return parsed.UnixMilli(), nil
}
