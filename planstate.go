package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Each planned note keeps a sidecar named <id>.plan.json next to it. The
// sidecar records scheduling intent and completed history, so the planner can
// answer queue and history queries without a database.
const (
	planStateVersion = 1

	stageIdea     = "idea"
	stageDrafting = "drafting"
	stageReady    = "ready"

	slotScheduled = "scheduled"
	slotDone      = "done"
	slotSkipped   = "skipped"

	// Derived display statuses (never stored).
	statusOverdue   = "overdue"
	statusScheduled = "scheduled"
	statusPublished = "published"
	statusPosted    = "posted"
	statusDraft     = "drafting"
	statusIdea      = "idea"
)

// PlanSlot is one intended post: a platform plus the moment it should go out.
type PlanSlot struct {
	ID          string     `json:"id"`
	Platform    string     `json:"platform"`
	ScheduledAt int64      `json:"scheduledAt"` // UTC ms; 0 means unscheduled
	Status      string     `json:"status"`      // scheduled | done | skipped
	NotifiedAt  int64      `json:"notifiedAt,omitempty"`
	CompletedAt int64      `json:"completedAt,omitempty"` // set for done/skipped
	Rendition   *Rendition `json:"rendition,omitempty"`
	Notes       string     `json:"notes,omitempty"`
}

// Rendition is the human-edited content for a slot. It is a snapshot:
// SourceHash records the note it was generated from so later edits can be
// detected. Social slots use Text/Parts; blog slots use Title/Body/Tags.
type Rendition struct {
	Title      string   `json:"title,omitempty"`
	Text       string   `json:"text,omitempty"`
	Parts      []string `json:"parts,omitempty"`
	Body       string   `json:"body,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Canonical  string   `json:"canonical,omitempty"`
	SourceHash string   `json:"sourceHash"`
	EditedAt   int64    `json:"editedAt"`
}

// PlanState is the whole sidecar document.
type PlanState struct {
	Version int        `json:"version"`
	NoteID  string     `json:"noteId"`
	Stage   string     `json:"stage,omitempty"` // idea | drafting | ready; empty means derive
	Updated int64      `json:"updated"`
	Slots   []PlanSlot `json:"slots"`
	// Drafts holds the last saved draft per platform, independent of whether a
	// slot exists. Keyed by the canonical platform name.
	Drafts map[string]*Rendition `json:"drafts,omitempty"`
}

func emptyPlanState(noteID string) PlanState {
	return PlanState{
		Version: planStateVersion,
		NoteID:  noteID,
		Slots:   []PlanSlot{},
		Drafts:  map[string]*Rendition{},
	}
}

func planStatePath(root, noteID string) string {
	return filepath.Join(root, noteID+".plan.json")
}

// readPlanState loads a sidecar. A missing file is not an error. A corrupt
// file is renamed to .bak and treated as if the note had no plan. Structurally
// valid JSON is normalised but its slots are preserved verbatim: an odd slot
// is skipped by queries rather than erased by the next write.
func readPlanState(root, noteID string) (PlanState, bool, error) {
	if err := validateID(noteID); err != nil {
		return PlanState{}, false, err
	}

	state := emptyPlanState(noteID)
	path := planStatePath(root, noteID)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, false, nil
	}
	if err != nil {
		return state, false, fmt.Errorf("read plan state: %w", err)
	}

	if err := json.Unmarshal(raw, &state); err != nil {
		_ = os.Rename(path, path+".bak")
		return emptyPlanState(noteID), false, nil
	}
	if state.Slots == nil {
		state.Slots = []PlanSlot{}
	}
	if state.Drafts == nil {
		state.Drafts = map[string]*Rendition{}
	}
	state.NoteID = noteID
	if state.Version == 0 {
		state.Version = planStateVersion
	}
	return state, true, nil
}

func writePlanState(root string, state PlanState) error {
	if err := validateID(state.NoteID); err != nil {
		return err
	}
	state.Version = planStateVersion
	state.Updated = time.Now().UnixMilli()
	if state.Slots == nil {
		state.Slots = []PlanSlot{}
	}
	if state.Drafts == nil {
		state.Drafts = map[string]*Rendition{}
	}

	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plan state: %w", err)
	}
	raw = append(raw, '\n')

	path := planStatePath(root, state.NoteID)
	if err := writeFileAtomic(path, 0o644, func(w io.Writer) error {
		_, err := w.Write(raw)
		return err
	}); err != nil {
		return fmt.Errorf("write plan state: %w", err)
	}
	return nil
}

func deletePlanState(root, noteID string) error {
	if err := validateID(noteID); err != nil {
		return err
	}
	if err := os.Remove(planStatePath(root, noteID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove plan state: %w", err)
	}
	return nil
}

func validateSlotID(id string) error {
	if id == "" {
		return errors.New("invalid slot id")
	}
	if !safeID.MatchString(id) {
		return errors.New("invalid slot id")
	}
	return nil
}

// newSlotID returns 12 lowercase hex characters.
func newSlotID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func findSlot(state *PlanState, slotID string) (*PlanSlot, bool) {
	for i := range state.Slots {
		if state.Slots[i].ID == slotID {
			return &state.Slots[i], true
		}
	}
	return nil, false
}

// normalizePlatform canonicalises a platform name and rejects unknown ones.
func normalizePlatform(p string) (string, error) {
	switch normalizePublishPlatform(p) {
	case platformX, platformLinkedIn, platformDevto, platformHashnode:
		return normalizePublishPlatform(p), nil
	default:
		return "", fmt.Errorf("unknown platform %q", p)
	}
}

func isKnownPlatform(p string) bool {
	_, err := normalizePlatform(p)
	return err == nil
}

// isUsableScheduledSlot reports whether a slot should appear in the queue and
// influence the note's derived status. A hand-edited slot with a bad ID is
// ignored by queries but left in the file.
func isUsableScheduledSlot(slot PlanSlot) bool {
	return validateSlotID(slot.ID) == nil &&
		isKnownPlatform(slot.Platform) &&
		slot.Status == slotScheduled &&
		slot.ScheduledAt > 0
}

// isHistorySlot reports whether a slot is a valid done/skipped history entry.
func isHistorySlot(slot PlanSlot) bool {
	return validateSlotID(slot.ID) == nil &&
		isKnownPlatform(slot.Platform) &&
		(slot.Status == slotDone || slot.Status == slotSkipped)
}

// effectiveCompletedAt returns the timestamp history should sort and group by.
// Hand-edited or legacy history slots may lack CompletedAt.
func effectiveCompletedAt(slot PlanSlot) int64 {
	if slot.CompletedAt > 0 {
		return slot.CompletedAt
	}
	return slot.ScheduledAt
}
