package memtree

import (
	"fmt"
	"strings"
	"testing"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/domain/scan"
)

// builder hands out dense node IDs, because the ID is the table index and a gap
// reads as a real node (ADR-0056 §1).
type builder struct {
	nodes []scan.Node
	next  int64
}

func (b *builder) add(parentID int64, path, kind string) int64 {
	b.next++
	id := b.next
	name := path[strings.LastIndex(path, "/")+1:]
	b.nodes = append(b.nodes, scan.Node{
		ID: id, ParentID: parentID, Path: path, Name: name, Kind: kind,
		OwnedAllocated: 1, HasChildren: kind == "directory",
	})
	return id
}

func (b *builder) store(t *testing.T) (*Store, int64) {
	t.Helper()
	store := OpenStore()
	id, err := store.CreateSnapshot("task", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(id, b.nodes); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(id, scan.JobCompleted, "", int64(len(b.nodes)), 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	return store, id
}

// checkInvariants is the whole reason the chunker gets its own test. The chunks
// are handed to four workers that call a recursive remove concurrently, so if
// one chunk's path is an ancestor of another's, two workers unlink inside the
// same tree at the same time. And a path outside the item is a deletion the user
// never staged.
func checkInvariants(t *testing.T, root string, subtree cleanup.Subtree) {
	t.Helper()
	seen := map[string]bool{}
	var all []string
	var counted int64
	for _, chunk := range subtree.Chunks {
		if len(chunk.Paths) == 0 {
			t.Error("a chunk names no paths, so a worker would credit its nodes without deleting anything")
		}
		if len(chunk.Paths) > maxChunkMembers {
			t.Errorf("chunk names %d paths, over the %d cap", len(chunk.Paths), maxChunkMembers)
		}
		counted += chunk.Nodes
		for _, path := range chunk.Paths {
			if !cleanup.IsPathWithin(root, path) {
				t.Errorf("chunk path %q is outside the staged item %q", path, root)
			}
			if seen[path] {
				t.Errorf("chunk path %q named twice", path)
			}
			seen[path] = true
			all = append(all, path)
		}
	}
	for _, outer := range all {
		for _, inner := range all {
			if outer != inner && cleanup.IsPathWithin(outer, inner) {
				t.Errorf("chunk path %q is an ancestor of %q: two workers would race inside one tree", outer, inner)
			}
		}
	}
	if counted > subtree.TotalNodes {
		t.Errorf("chunks cover %d nodes of a %d node subtree", counted, subtree.TotalNodes)
	}
}

// The shape that defeats the obvious algorithm: no single package is big enough
// to be worth its own unit, so a chunker that only ever promotes whole subtrees
// produces nothing at all and the entire delete falls back to one blocking
// sweep -- which is the bug this replaces.
func TestChunksAWideShallowTree(t *testing.T) {
	b := &builder{}
	root := b.add(0, "/root", "directory")
	for pkg := 0; pkg < 40; pkg++ {
		dir := fmt.Sprintf("/root/pkg-%02d", pkg)
		dirID := b.add(root, dir, "directory")
		for file := 0; file < 20; file++ {
			b.add(dirID, fmt.Sprintf("%s/f-%02d.js", dir, file), "file")
		}
	}
	store, snapshotID := b.store(t)
	subtree, err := store.SubtreeChunks(snapshotID, "/root", 8)
	if err != nil {
		t.Fatal(err)
	}
	if subtree.TotalNodes != int64(len(b.nodes)) {
		t.Fatalf("total is %d, tree has %d nodes", subtree.TotalNodes, len(b.nodes))
	}
	if len(subtree.Chunks) < 2 {
		t.Fatalf("a 841 node tree produced %d chunks: nothing to parallelise and nothing to draw", len(subtree.Chunks))
	}
	checkInvariants(t, "/root", subtree)
}

// The other shape: one branch deep enough to produce its own unit, so every
// directory above it is an ancestor of a chunk and none of them may be offered
// as a member.
func TestChunksADeepBranchWithoutNestingUnits(t *testing.T) {
	b := &builder{}
	root := b.add(0, "/root", "directory")
	deep, path := root, "/root"
	for level := 0; level < 5; level++ {
		path = fmt.Sprintf("%s/d%d", path, level)
		deep = b.add(deep, path, "directory")
	}
	for file := 0; file < 400; file++ {
		b.add(deep, fmt.Sprintf("%s/f-%03d.bin", path, file), "file")
	}
	sibling := b.add(root, "/root/sibling", "directory")
	for file := 0; file < 300; file++ {
		b.add(sibling, fmt.Sprintf("/root/sibling/f-%03d.bin", file), "file")
	}
	store, snapshotID := b.store(t)
	subtree, err := store.SubtreeChunks(snapshotID, "/root", 4)
	if err != nil {
		t.Fatal(err)
	}
	if subtree.TotalNodes != int64(len(b.nodes)) {
		t.Fatalf("total is %d, tree has %d nodes", subtree.TotalNodes, len(b.nodes))
	}
	checkInvariants(t, "/root", subtree)
}

// A flat directory of files is the shape chunking cannot help: members are
// directories only, so it stays one unit. It must still be counted correctly --
// silently reporting a smaller total would make the ring finish early.
func TestFlatDirectoryStaysOneUnitButCountsRight(t *testing.T) {
	b := &builder{}
	root := b.add(0, "/root", "directory")
	for file := 0; file < 900; file++ {
		b.add(root, fmt.Sprintf("/root/f-%03d.bin", file), "file")
	}
	store, snapshotID := b.store(t)
	subtree, err := store.SubtreeChunks(snapshotID, "/root", 8)
	if err != nil {
		t.Fatal(err)
	}
	if subtree.TotalNodes != 901 {
		t.Fatalf("total is %d, want 901", subtree.TotalNodes)
	}
	if len(subtree.Chunks) != 0 {
		t.Fatalf("a flat file directory produced %d chunks; files are never members", len(subtree.Chunks))
	}
	checkInvariants(t, "/root", subtree)
}
