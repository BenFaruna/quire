package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestPlanner returns a planner whose notes root is a temp dir, so
// publishRoot() resolves there and no test touches the real config directory.
// It also builds a PublishService over the same notes root for SlotText.
func newTestPlanner(t *testing.T) (*PlannerService, string) {
	t.Helper()
	root := t.TempDir()
	notes := &NotesService{root: root}
	publish := NewPublishService(notes)
	return NewPlannerService(notes, publish), root
}

// mustNote writes <id>.md with the given title/body via composeDocument.
func mustNote(t *testing.T, root, id, title, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, id+".md"), []byte(composeDocument(title, body)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustPlan writes a plan directly, for hand-edited and corrupt-file cases.
func mustPlan(t *testing.T, root string, state PlanState) {
	t.Helper()
	if err := writePlanState(root, state); err != nil {
		t.Fatal(err)
	}
}

func TestDerivedStatus(t *testing.T) {
	now := int64(1000)
	future := PlanState{Slots: []PlanSlot{{ID: "s1", Platform: platformX, ScheduledAt: 2000, Status: slotScheduled}}}
	past := PlanState{Slots: []PlanSlot{{ID: "s2", Platform: platformX, ScheduledAt: 500, Status: slotScheduled}}}
	atNow := PlanState{Slots: []PlanSlot{{ID: "s3", Platform: platformX, ScheduledAt: now, Status: slotScheduled}}}
	done := PlanState{Slots: []PlanSlot{{ID: "s4", Platform: platformX, ScheduledAt: 500, Status: slotDone, CompletedAt: 600}}}
	skipped := PlanState{Slots: []PlanSlot{{ID: "s5", Platform: platformX, ScheduledAt: 500, Status: slotSkipped, CompletedAt: 600}}}
	published := emptyPublishState("n1")
	published.Targets[platformDevto] = PublishTarget{Status: publishStatusPublished}

	cases := []struct {
		name string
		body string
		plan PlanState
		pub  PublishState
		want string
	}{
		{"overdue beats scheduled", "text", PlanState{Slots: append(future.Slots, past.Slots...)}, emptyPublishState("n1"), statusOverdue},
		{"scheduled beats published", "text", future, published, statusScheduled},
		{"published beats stage", "text", emptyPlanState("n1"), published, statusPublished},
		{"done slot is posted", "text", done, emptyPublishState("n1"), statusPosted},
		{"posted beats explicit stage", "text", PlanState{Stage: stageReady, Slots: done.Slots}, emptyPublishState("n1"), statusPosted},
		{"done slot plus future slot is scheduled", "text", PlanState{Slots: append(future.Slots, done.Slots...)}, emptyPublishState("n1"), statusScheduled},
		{"done slot plus published target is published", "text", done, published, statusPublished},
		{"skipped slot is still drafting", "text", skipped, emptyPublishState("n1"), statusDraft},
		{"legacy done slot without CompletedAt is posted", "text", PlanState{Slots: []PlanSlot{{ID: "s6", Platform: platformX, ScheduledAt: 500, Status: slotDone}}}, emptyPublishState("n1"), statusPosted},
		{"explicit stage wins over body", "text", PlanState{Stage: stageReady}, emptyPublishState("n1"), stageReady},
		{"body implies drafting", "text", emptyPlanState("n1"), emptyPublishState("n1"), statusDraft},
		{"empty body is an idea", "   ", emptyPlanState("n1"), emptyPublishState("n1"), statusIdea},
		{"slot exactly at now is due", "text", atNow, emptyPublishState("n1"), statusOverdue},
		{"unknown platform is ignored", "text", PlanState{Slots: []PlanSlot{{Platform: "myspace", ScheduledAt: 500, Status: slotScheduled}}}, emptyPublishState("n1"), statusDraft},
		{"invalid slot id is ignored", "text", PlanState{Slots: []PlanSlot{{ID: "bad id", Platform: platformX, ScheduledAt: 500, Status: slotScheduled}}}, emptyPublishState("n1"), statusDraft},
		{"zero scheduled time is ignored", "text", PlanState{Slots: []PlanSlot{{Platform: platformX, ScheduledAt: 0, Status: slotScheduled}}}, emptyPublishState("n1"), statusDraft},
		{"unknown stage falls through", "text", PlanState{Stage: "archived"}, emptyPublishState("n1"), statusDraft},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DerivedStatus(c.body, c.plan, c.pub, now); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestScheduleUpsertsOneSlotPerPlatform(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	if _, err := svc.Schedule("n1", platformX, 2000); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Schedule("n1", platformX, 3000); err != nil {
		t.Fatal(err)
	}
	state, err := svc.Plan("n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Slots) != 1 {
		t.Fatalf("got %d slots, want 1", len(state.Slots))
	}
	if state.Slots[0].ScheduledAt != 3000 {
		t.Fatalf("scheduledAt = %d, want 3000", state.Slots[0].ScheduledAt)
	}
}

func TestQueueSkipsOrphanedSidecar(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustPlan(t, root, PlanState{NoteID: "ghost", Slots: []PlanSlot{{ID: "a1", Platform: platformX, ScheduledAt: 5000, Status: slotScheduled}}})
	items, err := svc.Queue(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("orphan surfaced in queue: %+v", items)
	}
}

func TestQueueSurfacesMissedSlot(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	svc.now = func() time.Time { return time.UnixMilli(3 * 24 * 60 * 60 * 1000) }
	state, err := svc.Schedule("n1", platformX, 1000) // three days before "now"
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Queue(0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].Status != statusOverdue {
		t.Fatalf("status = %q, want overdue", items[0].Status)
	}
	if len(state.Slots) != 1 {
		t.Fatal("slot not persisted")
	}
}

func TestQueueIgnoresUnknownPlatformAndUnscheduledTimestamps(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	mustPlan(t, root, PlanState{NoteID: "n1", Slots: []PlanSlot{
		{ID: "a1", Platform: "myspace", ScheduledAt: 5000, Status: slotScheduled},
		{ID: "a2", Platform: platformX, ScheduledAt: 0, Status: slotScheduled},
		{ID: "a3", Platform: platformX, ScheduledAt: -1, Status: slotScheduled},
	}})
	items, err := svc.Queue(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("unusable slots leaked into queue: %+v", items)
	}
}

func TestDeleteRemovesPlanSidecar(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	if _, err := svc.Schedule("n1", platformX, 5000); err != nil {
		t.Fatal(err)
	}
	if err := svc.notes.Delete("n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(planStatePath(root, "n1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plan sidecar survived note deletion")
	}
}

func TestScheduleCollapsesDuplicateScheduledSlots(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	mustPlan(t, root, PlanState{NoteID: "n1", Slots: []PlanSlot{
		{ID: "a1", Platform: platformX, ScheduledAt: 1000, Status: slotScheduled},
		{ID: "a2", Platform: platformX, ScheduledAt: 2000, Status: slotScheduled},
		{ID: "a3", Platform: platformX, ScheduledAt: 500, Status: slotDone, CompletedAt: 600},
	}})
	state, err := svc.Schedule("n1", platformX, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Slots) != 2 {
		t.Fatalf("got %d slots, want earliest scheduled + done: %+v", len(state.Slots), state.Slots)
	}
	if state.Slots[0].ScheduledAt != 3000 || state.Slots[0].Status != slotScheduled {
		t.Fatalf("earliest slot not updated: %+v", state.Slots[0])
	}
}

func TestRescheduleResetsNotifiedAndKeepsRendition(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, _ := svc.Schedule("n1", platformX, 1000)
	slotID := state.Slots[0].ID
	if _, err := svc.SaveRendition("n1", slotID, Rendition{Text: "draft"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.markNotified("n1", slotID, 1000, 900); err != nil {
		t.Fatal(err)
	}
	state, err := svc.Reschedule("n1", slotID, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if state.Slots[0].ScheduledAt != 5000 || state.Slots[0].NotifiedAt != 0 {
		t.Fatalf("reschedule did not reset: %+v", state.Slots[0])
	}
	if state.Slots[0].Rendition == nil || state.Slots[0].Rendition.Text != "draft" {
		t.Fatal("rendition lost")
	}
}

func TestUnscheduleRemovesOrSkips(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, _ := svc.Schedule("n1", platformX, 1000)
	emptyID := state.Slots[0].ID
	state, _ = svc.Schedule("n1", platformLinkedIn, 2000)
	editedID := state.Slots[1].ID
	if _, err := svc.SaveRendition("n1", editedID, Rendition{Text: "keep me"}); err != nil {
		t.Fatal(err)
	}

	state, err := svc.Unschedule("n1", emptyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Slots) != 1 {
		t.Fatalf("empty slot not removed: %+v", state.Slots)
	}

	svc.now = func() time.Time { return time.UnixMilli(4000) }
	state, err = svc.Unschedule("n1", editedID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Slots[0].Status != slotSkipped || state.Slots[0].CompletedAt != 4000 {
		t.Fatalf("edited slot not moved to history: %+v", state.Slots[0])
	}
}

func TestHistoryIncludesDoneAndSkipped(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	mustPlan(t, root, PlanState{NoteID: "n1", Slots: []PlanSlot{
		{ID: "a1", Platform: platformX, ScheduledAt: 1000, Status: slotDone, CompletedAt: 1200},
		{ID: "a2", Platform: platformDevto, ScheduledAt: 2000, Status: slotSkipped, CompletedAt: 2200},
		{ID: "a3", Platform: platformX, ScheduledAt: 3000, Status: slotScheduled},
	}})
	items, err := svc.History(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Status != slotSkipped || items[1].Status != slotDone {
		t.Fatalf("history = %+v", items)
	}
	if items[0].CompletedAt != 2200 {
		t.Fatalf("completedAt = %d", items[0].CompletedAt)
	}
}

func TestStatusesAndBacklogExcludePublished(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "idea", "Idea", "")
	mustNote(t, root, "draft", "Draft", "Body")
	mustNote(t, root, "posted", "Posted", "Body")
	mustNote(t, root, "live", "Live", "Body")
	mustPlan(t, root, PlanState{NoteID: "posted", Slots: []PlanSlot{{ID: "a0", Platform: platformX, ScheduledAt: 900, Status: slotDone, CompletedAt: 950}}})
	mustPlan(t, root, PlanState{NoteID: "live", Slots: []PlanSlot{{ID: "a1", Platform: platformDevto, ScheduledAt: 1000, Status: slotDone, CompletedAt: 1100}}})
	pub := emptyPublishState("live")
	pub.Targets[platformDevto] = PublishTarget{Status: publishStatusPublished}
	if err := writePublishState(root, pub); err != nil {
		t.Fatal(err)
	}

	statuses, err := svc.Statuses()
	if err != nil {
		t.Fatal(err)
	}
	if statuses["idea"] != statusIdea || statuses["draft"] != statusDraft || statuses["posted"] != statusPosted || statuses["live"] != statusPublished {
		t.Fatalf("statuses = %+v", statuses)
	}
	backlog, err := svc.Backlog(20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range backlog {
		if item.NoteID == "live" || item.NoteID == "posted" {
			t.Fatalf("%s note leaked into backlog", item.NoteID)
		}
	}
}

func TestQueueIsolatesCorruptPlan(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "bad", "Bad", "Body")
	mustNote(t, root, "good", "Good", "Body")
	if err := os.WriteFile(planStatePath(root, "bad"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Schedule("good", platformX, 5000); err != nil {
		t.Fatal(err)
	}
	items, err := svc.Queue(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].NoteID != "good" {
		t.Fatalf("corrupt plan blocked queue: %+v", items)
	}
}

func TestSaveRenditionFillsHashAndEditedAt(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, _ := svc.Schedule("n1", platformDevto, 5000)
	state, err := svc.SaveRendition("n1", state.Slots[0].ID, Rendition{Body: "blog body"})
	if err != nil {
		t.Fatal(err)
	}
	r := state.Slots[0].Rendition
	if r == nil || r.SourceHash == "" || r.EditedAt == 0 {
		t.Fatalf("rendition not stamped: %+v", r)
	}
	if r.SourceHash != publishContentHash("Title", "Body") {
		t.Fatal("source hash does not match note")
	}
}

func TestQueueMarksStaleRenditionAfterNoteEdit(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, err := svc.Schedule("n1", platformX, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRendition("n1", state.Slots[0].ID, Rendition{Text: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	items, err := svc.Queue(0, 0)
	if err != nil || len(items) != 1 || items[0].Stale {
		t.Fatalf("fresh rendition marked stale: %+v err=%v", items, err)
	}

	mustNote(t, root, "n1", "Title", "Body changed")
	items, err = svc.Queue(0, 0)
	if err != nil || len(items) != 1 || !items[0].Stale {
		t.Fatalf("edited note did not mark the rendition stale: %+v err=%v", items, err)
	}
}

func TestSlotTextUsesRenditionOrPrepare(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body text")
	state, _ := svc.Schedule("n1", platformX, 5000)
	text, err := svc.SlotText("n1", state.Slots[0].ID)
	if err != nil || text.Text == "" {
		t.Fatalf("prepare fallback failed: %+v err=%v", text, err)
	}
	if _, err := svc.SaveRendition("n1", state.Slots[0].ID, Rendition{Text: "edited"}); err != nil {
		t.Fatal(err)
	}
	text, err = svc.SlotText("n1", state.Slots[0].ID)
	if err != nil || text.Text != "edited" {
		t.Fatalf("rendition not used: %+v err=%v", text, err)
	}
}

func TestDueNotifiesOnceAndMarkNotifiedRevalidates(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	svc.now = func() time.Time { return time.UnixMilli(1000) }
	state, _ := svc.Schedule("n1", platformX, 1000)
	slotID := state.Slots[0].ID

	due, err := svc.Due()
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %+v err=%v", due, err)
	}
	if err := svc.markNotified("n1", slotID, 1000, 1100); err != nil {
		t.Fatal(err)
	}
	due, err = svc.Due()
	if err != nil || len(due) != 0 {
		t.Fatalf("second due = %+v err=%v", due, err)
	}

	// Rescheduling must reset NotifiedAt so the new time notifies again.
	if _, err := svc.Reschedule("n1", slotID, 2000); err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.UnixMilli(2000) }
	due, err = svc.Due()
	if err != nil || len(due) != 1 {
		t.Fatalf("rescheduled slot not due: %+v err=%v", due, err)
	}

	// A stale stamp (wrong ScheduledAt) is a no-op.
	if err := svc.markNotified("n1", slotID, 1000, 2100); err != nil {
		t.Fatal(err)
	}
	due, _ = svc.Due()
	if len(due) != 1 {
		t.Fatalf("stale markNotified suppressed the reminder: %+v", due)
	}
}

func TestSavePlatformDraftWithoutSlot(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	svc.now = func() time.Time { return time.UnixMilli(1234) }

	state, err := svc.SavePlatformDraft("n1", "twitter", Rendition{Text: "edited post"})
	if err != nil {
		t.Fatal(err)
	}
	r := state.Drafts[platformX]
	if r == nil || r.Text != "edited post" {
		t.Fatalf("draft not saved: %+v", state.Drafts)
	}
	if r.EditedAt != 1234 || r.SourceHash == "" {
		t.Fatalf("metadata not filled: %+v", r)
	}

	// A reload from disk must keep the draft.
	reloaded, err := svc.Plan("n1")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Drafts[platformX] == nil || reloaded.Drafts[platformX].Text != "edited post" {
		t.Fatalf("draft lost on reload: %+v", reloaded.Drafts)
	}
}

func TestSavePlatformDraftMirrorsScheduledSlot(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, err := svc.Schedule("n1", platformX, 5000)
	if err != nil {
		t.Fatal(err)
	}
	slotID := state.Slots[0].ID

	if _, err := svc.SavePlatformDraft("n1", platformX, Rendition{Parts: []string{"one", "two"}}); err != nil {
		t.Fatal(err)
	}
	state, err = svc.Plan("n1")
	if err != nil {
		t.Fatal(err)
	}
	slot, ok := findSlot(&state, slotID)
	if !ok || slot.Rendition == nil {
		t.Fatalf("slot rendition missing: %+v", state.Slots)
	}
	if len(slot.Rendition.Parts) != 2 {
		t.Fatalf("slot rendition not mirrored: %+v", slot.Rendition)
	}
}

func TestSaveRenditionMirrorsPlatformDraft(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, err := svc.Schedule("n1", platformLinkedIn, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRendition("n1", state.Slots[0].ID, Rendition{Text: "slot edit"}); err != nil {
		t.Fatal(err)
	}
	state, err = svc.Plan("n1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Drafts[platformLinkedIn] == nil || state.Drafts[platformLinkedIn].Text != "slot edit" {
		t.Fatalf("platform draft not mirrored: %+v", state.Drafts)
	}
}

func TestClearPlatformDraft(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, err := svc.Schedule("n1", platformX, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SavePlatformDraft("n1", platformX, Rendition{Text: "draft"}); err != nil {
		t.Fatal(err)
	}
	state, err = svc.ClearPlatformDraft("n1", platformX)
	if err != nil {
		t.Fatal(err)
	}
	if state.Drafts[platformX] != nil {
		t.Fatalf("draft not cleared: %+v", state.Drafts)
	}
	if state.Slots[0].Rendition != nil {
		t.Fatalf("scheduled slot rendition not cleared: %+v", state.Slots[0])
	}
}

func TestSlotTextUsesSavedPlatformDraft(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	state, err := svc.Schedule("n1", platformX, 5000)
	if err != nil {
		t.Fatal(err)
	}
	slotID := state.Slots[0].ID
	if _, err := svc.ClearPlatformDraft("n1", platformX); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SavePlatformDraft("n1", platformX, Rendition{Parts: []string{"saved one", "saved two"}}); err != nil {
		t.Fatal(err)
	}
	// Remove the slot mirror to prove SlotText falls back to the platform draft.
	state, err = svc.Plan("n1")
	if err != nil {
		t.Fatal(err)
	}
	state.Slots[0].Rendition = nil
	mustPlan(t, root, state)

	text, err := svc.SlotText("n1", slotID)
	if err != nil {
		t.Fatal(err)
	}
	if len(text.Parts) != 2 || text.Parts[0] != "saved one" {
		t.Fatalf("SlotText did not use platform draft: %+v", text)
	}
}

func TestQueuePreviewUsesPlatformDraft(t *testing.T) {
	svc, root := newTestPlanner(t)
	mustNote(t, root, "n1", "Title", "Body")
	svc.now = func() time.Time { return time.UnixMilli(1000) }
	state, err := svc.Schedule("n1", platformX, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SavePlatformDraft("n1", platformX, Rendition{Text: "saved preview"}); err != nil {
		t.Fatal(err)
	}
	// Keep the draft but remove the slot mirror.
	state, err = svc.Plan("n1")
	if err != nil {
		t.Fatal(err)
	}
	state.Slots[0].Rendition = nil
	mustPlan(t, root, state)

	items, err := svc.Queue(0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("queue = %+v err=%v", items, err)
	}
	if items[0].Preview != "saved preview" {
		t.Fatalf("preview = %q", items[0].Preview)
	}
}
