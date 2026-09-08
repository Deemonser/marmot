//go:build darwin

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/marmot/internal/ports"
)

// The real bridge, end to end: a stream on a temp directory, one file written
// into a subdirectory, the subdirectory reported back (ADR-0072). Guards the
// dispatch-queue and cgo-handle plumbing that the application tests fake.
func TestWatchFileEventsReportsTheChangedDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "watched")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got := make(chan []ports.FileEvent, 16)
	stop, err := (Adapter{}).WatchFileEvents(root, 200*time.Millisecond, func(events []ports.FileEvent) { got <- events })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// Let the stream settle before the change it must catch.
	time.Sleep(300 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(sub, "file.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case events := <-got:
			for _, event := range events {
				if strings.TrimSuffix(event.Path, "/") == sub {
					return
				}
			}
		case <-deadline:
			t.Fatal("the changed directory was never reported")
		}
	}
}

func TestWatchFileEventsRefusesASecondStreamAndStopsCleanly(t *testing.T) {
	root := t.TempDir()
	stop, err := (Adapter{}).WatchFileEvents(root, time.Second, func([]ports.FileEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, second := (Adapter{}).WatchFileEvents(root, time.Second, func([]ports.FileEvent) {}); second == nil {
		t.Fatal("a second stream must be refused while one runs")
	}
	stop()
	stop() // idempotent
	again, err := (Adapter{}).WatchFileEvents(root, time.Second, func([]ports.FileEvent) {})
	if err != nil {
		t.Fatalf("after stop a new stream must start: %v", err)
	}
	again()
}
