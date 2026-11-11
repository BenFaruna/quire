package main

import (
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// Wails embeds everything in frontend/dist into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// `quire mcp` runs the agent-facing MCP server over stdin/stdout.
	// It shares the storage and business rules with the GUI but never starts
	// the webview, so it works while the app is closed.
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := runMCP(); err != nil {
			log.Fatal(err)
		}
		return
	}

	releaseSIGUSR1()

	notes := NewNotesService()
	publish := NewPublishService(notes)
	planner := NewPlannerService(notes, publish)
	tracker := newWriteTracker()
	notes.writes = tracker
	notifier := notifications.New()
	app := application.New(application.Options{
		Name:        appName,
		Description: "A local-first Markdown notebook built with Wails",
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
	app.RegisterService(application.NewService(NewNotesWatcher(notes, tracker)))

	go func() {
		if _, err := notifier.RequestNotificationAuthorization(); err != nil {
			log.Printf("planner: request notification authorization: %v", err)
		}
	}()

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     appName,
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
