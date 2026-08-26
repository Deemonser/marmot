package memtree

import (
	"fmt"
	"testing"
	"time"

	"example.com/marmot/internal/domain/scan"
)

// TestInsertThroughput isolates the store from the scanner: if a full-volume scan
// slows down, this says whether the store is the reason.
func TestInsertThroughput(t *testing.T) {
	const nodes = 2_800_000
	const batchSize = 4096
	store := OpenStore()
	id, err := store.CreateSnapshot("bench", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(id, []scan.Node{{ID: 1, Path: "/root", Name: "root", Kind: "directory", VolumeID: "mount:/dev/disk3s1s1", Confidence: "exact", SizeBasis: "darwin_getattrlistbulk_native_v1", HasChildren: true}}); err != nil {
		t.Fatal(err)
	}
	batch := make([]scan.Node, 0, batchSize)
	started := time.Now()
	for index := int64(2); index <= nodes; index++ {
		batch = append(batch, scan.Node{
			ID: index, ParentID: 1 + (index % 1000), Path: fmt.Sprintf("/root/dir%d/file%d", index%1000, index),
			Name: fmt.Sprintf("file%d", index), Kind: "file", OwnedAllocated: index,
			VolumeID: "mount:/dev/disk3s1s1", Confidence: "exact", SizeBasis: "darwin_getattrlistbulk_native_v1",
		})
		if len(batch) == batchSize {
			if err := store.InsertNodes(id, batch); err != nil {
				t.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := store.InsertNodes(id, batch); err != nil {
			t.Fatal(err)
		}
	}
	inserted := time.Since(started)
	started = time.Now()
	if err := store.FinishScan(id, scan.JobCompleted, "", nodes, nodes-1, 1, 0, 0); err != nil {
		t.Fatal(err)
	}
	finished := time.Since(started)
	t.Logf("BENCH insert=%.2fs (%.0f ns/node) finish=%.2fs", inserted.Seconds(), float64(inserted.Nanoseconds())/nodes, finished.Seconds())
}
