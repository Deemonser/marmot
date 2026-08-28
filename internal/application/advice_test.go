package application

import (
	"strings"
	"testing"
	"time"

	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/ports"
)

type stubEvidenceStore struct {
	ports.SnapshotStore
	result recommendation.EvidenceResult
	query  recommendation.EvidenceQuery
	calls  int
}

func (s *stubEvidenceStore) EvidenceNodes(query recommendation.EvidenceQuery) (recommendation.EvidenceResult, error) {
	s.query = query
	s.calls++
	// The real store keeps nodes at or above the floor; the stub honours that so
	// a raised floor actually shrinks the result.
	filtered := recommendation.EvidenceResult{
		Root: s.result.Root, VolumeTotalBytes: s.result.VolumeTotalBytes,
		VolumeUsedBytes: s.result.VolumeUsedBytes, VolumeFreeBytes: s.result.VolumeFreeBytes,
		FloorBytes: query.MinBytes,
	}
	for _, node := range s.result.Nodes {
		if node.ParentID == 0 || node.OwnedAllocated >= query.MinBytes {
			filtered.Nodes = append(filtered.Nodes, node)
		}
	}
	return filtered, nil
}

func serviceWithEvidence(result recommendation.EvidenceResult) (*Service, *stubEvidenceStore) {
	store := &stubEvidenceStore{result: result}
	return NewService(Dependencies{Store: store}), store
}

func evidenceNode(id, parent int64, path, name, kind string, bytes, residue int64) recommendation.EvidenceNode {
	return recommendation.EvidenceNode{
		ID: id, ParentID: parent, Path: path, Name: name, Kind: kind,
		OwnedAllocated: bytes, Residue: residue,
		NewestModified: time.Now().Add(-24 * time.Hour),
		OldestModified: time.Now().Add(-240 * time.Hour),
	}
}

// Offering both a directory and something inside it would double count the
// reclaimable bytes and stage two plan items covering the same files -- the
// overlap the cleanup guard refuses at plan creation anyway.
func TestRuleFindingsCollapseOverlapsToTheOutermost(t *testing.T) {
	service, _ := serviceWithEvidence(recommendation.EvidenceResult{
		Root: "/Users/alice", FloorBytes: 1 << 20,
		Nodes: []recommendation.EvidenceNode{
			evidenceNode(1, 0, "/Users/alice", "alice", "directory", 900<<20, 0),
			evidenceNode(2, 1, "/Users/alice/Library/Caches", "Caches", "directory", 600<<20, 100<<20),
			evidenceNode(3, 2, "/Users/alice/Library/Caches/com.example", "com.example", "directory", 500<<20, 500<<20),
		},
	})
	pack, err := service.BuildEvidencePack(7)
	if err != nil {
		t.Fatal(err)
	}
	findings := pack.RuleFindings()
	if len(findings) != 1 {
		t.Fatalf("expected one finding for the outer cache, got %d: %#v", len(findings), findings)
	}
	if findings[0].NodeID != 2 {
		t.Fatalf("expected the outer Caches node, got %d", findings[0].NodeID)
	}
	if findings[0].ReclaimableBytes != 600<<20 {
		t.Fatalf("reclaimable is %d, expected the snapshot's own 600 MB figure", findings[0].ReclaimableBytes)
	}
	if findings[0].Source != recommendation.SourceRule || findings[0].Confidence != 1 {
		t.Fatalf("a catalog match is not a guess: %#v", findings[0])
	}
}

// A suggestion on a path that can never be staged is not a suggestion. The home
// folder itself is the case that matters: everything inside it is deletable and
// the folder is not.
func TestRuleFindingsSkipProtectedPaths(t *testing.T) {
	service, _ := serviceWithEvidence(recommendation.EvidenceResult{
		Root: "/", FloorBytes: 1 << 20,
		Nodes: []recommendation.EvidenceNode{
			evidenceNode(1, 0, "/", "/", "directory", 900<<20, 0),
			evidenceNode(2, 1, "/Users/alice", "alice", "directory", 800<<20, 800<<20),
		},
	})
	pack, err := service.BuildEvidencePack(7)
	if err != nil {
		t.Fatal(err)
	}
	if findings := pack.RuleFindings(); len(findings) != 0 {
		t.Fatalf("a protected path produced findings: %#v", findings)
	}
}

