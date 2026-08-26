package probe

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"example.com/marmot/internal/domain/scan"
)

// Measured on the real / volume by TestScannerOutputContractCost. The batch
// shape matters more than the node count: the scanner emits 421,701 batches
// averaging 6.6 nodes, and 45.7% of them carry exactly one node, so every
// per-batch allocation is paid hundreds of thousands of times.
const (
	probeNodes       = 2782493
	probeDirectories = 588786
	probeFiles       = probeNodes - probeDirectories
	probeParentLen   = 101 // 269.1 MiB of parent prefix over 2.78M nodes
	probeNameLen     = 21  // 55.7 MiB of names over 2.78M nodes
)

// probeBatchSizes reproduces the measured batch-size histogram exactly: the
// bucket counts are the measured ones and the sizes within a bucket are spread
// across its range, so both the batch count and the node total are preserved.
func probeBatchSizes() []int {
	buckets := []struct {
		count    int
		low, cap int
	}{
		{192771, 1, 1},
		{186853, 2, 8},
		{38087, 9, 64},
		{3572, 65, 512},
		{418, 513, 2773},
	}
	sizes := make([]int, 0, 421701)
	for _, bucket := range buckets {
		span := bucket.cap - bucket.low + 1
		for index := 0; index < bucket.count; index++ {
			sizes = append(sizes, bucket.low+index%span)
		}
	}
	return sizes
}

// allocatedBy reports how many bytes the given function allocates, which is the
// quantity that drives peak footprint — not how much it retains.
func allocatedBy(work func()) float64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	work()
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(1<<20)
}

// TestPerBatchAllocationCost isolates what the scanner pays per emitted batch.
// The current code allocates a node slice, a parent-path slice and a delta map
// for every batch, including the 45.7% that carry a single node.
func TestPerBatchAllocationCost(t *testing.T) {
	if os.Getenv("PROBE_ALTERNATIVES") == "" {
		t.Skip("set PROBE_ALTERNATIVES to run the contract-alternatives probe")
	}
	sizes := probeBatchSizes()

	current := allocatedBy(func() {
		for _, size := range sizes {
			parentPaths := make([]string, size)
			nodes := make([]scan.Node, 0, size)
			linkCount := make([]uint32, 0, size)
			deltas := make(map[int64]scan.DirectorySize)
			for entry := 0; entry < size; entry++ {
				nodes = append(nodes, scan.Node{ID: int64(entry)})
				linkCount = append(linkCount, 1)
				deltas[int64(entry)&7] = scan.DirectorySize{LogicalSize: int64(entry)}
			}
			runtime.KeepAlive(parentPaths)
			runtime.KeepAlive(nodes)
			runtime.KeepAlive(linkCount)
			runtime.KeepAlive(deltas)
		}
	})

	// The emit contract transfers ownership of the slice, and the callback runs
	// on up to 2 worker threads concurrently, so recycling needs one buffer set
	// per worker plus a consumer that copies out before returning.
	recycled := allocatedBy(func() {
		parentPaths := make([]string, 0, 2773)
		nodes := make([]scan.Node, 0, 2773)
		linkCount := make([]uint32, 0, 2773)
		deltaIDs := make([]int64, 0, 2773)
		deltaValues := make([]scan.DirectorySize, 0, 2773)
		for _, size := range sizes {
			parentPaths, nodes, linkCount = parentPaths[:0], nodes[:0], linkCount[:0]
			deltaIDs, deltaValues = deltaIDs[:0], deltaValues[:0]
			for entry := 0; entry < size; entry++ {
				parentPaths = append(parentPaths, "")
				nodes = append(nodes, scan.Node{ID: int64(entry)})
				linkCount = append(linkCount, 1)
				deltaIDs = append(deltaIDs, int64(entry)&7)
				deltaValues = append(deltaValues, scan.DirectorySize{LogicalSize: int64(entry)})
			}
		}
	})

	report := func(label string, value float64) { t.Logf("PROBE %-46s %8.1f MiB", label, value) }
	t.Logf("PROBE batches=%d nodes=%d sizeof(scan.Node)=%d", len(sizes), probeNodes, int(unsafe.Sizeof(scan.Node{})))
	report("per-batch allocation (current)", current)
	report("recycled per-worker buffers", recycled)
	fmt.Fprintln(os.Stderr)
}

