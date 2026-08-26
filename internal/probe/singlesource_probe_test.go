package probe

import (
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"runtime/metrics"
	"sort"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// heapMiB reports *retained* heap. The GC has to run first: without it the
// sample includes garbage the scanner produced and nothing has collected yet,
// which is the difference between "what we keep" and "what we allocated".
// The STW pause is why this must never be sampled during a timed scan
// (R-047 §3.11).
func heapMiB() float64 {
	runtime.GC()
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	return float64(sample[0].Value.Uint64()) / (1 << 20)
}

func countArcs(entries []marmotapp.MapEntry) int {
	total := 0
	var walk func(children []marmotapp.ProjectedEntry)
	walk = func(children []marmotapp.ProjectedEntry) {
		for _, child := range children {
			total++
			walk(child.Children)
		}
	}
	for _, entry := range entries {
		total++
		walk(entry.Children)
	}
	return total
}

func TestSingleSourceEndToEnd(t *testing.T) {
	if os.Getenv("PROBE_ROOT") == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	root := os.Getenv("PROBE_ROOT")
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter,
	})

	base := heapMiB()
	start := time.Now()
	status, err := service.StartScan(marmotapp.ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	var visible marmotapp.ScanStatus
	for {
		visible, err = service.GetScanStatus(status.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if visible.State != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	visibleAt := time.Since(start)
	heapVisible := heapMiB()

	// Depth and MinSweeps as the renderer sends them (ADR-0059 §3): twelve rings,
	// with the store pruning arcs the renderer could not show. Sending Depth
	// without MinSweeps is what used to blow the payload ceiling.
	query := marmotapp.MapQuery{
		SnapshotID: visible.SnapshotID, ParentID: 1, Limit: 256,
		Depth: 11, ProjectionLimit: 2000, MinSweeps: renderMinSweeps(11),
	}
	firstStart := time.Now()
	first, err := service.GetMap(query)
	if err != nil {
		t.Fatalf("first map: %v", err)
	}
	firstMS := float64(time.Since(firstStart).Microseconds()) / 1000

	payload, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}

	durableAt := time.Since(start)
	after, err := service.GetMap(query)
	if err != nil {
		t.Fatalf("map after durable: %v", err)
	}
	identical := reflect.DeepEqual(first, after)

	latencies := make([]float64, 0, 40)
	for round := 0; round < 40; round++ {
		at := time.Now()
		if _, err := service.GetMap(query); err != nil {
			t.Fatal(err)
		}
		latencies = append(latencies, float64(time.Since(at).Microseconds())/1000)
	}
	sort.Float64s(latencies)

	// ADR-0052 accounting: the root level must add up to the disk.
	const gb = 1e9
	t.Logf("PROBE volume total=%.1f GB used=%.1f GB free=%.1f GB", float64(first.VolumeTotalBytes)/gb, float64(first.VolumeUsedBytes)/gb, float64(first.VolumeFreeBytes)/gb)
	var childSum int64
	for _, entry := range first.Entries {
		childSum += entry.OwnedAllocated
		label := entry.Name
		if entry.Kind == "node" {
			label = entry.Node.Name
		}
		t.Logf("PROBE   %-22s %8.1f GB  kind=%-9s virtual=%-14s basis=%s", label, float64(entry.OwnedAllocated)/gb, entry.Kind, entry.VirtualType, entry.SizeBasis)
	}
	t.Logf("PROBE root_total=%.1f GB child_sum=%.1f GB used=%.1f GB balance_gap=%.2f GB",
		float64(first.Parent.OwnedAllocated)/gb, float64(childSum)/gb, float64(first.VolumeUsedBytes)/gb,
		(float64(childSum)-float64(first.VolumeUsedBytes))/gb)
	t.Logf("PROBE capacity_check used+free=%.1f GB total=%.1f GB",
		float64(first.VolumeUsedBytes+first.VolumeFreeBytes)/gb, float64(first.VolumeTotalBytes)/gb)

	covered, total, err := store.ProjectionCoverage(visible.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PROBE root=%s state=%s nodes=%d dirs=%d", root, visible.State, visible.Nodes, visible.Directories)
	t.Logf("PROBE visible_terminal_s=%.3f durable_complete_s=%.3f", visibleAt.Seconds(), durableAt.Seconds())
	t.Logf("PROBE coverage=%d/%d arcs=%d payload_kib=%.1f density_truncated=%v", covered, total, countArcs(first.Entries), float64(len(payload))/1024, first.DensityTruncated)
	t.Logf("PROBE first_map_ms=%.2f p50_ms=%.2f p95_ms=%.2f", firstMS, latencies[len(latencies)/2], latencies[int(float64(len(latencies))*0.95)])
	t.Logf("PROBE map_identical_across_durable=%v", identical)
	t.Logf("PROBE heap_base_mib=%.1f heap_at_visible_mib=%.1f heap_after_durable_mib=%.1f", base, heapVisible, heapMiB())
	if !identical {
		t.Errorf("the map changed when the durable publish landed")
	}
}

// renderMinSweeps mirrors frontend/src/sunburst.ts: the same ring ratios, so the
// probe measures the payload the renderer will actually ask for. Two copies of
// these numbers can drift, which is why ADR-0059 §3 records the coupling.
func renderMinSweeps(depth int) []float64 {
	const (
		viewRadius     = 296.0
		mainRings      = 5
		maxDepth       = 12
		hubRatio       = 1.38
		thinRingRatio  = 0.147
		thinGapRatio   = 0.46
		radialGapRatio = 1.0 / 33.5
		minArcPixels   = 2.5
	)
	units := hubRatio
	for level := 0; level < maxDepth; level++ {
		if level < mainRings {
			units += 1
		} else {
			units += thinRingRatio
		}
		if level == maxDepth-1 {
			break
		}
		if level < mainRings {
			units += radialGapRatio
		} else {
			units += thinRingRatio * thinGapRatio
		}
	}
	ringWidth := viewRadius / units
	thinRing := ringWidth * thinRingRatio
	sweeps := make([]float64, 0, depth)
	for level := 1; level <= depth; level++ {
		r0 := ringWidth * hubRatio
		for inner := 0; inner < level; inner++ {
			if inner < mainRings {
				r0 += ringWidth + ringWidth*radialGapRatio
			} else {
				r0 += thinRing + thinRing*thinGapRatio
			}
		}
		thickness := ringWidth
		if level >= mainRings {
			thickness = thinRing
		}
		sweeps = append(sweeps, minArcPixels/((r0+r0+thickness)/2))
	}
	return sweeps
}
