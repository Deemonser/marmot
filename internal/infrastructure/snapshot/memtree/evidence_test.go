package memtree

import (
	"errors"
	"testing"
	"time"

	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/domain/scan"
)

// A tree with one heavy directory of many medium files, one single heavy file,
// and one directory below the floor. That is the shape R-062 §3.3 found to
// matter: a 12 GB swarm of 139,969 jars and a single 3.28 GB git pack are the
// same size and completely different recommendations.
func evidenceTree(t *testing.T, modified time.Time) (*Store, int64) {
	t.Helper()
	store := OpenStore()
	id, err := store.CreateSnapshot("task", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertNodes(id, []scan.Node{
		{ID: 1, Path: "/root", Name: "root", Kind: "directory", HasChildren: true, ModifiedAt: modified},
		{ID: 2, ParentID: 1, Path: "/root/keep", Name: "keep", Kind: "directory", HasChildren: true, ModifiedAt: modified},
		{ID: 3, ParentID: 1, Path: "/root/big.bin", Name: "big.bin", Kind: "file", OwnedAllocated: 3000, ModifiedAt: modified},
		{ID: 4, ParentID: 2, Path: "/root/keep/a.jar", Name: "a.jar", Kind: "file", OwnedAllocated: 2000, ModifiedAt: modified},
		{ID: 5, ParentID: 2, Path: "/root/keep/b.jar", Name: "b.jar", Kind: "file", OwnedAllocated: 2000, ModifiedAt: modified},
		{ID: 6, ParentID: 2, Path: "/root/keep/c.jar", Name: "c.jar", Kind: "file", OwnedAllocated: 2000, ModifiedAt: modified},
		{ID: 7, ParentID: 1, Path: "/root/small", Name: "small", Kind: "directory", HasChildren: true, ModifiedAt: modified},
		{ID: 8, ParentID: 7, Path: "/root/small/x.txt", Name: "x.txt", Kind: "file", OwnedAllocated: 1000, ModifiedAt: modified},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDirectorySizes(id, map[int64]scan.DirectorySize{
		1: {OwnedAllocated: 10000, AllocatedSize: 10000, Confidence: "exact", SizeBasis: "test"},
		2: {OwnedAllocated: 6000, AllocatedSize: 6000, Confidence: "exact", SizeBasis: "test"},
		7: {OwnedAllocated: 1000, AllocatedSize: 1000, Confidence: "exact", SizeBasis: "test"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(id, scan.JobCompleted, "", 8, 5, 3, 10000, 0); err != nil {
		t.Fatal(err)
	}
	return store, id
}

func evidenceByName(nodes []recommendation.EvidenceNode) map[string]recommendation.EvidenceNode {
	byName := make(map[string]recommendation.EvidenceNode, len(nodes))
	for _, node := range nodes {
		byName[node.Name] = node
	}
	return byName
}

// The invariant the whole skeleton rests on, and the one that caught the broken
// first version of the metric in R-062 §2.1: residues partition the total. If
// they did not, "what the model cannot see inside of" would be either double
// counted or silently dropped.
func TestEvidenceResiduesPartitionTheTotal(t *testing.T) {
	store, id := evidenceTree(t, time.Now().Add(-48*time.Hour))
	result, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 2500})
	if err != nil {
		t.Fatal(err)
	}
	nodes := result.Nodes
	var sum int64
	for _, node := range nodes {
		sum += node.Residue
	}
	if sum != 10000 {
		t.Fatalf("residues sum to %d, expected the root total 10000: %#v", sum, nodes)
	}
}

func TestEvidenceKeepsTheFloorAndStaysAConnectedSubtree(t *testing.T) {
	store, id := evidenceTree(t, time.Now().Add(-48*time.Hour))
	result, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 2500})
	if err != nil {
		t.Fatal(err)
	}
	nodes := result.Nodes
	present := map[int64]bool{}
	for _, node := range nodes {
		present[node.ID] = true
	}
	if len(nodes) != 3 {
		t.Fatalf("expected root, keep and big.bin, got %d: %#v", len(nodes), nodes)
	}
	for _, node := range nodes {
		if node.OwnedAllocated < 2500 && node.ID != 1 {
			t.Fatalf("%s is below the floor", node.Name)
		}
		// Closed under ancestors: a node above the floor forces its parent above
		// it, so the result is always a subtree rooted at the scan root.
		if node.ParentID != 0 && !present[node.ParentID] {
			t.Fatalf("%s was kept but its parent %d was not", node.Name, node.ParentID)
		}
	}
}

// A directory holding one huge file and a directory holding a swarm of medium
// files must be distinguishable, because they are different recommendations.
func TestEvidenceSeparatesASwarmFromOneBigObject(t *testing.T) {
	store, id := evidenceTree(t, time.Now().Add(-48*time.Hour))
	result, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 2500})
	if err != nil {
		t.Fatal(err)
	}
	byName := evidenceByName(result.Nodes)

	keep := byName["keep"]
	if keep.SubtreeFiles != 3 || keep.BiggestFile != 2000 {
		t.Fatalf("keep: files=%d biggest=%d, expected 3 and 2000", keep.SubtreeFiles, keep.BiggestFile)
	}
	big := byName["big.bin"]
	if big.SubtreeFiles != 1 || big.BiggestFile != 3000 {
		t.Fatalf("big.bin: files=%d biggest=%d, expected 1 and 3000", big.SubtreeFiles, big.BiggestFile)
	}
	root := byName["root"]
	if root.SubtreeFiles != 5 || root.SubtreeDirs != 3 {
		t.Fatalf("root: files=%d dirs=%d, expected 5 and 3", root.SubtreeFiles, root.SubtreeDirs)
	}
}

