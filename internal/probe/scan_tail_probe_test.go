package probe

import (
	"fmt"
	"os"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// TestScanTail measures what the user sees as "the bar stopped, then the page
// changed": how far the counted bytes get, and how long the run keeps going
// after they stop moving.
func TestScanTail(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	const gb = float64(1e9)
	adapter := platform.Adapter{}
	store := memtree.OpenStore()
	defer store.Close()
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter,
	})

	started := time.Now()
	status, err := service.StartScan(marmotapp.ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var denominatorAt time.Duration
	var lastGrowth time.Duration
	var peakCounted, denominator int64
	var lastPhase string
	phaseAt := map[string]time.Duration{}
	for {
		current, err := service.GetScanStatus(status.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Phase != lastPhase {
			lastPhase = current.Phase
			if _, seen := phaseAt[current.Phase]; !seen {
				phaseAt[current.Phase] = time.Since(started)
			}
		}
		if denominator == 0 && current.VolumeUsedBytes > 0 {
			denominator = int64(current.VolumeUsedBytes)
			denominatorAt = time.Since(started)
		}
		if current.CountedBytes > peakCounted {
			peakCounted = current.CountedBytes
			lastGrowth = time.Since(started)
		}
		if current.State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	terminal := time.Since(started)

	t.Logf("PROBE denominator known at    %7.3fs  (%.1f GB)", denominatorAt.Seconds(), float64(denominator)/gb)
	for _, phase := range []string{"catalog", "volume_overview", "top_level_publish", "deep_scan"} {
		if at, ok := phaseAt[phase]; ok {
			t.Logf("PROBE phase %-18s %7.3fs", phase, at.Seconds())
		}
	}
	t.Logf("PROBE counted stopped at      %7.3fs  (%.1f GB)", lastGrowth.Seconds(), float64(peakCounted)/gb)
	t.Logf("PROBE terminal state at       %7.3fs", terminal.Seconds())
	t.Logf("PROBE ---")
	t.Logf("PROBE bar tops out at         %7.1f%%   <- what the user sees it stop at", 100*float64(peakCounted)/float64(denominator))
	t.Logf("PROBE frozen tail             %7.3fs  <- bar still, page not switched yet", (terminal - lastGrowth).Seconds())
	fmt.Fprintln(os.Stderr)
}
