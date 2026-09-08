package memtree

import (
	"fmt"
	"sort"

	"example.com/marmot/internal/domain/scan"
)

// replaceSubtree swaps one directory's contents for a fresh read of it, in
// place. `nodes` and `sizes` come from a scan whose root was that directory, so
// they are numbered by that sub-scan: node 1 is the directory itself, and every
// other node's ParentID is a sub-scan ID. They are re-numbered as they are
// stored.
//
// IDs are kept where the object is the same: a new node whose name matches an
// old child of the same (already mapped) parent takes the old child's ID and
// overwrites its record. A client holding that ID -- a staged row in the dock,
// a breadcrumb, an outer-ring arc -- goes on pointing at the object it meant.
// Objects with no such match get a fresh ID at the end of the table; old
// records nothing claimed are detached the way removeSubtree detaches, by
// clearing parentID, which valid() reads as "gone".
//
// This exists for the same reason removeSubtree does: a full re-scan is 10-20
// seconds of work to learn what one directory now holds. The cost here is the
// sub-scan plus O(size of old subtree + size of new subtree), and one child
// regroup afterwards, which is ~70 ms on a 2.7M-node tree (R-072).
func (t *tree) replaceSubtree(keep int64, nodes []scan.Node, sizes map[int64]scan.DirectorySize) (scan.SubtreeReplacement, error) {
	t.ensureGrouped()
	if !t.valid(keep) {
		return scan.SubtreeReplacement{}, ErrNodeNotFound
	}
	if keep == t.rootNodeID {
		return scan.SubtreeReplacement{}, fmt.Errorf("%w: the scan root is re-read by scanning again", ErrInvalidRequest)
	}
	keepRecord := t.records.at(keep)
	if t.kinds.value(keepRecord.kind) != "directory" {
		return scan.SubtreeReplacement{}, fmt.Errorf("%w: only a directory can be re-read", ErrInvalidRequest)
	}

	// The old subtree, counted and remembered so what is not reclaimed can be
	// detached. An attached volume (ADR-0052 §3) has no identity on disk and
	// would not come back from a re-read, so a subtree holding one is refused
	// rather than quietly losing its volumes.
	var old removal
	oldIDs := []int64{}
	stack := []int64{keep}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		kind := t.kinds.value(t.records.at(current).kind)
		if kind == "volume" {
			return scan.SubtreeReplacement{}, ErrSubtreeHasVolumes
		}
		old.nodes++
		if kind == "directory" {
			old.directories++
		} else {
			old.files++
		}
		oldIDs = append(oldIDs, current)
		for _, child := range t.children(current) {
			stack = append(stack, int64(child))
		}
	}
	oldAllocated, oldLogical := keepRecord.ownedAllocated, keepRecord.logicalSize

	// Parents precede children in the sub-scan's numbering, so storing in ID
	// order guarantees a node's parent is mapped before the node is reached.
	ordered := make([]scan.Node, len(nodes))
	copy(ordered, nodes)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	if len(ordered) == 0 || ordered[0].ID != 1 || ordered[0].ParentID != 0 {
		return scan.SubtreeReplacement{}, fmt.Errorf("%w: a re-read must start at its own root, node 1", ErrInvalidRequest)
	}

	oldLen := t.records.len()
	idMap := map[int64]int64{1: keep}
	claimed := map[int64]struct{}{}
	// name -> old child ID, per mapped parent, built from the index as it stood
	// before this call. Only old records qualify: nothing added here can be
	// reclaimed by a later node of the same read.
	nameIndex := map[int64]map[string]int64{}
	childrenByName := func(parent int64) map[string]int64 {
		if index, ok := nameIndex[parent]; ok {
			return index
		}
		index := map[string]int64{}
		if parent < oldLen {
			for _, child := range t.children(parent) {
				entry := t.records.at(int64(child))
				index[t.names.get(entry.nameOffset, entry.nameLength)] = int64(child)
			}
		}
		nameIndex[parent] = index
		return index
	}

	var fresh removal
	var kept, added int64
	for _, node := range ordered {
		if node.ID == 1 {
			// The directory keeps its record -- name, parent, ID -- and takes the
			// fresh identity and mtime the read found.
			keepRecord.device, keepRecord.inode = int64(node.Device), int64(node.Inode)
			keepRecord.modifiedUnix = 0
			if !node.ModifiedAt.IsZero() {
				keepRecord.modifiedUnix = node.ModifiedAt.UnixNano()
			}
			fresh.nodes++
			fresh.directories++
			continue
		}
		parent, ok := idMap[node.ParentID]
		if !ok {
			return scan.SubtreeReplacement{}, fmt.Errorf("%w: node %d arrived before its parent %d", ErrInvalidRequest, node.ID, node.ParentID)
		}
		id := int64(0)
		if oldID, found := childrenByName(parent)[node.Name]; found {
			if _, taken := claimed[oldID]; !taken && t.kinds.value(t.records.at(oldID).kind) == node.Kind {
				id = oldID
				claimed[oldID] = struct{}{}
				kept++
			}
		}
		if id == 0 {
			id = t.records.len()
			t.records.grow(id + 1)
			added++
		}
		entry, err := t.encode(node, parent)
		if err != nil {
			return scan.SubtreeReplacement{}, err
		}
		*t.records.at(id) = entry
		idMap[node.ID] = id
		fresh.nodes++
		if node.Kind == "directory" {
			fresh.directories++
		} else {
			fresh.files++
		}
	}

	var removed int64
	for _, id := range oldIDs {
		if id == keep {
			continue
		}
		if _, isKept := claimed[id]; isKept {
			continue
		}
		t.records.at(id).parentID = 0
		removed++
	}

	for subID, size := range sizes {
		id, ok := idMap[subID]
		if !ok {
			continue
		}
		entry := t.records.at(id)
		entry.ownedAllocated = size.OwnedAllocated
		entry.logicalSize = size.LogicalSize
		if size.Confidence != "" {
			code, err := t.confidences.code(size.Confidence)
			if err != nil {
				return scan.SubtreeReplacement{}, err
			}
			entry.confidence = code
		}
		if size.SizeBasis != "" {
			code, err := t.bases.code(size.SizeBasis)
			if err != nil {
				return scan.SubtreeReplacement{}, err
			}
			entry.basis = code
		}
	}
	if fresh.nodes > 1 {
		keepRecord.flags |= flagHasChildren
	} else {
		keepRecord.flags &^= flagHasChildren
	}

	// The ancestors move by the difference, exactly as a removal moves them
	// (ADR-0064 §1): the directory's new total replaces its old one.
	allocatedDelta := keepRecord.ownedAllocated - oldAllocated
	logicalDelta := keepRecord.logicalSize - oldLogical
	for ancestor := keepRecord.parentID; ancestor > 0 && ancestor < t.records.len(); {
		record := t.records.at(ancestor)
		record.ownedAllocated += allocatedDelta
		record.logicalSize += logicalDelta
		if ancestor == t.rootNodeID {
			break
		}
		ancestor = record.parentID
	}

	t.nodeCount += fresh.nodes - old.nodes
	t.fileCount += fresh.files - old.files
	t.directoryCount += fresh.directories - old.directories
	t.bytes += allocatedDelta
	t.grouped = false
	t.version++
	return scan.SubtreeReplacement{
		Nodes: fresh.nodes, Files: fresh.files, Directories: fresh.directories,
		AllocatedBytes: keepRecord.ownedAllocated,
		Kept:           kept, Added: added, Removed: removed, Version: t.version,
	}, nil
}
