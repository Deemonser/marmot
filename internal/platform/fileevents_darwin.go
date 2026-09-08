//go:build darwin

package platform

/*
#cgo LDFLAGS: -framework CoreServices -framework CoreFoundation
#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
// Declarations only: this file exports a Go function to C, and cgo then
// forbids definitions in the preamble. The implementation is fileevents_darwin.c.
int marmot_file_events_start(const char *root, double latency, uintptr_t handle);
void marmot_file_events_stop(void);
*/
import "C"

import (
	"errors"
	"runtime/cgo"
	"sync"
	"time"
	"unsafe"

	"example.com/marmot/internal/ports"
)

// FSEvents flag bits (FSEventStreamEventFlags), the ones the application acts on.
const (
	fsEventMustScanSubDirs = 0x00000001
	fsEventUserDropped     = 0x00000002
	fsEventKernelDropped   = 0x00000004
)

var fileEventsMu sync.Mutex
var fileEventsActive bool

type fileEventSink struct {
	onEvents func([]ports.FileEvent)
}

// marmotFileEvents is the FSEvents callback, on the stream's private dispatch
// queue. It only converts and forwards; batching and resolution against the
// tree are the application's job (ADR-0072).
//
//export marmotFileEvents
func marmotFileEvents(handle C.uintptr_t, count C.size_t, paths **C.char, flags *C.uint32_t) {
	sink, ok := cgo.Handle(handle).Value().(*fileEventSink)
	if !ok || sink == nil || count == 0 {
		return
	}
	n := int(count)
	rawPaths := unsafe.Slice(paths, n)
	rawFlags := unsafe.Slice(flags, n)
	events := make([]ports.FileEvent, 0, n)
	for i := 0; i < n; i++ {
		flag := uint32(rawFlags[i])
		events = append(events, ports.FileEvent{
			Path:            C.GoString(rawPaths[i]),
			MustScanSubDirs: flag&fsEventMustScanSubDirs != 0,
			Dropped:         flag&(fsEventUserDropped|fsEventKernelDropped) != 0,
		})
	}
	sink.onEvents(events)
}

// WatchFileEvents implements ports.FileEventWatcher on FSEvents: one stream on
// the scan root, directory-level events (no per-file flag: the application
// re-lists the directory anyway), coalesced by the system over `latency`,
// delivered on a private serial dispatch queue. One stream at a time: the
// process holds one result.
func (Adapter) WatchFileEvents(root string, latency time.Duration, onEvents func([]ports.FileEvent)) (func(), error) {
	if onEvents == nil || root == "" {
		return nil, errors.New("file event watch needs a root and a callback")
	}
	fileEventsMu.Lock()
	defer fileEventsMu.Unlock()
	if fileEventsActive {
		return nil, errors.New("file event watch already running")
	}
	handle := cgo.NewHandle(&fileEventSink{onEvents: onEvents})
	cRoot := C.CString(root)
	defer C.free(unsafe.Pointer(cRoot))
	if C.marmot_file_events_start(cRoot, C.double(latency.Seconds()), C.uintptr_t(handle)) != 0 {
		handle.Delete()
		return nil, errors.New("FSEventStream could not be created")
	}
	fileEventsActive = true
	return func() {
		fileEventsMu.Lock()
		defer fileEventsMu.Unlock()
		if !fileEventsActive {
			return
		}
		// Stop and invalidate synchronously on the stream's queue before the
		// handle goes: no callback can still be in flight afterwards.
		C.marmot_file_events_stop()
		fileEventsActive = false
		handle.Delete()
	}, nil
}
