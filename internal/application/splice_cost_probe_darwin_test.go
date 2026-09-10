package application

import (
	"os"
	"strings"
	"testing"
	"time"

	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/ports"
)

// ADR-0075 §7 first step: what does splicing a day's worth of changed
// directories into a finished tree cost? R-078 measured the replay (7-11 s for
// ~8,150 directories) but not the splice that follows it. This scans the real
// volume, replays the FSEvents journal from N hours ago, hands the resulting
// directory list to the same batch path the live update uses (ADR-0072 §3), and
// times that one batch.
//
// The scan here is fresh, so almost every directory in the list is already
// current and the splice does nothing but re-list and compare -- which is the
// warm-start case exactly: the seed is the previous scan, the replay names what
// moved since, and most of those directories are caches that changed again.
//
//	PROBE_ROOT=/ PROBE_SPLICE_AGE=24h go test ./internal/application -run SpliceCost -v -timeout 20m
func TestSpliceCost(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to scan a real volume")
	}
	age := 24 * time.Hour
	if raw := os.Getenv("PROBE_SPLICE_AGE"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("PROBE_SPLICE_AGE: %v", err)
		}
		age = parsed
	}

	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	// No FileEvents: the service must not start its own live update, because
	// this probe drives the batch by hand.
	service := NewService(Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter, ScanTotals: adapter,
		Emit: func(string, any) {},
	})
	// The replay must start from before the scan so that directories that
	// change during the scan are in the list too -- ADR-0075 §3.
	since, err := platform.FileEventIDBefore("/System/Volumes/Data", time.Now().Add(-age))
	if err != nil {
		t.Fatal(err)
	}
	scanStarted := time.Now()
	status, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var final ScanStatus
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
	scanWall := time.Since(scanStarted)
	t.Logf("PROBE root=%s scan_wall=%.3fs nodes=%d state=%s", root, scanWall.Seconds(), final.Nodes, final.State)

	report, err := adapter.FileEventHistory(root, since, 0, 180*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PROBE replay age=%s events=%d distinct_dirs=%d must_scan=%d elapsed=%.3fs reliable=%v",
		age, report.Events, len(report.Directories), report.MustScanSubDirs, report.Elapsed.Seconds(), report.Reliable())
	if !report.Reliable() {
		t.Fatalf("replay unreliable; pick a shorter PROBE_SPLICE_AGE")
	}

	events := make([]ports.FileEvent, 0, len(report.Directories))
	for path := range report.Directories {
		events = append(events, ports.FileEvent{Path: strings.TrimSuffix(path, "/")})
	}
	// Where does a splice spend its time? The batch does three things per
	// directory -- find the node by path, list the directory, reconcile -- and
	// R-072 only measured the last. Time the first two on their own first.
	reader := service.scanner.(ports.DirectoryReader)
	var lookupWall, readWall time.Duration
	var found, listed int
	for _, event := range events {
		lookupStarted := time.Now()
		node, err := store.NodeByPath(final.SnapshotID, event.Path)
		lookupWall += time.Since(lookupStarted)
		if err != nil || node.Kind != "directory" {
			continue
		}
		found++
		readStarted := time.Now()
		if _, _, err := reader.ReadDirectory(node.Path, node.VolumeID); err == nil {
			listed++
		}
		readWall += time.Since(readStarted)
	}
	t.Logf("PROBE breakdown: NodeByPath %d lookups %.3fs (%.2f ms each); ReadDirectory %d listings %.3fs (%.2f ms each)",
		len(events), lookupWall.Seconds(), 1000*lookupWall.Seconds()/float64(max(len(events), 1)),
		listed, readWall.Seconds(), 1000*readWall.Seconds()/float64(max(listed, 1)))
	_ = found

	updater := &liveUpdater{snapshotID: final.SnapshotID, root: root, pending: map[string]bool{}}
	service.liveEvents(updater, events)
	// liveEvents armed a timer; the batch is applied here by hand instead.
	updater.mu.Lock()
	if updater.timer != nil {
		updater.timer.Stop()
		updater.timer = nil
	}
	updater.mu.Unlock()

	spliceStarted := time.Now()
	service.applyLiveBatch(updater)
	spliceWall := time.Since(spliceStarted)
	t.Logf("PROBE splice: %d directories in %.3fs (%.2f ms/dir) touched=%d dirty=%d",
		len(events), spliceWall.Seconds(), 1000*spliceWall.Seconds()/float64(max(len(events), 1)), updater.directories, updater.dirty)
	t.Logf("PROBE warm start estimate for age=%s: load 0.3s + replay %.1fs + splice %.1fs = %.1fs  vs full scan %.1fs (%.0f%%)",
		age, report.Elapsed.Seconds(), spliceWall.Seconds(), 0.3+report.Elapsed.Seconds()+spliceWall.Seconds(), scanWall.Seconds(),
		100*(0.3+report.Elapsed.Seconds()+spliceWall.Seconds())/scanWall.Seconds())
}
