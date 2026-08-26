package probe

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

func metricValue(name string) uint64 {
	sample := []metrics.Sample{{Name: name}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return sample[0].Value.Uint64()
}

func selfRSSMiB(t *testing.T) float64 {
	t.Helper()
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kb, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return kb / 1024
}

// selfFootprintMiB reads the number Activity Monitor shows in its Memory
// column. It is not RSS: Darwin moves MADV_FREE_REUSABLE pages out of the
// footprint while they stay resident, so the two diverge exactly where the Go
// runtime holds freed spans.
func selfFootprintMiB(t *testing.T) (current float64, peak float64) {
	t.Helper()
	out, err := exec.Command("vmmap", "--summary", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0, 0
	}
	parse := func(line string) float64 {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return 0
		}
		raw := fields[len(fields)-1]
		scale := 1.0 / 1024
		switch {
		case strings.HasSuffix(raw, "K"):
			raw = strings.TrimSuffix(raw, "K")
		case strings.HasSuffix(raw, "M"):
			raw, scale = strings.TrimSuffix(raw, "M"), 1
		case strings.HasSuffix(raw, "G"):
			raw, scale = strings.TrimSuffix(raw, "G"), 1024
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0
		}
		return value * scale
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "Physical footprint (peak)"):
			peak = parse(line)
		case strings.HasPrefix(line, "Physical footprint"):
			current = parse(line)
		}
	}
	return current, peak
}

// TestRSSBreakdown separates the three things that get lumped together as
// "memory use": what the result actually retains, what the scan allocated and
// threw away, and what the Go runtime is holding mapped after freeing it.
func TestRSSBreakdown(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	const mib = float64(1 << 20)
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter,
	})

	if profile := os.Getenv("PROBE_ALLOC_OUT"); profile != "" {
		defer func() {
			file, err := os.Create(profile)
			if err != nil {
				return
			}
			defer file.Close()
			runtime.GC()
			_ = pprof.Lookup("allocs").WriteTo(file, 0)
		}()
	}

	baseRSS := selfRSSMiB(t)
	baseFootprint, _ := selfFootprintMiB(t)
	status, err := service.StartScan(marmotapp.ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	peakRSS := baseRSS
	for {
		current, err := service.GetScanStatus(status.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if rss := selfRSSMiB(t); rss > peakRSS {
			peakRSS = rss
		}
		if current.State != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	afterScanRSS := selfRSSMiB(t)
	afterScanFootprint, peakFootprint := selfFootprintMiB(t)
	allocated := metricValue("/gc/heap/allocs:bytes")
	runtime.GC()
	// /gc/heap/live:bytes, not /memory/classes/heap/objects:bytes: the latter
	// counts dead objects that the sweeper has not reached yet, so right after a
	// scan it reads far above what the result actually retains.
	liveHeap := metricValue("/gc/heap/live:bytes")
	heapFree := metricValue("/memory/classes/heap/free:bytes")
	heapReleased := metricValue("/memory/classes/heap/released:bytes")
	totalMapped := metricValue("/memory/classes/total:bytes")
	afterGCRSS := selfRSSMiB(t)
	afterGCFootprint, _ := selfFootprintMiB(t)

	// FreeOSMemory hands the free spans back instead of leaving them mapped with
	// MADV_FREE, which is what the OS reports as resident until it needs the pages.
	debug.FreeOSMemory()
	time.Sleep(500 * time.Millisecond)
	afterReleaseRSS := selfRSSMiB(t)
	afterReleaseFootprint, _ := selfFootprintMiB(t)

	// The background scavenger returns spans on its own schedule, so a single
	// sample right after FreeOSMemory understates what an idle app settles at.
	time.Sleep(10 * time.Second)
	settledRSS := selfRSSMiB(t)
	settledFootprint, _ := selfFootprintMiB(t)
	settledLive := metricValue("/gc/heap/live:bytes")
	settledFree := metricValue("/memory/classes/heap/free:bytes")
	settledReleased := metricValue("/memory/classes/heap/released:bytes")
	settledStacks := metricValue("/memory/classes/heap/stacks:bytes")
	settledOther := metricValue("/memory/classes/os-stacks:bytes") +
		metricValue("/memory/classes/metadata/mspan/inuse:bytes") +
		metricValue("/memory/classes/metadata/mcache/inuse:bytes") +
		metricValue("/memory/classes/metadata/other:bytes") +
		metricValue("/memory/classes/other:bytes")
	settledMapped := metricValue("/memory/classes/total:bytes")
	if dump := os.Getenv("PROBE_VMMAP_OUT"); dump != "" {
		summary, _ := exec.Command("vmmap", "--summary", strconv.Itoa(os.Getpid())).Output()
		_ = os.WriteFile(dump, summary, 0o600)
	}

	report := func(label string, value float64) { t.Logf("PROBE %-34s %8.1f MiB", label, value) }
	report("RSS at start", baseRSS)
	report("RSS peak during scan", peakRSS)
	report("RSS after scan", afterScanRSS)
	report("RSS after runtime.GC()", afterGCRSS)
	report("RSS after debug.FreeOSMemory()", afterReleaseRSS)
	t.Log("PROBE --- footprint (what Activity Monitor shows)")
	report("footprint at start", baseFootprint)
	report("footprint peak", peakFootprint)
	report("footprint after scan", afterScanFootprint)
	report("footprint after runtime.GC()", afterGCFootprint)
	report("footprint after FreeOSMemory()", afterReleaseFootprint)
	t.Log("PROBE --- settled, 10s idle after FreeOSMemory")
	report("RSS settled", settledRSS)
	report("footprint settled", settledFootprint)
	report("  of which live heap", float64(settledLive)/mib)
	report("  of which free, still mapped", float64(settledFree)/mib)
	report("  of which released to OS", float64(settledReleased)/mib)
	report("  of which goroutine stacks", float64(settledStacks)/mib)
	report("  of which runtime metadata", float64(settledOther)/mib)
	report("  total mapped by runtime", float64(settledMapped)/mib)
	t.Log("PROBE --- immediately after scan")
	report("live heap (what we retain)", float64(liveHeap)/mib)
	report("heap free, still mapped", float64(heapFree)/mib)
	report("heap released to OS", float64(heapReleased)/mib)
	report("total mapped by runtime", float64(totalMapped)/mib)
	t.Log("PROBE ---")
	report("TOTAL allocated during scan", float64(allocated)/mib)
	t.Logf("PROBE churn ratio: allocated / retained = %.1fx", float64(allocated)/float64(liveHeap))
	fmt.Fprintln(os.Stderr)
}
