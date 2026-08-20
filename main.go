package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/presentation/wails"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[wails.ScanProgress]("scan-progress")
}

func main() {
	store, err := openSnapshotStore()
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	adapter := platform.Adapter{}
	var emit func(string, any)
	core := marmotapp.NewService(marmotapp.Dependencies{
		Store:       store,
		Scanner:     scanner.Scanner{},
		FileSystem:  adapter,
		Permissions: adapter,
		Trash:       adapter,
		Volumes:     adapter,
		Preview:     adapter,
		Emit: func(name string, data any) {
			if emit != nil {
				emit(name, data)
			}
		},
	})
	if err := core.RecoverInterruptedScans(); err != nil {
		log.Fatal(err)
	}
	service := wails.NewService(core)
	app := application.New(application.Options{
		Name:        "Marmot",
		Description: "macOS disk space analysis and safe cleanup",
		Services:    []application.Service{application.NewService(service)},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})
	emit = func(name string, data any) {
		if progress, ok := data.(marmotapp.ScanProgress); ok {
			app.Event.Emit(name, wails.ScanProgressView(progress))
			return
		}
		app.Event.Emit(name, data)
	}

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Marmot",
		Width:  1180,
		Height: 760,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(16, 20, 24),
		URL:              "/",
	})
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		window.Hide()
		event.Cancel()
	})
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		window.Show()
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func openSnapshotStore() (*snapshot.Store, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return snapshot.Open(filepath.Join(cacheDir, "marmot", "snapshots.db"))
}
