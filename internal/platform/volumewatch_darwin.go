//go:build darwin

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
// Declarations only: this file exports a Go function to C, and cgo then
// forbids definitions in the preamble. The implementation is volumewatch_darwin.m.
void marmot_volume_watch_start(void);
void marmot_volume_watch_stop(void);
*/
import "C"

import (
	"errors"
	"sync"
)

var (
	volumeWatchMu       sync.Mutex
	volumeWatchCallback func()
)

// marmotVolumesChanged is called by the AppKit observers, on the main thread.
// It hands off to a goroutine so the main thread never waits on the
// application's lock; the callback itself only re-arms a debounce timer, and
// the re-read happens later, on the frontend's request.
//
//export marmotVolumesChanged
func marmotVolumesChanged() {
	volumeWatchMu.Lock()
	callback := volumeWatchCallback
	volumeWatchMu.Unlock()
	if callback != nil {
		go callback()
	}
}

// WatchVolumes implements ports.VolumeWatcher with NSWorkspace's mount,
// unmount and rename notifications. One watcher at a time: the process has one
// source page.
func (Adapter) WatchVolumes(onChange func()) (func(), error) {
	if onChange == nil {
		return nil, errors.New("volume watch needs a callback")
	}
	volumeWatchMu.Lock()
	if volumeWatchCallback != nil {
		volumeWatchMu.Unlock()
		return nil, errors.New("volume watch already running")
	}
	volumeWatchCallback = onChange
	volumeWatchMu.Unlock()
	C.marmot_volume_watch_start()
	return func() {
		// Disarm first, then unregister: a block already running when the
		// observer is removed then finds no callback to call.
		volumeWatchMu.Lock()
		volumeWatchCallback = nil
		volumeWatchMu.Unlock()
		C.marmot_volume_watch_stop()
	}, nil
}
