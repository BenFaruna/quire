package main

import (
	"sync"
	"testing"
)

func TestPlannerLockSerialisesAcrossServiceInstances(t *testing.T) {
	root := t.TempDir()

	// Two services model the GUI process and the MCP process: separate mutexes,
	// same notes directory. The file lock is what keeps both schedule writes.
	notesA := &NotesService{root: root}
	publishA := NewPublishService(notesA)
	plannerA := NewPlannerService(notesA, publishA)

	notesB := &NotesService{root: root}
	publishB := NewPublishService(notesB)
	plannerB := NewPlannerService(notesB, publishB)

	note, err := notesA.createTitled("Concurrent", "Body")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := plannerA.Schedule(note.ID, platformX, 1893574800000)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := plannerB.Schedule(note.ID, platformLinkedIn, 1893574800000)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("schedule: %v", err)
		}
	}

	state, err := plannerA.Plan(note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Slots) != 2 {
		t.Fatalf("slots = %+v, want both platforms", state.Slots)
	}
}
