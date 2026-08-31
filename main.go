package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/advisor/openaicompat"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/ports"
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

	// Before anything else that could fail, so the failure is written down.
	if logFile, logPath, logErr := platform.OpenLog(); logErr != nil {
		log.Printf("无法写入日志文件，仅输出到 stderr: %v", logErr)
	} else {
		defer logFile.Close()
		log.Printf("日志文件: %s", logPath)
	}

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
		Credentials:    adapter,
		// The composition root is where a transport is chosen; the application
		// layer only ever sees the port.
		AdvisorFactory: buildAdvisor,
		Emit: func(name string, data any) {
			if emit != nil {
				emit(name, data)
			}
		},
	})
	// Whatever the user configured last time. A missing configuration is the
	// shipping state, not an error: the advice feature is then the local rule
	// layer and the app makes no network request at all (ADR-0061 §4).
	core.RestoreAdvisor()

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

// buildAdvisor turns stored settings into a transport. One OpenAI-compatible
// client covers every provider the product targets, so "支持多家" is a
// configuration field rather than a second adapter (ADR-0061 §5).
func buildAdvisor(settings marmotapp.AdvisorSettings, apiKey string) (ports.Advisor, error) {
	switch settings.Provider {
	case marmotapp.ProviderOpenAICompatible:
		return openaicompat.New(openaicompat.Config{
			BaseURL:         settings.BaseURL,
			Model:           settings.Model,
			APIKey:          apiKey,
			JSONMode:        settings.JSONMode,
			ReasoningEffort: settings.ReasoningEffort,
		})
	default:
		return nil, fmt.Errorf("不支持的 provider: %q", settings.Provider)
	}
}
