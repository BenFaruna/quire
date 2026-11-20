package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PlannerService owns scheduling intent and done/skipped history. It keeps
// that state in <id>.plan.json sidecars next to the notes, so the notes
// directory remains the single source of truth.
type PlannerService struct {
	notes   *NotesService
	publish *PublishService // used only by SlotText's Prepare fallback
	mu      sync.Mutex      // serialises read-modify-write sidecar updates
	now     func() time.Time
}

func NewPlannerService(notes *NotesService, publish *PublishService) *PlannerService {
	return &PlannerService{
		notes:   notes,
		publish: publish,
		now:     time.Now,
	}
}

// QueueItem is one slot as the queue or history renders it.
type QueueItem struct {
	NoteID      string `json:"noteId"`
	SlotID      string `json:"slotId"`
	Title       string `json:"title"`
	Platform    string `json:"platform"`
	Status      string `json:"status"` // queue: scheduled|overdue; history: done|skipped
	ScheduledAt int64  `json:"scheduledAt"`
	CompletedAt int64  `json:"completedAt,omitempty"`
	Preview     string `json:"preview"`
	Stale       bool   `json:"stale"`
}

// BacklogItem is an unpublished note with no scheduled slot.
type BacklogItem struct {
	NoteID   string `json:"noteId"`
	Title    string `json:"title"`
	Status   string `json:"status"` // derived status
	Modified int64  `json:"modified"`
}

