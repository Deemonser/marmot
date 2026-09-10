package scanner

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/platform"
)

// ADR-0074 gate 1: how big is the reclaimable side table on a real volume? The
// record table is 64 bytes per node and pinned; the side table holds only the
// exceptions -- files whose private size differs from allocated, hardlinked
// files, and members of clone groups larger than one. Counted from the emitted
// nodes, so it measures exactly what memtree will store.
//
// Measured 2026-09-10 on the reference volume: 2,069,292 files, 362,144 with
// private != allocated (20.9 GiB shared), 50,322 clone members in 18,489 groups.
//
//	PROBE_ROOT=/ go test ./internal/infrastructure/scanner -run ReclaimSideTable -v -timeout 10m
func TestReclaimSideTable(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to scan a real volume")
	}
	engine := Scanner{MountResolver: platform.Adapter{}.ListMounts}
	started := time.Now()
	var nodes, files, withPrivate, differs, hardlinked, cloneMembers, exceptions int64
	var sharedBytes int64
	groups := map[uint64]struct{}{}
	// The batch emitter runs on several native workers at once (ADR-0045).
	var mu sync.Mutex
	if _, err := engine.ScanBatched(context.Background(), root, func(batch []scan.Node) error {
		mu.Lock()
		defer mu.Unlock()
		nodes += int64(len(batch))
		for _, node := range batch {
			if node.Kind == "directory" || node.Kind == "volume" {
				continue
			}
			files++
			if node.HasPrivate {
				withPrivate++
				if node.PrivateSize != node.AllocatedSize {
					differs++
					sharedBytes += node.AllocatedSize - node.PrivateSize
				}
			}
			if node.LinkCount > 1 {
				hardlinked++
			}
			if node.CloneRefCount > 1 {
				cloneMembers++
				groups[node.CloneID] = struct{}{}
			}
			if node.HasPrivate && (node.PrivateSize != node.AllocatedSize || node.CloneRefCount > 1 || node.LinkCount > 1) {
				exceptions++
			}
		}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	const mib = float64(1 << 20)
	t.Logf("PROBE root=%s elapsed=%.3fs nodes=%d files=%d", root, elapsed.Seconds(), nodes, files)
	t.Logf("PROBE private: reported=%d (%.1f%% of files) differs_from_allocated=%d shared_bytes=%.1f MiB",
		withPrivate, 100*float64(withPrivate)/float64(max(files, 1)), differs, float64(sharedBytes)/mib)
	t.Logf("PROBE hardlinked=%d clone_members=%d distinct_clone_groups=%d", hardlinked, cloneMembers, len(groups))
	t.Logf("PROBE side table: %d exception files -> %.1f MiB at 40 B each", exceptions, float64(exceptions*40)/mib)
}
