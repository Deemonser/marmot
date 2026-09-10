package memtree

import (
	"testing"

	"example.com/marmot/internal/domain/scan"
)

// A small tree with one of everything ADR-0074 §2 has to get right:
//
//	1 root/
//	2   plain        100 B, private 100, alone
//	3   link-a        50 B, inode 7, 2 links, private 50
//	4   link-b        50 B, inode 7, 2 links (same inode)
//	5   clone-a       80 B, clone group 9 of 2, private 0
//	6   clone-b       80 B, clone group 9 of 2, private 0
//	7   snapshotted   30 B, private 10, not cloned, not linked
//	8   unknown       20 B, volume reported no attributes
//	9   sub/
//	10    clone-c     40 B, clone group 11 of 2 -- its twin is OUTSIDE the scan
func reclaimFixture(t *testing.T) *tree {
	t.Helper()
	tr := newTree("task", "/root")
	file := func(id, parent int64, name string, alloc, private int64, hasPrivate bool, device, inode uint64, links uint32, cloneID uint64, refs uint32) scan.Node {
		return scan.Node{
			ID: id, ParentID: parent, Name: name, Kind: "file",
			LogicalSize: alloc, AllocatedSize: alloc, OwnedAllocated: alloc,
			Device: device, Inode: inode, LinkCount: links,
			PrivateSize: private, HasPrivate: hasPrivate, CloneID: cloneID, CloneRefCount: refs,
		}
	}
	nodes := []scan.Node{
		{ID: 1, ParentID: 0, Name: "root", Kind: "directory", Path: "/root", HasChildren: true},
		file(2, 1, "plain", 100, 100, true, 1, 2, 1, 0, 1),
		file(3, 1, "link-a", 50, 50, true, 1, 7, 2, 0, 1),
		file(4, 1, "link-b", 50, 50, true, 1, 7, 2, 0, 1),
		file(5, 1, "clone-a", 80, 0, true, 1, 5, 1, 9, 2),
		file(6, 1, "clone-b", 80, 0, true, 1, 6, 1, 9, 2),
		file(7, 1, "snapshotted", 30, 10, true, 1, 8, 1, 0, 1),
		file(8, 1, "unknown", 20, 0, false, 1, 9, 1, 0, 1),
		{ID: 9, ParentID: 1, Name: "sub", Kind: "directory", Path: "/root/sub", HasChildren: true},
		file(10, 9, "clone-c", 40, 0, true, 1, 10, 1, 11, 2),
	}
	// The scanner folds the second hardlink to zero owned bytes; do the same.
	nodes[3].OwnedAllocated = 0
	if err := tr.insert(nodes); err != nil {
		t.Fatal(err)
	}
	tr.finish(scan.JobCompleted, "", int64(len(nodes)), 8, 2, 0, 0)
	return tr
}