// A kept child accounts for its own extensions. If they leaked upward the
// parent's profile would describe bytes it does not own, and two suggestions
// would claim the same files.
func TestEvidenceAttributesExtensionsToTheResidue(t *testing.T) {
	store, id := evidenceTree(t, time.Now().Add(-48*time.Hour))
	result, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 2500})
	if err != nil {
		t.Fatal(err)
	}
	byName := evidenceByName(result.Nodes)

	keep := byName["keep"]
	if len(keep.TopExtensions) != 1 || keep.TopExtensions[0].Extension != ".jar" ||
		keep.TopExtensions[0].Bytes != 6000 || keep.TopExtensions[0].Files != 3 {
		t.Fatalf("keep residue profile: %#v", keep.TopExtensions)
	}
	// The root's residue is only the sub-floor `small` directory, so .jar and
	// .bin must not appear here -- they belong to kept nodes.
	root := byName["root"]
	if len(root.TopExtensions) != 1 || root.TopExtensions[0].Extension != ".txt" || root.TopExtensions[0].Bytes != 1000 {
		t.Fatalf("root residue profile leaked a kept child's files: %#v", root.TopExtensions)
	}
}

// A silently shortened skeleton reads as "this is everything" when it is not, so
// the ceiling is an error and not a truncation point.
func TestEvidenceRefusesRatherThanTruncating(t *testing.T) {
	store, id := evidenceTree(t, time.Now().Add(-48*time.Hour))
	_, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 1, MaxNodes: 2})
	if err == nil {
		t.Fatal("a floor that keeps more nodes than the ceiling returned a truncated skeleton instead of an error")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest, got %v", err)
	}
}

// An mtime in the future is not "modified -534 days ago" -- R-062 §3.6 measured
// exactly that on the reference machine. The age clamps and the oddity is
// reported instead of being smoothed away.
func TestEvidenceReportsFutureModificationInsteadOfNegativeAge(t *testing.T) {
	future := time.Now().Add(72 * time.Hour)
	store, id := evidenceTree(t, future)
	result, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 2500})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, node := range result.Nodes {
		if !node.FutureModified {
			t.Fatalf("%s has a future mtime but was not flagged", node.Name)
		}
		if age := node.AgeDays(now); age != 0 {
			t.Fatalf("%s reported age %d, expected the clamp to 0", node.Name, age)
		}
	}
}

