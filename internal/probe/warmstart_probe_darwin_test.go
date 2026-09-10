package probe

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// ADR-0075 gates 1 and 3 on the real volume: a cold scan writes the seed, a
// second service (fresh store, as a relaunch would be) starts the same root and
// should come up from the seed. Reports both wall times, whether the second
// start was warm, and how far the two trees' totals are apart.
//
//	PROBE_ROOT=/ go test ./internal/probe -run WarmStart -v -timeout 20m
func TestWarmStartOnRealVolume(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to scan a real volume")
	}
	seedDir := t.TempDir()
	adapter := platform.Adapter{}

	run := func(label string) (marmotapp.ScanStatus, time.Duration, bool) {
		store := memtree.OpenStore()
		defer store.Close()
		var mu sync.Mutex
		warm := false
		service := marmotapp.NewService(marmotapp.Dependencies{
			Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
			FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter, ScanTotals: adapter,
			FileEventHistory: adapter, SeedDir: seedDir,
			Emit: func(name string, data any) {
				if progress, ok := data.(marmotapp.ScanProgress); ok && name == "scan-progress" && progress.Warm {
					mu.Lock()
					warm = true
					mu.Unlock()
				}
			},
		})
		started := time.Now()
		status, err := service.StartScan(marmotapp.ScanOptions{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		var final marmotapp.ScanStatus
		for {
			final, err = service.GetScanStatus(status.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if final.State != "running" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		wall := time.Since(started)
		// The seed is written in the background after the result is shown; give
		// it time to land before the store goes away.
		seed := filepath.Join(seedDir, "current.seed")
		deadline := time.Now().Add(30 * time.Second)
		var lastSize int64 = -1
		for time.Now().Before(deadline) {
			info, err := os.Stat(seed)
			if err == nil && info.Size() == lastSize && lastSize > 0 {
				break
			}
			if err == nil {
				lastSize = info.Size()
			}
			time.Sleep(200 * time.Millisecond)
		}
		service.StopLiveUpdate()
		mu.Lock()
		defer mu.Unlock()
		t.Logf("PROBE %s: state=%s wall=%.3fs nodes=%d bytes=%d warm=%v", label, final.State, wall.Seconds(), final.Nodes, final.Bytes, warm)
		return final, wall, warm
	}

	cold, coldWall, _ := run("cold")
	if info, err := os.Stat(filepath.Join(seedDir, "current.seed")); err != nil {
		t.Fatalf("no seed after the cold scan: %v", err)
	} else {
		t.Logf("PROBE seed: %.1f MiB", float64(info.Size())/float64(1<<20))
	}
	warmStatus, warmWall, warm := run("warm")
	if !warm {
		t.Logf("PROBE second start was NOT warm (over budget, or replay unreliable): see the log lines above")
	}
	drift := float64(warmStatus.Bytes-cold.Bytes) / float64(max(cold.Bytes, 1)) * 100
	t.Logf("PROBE warm/cold wall = %.3fs / %.3fs (%.0f%%), nodes %d vs %d, bytes drift %.3f%%",
		warmWall.Seconds(), coldWall.Seconds(), 100*warmWall.Seconds()/coldWall.Seconds(), warmStatus.Nodes, cold.Nodes, drift)
	if warm && (drift > 0.5 || drift < -0.5) {
		t.Errorf("ADR-0075 gate 3: totals drifted %.3f%% between the cold tree and the warm one", drift)
	}
}
