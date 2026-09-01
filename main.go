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
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[wails.ScanProgress]("scan-progress")
	application.RegisterEvent[wails.CleanupProgress]("cleanup-progress")
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
		ScanTotals:     adapter,
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
			// Closing the window quits. It used to hide instead, with a reopen hook
			// to bring it back, which kept the in-memory scan result alive across a
			// close -- and there is nowhere else for it to live (ADR-0055).
			//
			// That trade is the wrong way round for this app. A 2.2M-node tree is
			// hundreds of MB of resident memory, and hiding leaves a process holding
			// it while the user believes they closed the program. A disk-space tool
			// silently occupying that much after being dismissed is the exact
			// complaint it exists to answer. ADR-0055 already accepts that a result
			// does not survive the process, so nothing durable is lost by exiting.
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	emit = func(name string, data any) {
		if progress, ok := data.(marmotapp.ScanProgress); ok {
			app.Event.Emit(name, wails.ScanProgressView(progress))
			return
		}
		app.Event.Emit(name, data)
	}

	// The window is born at its final size. The disk list and the permission
	// strip are known before the window exists (ListVolumes answers from the
	// identity cache in ~100µs, R-068 — only a machine with never-seen volumes
	// pays diskutil here), so the height App.tsx would otherwise correct on
	// its first renders is computed once, and locked: the source page's height
	// is the content's, never the user's, so it is not draggable (the frontend
	// re-locks it on every page transition). MinWidth sits above the 820px
	// stylesheet breakpoint, which exists for the browser preview — dragging
	// across it would snap the layout.
	// The constants mirror App.tsx sourceWindowSize / sourceRowHeight / the
	// 34px permission strip — change them together.
	sourceRows := 1
	if sources, err := core.GetStorageSources(); err == nil && len(sources) > 0 {
		sourceRows = len(sources)
	}
	initialHeight := 151 + (sourceRows-1)*54
	if core.GetPermissionStatus().State != "available" {
		initialHeight += 34
	}
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Marmot",
		Width:     968,
		Height:    initialHeight,
		MinWidth:  830,
		MinHeight: initialHeight,
		MaxHeight: initialHeight,
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
