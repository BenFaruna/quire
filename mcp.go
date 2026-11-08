package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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
- Never invent a note id: use the ids returned by search_notes or create_note.
- Notes can be filed in user categories. Call list_categories first: each description states what the user wants in that category, so follow it. list_category_notes returns a category's notes with full bodies and is the source material for a summary. create_note and set_note_category file a note.
- Daily logs are notes titled YYYY-MM-DD in the Daily log category. Use get_journal_entry and append_journal to log a day, and list_journal plus create_note to write a track record.`

const mcpVersion = "1.0.0"

func runMCP() error {
	logger, logPath, logFile := newMCPLogger()
	if logFile != nil {
		defer logFile.Close()
	}

	notes := NewNotesService()
	publish := NewPublishService(notes)
	planner := NewPlannerService(notes, publish)

	// Resolve the notes directory before the handshake. Clients report a
	// failed connection as a generic error, so an unusable storage directory
	// must be logged here rather than surface later as an empty tool list.
	if err := notes.init(); err != nil {
		logger.Error("mcp: notes directory unavailable", "error", err)
		return err
	}

	executable, _ := os.Executable()
	cwd, _ := os.Getwd()
	logger.Info("mcp: server starting",
		"version", mcpVersion,
		"pid", os.Getpid(),
		"executable", executable,
		"args", strings.Join(os.Args, " "),
		"cwd", cwd,
		"notes", notes.root,
		"log_file", logPath,
	)

	server := newMCPServer(notes, planner, publish, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("mcp: server exited with error", "error", err)
		return err
	}
	logger.Info("mcp: server stopped")
	return nil
}

// --- logging ----------------------------------------------------------------

// stdout carries protocol frames, so diagnostics go to stderr. Many GUI MCP
// clients hide or discard their child's stderr, which is why the same lines are
// also appended to a log file: a connection that fails still leaves a trace.
const (
	// mcpLogEnv moves the log file (or disables it with "off").
	mcpLogEnv = "POST_PLANNER_MCP_LOG"
	// mcpLogMaxBytes caps the file; it is rotated to a single .1 backup.
	mcpLogMaxBytes = 1 << 20
)

// newMCPLogger returns a logger writing to stderr and, when possible, to the
// log file. The file is best-effort: losing it must not stop the server.
func newMCPLogger() (*slog.Logger, string, io.Closer) {
	writers := []io.Writer{os.Stderr}
	logPath := mcpLogPath()
	var logFile *os.File
	if logPath != "" {
		file, err := openMCPLogFile(logPath)
		if err != nil {
			log.Printf("mcp: log file %s unavailable, using stderr only: %v", logPath, err)
			logPath = ""
		} else {
			writers = append(writers, file)
			logFile = file
		}
	}

	out := io.MultiWriter(writers...)
	// Planner, watcher and reminder code log through the stdlib logger.
	log.SetOutput(out)
	logger := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if logFile == nil {
		return logger, logPath, nil
	}
	return logger, logPath, logFile
}

// mcpLogPath honours POST_PLANNER_MCP_LOG, then falls back to the app config
// directory next to the notes.
func mcpLogPath() string {
	if value, ok := os.LookupEnv(mcpLogEnv); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "off", "none", "stderr":
			return ""
		default:
			return value
		}
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "post-planner", "mcp.log")
}

func openMCPLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > mcpLogMaxBytes {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// mcpRequestLogger records every protocol method the server handles, with its
// duration and outcome. Arguments and results are deliberately not logged: they
// carry note bodies and draft content.
func mcpRequestLogger(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			start := time.Now()
			result, err := next(ctx, method, req)

			attrs := []any{
				"method", method,
				"duration", time.Since(start).Round(time.Microsecond).String(),
			}
			if req != nil {
				// The server sees raw params for tools/call; clients see typed ones.
				switch params := req.GetParams().(type) {
				case *mcp.CallToolParamsRaw:
					if params.Name != "" {
						attrs = append(attrs, "tool", params.Name)
					}
				case *mcp.CallToolParams:
					if params.Name != "" {
						attrs = append(attrs, "tool", params.Name)
					}
				}
			}

			switch {
			case err != nil:
				logger.Warn("mcp: request failed", append(attrs, "error", err)...)
			case isToolError(result):
				// Tool errors come back as a result, not as a returned error.
				logger.Warn("mcp: tool returned an error", attrs...)
			default:
				logger.Info("mcp: request", attrs...)
			}
			return result, err
		}
	}
}

func isToolError(result mcp.Result) bool {
	res, ok := result.(*mcp.CallToolResult)
	return ok && res.IsError
}

type mcpTools struct {
	notes   *NotesService
	planner *PlannerService
	publish *PublishService
}

func newMCPServer(notes *NotesService, planner *PlannerService, publish *PublishService, logger *slog.Logger) *mcp.Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "post-planner", Version: mcpVersion},
		&mcp.ServerOptions{Instructions: mcpServerInstructions, Logger: logger},
	)
	// The SDK logs session connect/end events through this logger, so a client
	// that fails during the handshake is visible in the same file.
	server.AddReceivingMiddleware(mcpRequestLogger(logger))
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
	ID         string `json:"id"`
	Title      string `json:"title"`
	Preview    string `json:"preview"`
	Modified   int64  `json:"modified" jsonschema:"Unix milliseconds"`
	Status     string `json:"status,omitempty" jsonschema:"derived planner status"`
	CategoryID string `json:"category_id,omitempty" jsonschema:"category the note is filed in, if any"`
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
	Title    string `json:"title" jsonschema:"note title"`
	Body     string `json:"body" jsonschema:"Markdown body; Mermaid and LaTeX are supported"`
	Category string `json:"category,omitempty" jsonschema:"category id or name from list_categories; empty leaves the note uncategorised"`
}

type categoryListItem struct {
	Category
	Count int `json:"count" jsonschema:"notes filed in this category"`
}

type listCategoriesOutput struct {
	Categories []categoryListItem `json:"categories"`
}

type createCategoryInput struct {
	Name        string `json:"name" jsonschema:"short category name; must be unique"`
	Description string `json:"description,omitempty" jsonschema:"what the category is for; a model reads this before writing notes into it"`
}

type categoryNotesInput struct {
	Category string `json:"category" jsonschema:"category id or name from list_categories; empty lists uncategorised notes"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum notes to return with full bodies (default 50, max 200)"`
}