func TestEvidenceRefusesAnUnfinishedResult(t *testing.T) {
	store := OpenStore()
	id, err := store.CreateSnapshot("task", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 1}); !errors.Is(err, ErrResultUnavailable) {
		t.Fatalf("expected ErrResultUnavailable, got %v", err)
	}
}

func TestEvidenceRefusesANonPositiveFloor(t *testing.T) {
	store, id := evidenceTree(t, time.Now())
	if _, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for a zero floor, got %v", err)
	}
}

// Round two asks for the inside of one region, not a finer skeleton of the whole
// disk. The subtree it returns has to balance to that node's own total, or the
// advisor is being shown a partition that does not add up.
func TestEvidenceScopesToASubtree(t *testing.T) {
	store, id := evidenceTree(t, time.Now().Add(-48*time.Hour))
	result, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, RootID: 2, MinBytes: 1500})
	if err != nil {
		t.Fatal(err)
	}
	if result.Root != "/root/keep" {
		t.Fatalf("root is %q, expected the subtree's own path", result.Root)
	}
	var residue int64
	for _, node := range result.Nodes {
		residue += node.Residue
		if node.ID == 3 || node.ID == 7 || node.ID == 8 {
			t.Fatalf("a node outside the subtree leaked in: %#v", node)
		}
	}
	if residue != 6000 {
		t.Fatalf("subtree residues sum to %d, expected the subtree total 6000", residue)
	}
	// The jars are above this lower floor, so the region is now legible.
	if len(result.Nodes) != 4 {
		t.Fatalf("expected the directory and its three jars, got %d", len(result.Nodes))
	}
}

func TestEvidenceRefusesAnUnknownSubtreeRoot(t *testing.T) {
	store, id := evidenceTree(t, time.Now())
	if _, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, RootID: 999, MinBytes: 1}); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
}

