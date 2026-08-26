package memtree

import (
	"fmt"

	"example.com/marmot/internal/domain/scan"
)

// The tree is the only mapSource. Every method answers from memory; nothing here
// can fail for I/O reasons, which is why none of them touch the filesystem.
var _ mapSource = (*treeQuery)(nil)

// treeQuery pairs a tree with the snapshot ID the caller asked about, which is
// all buildMap needs on top of the tree itself.
type treeQuery struct {
	snapshotID int64
	tree       *tree
}

func (q *treeQuery) mapSnapshotID() int64      { return q.snapshotID }
func (q *treeQuery) mapSnapshotVersion() int64 { return q.tree.version }
func (q *treeQuery) mapRootNodeID() int64      { return q.tree.rootNodeID }

func (q *treeQuery) mapVolume() (total, used, free uint64) {
	return q.tree.volumeTotal, q.tree.volumeUsed, q.tree.volumeFree
}

func (q *treeQuery) mapNodeByID(nodeID int64) (scan.Node, error) {
	return q.tree.node(nodeID)
}

func (q *treeQuery) mapChildCount(parentID int64) (int, error) {
	return len(q.tree.children(parentID)), nil
}

func (q *treeQuery) mapChildren(parentID int64, limit, offset int) ([]scan.Node, error) {
	if offset < 0 {
		return nil, fmt.Errorf("%w: negative child offset", ErrInvalidRequest)
	}
	limit, err := normalizePageLimit(limit)
	if err != nil {
		return nil, err
	}
	ids := q.tree.children(parentID)
	if offset >= len(ids) {
		return []scan.Node{}, nil
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}
	nodes := make([]scan.Node, 0, end-offset)
	for _, id := range ids[offset:end] {
		node, err := q.tree.node(int64(id))
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// mapAggregateChildren folds the tail of a page into one entry. It sums the
// prefix rather than keeping running totals: the prefix is at most one page, and
// three int64 prefix sums per child cost 24 bytes each in the durable index that
// ADR-0055 removed.
func (q *treeQuery) mapAggregateChildren(parentID int64, offset int, basis string) (scan.MapEntry, error) {
	ids := q.tree.children(parentID)
	if offset < 0 || offset > len(ids) {
		return scan.MapEntry{}, fmt.Errorf("%w: invalid aggregate offset", ErrInvalidRequest)
	}
	entry := emptyRemainingEntry(basis)
	entry.Confidence = "estimated"
	entry.Count = int64(len(ids) - offset)
	for _, id := range ids[offset:] {
		child, err := q.tree.node(int64(id))
		if err != nil {
			return scan.MapEntry{}, err
		}
		entry.LogicalSize += child.LogicalSize
		entry.AllocatedSize += child.AllocatedSize
		entry.OwnedAllocated += child.OwnedAllocated
	}
	return entry, nil
}

// mapProjectedChildren returns the slim arcs below the current level. They carry
// no path, so a projected descendant cannot authorise a file operation
// (ADR-0048, DDD invariant 17).
// minSize stops the walk instead of filtering afterwards. Children are held in
// descending size order, so the first one below the threshold means every one
// after it is too. Filtering after the fact still materialised every child of
// every node the projection touched, which cost 14x the assembly time for fewer
// arcs once the depth went from 5 to 12 (ADR-0059 §3).
func (q *treeQuery) mapProjectedChildren(parentID int64, limit int, minSize int64) ([]scan.ProjectedEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	ids := q.tree.children(parentID)
	if limit > len(ids) {
		limit = len(ids)
	}
	entries := make([]scan.ProjectedEntry, 0, limit)
	for _, id := range ids[:limit] {
		slim, ok := q.tree.slim(int64(id))
		if !ok {
			continue
		}
		if minSize > 0 && slim.OwnedAllocated < minSize {
			break
		}
		entries = append(entries, slim)
	}
	return entries, nil
}
