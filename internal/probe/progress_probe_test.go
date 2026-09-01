package probe

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

type sample struct {
	at       time.Duration
	nodes    int64
	files    int64
	bytes    int64
	counted  int64
	volume   uint64
	expected int64
	phase    string
}

// TestScanProgressCurve records what a progress bar could actually plot: the
// ScanProgress stream the UI already receives, against the volume's used bytes
// from statfs (the only total known before the walk ends).
func TestScanProgressCurve(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}

	var mu sync.Mutex
	samples := make([]sample, 0, 4096)
	start := time.Now()
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter, ScanTotals: adapter,
		Emit: func(name string, data any) {
			progress, ok := data.(marmotapp.ScanProgress)
			if !ok || name != "scan-progress" {
				return
			}
			mu.Lock()
			samples = append(samples, sample{at: time.Since(start), nodes: progress.Nodes, files: progress.Files, bytes: progress.Bytes, counted: progress.CountedBytes, volume: progress.VolumeUsedBytes, expected: progress.ExpectedTotalBytes, phase: progress.Phase})
			mu.Unlock()
		},
	})

	sources, err := service.GetStorageSources()
	if err != nil {
		t.Fatal(err)
	}
	var used, total uint64
	for _, source := range sources {
		if source.Path == root {
			used, total = source.UsedBytes, source.TotalBytes
		}
	}
	// The volume group's auxiliary volumes are known from statfs before the walk
	// starts and are part of the final tree, so a progress bar can count them at
	// t=0 instead of adding them in one jump at the end.
	// GetStorageSources aggregates the volume group and drops the auxiliary
	// volumes, so read the catalog directly.
	var preCounted int64
	catalog, err := adapter.ListVolumes()
	if err != nil {
		t.Fatal(err)
	}
	for _, volume := range catalog {
		if volume.Kind != "system_auxiliary" || volume.Path == root || !strings.HasPrefix(volume.Path, root) {
			continue
		}
		// Same rule as the service: a mount nested inside another volume's mount
		// (e.g. /System/Volumes/Update/mnt1) is unreachable by the walk and is
		// not pre-counted.
		nested := false
		for _, other := range catalog {
			if other.Path != root && other.Path != volume.Path && strings.HasPrefix(volume.Path, other.Path+"/") {
				nested = true
			}
		}
		if nested {
			continue
		}
		preCounted += int64(volume.UsedBytes)
	}

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
	visible := time.Since(start)

	mu.Lock()
	defer mu.Unlock()
	const gib = float64(1 << 30)
	t.Logf("PROBE root=%s used_gib=%.1f total_gib=%.1f pre_counted_gib=%.2f", root, float64(used)/gib, float64(total)/gib, float64(preCounted)/gib)
	t.Logf("PROBE final_bytes_gib=%.1f nodes=%d visible_s=%.3f samples=%d", float64(final.Bytes)/gib, final.Nodes, visible.Seconds(), len(samples))
	if used > 0 {
		// The terminal sample already carries the auxiliary volumes, so the bar's
		// ceiling is final.Bytes over used — not final plus the pre-count.
		t.Logf("PROBE final_bytes/used=%.4f  bar_ceiling=%.4f", float64(final.Bytes)/float64(used), float64(final.Bytes)/float64(used))
	}

	// The complaint this measures: the bar reads 100% while the walk is still
	// running, so everything after the crossing is a silent wait. Where exactly
	// is the crossing, and how much wall clock lies beyond it?
	uiDenominator := func(s sample) float64 {
		if s.expected > 0 {
			return float64(s.expected)
		}
		return float64(s.volume)
	}
	crossed := map[float64]time.Duration{}
	for _, s := range samples {
		if uiDenominator(s) == 0 {
			continue
		}
		frac := float64(s.counted) / uiDenominator(s)
		for _, mark := range []float64{0.95, 0.99, 1.0} {
			if _, seen := crossed[mark]; !seen && frac >= mark {
				crossed[mark] = s.at
			}
		}
	}
	for _, mark := range []float64{0.95, 0.99, 1.0} {
		if at, seen := crossed[mark]; seen {
			t.Logf("PROBE ui_crossing mark=%.2f at_s=%.2f remaining_s=%.2f", mark, at.Seconds(), (visible - at).Seconds())
		} else {
			t.Logf("PROBE ui_crossing mark=%.2f never reached", mark)
		}
	}

	// Where the curve is at each tenth of the wall clock: this is what decides
	// whether a proportional bar tells the truth about remaining time.
	monotonic := true
	var prev int64
	for _, s := range samples {
		if s.bytes < prev {
			monotonic = false
		}
		prev = s.bytes
	}
	t.Logf("PROBE bytes_monotonic=%v", monotonic)
	for decile := 1; decile <= 10; decile++ {
		cut := visible * time.Duration(decile) / 10
		var last sample
		for _, s := range samples {
			if s.at <= cut {
				last = s
			}
		}
		byteFrac := 0.0
		if final.Bytes > 0 {
			byteFrac = float64(last.bytes) / float64(final.Bytes)
		}
		// The bar a user would see: walked bytes plus the pre-counted volumes,
		// over the volume's used bytes.
		barFrac := 0.0
		if used > 0 {
			// The terminal sample already carries the auxiliary volumes, which
			// the scanner attaches in one shot at the end; adding the pre-count
			// again would double it.
			counted := last.bytes + preCounted
			if last.bytes >= final.Bytes {
				counted = final.Bytes
			}
			barFrac = float64(counted) / float64(used)
		}
		// What the UI actually receives.
		uiFrac := 0.0
		if uiDenominator(last) > 0 {
			uiFrac = float64(last.counted) / uiDenominator(last)
		}
		t.Logf("PROBE t=%3d%% phase=%-16s bytes_gib=%7.1f counted_gib=%7.1f vol_gib=%7.1f of_final=%.3f  ui=%.3f  bar=%.3f (drift %+.3f)",
			decile*10, last.phase, float64(last.bytes)/gib, float64(last.counted)/gib, float64(last.volume)/gib, byteFrac, uiFrac, barFrac, barFrac-float64(decile)/10)
	}
}