// SlotText is the content a reminder copies or a publish dialog prefills.
type SlotText struct {
	Title     string   `json:"title"`
	Text      string   `json:"text,omitempty"`
	Parts     []string `json:"parts,omitempty"`
	Body      string   `json:"body,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Canonical string   `json:"canonical,omitempty"`
}

// scannedNote is one note plus its sidecars, read during a directory scan.
type scannedNote struct {
	id       string
	title    string
	body     string
	modified int64
	plan     PlanState
	publish  PublishState
}

// DerivedStatus returns the single label the sidebar groups a note under.
// Rules are evaluated top to bottom; the first match wins.
func DerivedStatus(body string, state PlanState, publish PublishState, now int64) string {
	for _, slot := range state.Slots {
		if isUsableScheduledSlot(slot) && slot.ScheduledAt <= now {
			return statusOverdue
		}
	}
	for _, slot := range state.Slots {
		if isUsableScheduledSlot(slot) && slot.ScheduledAt > now {
			return statusScheduled
		}
	}
	for _, target := range publish.Targets {
		if target.Status == publishStatusPublished {
			return statusPublished
		}
	}
	// Every scheduled slot has been dealt with and at least one was posted by
	// hand. This must beat a stale explicit stage, otherwise marking a "ready"
	// note as posted would drop it back to Ready.
	for _, slot := range state.Slots {
		if slot.Status == slotDone && isHistorySlot(slot) && effectiveCompletedAt(slot) > 0 {
			return statusPosted
		}
	}
	switch state.Stage {
	case stageIdea, stageDrafting, stageReady:
		return state.Stage
	}
	if strings.TrimSpace(body) != "" {
		return statusDraft
	}
	return statusIdea
}

// Plan returns a note's plan, or an empty plan when the note has no sidecar.
func (s *PlannerService) Plan(noteID string) (PlanState, error) {
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// SetStage sets the explicit stage, or clears it when stage is "".
func (s *PlannerService) SetStage(noteID, stage string) (PlanState, error) {
	switch stage {
	case "", stageIdea, stageDrafting, stageReady:
	default:
		return PlanState{}, fmt.Errorf("invalid stage %q", stage)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return PlanState{}, err
	}
	defer unlock()
	if err := requireNote(root, noteID); err != nil {
		return PlanState{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}
	state.Stage = stage
	if err := writePlanState(root, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// Schedule upserts the scheduled slot for platform at the given instant.
// Duplicate hand-edited scheduled slots for one platform collapse into the
// earliest one; done/skipped history is always preserved.
func (s *PlannerService) Schedule(noteID, platform string, at int64) (PlanState, error) {
	platform, err := normalizePlatform(platform)
	if err != nil {
		return PlanState{}, err
	}
	if at <= 0 {
		return PlanState{}, errors.New("scheduled time must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return PlanState{}, err
	}
	defer unlock()
	if err := requireNote(root, noteID); err != nil {
		return PlanState{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}

	earliest := -1
	for i, slot := range state.Slots {
		if slot.Status != slotScheduled || normalizePublishPlatform(slot.Platform) != platform {
			continue
		}
		if earliest == -1 || earlierSlot(slot, state.Slots[earliest]) {
			earliest = i
		}
	}

	if earliest == -1 {
		state.Slots = append(state.Slots, PlanSlot{
			ID:          newUniqueSlotID(&state),
			Platform:    platform,
			ScheduledAt: at,
			Status:      slotScheduled,
		})
	} else {
		kept := make([]PlanSlot, 0, len(state.Slots))
		for i, slot := range state.Slots {
			if slot.Status == slotScheduled && normalizePublishPlatform(slot.Platform) == platform {
				if i != earliest {
					continue // duplicate scheduled slot, collapsed away
				}
				slot.ScheduledAt = at
				slot.NotifiedAt = 0
				slot.CompletedAt = 0
				slot.Status = slotScheduled
			}
			kept = append(kept, slot)
		}
		state.Slots = kept
	}

	if err := writePlanState(root, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// Reschedule moves an existing scheduled slot, keeping its rendition and
// resetting notifiedAt.
func (s *PlannerService) Reschedule(noteID, slotID string, at int64) (PlanState, error) {
	if at <= 0 {
		return PlanState{}, errors.New("scheduled time must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return PlanState{}, err
	}
	defer unlock()
	if err := requireNote(root, noteID); err != nil {
		return PlanState{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}
	slot, ok := findSlot(&state, slotID)
	if !ok {
		return PlanState{}, fmt.Errorf("slot %q not found", slotID)
	}
	if slot.Status != slotScheduled {
		return PlanState{}, errors.New("only scheduled slots can be rescheduled")
	}
	for i := range state.Slots {
		other := state.Slots[i]
		if other.ID == slot.ID || other.Status != slotScheduled {
			continue
		}
		if normalizePublishPlatform(other.Platform) == normalizePublishPlatform(slot.Platform) {
			return PlanState{}, fmt.Errorf("%s already has a scheduled slot", slot.Platform)
		}
	}
	slot.ScheduledAt = at
	slot.NotifiedAt = 0
	slot.CompletedAt = 0
	if err := writePlanState(root, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// Unschedule removes a slot that has no rendition or notes; otherwise marks it
// skipped so it appears in History.
func (s *PlannerService) Unschedule(noteID, slotID string) (PlanState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return PlanState{}, err
	}
	defer unlock()
	if err := requireNote(root, noteID); err != nil {
		return PlanState{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}
	slot, ok := findSlot(&state, slotID)
	if !ok {
		return PlanState{}, fmt.Errorf("slot %q not found", slotID)
	}
	if slot.Status != slotScheduled {
		return PlanState{}, errors.New("only scheduled slots can be unscheduled")
	}

	if slot.Rendition == nil && strings.TrimSpace(slot.Notes) == "" {
		kept := make([]PlanSlot, 0, len(state.Slots))
		for _, candidate := range state.Slots {
			if candidate.ID == slotID {
				continue
			}
			kept = append(kept, candidate)
		}
		state.Slots = kept
	} else {
		slot.Status = slotSkipped
		slot.CompletedAt = s.now().UnixMilli()
	}
	if err := writePlanState(root, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// MarkSlot sets a slot to done or skipped and stamps CompletedAt.
func (s *PlannerService) MarkSlot(noteID, slotID, status string) (PlanState, error) {
	if status != slotDone && status != slotSkipped {
		return PlanState{}, fmt.Errorf("invalid slot status %q", status)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return PlanState{}, err
	}
	defer unlock()
	if err := requireNote(root, noteID); err != nil {
		return PlanState{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}
	slot, ok := findSlot(&state, slotID)
	if !ok {
		return PlanState{}, fmt.Errorf("slot %q not found", slotID)
	}
	slot.Status = status
	slot.CompletedAt = s.now().UnixMilli()
	if err := writePlanState(root, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// SaveRendition stores edited content for a slot. SourceHash and EditedAt are
// filled in by the service from the current note and clock.
func (s *PlannerService) SaveRendition(noteID, slotID string, r Rendition) (PlanState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return PlanState{}, err
	}
	defer unlock()
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}
	slot, ok := findSlot(&state, slotID)
	if !ok {
		return PlanState{}, fmt.Errorf("slot %q not found", slotID)
	}
	note, err := s.notes.Get(noteID)
	if err != nil {
		return PlanState{}, err
	}
	r.SourceHash = publishContentHash(note.Title, note.Body)
	r.EditedAt = s.now().UnixMilli()
	slot.Rendition = &r
	// Keep the per-platform draft in sync so opening Publish from the note
	// (without a slot) shows the work the user just saved here.
	platform := normalizePublishPlatform(slot.Platform)
	if state.Drafts == nil {
		state.Drafts = map[string]*Rendition{}
	}
	mirror := r
	state.Drafts[platform] = &mirror
	if err := writePlanState(root, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// SavePlatformDraft stores edited content for a platform even when the note
// has no scheduled slot. It also mirrors the draft onto that platform's
// scheduled slot, if one exists, so the queue and reminders stay in step.
func (s *PlannerService) SavePlatformDraft(noteID, platform string, r Rendition) (PlanState, error) {
	platform, err := normalizePlatform(platform)
	if err != nil {
		return PlanState{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return PlanState{}, err
	}
	defer unlock()
	if err := requireNote(root, noteID); err != nil {
		return PlanState{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}
	note, err := s.notes.Get(noteID)
	if err != nil {
		return PlanState{}, err
	}
	r.SourceHash = publishContentHash(note.Title, note.Body)
	r.EditedAt = s.now().UnixMilli()
	if state.Drafts == nil {
		state.Drafts = map[string]*Rendition{}
	}
	draft := r
	state.Drafts[platform] = &draft
	for i := range state.Slots {
		slot := &state.Slots[i]
		if slot.Status == slotScheduled && normalizePublishPlatform(slot.Platform) == platform {
			mirror := r
			slot.Rendition = &mirror
		}
	}
	if err := writePlanState(root, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// ClearPlatformDraft removes the saved draft for a platform and clears the
// rendition on that platform's scheduled slot. Done/skipped history is left
// alone so past posts keep their record.
func (s *PlannerService) ClearPlatformDraft(noteID, platform string) (PlanState, error) {
	platform, err := normalizePlatform(platform)
	if err != nil {
		return PlanState{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return PlanState{}, err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return PlanState{}, err
	}
	defer unlock()
	if err := requireNote(root, noteID); err != nil {
		return PlanState{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return PlanState{}, err
	}
	delete(state.Drafts, platform)
	for i := range state.Slots {
		slot := &state.Slots[i]
		if slot.Status == slotScheduled && normalizePublishPlatform(slot.Platform) == platform {
			slot.Rendition = nil
		}
	}
	if err := writePlanState(root, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

// Queue returns scheduled slots in [from, to), oldest first. to == 0 means no
// upper bound. Done and skipped slots are excluded; use History for those.
func (s *PlannerService) Queue(from, to int64) ([]QueueItem, error) {
	notes, err := s.scan()
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	seen := make(map[string]bool)
	items := make([]QueueItem, 0)
	for _, n := range notes {
		for _, slot := range n.plan.Slots {
			if !isUsableScheduledSlot(slot) {
				continue
			}
			key := n.id + "\x00" + slot.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			if from > 0 && slot.ScheduledAt < from {
				continue
			}
			if to > 0 && slot.ScheduledAt >= to {
				continue
			}
			status := statusScheduled
			if slot.ScheduledAt <= now {
				status = statusOverdue
			}
			items = append(items, queueItemFor(n, slot, status))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ScheduledAt != items[j].ScheduledAt {
			return items[i].ScheduledAt < items[j].ScheduledAt
		}
		if items[i].NoteID != items[j].NoteID {
			return items[i].NoteID < items[j].NoteID
		}
		return items[i].SlotID < items[j].SlotID
	})
	return items, nil
}

// History returns done and skipped slots in [from, to) by effective completion
// time, newest first. to == 0 means no upper bound.
func (s *PlannerService) History(from, to int64) ([]QueueItem, error) {
	notes, err := s.scan()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	items := make([]QueueItem, 0)
	for _, n := range notes {
		for _, slot := range n.plan.Slots {
			if !isHistorySlot(slot) {
				continue
			}
			key := n.id + "\x00" + slot.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			at := effectiveCompletedAt(slot)
			if at <= 0 {
				continue
			}
			if from > 0 && at < from {
				continue
			}
			if to > 0 && at >= to {
				continue
			}
			item := queueItemFor(n, slot, slot.Status)
			item.CompletedAt = at
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CompletedAt != items[j].CompletedAt {
			return items[i].CompletedAt > items[j].CompletedAt
		}
		if items[i].NoteID != items[j].NoteID {
			return items[i].NoteID < items[j].NoteID
		}
		return items[i].SlotID < items[j].SlotID
	})
	return items, nil
}

// Backlog returns unpublished notes with no scheduled slot, newest modified
// first. limit <= 0 means no limit.
func (s *PlannerService) Backlog(limit int) ([]BacklogItem, error) {
	notes, err := s.scan()
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	items := make([]BacklogItem, 0)
	for _, n := range notes {
		status := DerivedStatus(n.body, n.plan, n.publish, now)
		switch status {
		case statusOverdue, statusScheduled, statusPublished, statusPosted:
			continue
		}
		items = append(items, BacklogItem{
			NoteID:   n.id,
			Title:    n.title,
			Status:   status,
			Modified: n.modified,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Modified != items[j].Modified {
			return items[i].Modified > items[j].Modified
		}
		return items[i].NoteID < items[j].NoteID
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// Statuses returns the derived status for every note the planner can read.
func (s *PlannerService) Statuses() (map[string]string, error) {
	notes, err := s.scan()
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	statuses := make(map[string]string, len(notes))
	for _, n := range notes {
		statuses[n.id] = DerivedStatus(n.body, n.plan, n.publish, now)
	}
	return statuses, nil
}

// SlotText returns the text a reminder should copy: the saved rendition when
// present, otherwise a freshly prepared draft.
func (s *PlannerService) SlotText(noteID, slotID string) (SlotText, error) {
	root, err := s.notes.publishRoot()
	if err != nil {
		return SlotText{}, err
	}
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return SlotText{}, err
	}
	slot, ok := findSlot(&state, slotID)
	if !ok {
		return SlotText{}, fmt.Errorf("slot %q not found", slotID)
	}
	if slot.Rendition != nil {
		return renditionSlotText(slot.Rendition), nil
	}
	if r := state.Drafts[normalizePublishPlatform(slot.Platform)]; r != nil {
		return renditionSlotText(r), nil
	}

	note, err := s.notes.Get(noteID)
	if err != nil {
		return SlotText{}, err
	}
	draft, err := s.publish.Prepare(note.Title, note.Body, slot.Platform, PrepareOptions{})
	if err != nil {
		return SlotText{}, err
	}
	return SlotText{
		Title: draft.Title,
		Text:  draft.Text,
		Parts: draft.Parts,
		Body:  draft.Body,
		Tags:  draft.Tags,
	}, nil
}

func renditionSlotText(r *Rendition) SlotText {
	return SlotText{
		Title:     r.Title,
		Text:      r.Text,
		Parts:     r.Parts,
		Body:      r.Body,
		Tags:      r.Tags,
		Canonical: r.Canonical,
	}
}

// Due returns usable scheduled slots at or before now that have not been
// notified yet, oldest first. It is internal to ReminderService.
//
//wails:ignore
func (s *PlannerService) Due() ([]QueueItem, error) {
	notes, err := s.scan()
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	seen := make(map[string]bool)
	items := make([]QueueItem, 0)
	for _, n := range notes {
		for _, slot := range n.plan.Slots {
			if !isUsableScheduledSlot(slot) || slot.NotifiedAt != 0 {
				continue
			}
			if slot.ScheduledAt > now {
				continue
			}
			key := n.id + "\x00" + slot.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			items = append(items, queueItemFor(n, slot, statusOverdue))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ScheduledAt != items[j].ScheduledAt {
			return items[i].ScheduledAt < items[j].ScheduledAt
		}
		if items[i].NoteID != items[j].NoteID {
			return items[i].NoteID < items[j].NoteID
		}
		return items[i].SlotID < items[j].SlotID
	})
	return items, nil
}

// markNotified stamps NotifiedAt only if the slot still has the same status and
// ScheduledAt that produced the reminder.
func (s *PlannerService) markNotified(noteID, slotID string, scheduledAt, at int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.notes.publishRoot()
	if err != nil {
		return err
	}
	unlock, err := lockNotesDir(root)
	if err != nil {
		return err
	}
	defer unlock()
	state, _, err := readPlanState(root, noteID)
	if err != nil {
		return err
	}
	slot, ok := findSlot(&state, slotID)
	if !ok {
		return fmt.Errorf("slot %q not found", slotID)
	}
	if slot.Status != slotScheduled || slot.ScheduledAt != scheduledAt || slot.NotifiedAt != 0 {
		return nil // the user changed the slot; do not stamp a stale reminder
	}
	slot.NotifiedAt = at
	return writePlanState(root, state)
}

// scan reads every note and its sidecars. A note that cannot be read is
// skipped so one corrupt file cannot break a query for every other note.
func (s *PlannerService) scan() ([]scannedNote, error) {
	root, err := s.notes.publishRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read notes: %w", err)
	}
	notes := make([]scannedNote, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if validateID(id) != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		title, body := splitDocument(string(raw))
		plan, _, err := readPlanState(root, id)
		if err != nil {
			plan = emptyPlanState(id)
		}
		publish, _, err := readPublishState(root, id)
		if err != nil {
			publish = emptyPublishState(id)
		}
		notes = append(notes, scannedNote{
			id:       id,
			title:    title,
			body:     body,
			modified: info.ModTime().UnixMilli(),
			plan:     plan,
			publish:  publish,
		})
	}
	return notes, nil
}

func queueItemFor(n scannedNote, slot PlanSlot, status string) QueueItem {
	title := n.title
	preview := ""
	stale := false
	r := slot.Rendition
	if r == nil {
		r = n.plan.Drafts[normalizePublishPlatform(slot.Platform)]
	}
	if r != nil {
		if r.Title != "" {
			title = r.Title
		}
		switch {
		case r.Text != "":
			preview = r.Text
		case len(r.Parts) > 0:
			preview = strings.Join(r.Parts, " ")
		case r.Body != "":
			preview = r.Body
		}
		stale = r.SourceHash != publishContentHash(n.title, n.body)
	}
	if preview == "" {
		preview = previewText(n.body)
	}
	preview, _ = truncateRunes(strings.TrimSpace(preview), 140)
	return QueueItem{
		NoteID:      n.id,
		SlotID:      slot.ID,
		Title:       title,
		Platform:    normalizePublishPlatform(slot.Platform),
		Status:      status,
		ScheduledAt: slot.ScheduledAt,
		Preview:     preview,
		Stale:       stale,
	}
}

// earlierSlot reports whether a should be kept over b when collapsing duplicate
// scheduled slots for one platform. Slots with no positive time sort last.
func earlierSlot(a, b PlanSlot) bool {
	if a.ScheduledAt <= 0 {
		return false
	}
	if b.ScheduledAt <= 0 {
		return true
	}
	if a.ScheduledAt != b.ScheduledAt {
		return a.ScheduledAt < b.ScheduledAt
	}
	return a.ID < b.ID
}

func newUniqueSlotID(state *PlanState) string {
	for {
		id := newSlotID()
		if _, exists := findSlot(state, id); !exists {
			return id
		}
	}
}

func requireNote(root, noteID string) error {
	if err := validateID(noteID); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, noteID+".md")); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("note %q not found", noteID)
		}
		return fmt.Errorf("stat note: %w", err)
	}
	return nil
}
