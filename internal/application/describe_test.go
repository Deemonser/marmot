package application

import (
	"testing"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// describeStore builds a snapshot straight from nodes: DescribeNode never touches
// the disk, so the paths only have to exist in the tree.
func describeStore(t *testing.T, nodes []scan.Node) (*Service, int64) {
	t.Helper()
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	service := NewService(Dependencies{Store: store, FileSystem: adapter, Permissions: adapter, Trash: adapter})
	snapshotID, err := store.CreateSnapshot("describe", "/Users/tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(snapshotID, nodes); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(snapshotID, scan.JobCompleted, "", int64(len(nodes)), 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	return service, snapshotID
}

// The catalog already carries what a person in front of an 8 GB directory needs
// -- what it is, and what happens if it goes -- and it was only ever reachable
// through an advisor round. This is the same answer without one.
func TestDescribeNodeAnswersFromTheLocalCatalog(t *testing.T) {
	service, snapshotID := describeStore(t, []scan.Node{
		{ID: 1, Path: "/Users/tester", Name: "tester", Kind: "directory", HasChildren: true},
		{ID: 2, ParentID: 1, Path: "/Users/tester/Library", Name: "Library", Kind: "directory", HasChildren: true},
		{ID: 3, ParentID: 2, Path: "/Users/tester/Library/Caches", Name: "Caches", Kind: "directory", HasChildren: true},
		{ID: 4, ParentID: 3, Path: "/Users/tester/Library/Caches/com.example.ShipIt", Name: "com.example.ShipIt", Kind: "directory", HasChildren: true},
		{ID: 5, ParentID: 4, Path: "/Users/tester/Library/Caches/com.example.ShipIt/pkg", Name: "pkg", Kind: "file", OwnedAllocated: 100},
	})
	description, err := service.DescribeNode(snapshotID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if description.Rule == "" || description.Category == "" {
		t.Fatalf("the catalog recognises this path but the description says nothing: %#v", description)
	}
	// The two prose fields are what the catalog requires on every rule, and they
	// are the whole point of showing this at all.
	if description.WhatBreaks == "" || description.HowToRestore == "" {
		t.Errorf("a matched rule came through without its explanation: %#v", description)
	}
	if description.Recovery == "" || description.Risk == "" {
		t.Errorf("a matched rule came through without its recovery or risk: %#v", description)
	}
	// The subtree walk is the other half: the count and the age come from it.
	if description.Nodes != 2 {
		t.Errorf("subtree count is %d, want 2", description.Nodes)
	}
	if !description.AgeKnown {
		t.Error("a two node subtree was reported as too large to age")
	}
}

// An unrecognised directory must come back empty and stay empty. Silence is not
// a reassurance: rendering "no rule matched" as "safe to delete" is the exact
// inversion ADR-0061 §7 refuses, and here it would be attached to a delete.
func TestDescribeNodeSaysNothingAboutWhatItDoesNotRecognise(t *testing.T) {
	service, snapshotID := describeStore(t, []scan.Node{
		{ID: 1, Path: "/Users/tester", Name: "tester", Kind: "directory", HasChildren: true},
		{ID: 2, ParentID: 1, Path: "/Users/tester/whatever-this-is", Name: "whatever-this-is", Kind: "directory", OwnedAllocated: 10},
	})
	description, err := service.DescribeNode(snapshotID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if description.Rule != "" || description.Recovery != "" || description.Risk != "" ||
		description.WhatBreaks != "" || description.HowToRestore != "" {
		t.Fatalf("an unrecognised path was given a verdict: %#v", description)
	}
	// The facts are still facts.
	if description.Name != "whatever-this-is" || description.Nodes != 1 {
		t.Errorf("the plain facts are wrong: %#v", description)
	}
}

// A repository is the case where the advisory guard has to speak even though no
// cleanup rule matches: the catalog would say nothing, and "nothing" next to a
// delete button is not good enough.
func TestDescribeNodeCarriesTheIrreplaceableGuard(t *testing.T) {
	service, snapshotID := describeStore(t, []scan.Node{
		{ID: 1, Path: "/Users/tester", Name: "tester", Kind: "directory", HasChildren: true},
		{ID: 2, ParentID: 1, Path: "/Users/tester/app", Name: "app", Kind: "directory", HasChildren: true},
		{ID: 3, ParentID: 2, Path: "/Users/tester/app/.git", Name: ".git", Kind: "directory", OwnedAllocated: 10},
	})
	description, err := service.DescribeNode(snapshotID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if description.Irreplaceable == "" {
		t.Fatalf("a repository came through with no irreplaceable reason: %#v", description)
	}
}

// The catalog is a cleanup catalog: it answers "is this cleanable", so a source
// tree matches nothing and used to come back with nothing to say. Measured on
// this machine: 40/40 in ~/Library/Caches, 2/39 one level up in ~/Library. The
// project marker is the cheapest true statement about the rest.
func TestDescribeNodeRecognisesAProjectRoot(t *testing.T) {
	service, snapshotID := describeStore(t, []scan.Node{
		{ID: 1, Path: "/Users/tester", Name: "tester", Kind: "directory", HasChildren: true},
		{ID: 2, ParentID: 1, Path: "/Users/tester/work", Name: "work", Kind: "directory", HasChildren: true},
		{ID: 3, ParentID: 2, Path: "/Users/tester/work/.git", Name: ".git", Kind: "directory", OwnedAllocated: 10},
		{ID: 4, ParentID: 2, Path: "/Users/tester/work/main.go", Name: "main.go", Kind: "file", OwnedAllocated: 10},
		{ID: 5, ParentID: 1, Path: "/Users/tester/notes", Name: "notes", Kind: "directory", HasChildren: true},
		{ID: 6, ParentID: 5, Path: "/Users/tester/notes/a.md", Name: "a.md", Kind: "file", OwnedAllocated: 10},
	})
	work, err := service.DescribeNode(snapshotID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !work.IsProjectRoot {
		t.Errorf("a directory holding .git was not recognised as a project root: %#v", work)
	}
	// A marker somewhere below does not make an ancestor a project root, or every
	// directory above a repository would claim to be one.
	home, err := service.DescribeNode(snapshotID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if home.IsProjectRoot {
		t.Error("the parent of a project claimed to be a project root")
	}
	plain, err := service.DescribeNode(snapshotID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if plain.IsProjectRoot {
		t.Error("a directory with no marker claimed to be a project root")
	}
}
