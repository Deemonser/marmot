package probe

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
	"unsafe"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/platform"
)

// TestScannerOutputContractCost measures what the scanner's current output
// contract costs before anything downstream can use it: one scan.Node per node
// carrying a full path string, where the store keeps neither the struct nor the
// path. It separates directories (whose paths the walk genuinely needs, to open
// them) from files (whose paths are built and immediately discarded).
func TestScannerOutputContractCost(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	const mib = float64(1 << 20)

	// The scanner invokes the emit callback concurrently from its worker
	// threads, so every accumulator here needs a lock. An earlier version of
	// this probe incremented plain counters and reported understated numbers.
	var mu sync.Mutex
	var nodes, dirs, files int64
	var batches int64
	var concurrentEmits, peakConcurrentEmits int64
	sizeBuckets := map[string]int64{}
	var largestBatch int
	var dirPathBytes, filePathBytes, nameBytes int64
	var parentPathBytes int64

	adapter := platform.Adapter{}
	walker := scanner.Scanner{MountResolver: adapter.ListMounts}

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

	_, err := walker.ScanBatched(context.Background(), root, func(batch []scan.Node) error {
		mu.Lock()
		concurrentEmits++
		if concurrentEmits > peakConcurrentEmits {
			peakConcurrentEmits = concurrentEmits
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			concurrentEmits--
			mu.Unlock()
		}()
		mu.Lock()
		defer mu.Unlock()
		batches++
		switch size := len(batch); {
		case size == 1:
			sizeBuckets["1"]++
		case size <= 8:
			sizeBuckets["2-8"]++
		case size <= 64:
			sizeBuckets["9-64"]++
		case size <= 512:
			sizeBuckets["65-512"]++
		default:
			sizeBuckets["513+"]++
		}
		if len(batch) > largestBatch {
			largestBatch = len(batch)
		}
		for index := range batch {
			node := &batch[index]
			nodes++
			nameBytes += int64(len(node.Name))
			// The name is a tail slice of the path, so the parent prefix is the
			// part that is rebuilt for every single child of a directory.
			parentPathBytes += int64(len(node.Path) - len(node.Name))
			if node.Kind == "directory" {
				dirs++
				dirPathBytes += int64(len(node.Path))
			} else {
				files++
				filePathBytes += int64(len(node.Path))
			}
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	report := func(label string, value float64) { t.Logf("PROBE %-38s %10.1f MiB", label, value) }
	t.Logf("PROBE sizeof(scan.Node)                       %10d bytes", unsafe.Sizeof(scan.Node{}))
	t.Logf("PROBE nodes                                   %10d", nodes)
	t.Logf("PROBE   directories                           %10d (%.1f%%)", dirs, 100*float64(dirs)/float64(nodes))
	t.Logf("PROBE   files+symlinks                        %10d (%.1f%%)", files, 100*float64(files)/float64(nodes))
	t.Logf("PROBE peak concurrent emit callbacks       %10d", peakConcurrentEmits)
	t.Logf("PROBE batches                                 %10d (mean %.1f nodes, max %d)", batches, float64(nodes)/float64(batches), largestBatch)
	for _, bucket := range []string{"1", "2-8", "9-64", "65-512", "513+"} {
		t.Logf("PROBE   batches of %-8s                     %10d (%.1f%%)", bucket, sizeBuckets[bucket], 100*float64(sizeBuckets[bucket])/float64(batches))
	}
	t.Log("PROBE ---")
	report("scan.Node structs materialised", float64(uintptr(nodes)*unsafe.Sizeof(scan.Node{}))/mib)
	report("path bytes, directories", float64(dirPathBytes)/mib)
	report("path bytes, files+symlinks (discarded)", float64(filePathBytes)/mib)
	report("  of which parent prefix (rebuilt per child)", float64(parentPathBytes)/mib)
	report("name bytes (the only text kept)", float64(nameBytes)/mib)
	t.Log("PROBE ---")
	report("TOTAL contract cost", float64(uintptr(nodes)*unsafe.Sizeof(scan.Node{})+uintptr(dirPathBytes+filePathBytes))/mib)
	report("  minimum the store actually needs", float64(nameBytes)/mib)
	fmt.Fprintln(os.Stderr)
}
