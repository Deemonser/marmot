package probe

import (
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// TestAdaptiveMemoryLimit measures a memory limit that tracks the live heap
// instead of being a fixed number. A fixed GOMEMLIMIT is only safe while it
// stays above the live set, and the live set is a function of how many nodes
// the volume has — which is not known until the scan is over. A limit of
// live + headroom turns the gap between peak and steady state from a ratio
// (GOGC=100 gives 2x by construction) into a constant.
//
// Set PROBE_LIMIT_HEADROOM_MIB to the headroom; unset means no limit.
func TestAdaptiveMemoryLimit(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	headroom, _ := strconv.ParseInt(os.Getenv("PROBE_LIMIT_HEADROOM_MIB"), 10, 64)
	// A fixed limit, measured by the same probe so the two are comparable: the
	// baselines of two probes differ by more than the effect being measured.
	fixed, _ := strconv.ParseInt(os.Getenv("PROBE_FIXED_LIMIT_MIB"), 10, 64)
	if fixed > 0 {
		debug.SetMemoryLimit(fixed << 20)
	}

	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter,
	})

	stop := make(chan struct{})
	if headroom > 0 {
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-time.After(100 * time.Millisecond):
					// /gc/heap/live:bytes, not /memory/classes/heap/objects:bytes:
					// the latter counts dead objects that have not been swept yet,
					// so a limit derived from it always sits above where the heap
					// already is and does nothing.
					live := int64(metricValue("/gc/heap/live:bytes"))
					debug.SetMemoryLimit(live + headroom<<20)
				}
			}
		}()
	}

	started := time.Now()
	status, err := service.StartScan(marmotapp.ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	peakRSS, peakFootprint := 0.0, 0.0
	for {
		current, err := service.GetScanStatus(status.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if rss := selfRSSMiB(t); rss > peakRSS {
			peakRSS = rss
		}
		if footprint, _ := selfFootprintMiB(t); footprint > peakFootprint {
			peakFootprint = footprint
		}
		if current.State != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	elapsed := time.Since(started)
	close(stop)
	debug.SetMemoryLimit(1<<63 - 1)

	live := float64(metricValue("/gc/heap/live:bytes")) / float64(1<<20)
	allocated := float64(metricValue("/gc/heap/allocs:bytes")) / float64(1<<20)
	label := "no limit"
	switch {
	case headroom > 0:
		label = fmt.Sprintf("live + %d MiB", headroom)
	case fixed > 0:
		label = fmt.Sprintf("fixed %d MiB", fixed)
	}
	t.Logf("PROBE %-18s peak_footprint=%6.1f  peak_rss=%6.1f  live=%6.1f  allocated=%7.1f  wall=%5.2fs",
		label, peakFootprint, peakRSS, live, allocated, elapsed.Seconds())
	fmt.Fprintln(os.Stderr)
}
