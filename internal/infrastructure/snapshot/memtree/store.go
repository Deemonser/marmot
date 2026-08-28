package memtree

import (
	"fmt"
	"sync"

	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/domain/scan"
)

// Store holds the scan results this process produced. Only the newest is kept:
// the product shows one result at a time, and keeping more would just be a cache
// under another name (ADR-0055).
type Store struct {
	mu      sync.RWMutex
	nextID  int64
	current int64
	trees   map[int64]*tree
}

func OpenStore() *Store {
	return &Store{nextID: 1, trees: make(map[int64]*tree)}
}

// Close releases the result. There is nothing to flush.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trees = make(map[int64]*tree)
	s.current = 0
	return nil
}

func (s *Store) CreateSnapshot(taskID, root string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	// Replacing the previous result frees it: two full trees at once would double
	// the only cost this design has.
	s.trees = map[int64]*tree{id: newTree(taskID, root)}
	s.current = id
	return id, nil
}

func (s *Store) treeFor(snapshotID int64) (*tree, error) {
	result, ok := s.trees[snapshotID]
	if !ok {
		return nil, fmt.Errorf("%w: unknown snapshot %d", ErrInvalidRequest, snapshotID)
	}
	return result, nil
}

func (s *Store) UpdateSnapshotPhase(snapshotID int64, phase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return err
	}
	result.phase = phase
	return nil
}

func (s *Store) InsertNodes(snapshotID int64, nodes []scan.Node) error {
	if len(nodes) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return err
	}
	return result.insert(nodes)
}

func (s *Store) UpdateDirectorySizes(snapshotID int64, sizes map[int64]scan.DirectorySize) error {
	if len(sizes) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return err
	}
	return result.applySizes(sizes)
}

func (s *Store) InsertIssues(snapshotID int64, issues []scan.Issue) error {
	if len(issues) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return err
	}
	result.issues = append(result.issues, issues...)
	return nil
}

// SetSnapshotVolume records the volume state behind the result, so the space map
// balances to the disk it came from (ADR-0052 §4).
func (s *Store) SetSnapshotVolume(snapshotID int64, total, used, free uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return err
	}
	result.volumeTotal, result.volumeUsed, result.volumeFree = total, used, free
	return nil
}

// FinishScan makes the result queryable. It replaces the old two-step
// publish-then-persist: with nothing to persist, the visible terminal state is
// the only terminal state (ADR-0055).
func (s *Store) FinishScan(snapshotID int64, state, failure string, nodeCount, fileCount, directoryCount, bytes, issues int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return err
	}
	result.finish(state, failure, nodeCount, fileCount, directoryCount, bytes, issues)
	return nil
}

// ProjectionCoverage stays as ADR-0049's assertion hook: with the tree as the
// store there is nothing to trim, so it must always report full coverage.
func (s *Store) ProjectionCoverage(snapshotID int64) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return 0, 0, err
	}
	covered, total := result.coverage()
	return covered, total, nil
}

func (s *Store) NodeByID(snapshotID, nodeID int64) (scan.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return scan.Node{}, err
	}
	return result.node(nodeID)
}

func (s *Store) NodeByPath(snapshotID int64, path string) (scan.Node, error) {
	// Lock, not RLock: an early query may have to build the child grouping.
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return scan.Node{}, err
	}
	id, ok := result.nodeIDByPath(path)
	if !ok {
		return scan.Node{}, ErrNodeNotFound
	}
	return result.node(id)
}

func (s *Store) Children(snapshotID, parentID int64, limit, offset int) ([]scan.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return nil, err
	}
	return (&treeQuery{snapshotID: snapshotID, tree: result}).mapChildren(parentID, limit, offset)
}

func (s *Store) Map(query scan.MapQuery) (scan.MapResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(query.SnapshotID)
	if err != nil {
		return scan.MapResult{}, err
	}
	if !result.finished {
		return scan.MapResult{}, ErrResultUnavailable
	}
	return buildMap(&treeQuery{snapshotID: query.SnapshotID, tree: result}, query)
}

// EvidenceNodes walks the whole result once. It takes the write lock for the
// same reason Map does: the walk calls ensureGrouped, which rebuilds the child
// index in place when a node has been inserted since the last query.
func (s *Store) EvidenceNodes(query recommendation.EvidenceQuery) (recommendation.EvidenceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.treeFor(query.SnapshotID)
	if err != nil {
		return recommendation.EvidenceResult{}, err
	}
	if !result.finished {
		return recommendation.EvidenceResult{}, ErrResultUnavailable
	}
	return (&treeQuery{snapshotID: query.SnapshotID, tree: result}).evidenceNodes(query)
}

func (s *Store) SnapshotVersion(snapshotID int64) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, err := s.treeFor(snapshotID)
	if err != nil {
		return 0, err
	}
	return result.version, nil
}

func (s *Store) SnapshotByTaskID(taskID string) (scan.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, result := range s.trees {
		if result.taskID == taskID {
			return result.snapshot(id), nil
		}
	}
	return scan.Snapshot{}, fmt.Errorf("%w: no result for task %s", ErrInvalidRequest, taskID)
}
