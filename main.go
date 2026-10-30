package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails embeds everything in frontend/dist into the binary.
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	notes := NewNotesService()
	app := application.New(application.Options{
		Name:        "Notes",
		Description: "A Markdown notes app built with Wails",
		Services: []application.Service{
			application.NewService(notes),
			application.NewService(NewPublishService(notes)),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Notes",
		Width:     1280,
		Height:    820,
		MinWidth:  860,
		MinHeight: 560,
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
