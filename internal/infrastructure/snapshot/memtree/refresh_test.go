package memtree

import (
	"testing"
	"time"

	"example.com/marmot/internal/domain/scan"
)

// Shallow refresh of /r/a (fixture in replace_test.go): x grew, y is gone, z is
// new, sub is still there and keeps its (empty) subtree untouched, and a new
// directory `fresh` arrives to be read in depth by the caller.
func TestRefreshDirectoryUpdatesDirectChildrenOnly(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildReplaceFixture(t, store)
	// Give sub a child so "its subtree is untouched" is observable.
	if err := store.InsertNodes(snapshotID, []scan.Node{{ID: 7, ParentID: 5, Path: "/r/a/sub/deep", Name: "deep", Kind: "file", OwnedAllocated: 40, LogicalSize: 40}}); err != nil {
		t.Fatal(err)
	}
	// The fixture's roll-ups did not include deep; set them as a scan would have.
	if err := store.UpdateDirectorySizes(snapshotID, map[int64]scan.DirectorySize{
		5: {OwnedAllocated: 40, LogicalSize: 40}, 2: {OwnedAllocated: 340, LogicalSize: 340}, 1: {OwnedAllocated: 390, LogicalSize: 390},
	}); err != nil {
		t.Fatal(err)
	}

	stamp := time.Unix(1_700_000_000, 0)
	refresh, err := store.RefreshDirectory(snapshotID, 2, scan.Node{Device: 9, Inode: 90, ModifiedAt: stamp}, []scan.Node{
		{Name: "x", Kind: "file", OwnedAllocated: 150, LogicalSize: 150, Device: 9, Inode: 91},
		{Name: "sub", Kind: "directory", Device: 9, Inode: 92, HasChildren: true},
		{Name: "z", Kind: "file", OwnedAllocated: 25, LogicalSize: 25, Device: 9, Inode: 93},
		{Name: "fresh", Kind: "directory", Device: 9, Inode: 94, HasChildren: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if refresh.Updated != 2 || refresh.Added != 2 || refresh.Removed != 1 {
		t.Fatalf("updated/added/removed = %d/%d/%d, want 2/2/1", refresh.Updated, refresh.Added, refresh.Removed)
	}
	// x: +50, y: -200, z: +25, fresh: 0 (arrives empty) => -125.
	if refresh.AllocatedDelta != -125 {
		t.Fatalf("delta %d, want -125", refresh.AllocatedDelta)
	}
	if len(refresh.NewDirectoryIDs) != 1 {
		t.Fatalf("one new directory expected, got %v", refresh.NewDirectoryIDs)
	}

	x, err := store.NodeByPath(snapshotID, "/r/a/x")
	if err != nil || x.ID != 3 || x.OwnedAllocated != 150 || x.Inode != 91 {
		t.Fatalf("x must keep ID 3 and take the new size and identity: %#v err=%v", x, err)
	}
	sub, err := store.NodeByPath(snapshotID, "/r/a/sub")
	if err != nil || sub.ID != 5 || sub.OwnedAllocated != 40 || sub.Inode != 92 {
		t.Fatalf("sub must keep its ID and subtree total: %#v err=%v", sub, err)
	}
	if deep, err := store.NodeByPath(snapshotID, "/r/a/sub/deep"); err != nil || deep.ID != 7 {
		t.Fatalf("sub's child must be untouched: %#v err=%v", deep, err)
	}
	if _, err := store.NodeByPath(snapshotID, "/r/a/y"); err == nil {
		t.Fatal("y still resolves")
	}
	if _, err := store.NodeByID(snapshotID, 4); err == nil {
		t.Fatal("y's ID still resolves")
	}
	fresh, err := store.NodeByPath(snapshotID, "/r/a/fresh")
	if err != nil || fresh.ID != refresh.NewDirectoryIDs[0] || fresh.OwnedAllocated != 0 || fresh.HasChildren {
		t.Fatalf("fresh must arrive empty with the reported ID: %#v err=%v", fresh, err)
	}
	a, err := store.NodeByID(snapshotID, 2)
	if err != nil || a.OwnedAllocated != 340-125 || a.Inode != 90 || !a.ModifiedAt.Equal(stamp) {
		t.Fatalf("a: %#v err=%v", a, err)
	}
	root, err := store.NodeByPath(snapshotID, "/r")
	if err != nil || root.OwnedAllocated != 390-125 {
		t.Fatalf("root: %#v err=%v", root, err)
	}
	if b, err := store.NodeByPath(snapshotID, "/r/b"); err != nil || b.OwnedAllocated != 50 {
		t.Fatalf("sibling disturbed: %#v err=%v", b, err)
	}
	snapshot, _ := store.SnapshotByTaskID("task")
	// The counters are FinishScan's (6 nodes / 3 files / 3 dirs / 350 bytes; the
	// later InsertNodes and UpdateDirectorySizes do not touch them, as during a
	// scan), moved by the refresh's deltas: -y +z +fresh, -1 file +1 file, +1 dir.
	if snapshot.NodeCount != 7 || snapshot.FileCount != 3 || snapshot.DirCount != 4 || snapshot.Bytes != 350-125 {
		t.Fatalf("counters: %#v", snapshot)
	}
	if snapshot.SnapshotVersion != refresh.Version {
		t.Fatalf("version %d vs %d", snapshot.SnapshotVersion, refresh.Version)
	}
	children, err := store.Children(snapshotID, 2, 64, 0)
	if err != nil || len(children) != 4 || children[0].Name != "x" || children[1].Name != "sub" || children[2].Name != "z" || children[3].Name != "fresh" {
		t.Fatalf("children of a: %#v err=%v", children, err)
	}
}

// A refresh with nothing new is a no-op on every number and every ID.
func TestRefreshDirectoryWithoutChangesIsIdempotent(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildReplaceFixture(t, store)
	before, _ := store.SnapshotByTaskID("task")
	refresh, err := store.RefreshDirectory(snapshotID, 2, scan.Node{}, []scan.Node{
		{Name: "x", Kind: "file", OwnedAllocated: 100, LogicalSize: 100},
		{Name: "y", Kind: "file", OwnedAllocated: 200, LogicalSize: 200},
		{Name: "sub", Kind: "directory", HasChildren: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if refresh.Added != 0 || refresh.Removed != 0 || refresh.AllocatedDelta != 0 || len(refresh.NewDirectoryIDs) != 0 {
		t.Fatalf("a no-change refresh reported changes: %#v", refresh)
	}
	after, _ := store.SnapshotByTaskID("task")
	if after.NodeCount != before.NodeCount || after.Bytes != before.Bytes {
		t.Fatalf("counters moved: %#v -> %#v", before, after)
	}
	for id := int64(1); id <= 6; id++ {
		if _, err := store.NodeByID(snapshotID, id); err != nil {
			t.Fatalf("node %d lost: %v", id, err)
		}
	}
}

// A directory holding attached volumes keeps them across a refresh: a listing
// never contains them, and dropping them would lose the volume group's members.
func TestRefreshDirectoryKeepsAttachedVolumes(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildReplaceFixture(t, store)
	if err := store.InsertNodes(snapshotID, []scan.Node{{ID: 7, ParentID: 2, Path: "/r/a/Preboot", Name: "Preboot", Kind: "volume", OwnedAllocated: 9}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RefreshDirectory(snapshotID, 2, scan.Node{}, []scan.Node{
		{Name: "x", Kind: "file", OwnedAllocated: 100, LogicalSize: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if volume, err := store.NodeByPath(snapshotID, "/r/a/Preboot"); err != nil || volume.Kind != "volume" || volume.OwnedAllocated != 9 {
		t.Fatalf("the attached volume was lost: %#v err=%v", volume, err)
	}
	if _, err := store.NodeByPath(snapshotID, "/r/a/y"); err == nil {
		t.Fatal("y should have been removed by the refresh")
	}
}

func TestRefreshDirectoryRefusesFilesAndUnknownNodes(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildReplaceFixture(t, store)
	if _, err := store.RefreshDirectory(snapshotID, 6, scan.Node{}, nil); err == nil {
		t.Fatal("a file must be refused")
	}
	if _, err := store.RefreshDirectory(snapshotID, 99, scan.Node{}, nil); err == nil {
		t.Fatal("an unknown node must be refused")
	}
}