func TestReclaimableAccountsByGroup(t *testing.T) {
	tr := reclaimFixture(t)
	cases := []struct {
		name string
		ids  []int64
		want scan.Reclaimable
	}{
		{"plain file reclaims what it occupies", []int64{2}, scan.Reclaimable{Bytes: 100, UpperBound: 100, Files: 1}},
		{"one hardlink of two reclaims nothing", []int64{3}, scan.Reclaimable{Bytes: 0, SharedExcluded: 50, UpperBound: 50, Files: 1}},
		{"the carrying hardlink alone reclaims nothing either", []int64{4}, scan.Reclaimable{Bytes: 0, SharedExcluded: 50, UpperBound: 50, Files: 1}},
		{"both hardlinks reclaim the inode once", []int64{3, 4}, scan.Reclaimable{Bytes: 50, UpperBound: 50, Files: 2}},
		{"one clone of two reclaims nothing", []int64{5}, scan.Reclaimable{Bytes: 0, SharedExcluded: 80, UpperBound: 80, Files: 1}},
		{"both clones reclaim the stream once", []int64{5, 6}, scan.Reclaimable{Bytes: 80, UpperBound: 80, Files: 2}},
		{"snapshot-shared file reclaims only its private part", []int64{7}, scan.Reclaimable{Bytes: 10, SharedExcluded: 20, UpperBound: 30, Files: 1}},
		{"unknown attributes give an upper bound", []int64{8}, scan.Reclaimable{Unknown: true, UnknownBytes: 20, UpperBound: 20, Files: 1}},
		{"a clone whose twin is outside the scan never completes", []int64{10}, scan.Reclaimable{Bytes: 0, SharedExcluded: 40, UpperBound: 40, Files: 1}},
		{"the whole root", []int64{1}, scan.Reclaimable{Bytes: 100 + 50 + 80 + 10, SharedExcluded: 20 + 40, Unknown: true, UnknownBytes: 20, UpperBound: 320, Files: 8}},
		{"a directory listed twice counts once", []int64{1, 9}, scan.Reclaimable{Bytes: 240, SharedExcluded: 60, Unknown: true, UnknownBytes: 20, UpperBound: 320, Files: 8}},
	}
	for _, tc := range cases {
		got := tr.reclaimable(tc.ids)
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// A re-read that rewrites a clone into a plain file must move it out of the
// exception set, and one that turns a plain file into a clone must move it in.
func TestReclaimableFollowsDirectoryRefresh(t *testing.T) {
	tr := reclaimFixture(t)
	self := scan.Node{ID: 1, Kind: "directory", Name: "root", Path: "/root", HasChildren: true}
	children := []scan.Node{
		// plain becomes a clone member of a complete pair with clone-a
		{Name: "plain", Kind: "file", LogicalSize: 80, AllocatedSize: 80, OwnedAllocated: 80, Device: 1, Inode: 2, LinkCount: 1, PrivateSize: 0, HasPrivate: true, CloneID: 9, CloneRefCount: 3},
		{Name: "link-a", Kind: "file", LogicalSize: 50, AllocatedSize: 50, OwnedAllocated: 50, Device: 1, Inode: 7, LinkCount: 2, PrivateSize: 50, HasPrivate: true},
		{Name: "link-b", Kind: "file", LogicalSize: 50, AllocatedSize: 50, OwnedAllocated: 50, Device: 1, Inode: 7, LinkCount: 2, PrivateSize: 50, HasPrivate: true},
		{Name: "clone-a", Kind: "file", LogicalSize: 80, AllocatedSize: 80, OwnedAllocated: 80, Device: 1, Inode: 5, LinkCount: 1, PrivateSize: 0, HasPrivate: true, CloneID: 9, CloneRefCount: 3},
		// clone-b was rewritten in place: now a plain file
		{Name: "clone-b", Kind: "file", LogicalSize: 80, AllocatedSize: 80, OwnedAllocated: 80, Device: 1, Inode: 6, LinkCount: 1, PrivateSize: 80, HasPrivate: true},
		{Name: "snapshotted", Kind: "file", LogicalSize: 30, AllocatedSize: 30, OwnedAllocated: 30, Device: 1, Inode: 8, LinkCount: 1, PrivateSize: 10, HasPrivate: true},
		{Name: "unknown", Kind: "file", LogicalSize: 20, AllocatedSize: 20, OwnedAllocated: 20, Device: 1, Inode: 9, LinkCount: 1},
		{Name: "sub", Kind: "directory", HasChildren: true},
	}
	if _, err := tr.refreshDirectory(1, self, children); err != nil {
		t.Fatal(err)
	}
	if got := tr.reclaimable([]int64{6}); got.Bytes != 80 || got.SharedExcluded != 0 {
		t.Errorf("rewritten clone-b should reclaim like a plain file: %+v", got)
	}
	// plain + clone-a are two of a group of three: still incomplete.
	if got := tr.reclaimable([]int64{2, 5}); got.Bytes != 0 || got.SharedExcluded != 80 {
		t.Errorf("two of three clones must not reclaim the stream: %+v", got)
	}
}

func TestReclaimTableSealAndSet(t *testing.T) {
	var table reclaimTable
	for id := int32(10); id > 0; id-- {
		table.add(reclaimEntry{id: id, allocated: int64(id)})
	}
	table.seal()
	if got, ok := table.get(7); !ok || got.allocated != 7 {
		t.Fatalf("sealed lookup: %+v %v", got, ok)
	}
	table.set(reclaimEntry{id: 7, allocated: 70})
	table.set(reclaimEntry{id: 99, allocated: 99})
	if got, _ := table.get(7); got.allocated != 70 {
		t.Fatalf("set should overwrite in place: %+v", got)
	}
	if got, ok := table.get(99); !ok || got.allocated != 99 {
		t.Fatalf("set should append a new id: %+v %v", got, ok)
	}
	table.set(reclaimEntry{id: 99, allocated: 990})
	table.seal()
	if got, _ := table.get(99); got.allocated != 990 || table.len() != 11 {
		t.Fatalf("seal must keep the latest write and no duplicates: %+v len=%d", got, table.len())
	}
}
