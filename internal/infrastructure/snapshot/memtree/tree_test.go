package memtree

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"example.com/marmot/internal/domain/scan"
)

// The node ID is the table index (ADR-0056 §1), so a gap in the numbering would
// leave a zero record that reads as a real node. This is the load-bearing
// assumption of the whole layout, so it gets its own test: if the scanner ever
// starts skipping IDs, this fails instead of the tree silently mis-addressing.
func TestNodeIDsAreDenseAndAGapIsVisible(t *testing.T) {
	store := OpenStore()
	id, err := store.CreateSnapshot("task", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(id, []scan.Node{
		{ID: 1, Path: "/root", Name: "root", Kind: "directory", HasChildren: true},
		{ID: 2, ParentID: 1, Path: "/root/a", Name: "a", Kind: "file", OwnedAllocated: 10},
		// 3 is skipped on purpose.
		{ID: 4, ParentID: 1, Path: "/root/b", Name: "b", Kind: "file", OwnedAllocated: 20},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(id, scan.JobCompleted, "", 3, 2, 1, 30, 0); err != nil {
		t.Fatal(err)
	}
	// The gap must not surface as a child, and must not be answerable as a node.
	children, err := store.Children(id, 1, 64, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("a skipped ID leaked into the child list: %#v", children)
	}
	if _, err := store.NodeByID(id, 3); err == nil {
		t.Fatal("the skipped ID answered as a node")
	}
}

// The record is the memory budget: 2.8M nodes means every byte here is 2.8 MB of
// RSS. A change to this number is a change to the budget in ADR-0056, so it must
// be deliberate.
func TestRecordStaysPacked(t *testing.T) {
	if size := unsafe.Sizeof(record{}); size != 64 {
		t.Fatalf("record is %d bytes, expected 64: the memory budget in ADR-0056 assumes it", size)
	}
}

// The arena bounds are checked rather than assumed: a silent wrap would corrupt
// every name after it.
func TestArenaRefusesOversizedName(t *testing.T) {
	var a arena
	if _, _, err := a.put(string(make([]byte, 1<<17))); err == nil {
		t.Fatal("a name longer than uint16 must be refused")
	}
}

// The intern table is bounded: an unbounded one is a leak wearing an
// optimisation's clothes.
func TestCodeTableRefusesUnboundedCardinality(t *testing.T) {
	table := newCodeTable()
	for index := 0; index < maxCodes; index++ {
		if _, err := table.code(string(rune('a'+index%26)) + string(rune('a'+index/26))); err != nil {
			t.Fatalf("code %d rejected early: %v", index, err)
		}
	}
	if _, err := table.code("one too many"); err == nil {
		t.Fatal("the intern table must refuse more than maxCodes distinct values")
	}
}

// Paths are rebuilt from the parent chain, not stored.
func TestPathIsRebuiltFromParents(t *testing.T) {
	store := OpenStore()
	id, err := store.CreateSnapshot("task", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(id, []scan.Node{
		{ID: 1, Path: "/root", Name: "root", Kind: "directory", HasChildren: true},
		{ID: 2, ParentID: 1, Path: "/root/dir", Name: "dir", Kind: "directory", HasChildren: true},
		{ID: 3, ParentID: 2, Path: "/root/dir/leaf.txt", Name: "leaf.txt", Kind: "file", OwnedAllocated: 5},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(id, scan.JobCompleted, "", 3, 1, 2, 5, 0); err != nil {
		t.Fatal(err)
	}
	node, err := store.NodeByID(id, 3)
	if err != nil {
		t.Fatal(err)
	}
	if node.Path != "/root/dir/leaf.txt" {
		t.Fatalf("path rebuilt as %q", node.Path)
	}
	// And the reverse direction, which the cleanup plan depends on.
	found, err := store.NodeByPath(id, "/root/dir/leaf.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(found, node) {
		t.Fatalf("lookup by path disagreed with lookup by id:\n%#v\n%#v", found, node)
	}
}

// TestRecordPageIsBudgeted pins the page size for the same reason
// TestRecordStaysPacked pins the record: a page is a unit of memory the result
// holds for the whole session, and the slack a finished tree carries is at most
// one of them (ADR-0057 §3).
func TestRecordPageIsBudgeted(t *testing.T) {
	if got := recordsPerPage * int(unsafe.Sizeof(record{})); got != 256*1024 {
		t.Fatalf("a record page is %d bytes, want %d; changing it changes the memory budget", got, 256*1024)
	}
	if arenaPageSize != 1<<20 {
		t.Fatalf("the name arena page is %d bytes, want %d", arenaPageSize, 1<<20)
	}
}

// TestArenaNamesNeverStraddleAPage covers the edge the paged arena introduced:
// an offset only decodes as (page, offset within page) if no name crosses a
// boundary, so the last name before one has to be pushed to the next page.
func TestArenaNamesNeverStraddleAPage(t *testing.T) {
	var names arena
	// Fill to just short of a page boundary, then write a name that cannot fit
	// in the remainder.
	filler := strings.Repeat("a", 1000)
	written := map[uint32]string{}
	for total := 0; total < arenaPageSize+2*len(filler); total += len(filler) {
		offset, length, err := names.put(filler)
		if err != nil {
			t.Fatal(err)
		}
		written[offset] = filler
		if got := names.get(offset, length); got != filler {
			t.Fatalf("name at offset %d read back as %q", offset, got)
		}
	}
	straddler := strings.Repeat("b", math.MaxUint16)
	offset, length, err := names.put(straddler)
	if err != nil {
		t.Fatal(err)
	}
	if got := names.get(offset, length); got != straddler {
		t.Fatalf("a name at a page boundary read back with length %d, want %d", len(got), len(straddler))
	}
	// Everything written before the boundary must still read back intact.
	for offset, want := range written {
		if got := names.get(offset, uint16(len(want))); got != want {
			t.Fatalf("name at offset %d was corrupted by a later page", offset)
		}
	}
}

// TestArenaRefusesToExceedItsOffsetSpace keeps the uint32 offset bound checked
// rather than assumed, now that offsets skip page tails.
func TestArenaRefusesToExceedItsOffsetSpace(t *testing.T) {
	var names arena
	names.next = math.MaxUint32 - 4
	if _, _, err := names.put("12345678"); err == nil {
		t.Fatal("the arena accepted a write past its 4 GiB offset space")
	}
}

// Taking a deleted subtree out in place, instead of re-scanning the disk to learn
// something already known exactly. On the reference machine a full re-scan was
// 9.5s for 1.8M nodes.
func TestRemoveSubtreeRollsSpaceOutOfAncestors(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildRemovalFixture(t, store)

	before, err := store.NodeByPath(snapshotID, "/r")
	if err != nil {
		t.Fatal(err)
	}
	removal, err := store.RemoveSubtree(snapshotID, "/r/a")
	if err != nil {
		t.Fatal(err)
	}
	if removal.Nodes != 3 || removal.Files != 2 || removal.Directories != 1 {
		t.Fatalf("counted %#v leaving the tree", removal)
	}
	if removal.AllocatedBytes != 300 {
		t.Fatalf("subtracted %d bytes, expected 300", removal.AllocatedBytes)
	}
	after, err := store.NodeByPath(snapshotID, "/r")
	if err != nil {
		t.Fatal(err)
	}
	if after.OwnedAllocated != before.OwnedAllocated-300 {
		t.Fatalf("the root still holds %d, was %d", after.OwnedAllocated, before.OwnedAllocated)
	}
	// Gone from the tree, so the path no longer resolves -- which is also what
	// stops it being staged for deletion a second time.
	if _, err := store.NodeByPath(snapshotID, "/r/a"); err == nil {
		t.Fatal("the removed path still resolves")
	}
	if _, err := store.NodeByPath(snapshotID, "/r/a/x"); err == nil {
		t.Fatal("a descendant of the removed node is still reachable")
	}
	// The sibling is untouched, and still reachable.
	if node, err := store.NodeByPath(snapshotID, "/r/b"); err != nil || node.OwnedAllocated != 50 {
		t.Fatalf("the sibling was disturbed: %#v err=%v", node, err)
	}
	// The version has to move, or a client cannot tell this from what it drew.
	if version, _ := store.SnapshotVersion(snapshotID); version != removal.Version {
		t.Fatalf("version %d does not match the removal's %d", version, removal.Version)
	}
}

// group() rebuilds the child index from parentID alone. Patching the index
// without clearing the pointer would work until the next thing invalidated the
// grouping, and then the deleted subtree would reappear -- months later, looking
// like corruption.
func TestARemovedSubtreeSurvivesARegroup(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildRemovalFixture(t, store)
	if _, err := store.RemoveSubtree(snapshotID, "/r/a"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	result, err := store.treeFor(snapshotID)
	if err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	result.grouped = false
	result.group()
	store.mu.Unlock()
	if _, err := store.NodeByPath(snapshotID, "/r/a"); err == nil {
		t.Fatal("the removed subtree came back after the index was rebuilt")
	}
}

func TestRemoveSubtreeRefusesTheRoot(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	snapshotID := buildRemovalFixture(t, store)
	if _, err := store.RemoveSubtree(snapshotID, "/r"); err == nil {
		t.Fatal("the scan root was removed from its own result")
	}
}

// /r (350) -> a (300, dir, two files 200+100), b (50, file)
func buildRemovalFixture(t *testing.T, store *Store) int64 {
	t.Helper()
	snapshotID, err := store.CreateSnapshot("task-removal", "/r")
	if err != nil {
		t.Fatal(err)
	}
	nodes := []scan.Node{
		{ID: 1, ParentID: 0, Path: "/r", Name: "r", Kind: "directory", OwnedAllocated: 350, LogicalSize: 350},
		{ID: 2, ParentID: 1, Path: "/r/a", Name: "a", Kind: "directory", OwnedAllocated: 300, LogicalSize: 300},
		{ID: 3, ParentID: 2, Path: "/r/a/x", Name: "x", Kind: "file", OwnedAllocated: 200, LogicalSize: 200},
		{ID: 4, ParentID: 2, Path: "/r/a/y", Name: "y", Kind: "file", OwnedAllocated: 100, LogicalSize: 100},
		{ID: 5, ParentID: 1, Path: "/r/b", Name: "b", Kind: "file", OwnedAllocated: 50, LogicalSize: 50},
	}
	if err := store.InsertNodes(snapshotID, nodes); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(snapshotID, "completed", "", 5, 3, 2, 350, 0); err != nil {
		t.Fatal(err)
	}
	return snapshotID
}
