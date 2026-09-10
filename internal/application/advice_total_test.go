package application

import (
	"testing"

	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
)

func recommendationWithNode(nodeID, allocated int64) recommendation.Recommendation {
	return recommendation.Recommendation{NodeID: nodeID, ReclaimableBytes: allocated}
}

// ADR-0074 §2: an advice total is what deleting the suggestions TOGETHER gives
// back, not a sum. Two suggestions can hold the two halves of one clone group;
// each alone reclaims nothing, both together reclaim the stream once. Summing
// the per-item figures reports zero for exactly that case.
type reclaimStubStore struct {
	ports.SnapshotStore
	// perSet answers Reclaimable for a set, keyed by the sorted IDs.
	sets  map[int64]scan.Reclaimable
	pair  scan.Reclaimable
	calls int
}

func (s *reclaimStubStore) Reclaimable(_ int64, ids []int64) (scan.Reclaimable, error) {
	s.calls++
	if len(ids) == 1 {
		return s.sets[ids[0]], nil
	}
	return s.pair, nil
}

func TestAdviceTotalIsAskedForTheWholeSet(t *testing.T) {
	const half = int64(8 << 20)
	store := &reclaimStubStore{
		// Each clone alone: nothing certain, the whole extent shared away.
		sets: map[int64]scan.Reclaimable{
			11: {Bytes: 0, SharedExcluded: half, UpperBound: half, Files: 1},
			12: {Bytes: 0, SharedExcluded: half, UpperBound: half, Files: 1},
		},
		// Both together: the stream comes back once.
		pair: scan.Reclaimable{Bytes: half, UpperBound: half, Files: 2},
	}
	service := &Service{store: store}
	advice := Advice{Items: []AdviceItem{
		{Recommendation: recommendationWithNode(11, half)},
		{Recommendation: recommendationWithNode(12, half)},
	}}
	service.applyReclaimable(1, &advice)

	if advice.TotalBytes != half {
		t.Errorf("total: got %d, want %d -- the set answer, not the sum of the rows (which is 0)", advice.TotalBytes, half)
	}
	if advice.Items[0].ReclaimableBytes != 0 || advice.Items[1].ReclaimableBytes != 0 {
		t.Errorf("each row on its own still reclaims nothing: %d / %d", advice.Items[0].ReclaimableBytes, advice.Items[1].ReclaimableBytes)
	}
	if advice.TotalUpperBound != half || advice.TotalUnknown {
		t.Errorf("upper bound %d unknown %v", advice.TotalUpperBound, advice.TotalUnknown)
	}
}

func TestAdviceTotalCarriesUnknownAndSharedExcluded(t *testing.T) {
	store := &reclaimStubStore{
		sets: map[int64]scan.Reclaimable{
			// This one's volume said nothing: the row keeps its allocated figure.
			21: {Unknown: true, UnknownBytes: 100, UpperBound: 100, Files: 1},
			22: {Bytes: 50, UpperBound: 50, Files: 1},
		},
		pair: scan.Reclaimable{Bytes: 50, SharedExcluded: 30, Unknown: true, UnknownBytes: 100, UpperBound: 180, Files: 2},
	}
	service := &Service{store: store}
	advice := Advice{Items: []AdviceItem{
		{Recommendation: recommendationWithNode(21, 100)},
		{Recommendation: recommendationWithNode(22, 70)},
	}}
	service.applyReclaimable(1, &advice)

	if !advice.Items[0].ReclaimableUnknown {
		t.Error("an unknown row must say so")
	}
	if advice.Items[0].ReclaimableBytes != 100 {
		t.Errorf("an unknown row keeps its allocated figure, got %d", advice.Items[0].ReclaimableBytes)
	}
	if advice.TotalBytes != 50 || advice.TotalUpperBound != 180 || advice.TotalSharedExcluded != 30 || !advice.TotalUnknown {
		t.Errorf("the total must carry the floor, the ceiling and why: %+v", advice)
	}
}

// A store from before ADR-0074, or one that fails: the allocated figures stand,
// which is what this layer reported all along. Nothing becomes zero.
func TestAdviceTotalFallsBackWhenTheStoreCannotAnswer(t *testing.T) {
	service := &Service{store: &stubEvidenceStore{}}
	advice := Advice{Items: []AdviceItem{
		{Recommendation: recommendationWithNode(31, 400)},
		{Recommendation: recommendationWithNode(32, 600)},
	}}
	service.applyReclaimable(1, &advice)
	if advice.TotalBytes != 1000 || advice.TotalUpperBound != 1000 {
		t.Errorf("without an answer the allocated sum stands: %+v", advice)
	}
	if advice.Items[0].ReclaimableBytes != 400 || advice.Items[0].ReclaimableUnknown {
		t.Errorf("rows unchanged: %+v", advice.Items[0])
	}
}
