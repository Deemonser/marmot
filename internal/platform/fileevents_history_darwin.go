//go:build darwin

package platform

/*
#cgo LDFLAGS: -framework CoreServices -framework CoreFoundation
#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
// Declarations only, same reason as fileevents_darwin.go: this file exports a
// Go callback to C, so the definitions live in fileevents_history_darwin.c.
void *marmot_file_event_history_start(const char *root, uint64_t since, double latency, uintptr_t handle);
void marmot_file_event_history_stop(void *opaque);
uint64_t marmot_file_event_current_id(void);
uint64_t marmot_file_event_id_before(int32_t device, double posix_time);
int marmot_file_event_device_uuid(int32_t device, char *out, size_t capacity);
*/
import "C"

import (
	"errors"
	"os"
	"runtime/cgo"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"example.com/marmot/internal/ports"
)

// FSEvents flag bits the history replay acts on, beyond the three the live
// watcher already knows. Wrapped and RootChanged only matter when replaying
// from a stored ID: a stream started "since now" can never see either.
const (
	fsEventEventIDsWrapped = 0x00000008
	fsEventHistoryDone     = 0x00000010
	fsEventRootChanged     = 0x00000020
)

type fileEventHistorySink struct {
	mu     sync.Mutex
	report ports.FileEventHistoryReport
	done   chan struct{}
	closed bool
}

// marmotFileEventHistory is the history stream's callback, on its private
// dispatch queue. It accumulates and, on HistoryDone, releases the waiter.
//
//export marmotFileEventHistory
func marmotFileEventHistory(handle C.uintptr_t, count C.size_t, paths **C.char, flags *C.uint32_t, ids *C.uint64_t) {
	sink, ok := cgo.Handle(handle).Value().(*fileEventHistorySink)
	if !ok {
		return
	}
	n := int(count)
	rawPaths := unsafe.Slice(paths, n)
	rawFlags := unsafe.Slice(flags, n)
	rawIDs := unsafe.Slice(ids, n)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed {
		return
	}
	for i := range n {
		flag := uint32(rawFlags[i])
		if flag&fsEventHistoryDone != 0 {
			sink.report.HistoryDone = true
			sink.closed = true
			close(sink.done)
			return
		}
		id := uint64(rawIDs[i])
		if sink.report.Events == 0 {
			sink.report.FirstID = id
		}
		sink.report.LastID = id
		sink.report.Events++
		if flag&fsEventMustScanSubDirs != 0 {
			sink.report.MustScanSubDirs++
		}
		if flag&fsEventEventIDsWrapped != 0 {
			sink.report.IDsWrapped++
		}
		if flag&fsEventUserDropped != 0 {
			sink.report.UserDropped++
		}
		if flag&fsEventKernelDropped != 0 {
			sink.report.KernelDropped++
		}
		if flag&fsEventRootChanged != 0 {
			sink.report.RootChanged++
		}
		path := C.GoString(rawPaths[i])
		sink.report.Directories[path]++
		if flag&fsEventMustScanSubDirs != 0 {
			sink.report.SubtreeRescan[path] = struct{}{}
		}
	}
}

// FileEventHistory replays the FSEvents journal for root from the event ID
// `since` until the system reports HistoryDone or `timeout` passes, whichever
// comes first. Live events that arrive after HistoryDone are not counted.
// `latency` is the stream's coalescing interval, passed through so its effect
// on replay pacing can be measured.
func (Adapter) FileEventHistory(root string, since uint64, latency, timeout time.Duration) (ports.FileEventHistoryReport, error) {
	if root == "" {
		return ports.FileEventHistoryReport{}, errors.New("file event history needs a root")
	}
	sink := &fileEventHistorySink{done: make(chan struct{})}
	sink.report.Since = since
	sink.report.Directories = make(map[string]uint64)
	sink.report.SubtreeRescan = make(map[string]struct{})
	handle := cgo.NewHandle(sink)
	cRoot := C.CString(root)
	started := time.Now()
	stream := C.marmot_file_event_history_start(cRoot, C.uint64_t(since), C.double(latency.Seconds()), C.uintptr_t(handle))
	C.free(unsafe.Pointer(cRoot))
	if stream == nil {
		handle.Delete()
		return ports.FileEventHistoryReport{}, errors.New("FSEventStream for history could not be created")
	}
	timer := time.NewTimer(timeout)
	select {
	case <-sink.done:
	case <-timer.C:
	}
	timer.Stop()
	// Stop synchronously on the stream's queue, then take the report under the
	// sink lock: after stop returns no callback is in flight.
	C.marmot_file_event_history_stop(stream)
	sink.mu.Lock()
	sink.closed = true
	report := sink.report
	report.Elapsed = time.Since(started)
	sink.mu.Unlock()
	handle.Delete()
	return report, nil
}

// FileEventCurrentID is the journal's "now": the ID a scan records before it
// starts so that everything that changes during and after it replays next time.
func FileEventCurrentID() uint64 {
	return uint64(C.marmot_file_event_current_id())
}

// FileEventIDBefore is the last event ID the device holding path had issued
// before `at`. Conservative in the direction that matters: replaying from it
// may deliver a few extra events, never miss one.
func FileEventIDBefore(path string, at time.Time) (uint64, error) {
	device, err := deviceOf(path)
	if err != nil {
		return 0, err
	}
	return uint64(C.marmot_file_event_id_before(C.int32_t(device), C.double(float64(at.UnixNano())/1e9))), nil
}

// FileEventDeviceUUID identifies the journal behind the device holding path.
func FileEventDeviceUUID(path string) (string, error) {
	device, err := deviceOf(path)
	if err != nil {
		return "", err
	}
	buffer := make([]byte, 64)
	if C.marmot_file_event_device_uuid(C.int32_t(device), (*C.char)(unsafe.Pointer(&buffer[0])), C.size_t(len(buffer))) != 0 {
		return "", errors.New("no FSEvents UUID for device")
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buffer[0]))), nil
}

func deviceOf(path string) (int32, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("no stat for path")
	}
	return stat.Dev, nil
}

// CurrentFileEventID implements ports.FileEventHistorian.
func (Adapter) CurrentFileEventID() uint64 { return FileEventCurrentID() }

// FileEventJournalIdentity implements ports.FileEventHistorian: the device
// holding path and the UUID of the journal issuing its IDs (ADR-0075 §2).
func (Adapter) FileEventJournalIdentity(path string) (uint64, string, error) {
	device, err := deviceOf(path)
	if err != nil {
		return 0, "", err
	}
	uuid, err := FileEventDeviceUUID(path)
	if err != nil {
		return 0, "", err
	}
	return uint64(device), uuid, nil
}
