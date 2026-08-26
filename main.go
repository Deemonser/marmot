package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/presentation/wails"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[wails.ScanProgress]("scan-progress")
	application.RegisterEvent[wails.VolumeMenuAction]("volume-menu")
}

func main() {
	// The result lives in memory for as long as this process does, and nowhere
	// else (ADR-0055).
	store := memtree.OpenStore()
	defer store.Close()

	adapter := platform.Adapter{}
	var emit func(string, any)
	legacyCacheDir, err := appCacheDir()
	if err != nil {
		log.Printf("app cache directory unavailable, skipping legacy cleanup: %v", err)
	}
	core := marmotapp.NewService(marmotapp.Dependencies{
		LegacyCacheDir: legacyCacheDir,
		Store:          store,
		Scanner:        scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem:     adapter,
		Permissions:    adapter,
		Trash:          adapter,
		Volumes:        adapter,
		Preview:        adapter,
		Emit: func(name string, data any) {
			if emit != nil {
				emit(name, data)
			}
		},
	})
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
		Title:     "Marmot",
		Width:     968,
		Height:    151,
		MinWidth:  760,
		MinHeight: 140,
		Mac: application.MacWindow{
			// A native invisible titlebar would sit on top of the webview and
			// swallow every click in the first N pixels — which is exactly where
			// the breadcrumbs are. Dragging is declared in CSS instead
			// (--wails-draggable), so the chrome stays clickable.
			InvisibleTitleBarHeight: 0,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(43, 44, 49),
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

// appCacheDir is the app's own cache directory. The scan result never goes there
// any more (ADR-0055); it is only needed for the one-shot cleanup of what the
// superseded SQLite store left behind (ADR-0054).
func appCacheDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "marmot"), nil
}