func TestEvidencePackAppliesTheScaledFloor(t *testing.T) {
	service, store := serviceWithEvidence(recommendation.EvidenceResult{
		Root: "/", FloorBytes: 1 << 20,
		Nodes: []recommendation.EvidenceNode{evidenceNode(1, 0, "/", "/", "directory", 1<<30, 1<<30)},
	})
	if _, err := service.BuildEvidencePack(7); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Fatalf("a pack inside the ceiling should take one walk, took %d", store.calls)
	}
	if store.query.MinBytes != evidenceAbsoluteFloor {
		t.Fatalf("absolute floor is %d, expected %d", store.query.MinBytes, int64(evidenceAbsoluteFloor))
	}
	if store.query.MinShare != evidenceVolumeShare {
		t.Fatalf("share is %v, expected %v", store.query.MinShare, evidenceVolumeShare)
	}
	if store.query.MaxNodes != evidenceMaxNodes {
		t.Fatalf("ceiling is %d, expected %d", store.query.MaxNodes, evidenceMaxNodes)
	}
}

// A run of single-child waypoints says only "the thing below me is here". The
// row that survives has to keep the OUTERMOST id, because that is the outermost
// object whose deletion reclaims the bytes.
func TestTextCollapsesSingleChildChains(t *testing.T) {
	service, _ := serviceWithEvidence(recommendation.EvidenceResult{
		Root: "/Users/alice", FloorBytes: 1 << 20,
		Nodes: []recommendation.EvidenceNode{
			evidenceNode(1, 0, "/Users/alice", "alice", "directory", 900<<20, 0),
			evidenceNode(2, 1, "/Users/alice/one", "one", "directory", 800<<20, 0),
			evidenceNode(3, 2, "/Users/alice/one/two", "two", "directory", 800<<20, 0),
			evidenceNode(4, 3, "/Users/alice/one/two/three", "three", "directory", 800<<20, 800<<20),
		},
	})
	pack, err := service.BuildEvidencePack(7)
	if err != nil {
		t.Fatal(err)
	}
	text := pack.Text()
	if !strings.Contains(text, "one/two/three") {
		t.Fatalf("the chain was not collapsed:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "one/two/three") {
			if !strings.HasPrefix(line, "2\t") {
				t.Fatalf("the collapsed row must keep the outermost id 2, got %q", line)
			}
		}
	}
	// One row for the root and one for the whole chain.
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			rows++
		}
	}
	if rows != 2 {
		t.Fatalf("expected 2 rows after collapsing, got %d:\n%s", rows, text)
	}
}

// A waypoint that holds real bytes of its own is not a waypoint: collapsing it
// would hide an object worth acting on.
func TestTextKeepsANodeThatHoldsItsOwnBytes(t *testing.T) {
	service, _ := serviceWithEvidence(recommendation.EvidenceResult{
		Root: "/Users/alice", FloorBytes: 1 << 20,
		Nodes: []recommendation.EvidenceNode{
			evidenceNode(1, 0, "/Users/alice", "alice", "directory", 900<<20, 0),
			evidenceNode(2, 1, "/Users/alice/one", "one", "directory", 800<<20, 400<<20),
			evidenceNode(3, 2, "/Users/alice/one/two", "two", "directory", 400<<20, 400<<20),
		},
	})
	pack, err := service.BuildEvidencePack(7)
	if err != nil {
		t.Fatal(err)
	}
	text := pack.Text()
	if strings.Contains(text, "one/two") {
		t.Fatalf("a node holding half its own bytes was collapsed away:\n%s", text)
	}
}

// The preview and the payload are the same rendering, so they cannot drift.
func TestTextStatesTheFloorAndRoot(t *testing.T) {
	service, _ := serviceWithEvidence(recommendation.EvidenceResult{
		Root: "/Users/alice", FloorBytes: evidenceAbsoluteFloor, VolumeTotalBytes: 500_000_000_000, VolumeUsedBytes: 300_000_000_000, VolumeFreeBytes: 200_000_000_000,
		Nodes: []recommendation.EvidenceNode{evidenceNode(1, 0, "/Users/alice", "alice", "directory", 900<<20, 900<<20)},
	})
	pack, err := service.BuildEvidencePack(7)
	if err != nil {
		t.Fatal(err)
	}
	text := pack.Text()
	for _, want := range []string{"/Users/alice", "128MB", "500GB", "300GB"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the pack does not state %q:\n%s", want, text)
		}
	}
}

