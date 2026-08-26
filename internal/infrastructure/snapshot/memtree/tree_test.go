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
