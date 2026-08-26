package wails

import (
	"fmt"

	wailsapp "github.com/wailsapp/wails/v3/pkg/application"
)

// volumeMenuName is the context-menu name the frontend puts in its
// --custom-contextmenu CSS property. The @wailsio/runtime context-menu handler
// reads that property off the event target and asks the native side to open the
// menu with this name (ADR-0051).
const volumeMenuName = "volume-actions"

// VolumeMenuAction is emitted when the user picks an item in a volume row's
// native menu. The menu is only an input device: the frontend performs the
// action with the service methods it already uses, so the business rules and the
// error surface stay in one place (ADR-0051 §4).
type VolumeMenuAction struct {
	SourceID string `json:"sourceId"`
	Action   string `json:"action"`
}

// PrepareVolumeMenu rebuilds the native menu for one volume row and returns the
// name the frontend must trigger. It is rebuilt on every open so the item set
// always matches the row it belongs to (ADR-0051 §3).
func (s *Service) PrepareVolumeMenu(sourceID string, hasResult bool) (string, error) {
	if sourceID == "" {
		return "", fmt.Errorf("storage source is required")
	}
	app := wailsapp.Get()
	if app == nil {
		return "", fmt.Errorf("native menu is unavailable")
	}
	emit := func(action string) func(*wailsapp.Context) {
		return func(*wailsapp.Context) {
			app.Event.Emit("volume-menu", VolumeMenuAction{SourceID: sourceID, Action: action})
		}
	}
	menu := app.ContextMenu.New()
	menu.Add("重扫描").OnClick(emit("rescan"))
	if hasResult {
		menu.AddSeparator()
		menu.Add("放弃扫描结果").OnClick(emit("forget"))
	}
	menu.AddSeparator()
	menu.Add("在 Finder 中显示").OnClick(emit("reveal"))
	app.ContextMenu.Add(volumeMenuName, menu)
	return volumeMenuName, nil
}
