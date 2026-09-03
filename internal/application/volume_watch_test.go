package application

import (
	"sync"
	"testing"
	"time"
)

type fakeVolumeWatcher struct {
	onChange func()
	stopped  bool
}

func (watcher *fakeVolumeWatcher) WatchVolumes(onChange func()) (func(), error) {
	watcher.onChange = onChange
	return func() { watcher.stopped = true }, nil
}

type eventLog struct {
	mu    sync.Mutex
	names []string
}

func (log *eventLog) emit(name string, _ any) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.names = append(log.names, name)
}

func (log *eventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.names...)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestVolumeChangesAreCoalescedIntoOneEvent(t *testing.T) {
	previous := volumeChangeDebounce
	volumeChangeDebounce = 20 * time.Millisecond
	defer func() { volumeChangeDebounce = previous }()

	log := &eventLog{}
	watcher := &fakeVolumeWatcher{}
	service := NewService(Dependencies{VolumeWatcher: watcher, Emit: log.emit})
	service.StartVolumeWatch()
	if watcher.onChange == nil {
		t.Fatal("watcher not started")
	}
	// A mount produces a burst; the frontend should hear about it once.
	for range 5 {
		watcher.onChange()
	}
	waitFor(t, func() bool { return len(log.snapshot()) == 1 })
	time.Sleep(3 * volumeChangeDebounce)
	if got := log.snapshot(); len(got) != 1 || got[0] != StorageSourcesChangedEvent {
		t.Fatalf("expected one %q event, got %v", StorageSourcesChangedEvent, got)
	}
	// A second, separate change is a second event.
	watcher.onChange()
	waitFor(t, func() bool { return len(log.snapshot()) == 2 })

	service.StopVolumeWatch()
	if !watcher.stopped {
		t.Fatal("stop did not reach the watcher")
	}
}

func TestStopDropsAPendingChangeAndIgnoresLateOnes(t *testing.T) {
	previous := volumeChangeDebounce
	volumeChangeDebounce = 20 * time.Millisecond
	defer func() { volumeChangeDebounce = previous }()

	log := &eventLog{}
	watcher := &fakeVolumeWatcher{}
	service := NewService(Dependencies{VolumeWatcher: watcher, Emit: log.emit})
	service.StartVolumeWatch()
	watcher.onChange()
	service.StopVolumeWatch()
	// The observer may still deliver one it had in flight.
	watcher.onChange()
	time.Sleep(4 * volumeChangeDebounce)
	if got := log.snapshot(); len(got) != 0 {
		t.Fatalf("stopped watch still emitted %v", got)
	}
}

func TestStartVolumeWatchWithoutAWatcherIsANoOp(t *testing.T) {
	service := NewService(Dependencies{})
	service.StartVolumeWatch()
	service.StopVolumeWatch()
}
