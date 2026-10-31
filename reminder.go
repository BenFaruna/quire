package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// alertSender delivers one due reminder. The in-app event is required; the
// native notification is a best-effort enhancement.
type alertSender interface {
	send(item QueueItem) error
}

// eventAlert pushes the reminder to the frontend, which shows the persistent
// banner with Copy and Open actions.
type eventAlert struct{}

func (eventAlert) send(item QueueItem) error {
	app := application.Get()
	if app == nil {
		return errors.New("application is not running")
	}
	app.Event.Emit("planner:due", item)
	return nil
}

// nativeAlert wraps the platform notifier.
type nativeAlert struct {
	notifier *notifications.NotificationService
}

func (a nativeAlert) send(item QueueItem) error {
	if a.notifier == nil {
		return errors.New("native notifications are not available")
	}
	body := item.Preview
	if body == "" {
		body = item.Title
	}
	return a.notifier.SendNotification(notifications.NotificationOptions{
		ID:    item.NoteID + ":" + item.SlotID,
		Title: "Time to post",
		Body:  body,
	})
}

// ReminderService polls due slots and delivers one alert each. It recomputes
// due slots from disk, so reminders survive an app restart.
type ReminderService struct {
	planner  *PlannerService
	alert    alertSender
	native   alertSender
	now      func() time.Time
	interval time.Duration

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
}

func NewReminderService(planner *PlannerService, alert, native alertSender) *ReminderService {
	return &ReminderService{
		planner:  planner,
		alert:    alert,
		native:   native,
		now:      time.Now,
		interval: 30 * time.Second,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (s *ReminderService) ServiceName() string { return "ReminderService" }

func (s *ReminderService) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	s.mu.Unlock()

	go s.loop(ctx)
	return nil
}

func (s *ReminderService) ServiceShutdown() error {
	s.mu.Lock()
	started := s.started
	s.started = false
	s.mu.Unlock()
	if !started {
		return nil
	}
	close(s.stop)
	<-s.done
	return nil
}

func (s *ReminderService) loop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.runOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			s.runOnce()
		}
	}
}

// runOnce delivers every due reminder. A failed in-app send leaves NotifiedAt
// unstamped so the next tick retries; a failed native send is only logged.
func (s *ReminderService) runOnce() {
	if s.alert == nil {
		return
	}
	items, err := s.planner.Due()
	if err != nil {
		log.Printf("planner: read due slots: %v", err)
		return
	}
	now := s.now().UnixMilli()
	for _, item := range items {
		if err := s.alert.send(item); err != nil {
			log.Printf("planner: send reminder for %s: %v", item.NoteID, err)
			continue
		}
		if s.native != nil {
			if err := s.native.send(item); err != nil {
				log.Printf("planner: native reminder for %s: %v", item.NoteID, err)
			}
		}
		if err := s.planner.markNotified(item.NoteID, item.SlotID, item.ScheduledAt, now); err != nil {
			log.Printf("planner: stamp reminder for %s: %v", item.NoteID, err)
		}
	}
}
