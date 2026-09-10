package probe

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"example.com/marmot/internal/platform"
)

// R-078: is a persisted scan cache revalidatable from the FSEvents journal, and
// at what price? For a set of ages, take the ID the journal had issued that long
// ago and replay everything since -- what a warm start would do instead of a
// 20-second walk. What comes back is the number of events, how many distinct
// directories they touch (each one is a shallow re-read), how many were
// coalesced into whole-subtree rescans, whether the journal admitted to losing
// anything, and how long the replay itself took.
//
//	PROBE_ROOT=/ PROBE_HISTORY_AGES=1h,6h,24h,168h go test ./internal/probe -run FileEventHistory -v -timeout 30m
func TestFileEventHistory(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to replay the FSEvents journal")
	}
	ages := []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}
	if raw := os.Getenv("PROBE_HISTORY_AGES"); raw != "" {
		ages = ages[:0]
		for part := range strings.SplitSeq(raw, ",") {
			age, err := time.ParseDuration(strings.TrimSpace(part))
			if err != nil {
				t.Fatalf("PROBE_HISTORY_AGES: %v", err)
			}
			ages = append(ages, age)
		}
	}
	timeout := 120 * time.Second
	if raw := os.Getenv("PROBE_HISTORY_TIMEOUT"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("PROBE_HISTORY_TIMEOUT: %v", err)
		}
		timeout = parsed
	}
	// The live watcher uses a coalescing latency; whether that also paces the
	// history replay is one of the questions, so it is a knob here.
	latency := 50 * time.Millisecond
	if raw := os.Getenv("PROBE_HISTORY_LATENCY"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			t.Fatalf("PROBE_HISTORY_LATENCY: %v", err)
		}
		latency = parsed
	}

	adapter := platform.Adapter{}
	now := time.Now()
	current := platform.FileEventCurrentID()
	t.Logf("PROBE root=%s current_event_id=%d latency=%s", root, current, latency)
	// The journal is per device. On a volume group the sealed system volume at
	// / issues almost nothing; the data volume is where the changes are. Ask
	// both and replay from the earlier ID so nothing is missed.
	devices := []string{root}
	if root == "/" {
		devices = append(devices, "/System/Volumes/Data")
	}
	for _, device := range devices {
		uuid, err := platform.FileEventDeviceUUID(device)
		if err != nil {
			t.Logf("PROBE device=%s uuid: %v", device, err)
			continue
		}
		t.Logf("PROBE device=%s journal_uuid=%s", device, uuid)
	}

	// Freshness: does a replay reach all the way to "now"? Make a change under
	// the root, then replay from a moment before it and look for it. Without
	// this the replayed ID range ending short of the current ID is ambiguous.
	marker, err := os.MkdirTemp("", "marmot-fsevents-marker-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(marker)
	markerReal, _ := filepath.EvalSymlinks(marker)
	if markerReal == "" {
		markerReal = marker
	}
	if strings.HasPrefix(markerReal, strings.TrimSuffix(root, "/")+"/") || root == "/" {
		beforeMarker := time.Now()
		time.Sleep(1100 * time.Millisecond)
		if err := os.WriteFile(filepath.Join(markerReal, "touch"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		since, err := platform.FileEventIDBefore(markerReal, beforeMarker)
		if err != nil {
			t.Fatal(err)
		}
		report, err := adapter.FileEventHistory(root, since, latency, timeout)
		if err != nil {
			t.Fatal(err)
		}
		_, withSlash := report.Directories[markerReal+"/"]
		_, without := report.Directories[markerReal]
		t.Logf("PROBE freshness: since=%d events=%d last_id=%d current=%d marker_seen=%v history_done=%v elapsed=%.3fs",
			since, report.Events, report.LastID, platform.FileEventCurrentID(), withSlash || without, report.HistoryDone, report.Elapsed.Seconds())
	}

	for _, age := range ages {
		since := uint64(0)
		for _, device := range devices {
			id, err := platform.FileEventIDBefore(device, now.Add(-age))
			if err != nil {
				t.Logf("PROBE age=%s device=%s id_before: %v", age, device, err)
				continue
			}
			t.Logf("PROBE age=%s device=%s id_before=%d (span to now %d)", age, device, id, current-id)
			if since == 0 || (id != 0 && id < since) {
				since = id
			}
		}
		if since == 0 {
			t.Logf("PROBE age=%s: no event ID, skipped", age)
			continue
		}
		report, err := adapter.FileEventHistory(root, since, latency, timeout)
		if err != nil {
			t.Fatalf("age=%s: %v", age, err)
		}
		t.Logf("PROBE age=%s since=%d events=%d distinct_dirs=%d must_scan_subdirs=%d elapsed=%.3fs history_done=%v reliable=%v",
			age, since, report.Events, len(report.Directories), report.MustScanSubDirs, report.Elapsed.Seconds(), report.HistoryDone, report.Reliable())
		if report.IDsWrapped+report.UserDropped+report.KernelDropped+report.RootChanged > 0 {
			t.Logf("PROBE age=%s flags: wrapped=%d user_dropped=%d kernel_dropped=%d root_changed=%d",
				age, report.IDsWrapped, report.UserDropped, report.KernelDropped, report.RootChanged)
		}
		t.Logf("PROBE age=%s id range replayed: %d..%d", age, report.FirstID, report.LastID)

		type churn struct {
			path  string
			count uint64
		}
		ranked := make([]churn, 0, len(report.Directories))
		var depthTotal int
		for path, count := range report.Directories {
			ranked = append(ranked, churn{path, count})
			depthTotal += strings.Count(strings.TrimSuffix(path, "/"), "/")
		}
		sort.Slice(ranked, func(i, j int) bool { return ranked[i].count > ranked[j].count })
		if len(ranked) > 0 {
			t.Logf("PROBE age=%s mean_depth=%.1f  top directories by event count:", age, float64(depthTotal)/float64(len(ranked)))
		}
		for i := 0; i < len(ranked) && i < 12; i++ {
			t.Logf("PROBE   %8d  %s", ranked[i].count, ranked[i].path)
		}
		// Where the events live, by top-level prefix: says whether a warm start
		// would be re-reading caches and logs or the user's own folders.
		prefixes := map[string]uint64{}
		for path, count := range report.Directories {
			segments := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 4)
			key := "/" + strings.Join(segments[:min(len(segments), 3)], "/")
			prefixes[key] += count
		}
		top := make([]churn, 0, len(prefixes))
		for prefix, count := range prefixes {
			top = append(top, churn{prefix, count})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].count > top[j].count })
		for i := 0; i < len(top) && i < 10; i++ {
			t.Logf("PROBE   prefix %8d  %s", top[i].count, top[i].path)
		}
	}
}
