package probe

import (
	"context"
	"os"
	"sync"
	"testing"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/platform"
)

// TestMemoryOnlyFootprint measures what a self-contained in-memory tree would
// cost. Today the projection stores offsets and re-reads names, kinds and the
// cleanup identity fields from the data file (ADR-0049); with no file those have
// to live in memory, so the question is how much.
func TestMemoryOnlyFootprint(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	adapter := platform.Adapter{}
	engine := scanner.Scanner{MountResolver: adapter.ListMounts}
	var nodes, dirs, files, symlinks int64
	var nameBytes, longestName, pathBytes int64
	kinds := map[string]int64{}
	volumes := map[string]int64{}
	bases := map[string]int64{}
	confidences := map[string]int64{}
	// The batch emitter is called from several native workers (ADR-0045), so the
	// counters need a lock.
	var mu sync.Mutex
	result, err := engine.ScanBatched(context.Background(), root, func(batch []scan.Node) error {
		mu.Lock()
		defer mu.Unlock()
		for _, node := range batch {
			nodes++
			nameBytes += int64(len(node.Name))
			pathBytes += int64(len(node.Path))
			if int64(len(node.Name)) > longestName {
				longestName = int64(len(node.Name))
			}
			kinds[node.Kind]++
			volumes[node.VolumeID]++
			bases[node.SizeBasis]++
			confidences[node.Confidence]++
			switch node.Kind {
			case "directory":
				dirs++
			case "symlink":
				symlinks++
			default:
				files++
			}
		}
		return nil
	}, func(scan.Phase) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	const mib = float64(1 << 20)
	t.Logf("PROBE nodes=%d dirs=%d files=%d symlinks=%d issues=%d", nodes, dirs, files, symlinks, len(result.Issues))
	t.Logf("PROBE name_bytes=%.1f MiB avg=%.1f longest=%d", float64(nameBytes)/mib, float64(nameBytes)/float64(nodes), longestName)
	// scan.Node is 184 bytes and carries Path plus five string headers. Storing it
	// per node — which the ADR-0055 step does — is what the ADR-0056 layout fixes.
	t.Logf("PROBE path_bytes=%.1f MiB avg=%.1f  (the old projection never stored paths: it walked ancestors)", float64(pathBytes)/mib, float64(pathBytes)/float64(nodes))
	t.Logf("PROBE scan.Node struct alone: %.1f MiB (184 B x %d)", 184*float64(nodes)/mib, nodes)
	t.Logf("PROBE distinct kinds=%d volumes=%d size_bases=%d confidences=%d", len(kinds), len(volumes), len(bases), len(confidences))
	for value, count := range bases {
		t.Logf("PROBE   size_basis %q x%d (%.1f MiB if written per node)", value, count, float64(len(value))*float64(count)/mib)
	}
	for value, count := range volumes {
		t.Logf("PROBE   volume %q x%d (%.1f MiB if written per node)", value, count, float64(len(value))*float64(count)/mib)
	}

	// Proposed self-contained layout, per node:
	//   parentID int64(8) + ownedAllocated int64(8) + logicalSize int64(8)
	//   + device int64(8) + inode int64(8) + modtime int64(8)
	//   + nameOffset uint32(4) + nameLen uint16(2) + kind uint8(1) + flags uint8(1)
	// = 56 bytes, plus the name arena. Node IDs are the array index, so they cost
	// nothing; kind/volume/basis/confidence become small codes.
	const perNode = 56
	// Children: one uint32 index per child, grouped by parent.
	const perChild = 4
	fixed := float64(nodes) * perNode
	children := float64(nodes) * perChild
	arena := float64(nameBytes)
	// Directory roll-ups: childStart/childCount plus the three sizes.
	dirTable := float64(dirs) * 40
	t.Logf("PROBE projected memory-only: fixed=%.1f arena=%.1f children=%.1f dirs=%.1f total=%.1f MiB",
		fixed/mib, arena/mib, children/mib, dirTable/mib, (fixed+arena+children+dirTable)/mib)
}
