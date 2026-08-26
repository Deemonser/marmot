package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"unsafe"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
)

func buildEmitContractTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for directory := 0; directory < 12; directory++ {
		child := filepath.Join(root, "dir"+strconv.Itoa(directory))
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatal(err)
		}
		for file := 0; file < 40; file++ {
			name := filepath.Join(child, "file"+strconv.Itoa(file))
			if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// TestEmitBatchIsValidOnlyDuringTheCallback pins ADR-0057 §1. Under the old
// contract every batch had its own backing array and a consumer could keep the
// slice, so both assertions here would fail: no array would ever be reused, and
// a retained batch would still read back what it was handed.
func TestEmitBatchIsValidOnlyDuringTheCallback(t *testing.T) {
	root := buildEmitContractTree(t)
	type retainedBatch struct {
		slice    []scan.Node
		snapshot []scan.Node
	}
	// The emitter is called concurrently from scanner worker threads (R-058 §3),
	// so this bookkeeping is locked. It is the same mistake the probe made.
	var mu sync.Mutex
	seenArrays := map[uintptr]int{}
	retained := map[uintptr]retainedBatch{}
	overwritten := false

	_, err := (Scanner{MountResolver: func() ([]ports.Mount, error) {
		return []ports.Mount{{ID: "emit-contract-volume", Path: root, DeviceProfile: scan.DeviceProfileSSD}}, nil
	}}).ScanBatched(context.Background(), root, func(batch []scan.Node) error {
		if len(batch) == 0 {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		array := uintptr(unsafe.Pointer(unsafe.SliceData(batch)))
		seenArrays[array]++
		// A recycled buffer that grows moves to a new array, so the check has to
		// be "did any array we kept come back", not "did this one".
		if earlier, ok := retained[array]; ok && !overwritten {
			for index := range earlier.snapshot {
				if earlier.slice[index].ID != earlier.snapshot[index].ID {
					overwritten = true
					break
				}
			}
		}
		retained[array] = retainedBatch{slice: batch, snapshot: append([]scan.Node(nil), batch...)}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	batches := 0
	reused := 0
	for _, count := range seenArrays {
		batches += count
		if count > 1 {
			reused += count - 1
		}
	}
	if batches < 10 {
		t.Fatalf("the tree produced %d batches, too few to observe recycling", batches)
	}
	if reused == 0 {
		t.Fatalf("no backing array was reused across %d batches: the scanner is still allocating one per batch", batches)
	}
	if !overwritten {
		t.Fatal("a retained batch read back unchanged: the batch must not survive the callback")
	}
}

// TestEmitBatchOmitsFilePathsButKeepsDirectoryPaths pins ADR-0057 §2.
func TestEmitBatchOmitsFilePathsButKeepsDirectoryPaths(t *testing.T) {
	root := buildEmitContractTree(t)
	var mu sync.Mutex
	files, directories := 0, 0

	_, err := (Scanner{MountResolver: func() ([]ports.Mount, error) {
		return []ports.Mount{{ID: "path-contract-volume", Path: root, DeviceProfile: scan.DeviceProfileSSD}}, nil
	}}).ScanBatched(context.Background(), root, func(batch []scan.Node) error {
		mu.Lock()
		defer mu.Unlock()
		for index := range batch {
			node := &batch[index]
			switch node.Kind {
			case "directory":
				directories++
				if node.Path == "" {
					return errUnexpected("directory " + node.Name + " lost its path; the walk needs it to open the directory")
				}
			default:
				files++
				if node.Path != "" {
					return errUnexpected("file " + node.Name + " carries path " + node.Path + "; ADR-0057 §2 says it must not")
				}
			}
			if node.Name == "" {
				return errUnexpected("node lost its name")
			}
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 480 || directories != 13 {
		t.Fatalf("walked %d files and %d directories, want 480 and 13", files, directories)
	}
}

type errUnexpected string

func (e errUnexpected) Error() string { return string(e) }
