package snapshot

import (
	"testing"

	"example.com/marmot/internal/domain/scan"
)

func TestStorePersistsAndQueriesNodes(t *testing.T) {
	store, err := Open(t.TempDir() + "/snapshots.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshotID, err := store.CreateSnapshot("task-1", "/tmp/test-root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(snapshotID, []Node{{ID: 1, Path: "/tmp/test-root", Name: "test-root", Kind: "directory"}, {ID: 2, ParentID: 1, Path: "/tmp/test-root/a", Name: "a", Kind: "file", OwnedAllocated: 512}}); err != nil {
		t.Fatal(err)
	}
	nodes, err := store.Children(snapshotID, 1, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "a" {
		t.Fatalf("unexpected children: %#v", nodes)
	}
	if err := store.UpdateDirectorySizes(snapshotID, map[int64]DirectorySize{1: {LogicalSize: 1024, AllocatedSize: 768, OwnedAllocated: 512, Confidence: "estimated"}}); err != nil {
		t.Fatal(err)
	}
	root, err := store.NodeByPath(snapshotID, "/tmp/test-root")
	if err != nil {
		t.Fatal(err)
	}
	if root.LogicalSize != 1024 || root.AllocatedSize != 768 || root.OwnedAllocated != 512 || root.Confidence != "estimated" {
		t.Fatalf("directory size fields were mixed: %#v", root)
	}
	if err := store.UpdateDirectorySizes(snapshotID, map[int64]DirectorySize{1: {
		LogicalSize: 512, AllocatedSize: 256, OwnedAllocated: 128,
		Confidence: "partial", SizeBasis: "descendant_sum_v1_partial",
	}}); err != nil {
		t.Fatal(err)
	}
	root, err = store.NodeByPath(snapshotID, "/tmp/test-root")
	if err != nil {
		t.Fatal(err)
	}
	if root.Confidence != "partial" || root.SizeBasis != "descendant_sum_v1_partial" {
		t.Fatalf("partial directory size metadata was not persisted: %#v", root)
	}
}

func TestStoreInitialisesSchemaVersion(t *testing.T) {
	path := t.TempDir() + "/snapshots.db"
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("unexpected schema version: %d", version)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version changed after reopen: %d", version)
	}
}

func TestStoreMarksRunningSnapshotInterruptedAndQueriesByTaskID(t *testing.T) {
	store, err := Open(t.TempDir() + "/snapshots.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	snapshotID, err := store.CreateSnapshot("scan-restart", "/tmp/restart-root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunningInterrupted(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.SnapshotByTaskID("scan-restart")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != snapshotID || snapshot.TaskID != "scan-restart" || snapshot.State != "interrupted" || snapshot.Root != "/tmp/restart-root" {
		t.Fatalf("unexpected recovered snapshot: %#v", snapshot)
	}
}

func TestStoreMapReturnsStableEntriesAndRemainingAggregate(t *testing.T) {
	store, err := Open(t.TempDir() + "/snapshots.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshotID, err := store.CreateSnapshot("map-task", "/tmp/map-root")
	if err != nil {
		t.Fatal(err)
	}
	nodes := []Node{{ID: 1, Path: "/tmp/map-root", Name: "map-root", Kind: "directory", Confidence: "exact"}}
	for i, size := range []int64{400, 300, 200} {
		nodes = append(nodes, Node{ID: int64(i + 2), ParentID: 1, Path: "/tmp/map-root/item-" + string(rune('a'+i)), Name: "item", Kind: "file", LogicalSize: size, AllocatedSize: size, OwnedAllocated: size, Confidence: "exact", SizeBasis: "test"})
	}
	if err := store.InsertNodes(snapshotID, nodes); err != nil {
		t.Fatal(err)
	}
	result, err := store.Map(scan.MapQuery{SnapshotID: snapshotID, ParentID: 1, Limit: 2, Measure: "owned_allocated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || result.Entries[0].Kind != "node" || result.Entries[0].OwnedAllocated != 400 || result.Entries[1].Kind != "aggregate" {
		t.Fatalf("unexpected map entries: %#v", result.Entries)
	}
	if result.Remaining.Count != 2 || result.Remaining.OwnedAllocated != 500 || !result.HasMore || result.SnapshotVersion <= 1 {
		t.Fatalf("unexpected map metadata: %#v", result)
	}
	if result.Entries[1].Node.ID != 0 {
		t.Fatal("aggregate entry must not carry a real snapshot node")
	}
}
