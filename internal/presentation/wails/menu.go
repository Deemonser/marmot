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

// nodeMenuName is the --custom-contextmenu value the result page puts on every
// arc and list row. A real right-click never reaches the runtime with it: the
// frontend stops the trusted event, rebuilds this menu for the node under the
// pointer, and re-dispatches a synthetic one (ADR-0069 §1).
const nodeMenuName = "node-actions"

// NodeMenuSpec is what the frontend already knows about the node it is about to
// open a menu for: the capabilities the map gave it, and its name for the two
// items that quote it. The Go side only lays the items out; it decides nothing
// about what the node may do (ADR-0069 §4).
type NodeMenuSpec struct {
	SnapshotID int64  `json:"snapshotId"`
	NodeID     int64  `json:"nodeId"`
	Name       string `json:"name"`
	CanEnter   bool   `json:"canEnter"`
	CanReveal  bool   `json:"canReveal"`
	CanCollect bool   `json:"canCollect"`
	Collected  bool   `json:"collected"`
}

// NodeMenuAction is emitted when the user picks an item in a node's menu. The
// frontend checks the node against the one it opened the menu for, then runs
// the same method the keyboard or a click would have (ADR-0069 §4).
type NodeMenuAction struct {
	SnapshotID int64  `json:"snapshotId"`
	NodeID     int64  `json:"nodeId"`
	Action     string `json:"action"`
}

// PrepareNodeMenu rebuilds the result page's node menu for one node and returns
// the name the frontend must trigger. The accelerator label is the original's
// hint and nothing more: a context menu's key equivalents only work while it is
// open, and the real ⌘⌫ path stays in the frontend (R-071 §3).
//
// No "预览" item: the original has one, but Quick Look from a menu was judged
// useless in use (2026-09-09) -- Space on a focused row does the same with no
// round trip. ADR-0069's revision note records the removal.
func (s *Service) PrepareNodeMenu(spec NodeMenuSpec) (string, error) {
	if spec.SnapshotID <= 0 || spec.NodeID <= 0 {
		return "", fmt.Errorf("snapshot and node are required")
	}
	if !spec.CanEnter && !spec.CanReveal && !spec.CanCollect && !spec.Collected {
		return "", fmt.Errorf("the node has no menu actions")
	}
	app := wailsapp.Get()
	if app == nil {
		return "", fmt.Errorf("native menu is unavailable")
	}
	emit := func(action string) func(*wailsapp.Context) {
		return func(*wailsapp.Context) {
			app.Event.Emit("node-menu", NodeMenuAction{SnapshotID: spec.SnapshotID, NodeID: spec.NodeID, Action: action})
		}
	}
	quoted := "“" + spec.Name + "”"
	menu := app.ContextMenu.New()
	if spec.CanEnter {
		menu.Add("展开 " + quoted).OnClick(emit("enter"))
	}
	if spec.CanReveal {
		menu.Add("在 Finder 中显示").OnClick(emit("reveal"))
		menu.Add("在终端中打开").OnClick(emit("terminal"))
	}
	if spec.Collected {
		menu.Add("将 " + quoted + " 移出收集站").SetAccelerator("cmd+backspace").OnClick(emit("collect"))
	} else if spec.CanCollect {
		menu.Add("将 " + quoted + " 移入收集站").SetAccelerator("cmd+backspace").OnClick(emit("collect"))
	}
	app.ContextMenu.Add(nodeMenuName, menu)
	return nodeMenuName, nil
}