// The ceiling has to be enforced, not just documented. It previously checked
// only the node count, so a real full-disk pack came out at 84.9 KB against a
// stated 64 KB limit and nothing objected.
//
// Raising the floor cannot be simulated by filtering an existing result:
// residues merge into the nearest KEPT ancestor, so every surviving residue
// would understate what it covers. The walk has to be redone.
func TestEvidencePackRaisesTheFloorToFitTheCeiling(t *testing.T) {
	nodes := []recommendation.EvidenceNode{evidenceNode(1, 0, "/Users/alice", "alice", "directory", 1<<40, 0)}
	// Enough rows to blow the ceiling at the absolute floor, half of which
	// survive one doubling.
	for index := 0; index < 2400; index++ {
		size := int64(evidenceAbsoluteFloor)
		if index%2 == 0 {
			size = int64(evidenceAbsoluteFloor) * 4
		}
		name := strings.Repeat("n", 40) + string(rune('a'+index%26))
		nodes = append(nodes, evidenceNode(int64(index+2), 1, "/Users/alice/"+name, name, "directory", size, size))
	}
	service, store := serviceWithEvidence(recommendation.EvidenceResult{Root: "/Users/alice", Nodes: nodes})

	pack, err := service.BuildEvidencePack(7)
	if err != nil {
		t.Fatal(err)
	}
	if size := len(pack.Text()); size > evidenceMaxPayloadBytes {
		t.Fatalf("pack is %d bytes, over the %d ceiling", size, evidenceMaxPayloadBytes)
	}
	if store.calls < 2 {
		t.Fatalf("the floor was never raised: %d walk(s)", store.calls)
	}
	if pack.FloorBytes <= evidenceAbsoluteFloor {
		t.Fatalf("floor is %d, expected it to be raised above %d", pack.FloorBytes, int64(evidenceAbsoluteFloor))
	}
}

// Rows below a rule that already settles the whole subtree buy nothing: the
// advice for everything under ~/.gradle/caches is one sentence, and 77 rows of
// transform hashes do not make it a different sentence. Measured on a real
// full-disk pack, those rows were 12.6% of the payload.
func TestEvidencePackFoldsBelowSafeRules(t *testing.T) {
	const gb = 1_000_000_000
	service, _ := serviceWithEvidence(recommendation.EvidenceResult{
		Root: "/Users/alice", FloorBytes: 1 << 20,
		Nodes: []recommendation.EvidenceNode{
			evidenceNode(1, 0, "/Users/alice", "alice", "directory", 10*gb, 1*gb),
			// Gradle cache: safe + redownloadable, one decision for the lot.
			evidenceNode(2, 1, "/Users/alice/.gradle/caches", "caches", "directory", 6*gb, 1*gb),
			evidenceNode(3, 2, "/Users/alice/.gradle/caches/transforms", "transforms", "directory", 5*gb, 2*gb),
			evidenceNode(4, 3, "/Users/alice/.gradle/caches/transforms/abc", "abc", "directory", 3*gb, 3*gb),
			// User cache: review, so a person may keep one app and drop another.
			evidenceNode(5, 1, "/Users/alice/Library/Caches", "Caches", "directory", 3*gb, 1*gb),
			evidenceNode(6, 5, "/Users/alice/Library/Caches/com.example", "com.example", "directory", 2*gb, 2*gb),
		},
	})
	pack, err := service.BuildEvidencePack(7)
	if err != nil {
		t.Fatal(err)
	}
	present := map[int64]recommendation.EvidenceNode{}
	for _, node := range pack.Nodes {
		present[node.ID] = node
	}
	for _, id := range []int64{3, 4} {
		if _, ok := present[id]; ok {
			t.Fatalf("node %d sits below a settled safe rule and should have folded away", id)
		}
	}
	// The review-rule subtree stays: that is where picking from inside matters.
	if _, ok := present[6]; !ok {
		t.Fatal("a subtree under a review rule was folded away")
	}
	// Residues partition. Folding must move bytes, not lose them.
	var sum int64
	for _, node := range pack.Nodes {
		sum += node.Residue
	}
	if sum != 10*gb {
		t.Fatalf("residues sum to %d after folding, expected the root total %d", sum, int64(10*gb))
	}
	if got, want := present[2].Residue, int64(1*gb+2*gb+3*gb); got != want {
		t.Fatalf("the settled node absorbed %d, expected %d", got, want)
	}
}
