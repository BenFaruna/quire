package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// Wails embeds everything in frontend/dist into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	notes := NewNotesService()
	publish := NewPublishService(notes)
	planner := NewPlannerService(notes, publish)
	notifier := notifications.New()
	app := application.New(application.Options{
		Name:        "Notes",
		Description: "A Markdown notes app built with Wails",
		Services: []application.Service{
			application.NewService(notes),
			application.NewService(publish),
			application.NewService(planner),
			application.NewService(notifier),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// The reminder service needs the application for its in-app event, so it is
	// registered after construction. Native notifications are best-effort.
	reminders := NewReminderService(planner, eventAlert{}, nativeAlert{notifier: notifier})
	app.RegisterService(application.NewService(reminders))

	go func() {
		if _, err := notifier.RequestNotificationAuthorization(); err != nil {
			log.Printf("planner: request notification authorization: %v", err)
		}
	}()

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Notes",
		Width:     1280,
		Height:    820,
		MinWidth:  380,
		MinHeight: 520,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(14, 17, 22),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
