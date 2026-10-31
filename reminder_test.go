package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeAlert struct {
	sent []QueueItem
	err  error
	on   func(item QueueItem)
}

func (f *fakeAlert) send(item QueueItem) error {
	if f.on != nil {
		f.on(item)
	}
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, item)
	return nil
}

func newTestReminder(t *testing.T) (*ReminderService, *PlannerService, string, *fakeAlert, *fakeAlert) {
	t.Helper()
	planner, root := newTestPlanner(t)
	alert := &fakeAlert{}
	native := &fakeAlert{}
	return NewReminderService(planner, alert, native), planner, root, alert, native
}

func TestReminderNotifiesOnceAndStamps(t *testing.T) {
	rem, planner, root, alert, native := newTestReminder(t)
	mustNote(t, root, "n1", "Title", "Body")
	planner.now = func() time.Time { return time.UnixMilli(1000) }
	rem.now = planner.now
	_, err := planner.Schedule("n1", platformX, 1000)
	if err != nil {
		t.Fatal(err)
	}

	rem.runOnce()
	if len(alert.sent) != 1 || len(native.sent) != 1 {
		t.Fatalf("sent = %d native = %d", len(alert.sent), len(native.sent))
	}
	plan, _ := planner.Plan("n1")
	if plan.Slots[0].NotifiedAt != 1000 {
		t.Fatalf("notifiedAt = %d", plan.Slots[0].NotifiedAt)
	}

	rem.runOnce()
	if len(alert.sent) != 1 {
		t.Fatalf("duplicate reminder: %d", len(alert.sent))
	}
}

func TestReminderSkipsFutureSlots(t *testing.T) {
	rem, planner, root, alert, _ := newTestReminder(t)
	mustNote(t, root, "n1", "Title", "Body")
	planner.now = func() time.Time { return time.UnixMilli(1000) }
	rem.now = planner.now
	if _, err := planner.Schedule("n1", platformX, 2000); err != nil {
		t.Fatal(err)
	}
	rem.runOnce()
	if len(alert.sent) != 0 {
		t.Fatalf("future slot notified: %+v", alert.sent)
	}
}

func TestReminderSenderErrorRetries(t *testing.T) {
	rem, planner, root, alert, _ := newTestReminder(t)
	mustNote(t, root, "n1", "Title", "Body")
	planner.now = func() time.Time { return time.UnixMilli(1000) }
	rem.now = planner.now
	if _, err := planner.Schedule("n1", platformX, 1000); err != nil {
		t.Fatal(err)
	}

	alert.err = errors.New("no webview")
	rem.runOnce()
	plan, _ := planner.Plan("n1")
	if plan.Slots[0].NotifiedAt != 0 {
		t.Fatal("failed send stamped notifiedAt")
	}

	alert.err = nil
	rem.runOnce()
	if len(alert.sent) != 1 {
		t.Fatalf("retry did not deliver: %d", len(alert.sent))
	}
}

func TestReminderNativeFailureStillStamps(t *testing.T) {
	rem, planner, root, alert, native := newTestReminder(t)
	mustNote(t, root, "n1", "Title", "Body")
	planner.now = func() time.Time { return time.UnixMilli(1000) }
	rem.now = planner.now
	if _, err := planner.Schedule("n1", platformX, 1000); err != nil {
		t.Fatal(err)
	}
	native.err = errors.New("no daemon")

	rem.runOnce()
	if len(alert.sent) != 1 {
		t.Fatal("in-app alert not sent")
	}
	plan, _ := planner.Plan("n1")
	if plan.Slots[0].NotifiedAt != 1000 {
		t.Fatal("native failure blocked the stamp")
	}
}

func TestReminderDoesNotStampRescheduledSlot(t *testing.T) {
	rem, planner, root, alert, _ := newTestReminder(t)
	mustNote(t, root, "n1", "Title", "Body")
	planner.now = func() time.Time { return time.UnixMilli(1000) }
	rem.now = planner.now
	state, err := planner.Schedule("n1", platformX, 1000)
	if err != nil {
		t.Fatal(err)
	}
	slotID := state.Slots[0].ID

	alert.on = func(item QueueItem) {
		// The user reschedules while the alert is being delivered.
		if _, err := planner.Reschedule("n1", item.SlotID, 5000); err != nil {
			t.Error(err)
		}
	}
	rem.runOnce()

	plan, _ := planner.Plan("n1")
	if plan.Slots[0].ID != slotID || plan.Slots[0].ScheduledAt != 5000 {
		t.Fatalf("slot = %+v", plan.Slots[0])
	}
	if plan.Slots[0].NotifiedAt != 0 {
		t.Fatal("reminder stamped the rescheduled time")
	}
}

func TestReminderShutdownStopsLoop(t *testing.T) {
	rem, _, _, _, _ := newTestReminder(t)
	rem.interval = time.Hour
	if err := rem.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := rem.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
}
