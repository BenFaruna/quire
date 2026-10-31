package main

import (
	"errors"
	"os"
	"testing"
)

func TestPlanStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	in := emptyPlanState("n1")
	in.Stage = stageReady
	in.Slots = []PlanSlot{{
		ID: "abc123def456", Platform: platformX, ScheduledAt: 1000, Status: slotScheduled,
		CompletedAt: 900, Rendition: &Rendition{Text: "hello", Body: "blog body"},
	}}
	if err := writePlanState(root, in); err != nil {
		t.Fatal(err)
	}
	out, found, err := readPlanState(root, "n1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if out.Version != planStateVersion || out.Slots[0].ID != "abc123def456" {
		t.Fatalf("bad round trip: %+v", out)
	}
	if out.Slots[0].CompletedAt != 900 || out.Slots[0].Rendition == nil || out.Slots[0].Rendition.Body != "blog body" {
		t.Fatalf("lost fields: %+v", out.Slots[0])
	}
	if out.Updated == 0 {
		t.Fatal("Updated not stamped")
	}
}

func TestReadPlanStateMissingIsNotAnError(t *testing.T) {
	state, found, err := readPlanState(t.TempDir(), "nope")
	if err != nil || found || state.NoteID != "nope" || len(state.Slots) != 0 {
		t.Fatalf("state=%+v found=%v err=%v", state, found, err)
	}
}

func TestReadPlanStateNormalisesVersionAndNoteID(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(planStatePath(root, "n1"), []byte(`{"noteId":"other","slots":null}`), 0o644)
	state, found, err := readPlanState(root, "n1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if state.NoteID != "n1" || state.Slots == nil || state.Version != planStateVersion {
		t.Fatalf("not normalised: %+v", state)
	}
}

func TestReadPlanStateCorruptRenamesToBak(t *testing.T) {
	root := t.TempDir()
	path := planStatePath(root, "n1")
	os.WriteFile(path, []byte("{not json"), 0o644)
	state, found, err := readPlanState(root, "n1")
	if err != nil || found || len(state.Slots) != 0 {
		t.Fatalf("state=%+v found=%v err=%v", state, found, err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatal("corrupt file not preserved as .bak")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("corrupt file not moved out of the way")
	}
}

func TestReadPlanStateRejectsPathTraversal(t *testing.T) {
	if _, _, err := readPlanState(t.TempDir(), "../evil"); err == nil {
		t.Fatal("expected error for unsafe id")
	}
}

func TestNormalizePlatform(t *testing.T) {
	for in, want := range map[string]string{
		"x":        platformX,
		"Twitter":  platformX,
		"linkedin": platformLinkedIn,
		"dev.to":   platformDevto,
		"devto":    platformDevto,
		"hashnode": platformHashnode,
	} {
		got, err := normalizePlatform(in)
		if err != nil || got != want {
			t.Fatalf("normalizePlatform(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizePlatform("myspace"); err == nil {
		t.Fatal("expected unknown platform to be rejected")
	}
}

func TestEffectiveCompletedAt(t *testing.T) {
	if got := effectiveCompletedAt(PlanSlot{CompletedAt: 200, ScheduledAt: 100}); got != 200 {
		t.Fatalf("got %d, want CompletedAt", got)
	}
	if got := effectiveCompletedAt(PlanSlot{ScheduledAt: 100}); got != 100 {
		t.Fatalf("got %d, want ScheduledAt fallback", got)
	}
}