// Source activity is not the artifact's own mtime. A build directory written
// long ago inside a project whose source changed yesterday is not cold: it is
// about to be rebuilt, and calling it stale is how a tool offers to delete the
// cache its user is in the middle of using.
func TestEvidenceSeparatesSourceActivityFromToolOutput(t *testing.T) {
	store := OpenStore()
	id, err := store.CreateSnapshot("task", "/root")
	if err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-24 * time.Hour)
	ancient := time.Now().Add(-600 * 24 * time.Hour)
	if err := store.InsertNodes(id, []scan.Node{
		{ID: 1, Path: "/root", Name: "root", Kind: "directory", HasChildren: true, ModifiedAt: ancient},
		{ID: 2, ParentID: 1, Path: "/root/proj", Name: "proj", Kind: "directory", HasChildren: true, ModifiedAt: ancient},
		// The marker that makes /root/proj a project root.
		{ID: 3, ParentID: 2, Path: "/root/proj/Cargo.toml", Name: "Cargo.toml", Kind: "file", OwnedAllocated: 100, ModifiedAt: ancient},
		// Source touched yesterday.
		{ID: 4, ParentID: 2, Path: "/root/proj/main.rs", Name: "main.rs", Kind: "file", OwnedAllocated: 200, ModifiedAt: recent},
		// Tool output written long ago and huge.
		{ID: 5, ParentID: 2, Path: "/root/proj/target", Name: "target", Kind: "directory", HasChildren: true, ModifiedAt: ancient},
		{ID: 6, ParentID: 5, Path: "/root/proj/target/blob", Name: "blob", Kind: "file", OwnedAllocated: 9000, ModifiedAt: ancient},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDirectorySizes(id, map[int64]scan.DirectorySize{
		1: {OwnedAllocated: 9300}, 2: {OwnedAllocated: 9300}, 5: {OwnedAllocated: 9000},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(id, scan.JobCompleted, "", 6, 4, 2, 9300, 0); err != nil {
		t.Fatal(err)
	}
	result, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	byName := evidenceByName(result.Nodes)

	proj := byName["proj"]
	if !proj.IsProjectRoot {
		t.Fatal("a directory holding Cargo.toml is a project root")
	}
	// The project's source moved yesterday, so it is live.
	if proj.SourceNewestModified.Before(recent.Add(-time.Hour)) {
		t.Fatalf("project source activity is %v, expected yesterday", proj.SourceNewestModified)
	}

	// The tool output is the point of the exercise: 9000 of the project's 9300
	// bytes, all of it written 600 days ago. Its own age says "cold" and its
	// source activity correctly says nothing -- so a rule conditioned on the
	// artifact's age would offer to delete the cache of a project touched
	// yesterday, and one conditioned on project source activity will not.
	target := byName["target"]
	if age := target.AgeDays(time.Now()); age < 500 {
		t.Fatalf("target's own age is %d days, expected it to look cold", age)
	}
	if target.SourceNewestModified.After(ancient.Add(time.Hour)) {
		t.Fatalf("target's source activity is %v; nothing inside it is source", target.SourceNewestModified)
	}
	if target.IsProjectRoot {
		t.Fatal("target holds no project marker")
	}
}

// Browsing a folder in Finder is not working on a project. Measured on a real
// tree: a project last edited 641 days ago reported 148 days of idleness purely
// because Finder had written a .DS_Store, which was enough to keep its 547 MB of
// build output out of the dormant bracket.
func TestEvidenceIgnoresSystemWrittenFilesForSourceActivity(t *testing.T) {
	store := OpenStore()
	id, err := store.CreateSnapshot("task", "/root")
	if err != nil {
		t.Fatal(err)
	}
	browsed := time.Now().Add(-148 * 24 * time.Hour)
	edited := time.Now().Add(-641 * 24 * time.Hour)
	if err := store.InsertNodes(id, []scan.Node{
		{ID: 1, Path: "/root", Name: "root", Kind: "directory", HasChildren: true, ModifiedAt: edited},
		{ID: 2, ParentID: 1, Path: "/root/proj", Name: "proj", Kind: "directory", HasChildren: true, ModifiedAt: edited},
		{ID: 3, ParentID: 2, Path: "/root/proj/build.gradle", Name: "build.gradle", Kind: "file", OwnedAllocated: 100, ModifiedAt: edited},
		{ID: 4, ParentID: 2, Path: "/root/proj/Main.kt", Name: "Main.kt", Kind: "file", OwnedAllocated: 200, ModifiedAt: edited},
		// Finder, not the developer.
		{ID: 5, ParentID: 2, Path: "/root/proj/.DS_Store", Name: ".DS_Store", Kind: "file", OwnedAllocated: 6148, ModifiedAt: browsed},
		{ID: 6, ParentID: 2, Path: "/root/proj/pad", Name: "pad", Kind: "file", OwnedAllocated: 9000, ModifiedAt: edited},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDirectorySizes(id, map[int64]scan.DirectorySize{
		1: {OwnedAllocated: 15448}, 2: {OwnedAllocated: 15448},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishScan(id, scan.JobCompleted, "", 6, 4, 2, 15448, 0); err != nil {
		t.Fatal(err)
	}
	result, err := store.EvidenceNodes(recommendation.EvidenceQuery{SnapshotID: id, MinBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	proj := evidenceByName(result.Nodes)["proj"]
	if proj.SourceNewestModified.After(edited.Add(24 * time.Hour)) {
		t.Fatalf("source activity is %v; a .DS_Store made a dormant project look live", proj.SourceNewestModified)
	}
	// The overall newest mtime still sees it -- only the source signal ignores it.
	if proj.NewestModified.Before(browsed.Add(-24 * time.Hour)) {
		t.Fatalf("overall newest mtime is %v; it should still include the .DS_Store", proj.NewestModified)
	}
}
