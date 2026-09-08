package memtree

import (
	"errors"
	"testing"

	"example.com/marmot/internal/domain/scan"
)

// /r (1)
//
//	a (2, dir, 300)      -- the directory that gets re-read
//	  x (3, file, 100)
//	  y (4, file, 200)
//	  sub (5, dir, 0)    -- empty directory
//	b (6, file, 50)
func buildReplaceFixture(t *testing.T, store *Store) int64 {
	t.Helper()
	id, err := store.CreateSnapshot("task", "/r")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(id, []scan.Node{
		{ID: 1, Path: "/r", Name: "r", Kind: "directory", HasChildren: true, OwnedAllocated: 350, LogicalSize: 350},
		{ID: 2, ParentID: 1, Path: "/r/a", Name: "a", Kind: "directory", HasChildren: true, OwnedAllocated: 300, LogicalSize: 300},
		{ID: 3, ParentID: 2, Path: "/r/a/x", Name: "x", Kind: "file", OwnedAllocated: 100, LogicalSize: 100},
		{ID: 4, ParentID: 2, Path: "/r/a/y", Name: "y", Kind: "file", OwnedAllocated: 200, LogicalSize: 200},
		{ID: 5, ParentID: 2, Path: "/r/a/sub", Name: "sub", Kind: "directory"},
		{ID: 6, ParentID: 1, Path: "/r/b", Name: "b", Kind: "file", OwnedAllocated: 50, LogicalSize: 50},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(id, scan.JobCompleted, "", 6, 3, 3, 350, 0); err != nil {
		t.Fatal(err)
	}
	return id
}

// The re-read of /r/a, numbered by its own sub-scan: x grew, y is gone, z is new,
// sub now holds a file.
func rereadOfA() ([]scan.Node, map[int64]scan.DirectorySize) {
	nodes := []scan.Node{
		{ID: 1, Path: "/r/a", Name: "a", Kind: "directory", HasChildren: true, Device: 7, Inode: 70},
		{ID: 2, ParentID: 1, Path: "/r/a/x", Name: "x", Kind: "file", OwnedAllocated: 150, LogicalSize: 150},
		{ID: 3, ParentID: 1, Path: "/r/a/sub", Name: "sub", Kind: "directory", HasChildren: true},
		{ID: 4, ParentID: 1, Path: "/r/a/z", Name: "z", Kind: "file", OwnedAllocated: 25, LogicalSize: 25},
		{ID: 5, ParentID: 3, Path: "/r/a/sub/deep", Name: "deep", Kind: "file", OwnedAllocated: 5, LogicalSize: 5},
	}
	sizes := map[int64]scan.DirectorySize{
		1: {OwnedAllocated: 180, LogicalSize: 180, Confidence: "exact"},
		3: {OwnedAllocated: 5, LogicalSize: 5, Confidence: "exact"},
	}
	return nodes, sizes
}

func TestReplaceSubtreeKeepsIDsForUnchangedObjectsAndDetachesTheRest(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildReplaceFixture(t, store)
	nodes, sizes := rereadOfA()

	replacement, err := store.ReplaceSubtree(snapshotID, 2, nodes, sizes)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Nodes != 5 || replacement.Files != 3 || replacement.Directories != 2 {
		t.Fatalf("counted %#v in the new subtree", replacement)
	}
	if replacement.Kept != 2 || replacement.Added != 2 || replacement.Removed != 1 {
		t.Fatalf("kept/added/removed = %d/%d/%d, want 2/2/1: %#v", replacement.Kept, replacement.Added, replacement.Removed, replacement)
	}
	if replacement.AllocatedBytes != 180 {
		t.Fatalf("the directory now holds %d, want 180", replacement.AllocatedBytes)
	}

	// x and sub existed under a before, so they keep 3 and 5; z and deep are new.
	x, err := store.NodeByPath(snapshotID, "/r/a/x")
	if err != nil || x.ID != 3 || x.OwnedAllocated != 150 {
		t.Fatalf("x must keep ID 3 and take its new size: %#v err=%v", x, err)
	}
	sub, err := store.NodeByPath(snapshotID, "/r/a/sub")
	if err != nil || sub.ID != 5 || sub.OwnedAllocated != 5 || !sub.HasChildren {
		t.Fatalf("sub must keep ID 5 and gain its child: %#v err=%v", sub, err)
	}
	deep, err := store.NodeByPath(snapshotID, "/r/a/sub/deep")
	if err != nil || deep.ID < 7 || deep.ParentID != 5 {
		t.Fatalf("deep must be a fresh ID under sub: %#v err=%v", deep, err)
	}
	if z, err := store.NodeByPath(snapshotID, "/r/a/z"); err != nil || z.ID < 7 || z.ParentID != 2 {
		t.Fatalf("z must be a fresh ID under a: %#v err=%v", z, err)
	}
	// y is gone: by path, and by the ID a client might still hold.
	if _, err := store.NodeByPath(snapshotID, "/r/a/y"); err == nil {
		t.Fatal("y still resolves by path")
	}
	if _, err := store.NodeByID(snapshotID, 4); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("y's old ID must answer not-found, got %v", err)
	}

	// The directory kept its ID and took the fresh identity.
	a, err := store.NodeByID(snapshotID, 2)
	if err != nil || a.Device != 7 || a.Inode != 70 || a.OwnedAllocated != 180 || a.Path != "/r/a" {
		t.Fatalf("a: %#v err=%v", a, err)
	}
	// Ancestors moved by the difference: 350 - 300 + 180 = 230. The sibling is untouched.
	root, err := store.NodeByPath(snapshotID, "/r")
	if err != nil || root.OwnedAllocated != 230 || root.LogicalSize != 230 {
		t.Fatalf("root: %#v err=%v", root, err)
	}
	if b, err := store.NodeByPath(snapshotID, "/r/b"); err != nil || b.OwnedAllocated != 50 || b.ID != 6 {
		t.Fatalf("sibling disturbed: %#v err=%v", b, err)
	}
	// Counters: 6 nodes - 4 old under a + 5 new = 7; bytes 350 -> 230.
	snapshot, err := store.SnapshotByTaskID("task")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.NodeCount != 7 || snapshot.FileCount != 4 || snapshot.DirCount != 3 || snapshot.Bytes != 230 {
		t.Fatalf("counters: %#v", snapshot)
	}
	if snapshot.SnapshotVersion != replacement.Version {
		t.Fatalf("version %d does not match the replacement's %d", snapshot.SnapshotVersion, replacement.Version)
	}
	// The child list of a is exactly the new set, in size order.
	children, err := store.Children(snapshotID, 2, 64, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 3 || children[0].Name != "x" || children[1].Name != "z" || children[2].Name != "sub" {
		t.Fatalf("children of a: %#v", children)
	}
}

// Detaching by parentID is what makes a dropped node stay dropped across the
// regroup the replacement forces (ADR-0064's lesson, applied here).
func TestReplaceSubtreeDroppedNodesSurviveARegroup(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildReplaceFixture(t, store)
	nodes, sizes := rereadOfA()
	if _, err := store.ReplaceSubtree(snapshotID, 2, nodes, sizes); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(snapshotID, []scan.Node{{ID: 40, ParentID: 1, Name: "late", Kind: "file", OwnedAllocated: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NodeByPath(snapshotID, "/r/a/y"); err == nil {
		t.Fatal("y came back after a regroup")
	}
	children, err := store.Children(snapshotID, 2, 64, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if child.Name == "y" {
			t.Fatalf("y is listed again: %#v", children)
		}
	}
}

func TestReplaceSubtreeRefusesRootFilesAndVolumes(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildReplaceFixture(t, store)
	nodes, sizes := rereadOfA()

	if _, err := store.ReplaceSubtree(snapshotID, 1, nodes, sizes); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("the root must be refused, got %v", err)
	}
	if _, err := store.ReplaceSubtree(snapshotID, 6, nodes, sizes); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("a file must be refused, got %v", err)
	}
	if _, err := store.ReplaceSubtree(snapshotID, 99, nodes, sizes); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("an unknown node must be refused, got %v", err)
	}
	// A sub-scan that does not start at node 1 is malformed.
	if _, err := store.ReplaceSubtree(snapshotID, 2, nodes[1:], sizes); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("a read without its root must be refused, got %v", err)
	}

	if err := store.InsertNodes(snapshotID, []scan.Node{{ID: 7, ParentID: 5, Name: "Preboot", Kind: "volume", OwnedAllocated: 9}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceSubtree(snapshotID, 2, nodes, sizes); !errors.Is(err, ErrSubtreeHasVolumes) {
		t.Fatalf("a subtree with an attached volume must be refused, got %v", err)
	}
	// Refused means untouched.
	if y, err := store.NodeByPath(snapshotID, "/r/a/y"); err != nil || y.ID != 4 {
		t.Fatalf("a refused replacement must leave the tree alone: %#v err=%v", y, err)
	}
}
