package application

import (
	"context"
	"errors"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/ports"
)

// currentMemoryLimit reads the runtime's soft limit without changing it: a
// negative argument makes SetMemoryLimit a pure getter.
func currentMemoryLimit() int64 { return debug.SetMemoryLimit(-1) }

func restoreMemoryLimitAfterTest(t *testing.T) {
	t.Helper()
	before := currentMemoryLimit()
	t.Cleanup(func() { debug.SetMemoryLimit(before) })
}

func waitForLimit(t *testing.T, want func(int64) bool, reason string) int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if limit := currentMemoryLimit(); want(limit) {
			return limit
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the memory limit: %s (limit is %d)", reason, currentMemoryLimit())
	return 0
}

func TestScanMemoryLimitTracksLiveHeapAndIsRestored(t *testing.T) {
	restoreMemoryLimitAfterTest(t)
	debug.SetMemoryLimit(noMemoryLimit)

	var limiter scanMemoryLimiter
	release := limiter.hold()
	limit := waitForLimit(t, func(value int64) bool { return value != noMemoryLimit }, "a held scan should lower it below the default")
	if live := liveHeapBytes(); limit < live {
		t.Fatalf("the limit %d is below the live heap %d, which would make the collector spin", limit, live)
	}
	if live := liveHeapBytes(); limit > live+2*scanMemoryHeadroom {
		t.Fatalf("the limit %d is more than the headroom above the live heap %d", limit, live)
	}

	release()
	if got := currentMemoryLimit(); got != noMemoryLimit {
		t.Fatalf("the limit is %d after release, want it restored to %d; this knob is process-global", got, int64(noMemoryLimit))
	}
	// Releasing twice must not disturb a later scan.
	release()
	if got := currentMemoryLimit(); got != noMemoryLimit {
		t.Fatalf("a second release changed the limit to %d", got)
	}
}

// TestScanMemoryLimitSurvivesAConcurrentScanFinishing covers the case a plain
// defer would get wrong: one scan ending must not lift the limit for another
// that is still running.
func TestScanMemoryLimitSurvivesAConcurrentScanFinishing(t *testing.T) {
	restoreMemoryLimitAfterTest(t)
	debug.SetMemoryLimit(noMemoryLimit)

	var limiter scanMemoryLimiter
	first := limiter.hold()
	second := limiter.hold()
	waitForLimit(t, func(value int64) bool { return value != noMemoryLimit }, "two held scans should lower it")

	first()
	if got := currentMemoryLimit(); got == noMemoryLimit {
		t.Fatal("the limit was restored while a second scan was still running")
	}
	second()
	if got := currentMemoryLimit(); got != noMemoryLimit {
		t.Fatalf("the limit is %d after the last scan released, want %d", got, int64(noMemoryLimit))
	}
}

type memoryLimitScanner struct {
	observed chan int64
	fail     error
	block    chan struct{}
}

func (s *memoryLimitScanner) Scan(ctx context.Context, root string, emit scan.Emitter, _ scan.PhaseEmitter) (scan.Result, error) {
	if err := emit(scan.Node{ID: 1, Path: root, Name: filepath.Base(root), Kind: "directory", HasChildren: true}); err != nil {
		return scan.Result{}, err
	}
	// Reported from inside the scan, which is the only place the limit is
	// expected to be held.
	select {
	case s.observed <- currentMemoryLimit():
	default:
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return scan.Result{}, ctx.Err()
		}
	}
	if s.fail != nil {
		return scan.Result{}, s.fail
	}
	return scan.Result{Nodes: 1, Directories: 1}, nil
}

func serviceWithScanner(t *testing.T, scanner ports.Scanner) *Service {
	t.Helper()
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	return NewService(Dependencies{Store: store, Scanner: scanner, FileSystem: adapter, Permissions: adapter, Trash: adapter})
}

// TestScanMemoryLimitIsRestoredOnEveryExit is the gate ADR-0058 §验收 2 asks
// for: the limit is process-global, so leaving it set would change how the rest
// of the app collects for the remaining life of the process.
func TestScanMemoryLimitIsRestoredOnEveryExit(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		fail   error
		cancel bool
	}{
		{name: "completed"},
		{name: "failed", fail: errors.New("scan blew up")},
		{name: "cancelled", cancel: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			restoreMemoryLimitAfterTest(t)
			debug.SetMemoryLimit(noMemoryLimit)

			fake := &memoryLimitScanner{observed: make(chan int64, 1), fail: testCase.fail}
			if testCase.cancel {
				fake.block = make(chan struct{})
			}
			service := serviceWithScanner(t, fake)
			status, err := service.StartScan(ScanOptions{Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			var heldDuringScan int64
			if testCase.cancel {
				// Wait until the scan is inside Scan before cancelling it.
				heldDuringScan = <-fake.observed
				if _, err := service.CancelScan(status.TaskID); err != nil {
					t.Fatal(err)
				}
				close(fake.block)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				current, err := service.GetScanStatus(status.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if current.State != "running" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the scan never left the running state")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !testCase.cancel {
				select {
				case heldDuringScan = <-fake.observed:
				default:
					t.Fatal("the scanner never reported the limit it ran under")
				}
			}
			// Without this the restore assertion would pass trivially: a limit
			// that was never set is indistinguishable from one that was restored.
			if heldDuringScan == noMemoryLimit {
				t.Fatal("the scan ran without a memory limit; nothing was held to restore")
			}
			if got := waitForLimit(t, func(value int64) bool { return value == noMemoryLimit }, "the limit must be restored when the scan exits"); got != noMemoryLimit {
				t.Fatalf("the limit is %d, want %d", got, int64(noMemoryLimit))
			}
		})
	}
}