type categoryNotesOutput struct {
	Category Category `json:"category"`
	Notes    []Note   `json:"notes"`
}

type setNoteCategoryInput struct {
	NoteID   string `json:"note_id" jsonschema:"note id"`
	Category string `json:"category" jsonschema:"category id or name; empty removes the note from its category"`
}

type journalInput struct {
	Date string `json:"date,omitempty" jsonschema:"calendar day in YYYY-MM-DD; empty means today"`
}

type appendJournalInput struct {
	Date     string `json:"date,omitempty" jsonschema:"calendar day in YYYY-MM-DD; empty means today"`
	Markdown string `json:"markdown" jsonschema:"Markdown to append to the day's entry"`
}

type journalRangeInput struct {
	From string `json:"from,omitempty" jsonschema:"first day, YYYY-MM-DD (inclusive); empty means no lower bound"`
	To   string `json:"to,omitempty" jsonschema:"last day, YYYY-MM-DD (inclusive); empty means no upper bound"`
}

type journalListOutput struct {
	Category Category `json:"category"`
	Entries  []Note   `json:"entries"`
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
				ID:         note.ID,
				Title:      note.Title,
				Preview:    note.Preview,
				Modified:   note.Modified,
				Status:     statuses[note.ID],
				CategoryID: note.CategoryID,
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
		Description: "Create a new note from Markdown, optionally filed in a category. Returns the new note.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createNoteInput) (*mcp.CallToolResult, Note, error) {
		note, err := t.notes.createTitledIn(in.Title, in.Body, in.Category)
		return nil, note, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_categories",
		Description: "List the user's categories with their purpose descriptions and note counts. Read this before filing notes or summarising a category.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in emptyInput) (*mcp.CallToolResult, listCategoriesOutput, error) {
		items, err := t.categoryItems()
		return nil, listCategoriesOutput{Categories: items}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_category",
		Description: "Create a category. The description should state what belongs there: it is how the user tells agents what to write into the category.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createCategoryInput) (*mcp.CallToolResult, Category, error) {
		category, err := t.notes.CreateCategory(in.Name, in.Description)
		return nil, category, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_category_notes",
		Description: "Read the notes filed in a category, with full Markdown bodies, newest first. This is the source material for a summary.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in categoryNotesInput) (*mcp.CallToolResult, categoryNotesOutput, error) {
		category, notes, err := t.notes.categoryNotes(in.Category, in.Limit)
		return nil, categoryNotesOutput{Category: category, Notes: notes}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_note_category",
		Description: "File an existing note in a category, or remove it from its category with an empty name.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in setNoteCategoryInput) (*mcp.CallToolResult, Note, error) {
		category, err := t.notes.resolveCategoryID(in.Category)
		if err != nil {
			return nil, Note{}, err
		}
		note, err := t.notes.AssignCategory(in.NoteID, category.ID)
		return nil, note, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_journal_entry",
		Description: "Return the daily log entry for a date, creating it from a template when it does not exist yet. An empty date means today.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in journalInput) (*mcp.CallToolResult, Note, error) {
		note, err := t.notes.JournalEntry(in.Date)
		return nil, note, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "append_journal",
		Description: "Append Markdown to a day's log entry, creating the entry if needed. Use it to record what happened without rewriting the page.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in appendJournalInput) (*mcp.CallToolResult, Note, error) {
		entry, err := t.notes.JournalEntry(in.Date)
		if err != nil {
			return nil, Note{}, err
		}
		note, err := t.notes.appendTo(entry.ID, in.Markdown)
		return nil, note, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_journal",
		Description: "List daily log entries in an inclusive YYYY-MM-DD range, newest first. Use it to build a track record over a week or month.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in journalRangeInput) (*mcp.CallToolResult, journalListOutput, error) {
		category, entries, err := t.notes.journalNotes(in.From, in.To)
		return nil, journalListOutput{Category: category, Entries: entries}, err
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

// categoryItems pairs each category with its note count for the tools and the
// categories://index resource.
func (t *mcpTools) categoryItems() ([]categoryListItem, error) {
	categories, err := t.notes.Categories()
	if err != nil {
		return nil, err
	}
	summaries, err := t.notes.List()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(categories))
	for _, note := range summaries {
		if note.CategoryID != "" {
			counts[note.CategoryID]++
		}
	}
	items := make([]categoryListItem, 0, len(categories))
	for _, category := range categories {
		items = append(items, categoryListItem{Category: category, Count: counts[category.ID]})
	}
	return items, nil
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
				ID:         note.ID,
				Title:      note.Title,
				Preview:    note.Preview,
				Modified:   note.Modified,
				Status:     statuses[note.ID],
				CategoryID: note.CategoryID,
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

	server.AddResource(&mcp.Resource{
		URI:         "categories://index",
		Name:        "categories index",
		Description: "JSON list of categories with their purpose descriptions and note counts.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		items, err := tools.categoryItems()
		if err != nil {
			return nil, err
		}
		raw, err := json.MarshalIndent(listCategoriesOutput{Categories: items}, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      "categories://index",
			MIMEType: "application/json",
			Text:     string(raw),
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

	server.AddPrompt(&mcp.Prompt{
		Name:        "summarize_category",
		Description: "Read every note in a category and write a summary back into the notes.",
		Arguments: []*mcp.PromptArgument{
			{Name: "category", Description: "category id or name from list_categories", Required: true},
			{Name: "focus", Description: "optional angle for the summary, e.g. open decisions", Required: false},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		category := req.Params.Arguments["category"]
		focus := req.Params.Arguments["focus"]
		return &mcp.GetPromptResult{
			Description: "Summarise a category",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: summarizeCategoryPrompt(category, focus)},
			}},
		}, nil
	})

	server.AddPrompt(&mcp.Prompt{
		Name:        "track_record",
		Description: "Turn a run of daily log entries into a track record note.",
		Arguments: []*mcp.PromptArgument{
			{Name: "from", Description: "first day, YYYY-MM-DD; empty means the last seven days", Required: false},
			{Name: "to", Description: "last day, YYYY-MM-DD; empty means today", Required: false},
		},
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		from := req.Params.Arguments["from"]
		to := req.Params.Arguments["to"]
		return &mcp.GetPromptResult{
			Description: "Write a track record",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: trackRecordPrompt(from, to)},
			}},
		}, nil
	})
}

func summarizeCategoryPrompt(category, focus string) string {
	category = strings.TrimSpace(category)
	focus = strings.TrimSpace(focus)
	if focus == "" {
		focus = "the main themes, decisions, open questions and anything that has changed"
	}
	return fmt.Sprintf(`Summarise the category %q for the user.
1. Call list_categories, then list_category_notes for %q to read the source notes.
2. Cover %s.
3. Refer to notes by title; do not dump their text back.
4. Save the summary with create_note in the same category (or a summary category the user names) and say which note you created.
Do not modify or delete the source notes.`, category, category, focus)
}

func trackRecordPrompt(from, to string) string {
	period := "the last seven days"
	if strings.TrimSpace(from) != "" || strings.TrimSpace(to) != "" {
		period = strings.TrimSpace(from + " to " + to)
	}
	return fmt.Sprintf(`Build a track record from the user's daily log entries for %s.
1. Call list_journal with from=%q and to=%q.
2. Summarise progress, recurring themes, decisions and blockers, and what moved between the first and last entry.
3. Write it with create_note in the Track record category, titled "Track record: <period>".
4. List the entries you used. Do not invent events that are not in the logs.`, period, from, to)
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