// TestDirectoryStateMapCost isolates the two persistent maps the scanner keys
// by directory node ID. Node IDs come from a single counter, so they are dense
// and an ordinal-indexed array is admissible.
func TestDirectoryStateMapCost(t *testing.T) {
	if os.Getenv("PROBE_ALTERNATIVES") == "" {
		t.Skip("set PROBE_ALTERNATIVES to run the contract-alternatives probe")
	}
	type dirState struct {
		parentID int64
		path     string
	}

	grownFromEmpty := allocatedBy(func() {
		directories := make(map[int64]dirState)
		dirSizes := make(map[int64]scan.DirectorySize)
		for id := int64(0); id < probeDirectories; id++ {
			directories[id] = dirState{parentID: id >> 1}
			dirSizes[id] = scan.DirectorySize{Confidence: "exact", SizeBasis: "probe"}
		}
		runtime.KeepAlive(directories)
		runtime.KeepAlive(dirSizes)
	})
	preSized := allocatedBy(func() {
		directories := make(map[int64]dirState, probeDirectories)
		dirSizes := make(map[int64]scan.DirectorySize, probeDirectories)
		for id := int64(0); id < probeDirectories; id++ {
			directories[id] = dirState{parentID: id >> 1}
			dirSizes[id] = scan.DirectorySize{Confidence: "exact", SizeBasis: "probe"}
		}
		runtime.KeepAlive(directories)
		runtime.KeepAlive(dirSizes)
	})
	denseArray := allocatedBy(func() {
		ordinal := make([]int32, probeNodes)
		directories := make([]dirState, 0, probeDirectories)
		dirSizes := make([]scan.DirectorySize, 0, probeDirectories)
		for id := int64(0); id < probeDirectories; id++ {
			ordinal[id] = int32(len(directories))
			directories = append(directories, dirState{parentID: id >> 1})
			dirSizes = append(dirSizes, scan.DirectorySize{Confidence: "exact", SizeBasis: "probe"})
		}
		runtime.KeepAlive(ordinal)
		runtime.KeepAlive(directories)
		runtime.KeepAlive(dirSizes)
	})

	report := func(label string, value float64) { t.Logf("PROBE %-46s %8.1f MiB", label, value) }
	report("two maps grown from empty (current)", grownFromEmpty)
	report("same maps, pre-sized", preSized)
	report("dense arrays + ordinal index", denseArray)
	fmt.Fprintln(os.Stderr)
}

// TestPathVersusName isolates the cost of building a full path for every file
// against building only the name, which is the only text the store keeps.
func TestPathVersusName(t *testing.T) {
	if os.Getenv("PROBE_ALTERNATIVES") == "" {
		t.Skip("set PROBE_ALTERNATIVES to run the contract-alternatives probe")
	}
	parent := make([]byte, probeParentLen)
	name := make([]byte, probeNameLen)

	// Production builds the path once and returns the name as a tail slice of it
	// (unsafe.String over the same bytes), so the honest comparison is one
	// allocation of len(parent)+len(name) against one of len(name).
	fullPath := allocatedBy(func() {
		sink := make([]string, 0, 4096)
		for index := 0; index < probeFiles; index++ {
			joined := make([]byte, len(parent)+len(name))
			copy(joined, parent)
			copy(joined[len(parent):], name)
			sink = append(sink[:0], unsafe.String(unsafe.SliceData(joined), len(joined)))
		}
		runtime.KeepAlive(sink)
	})
	nameOnly := allocatedBy(func() {
		sink := make([]string, 0, 4096)
		for index := 0; index < probeFiles; index++ {
			only := make([]byte, len(name))
			copy(only, name)
			sink = append(sink[:0], unsafe.String(unsafe.SliceData(only), len(only)))
		}
		runtime.KeepAlive(sink)
	})

	report := func(label string, value float64) { t.Logf("PROBE %-46s %8.1f MiB", label, value) }
	report("full path per file (current)", fullPath)
	report("name only per file", nameOnly)
	fmt.Fprintln(os.Stderr)
}
