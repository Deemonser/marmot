package memtree

import (
	"fmt"

	"example.com/marmot/internal/domain/scan"
)

// refreshDirectory brings one directory's direct children up to date from a
// shallow listing, without touching the subtrees below them (ADR-0072). This is
// what a file-system event resolves to: "something in this directory changed",
// and a listing of that directory is the whole answer.
//
// children carry names, kinds, sizes, identities and mtimes; their IDs and
// ParentIDs are ignored. Matching is by name, as in replaceSubtree:
//
//   - a file or symlink with the same name keeps its ID and takes the new size,
//     identity and mtime; the size difference rolls up the ancestors;
//   - a directory with the same name keeps its ID and its whole subtree, and
//     only its identity and mtime move -- whatever changed below it has its own
//     event;
//   - a name that is new gets a fresh ID; a new directory arrives empty and is
//     reported in NewDirectoryIDs so the caller can read it in depth;
//   - a name that is gone is detached with its subtree, the way removeSubtree
//     detaches, and its space leaves the ancestors.
//
// self, when non-zero, is the directory's own fresh identity and mtime.
func (t *tree) refreshDirectory(keep int64, self scan.Node, children []scan.Node) (scan.DirectoryRefresh, error) {
	t.ensureGrouped()
	if !t.valid(keep) {
		return scan.DirectoryRefresh{}, ErrNodeNotFound
	}
	keepRecord := t.records.at(keep)
	if t.kinds.value(keepRecord.kind) != "directory" {
		return scan.DirectoryRefresh{}, fmt.Errorf("%w: only a directory can be refreshed", ErrInvalidRequest)
	}
	// Attached volumes (ADR-0052 §3) live under a directory as children with no
	// disk identity; a listing of that directory will not contain them. They are
	// kept as they are rather than dropped: an event under /System/Volumes must
	// not make the volume group's members disappear.
	existing := map[string]int64{}
	var volumes []int64
	for _, child := range t.children(keep) {
		entry := t.records.at(int64(child))
		if t.kinds.value(entry.kind) == "volume" {
			volumes = append(volumes, int64(child))
			continue
		}
		existing[t.names.get(entry.nameOffset, entry.nameLength)] = int64(child)
	}

	var result scan.DirectoryRefresh
	var allocatedDelta, logicalDelta int64
	var nodesDelta, filesDelta, directoriesDelta int64
	claimed := map[int64]struct{}{}
	for _, child := range children {
		if child.Kind == "volume" {
			continue
		}
		oldID, found := existing[child.Name]
		if found {
			old := t.records.at(oldID)
			if t.kinds.value(old.kind) == child.Kind {
				claimed[oldID] = struct{}{}
				old.device, old.inode = int64(child.Device), int64(child.Inode)
				old.modifiedUnix = 0
				if !child.ModifiedAt.IsZero() {
					old.modifiedUnix = child.ModifiedAt.UnixNano()
				}
				if child.Kind != "directory" {
					allocatedDelta += child.OwnedAllocated - old.ownedAllocated
					logicalDelta += child.LogicalSize - old.logicalSize
					old.ownedAllocated = child.OwnedAllocated
					old.logicalSize = child.LogicalSize
					t.noteReclaim(oldID, child, old)
					if code, err := t.confidences.code(child.Confidence); err == nil && child.Confidence != "" {
						old.confidence = code
					}
					if code, err := t.bases.code(child.SizeBasis); err == nil && child.SizeBasis != "" {
						old.basis = code
					}
				}
				result.Updated++
				continue
			}
			// Same name, different kind: the old object is gone and a new one
			// stands in its place. Fall through to detach + add.
		}
		id := t.records.len()
		t.records.grow(id + 1)
		entry, err := t.encode(child, keep)
		if err != nil {
			return scan.DirectoryRefresh{}, err
		}
		t.noteReclaim(id, child, &entry)
		if child.Kind == "directory" {
			// Arrives empty; the caller reads it in depth. Until then it must not
			// claim children it does not have.
			entry.ownedAllocated, entry.logicalSize = 0, 0
			entry.flags &^= flagHasChildren
			result.NewDirectoryIDs = append(result.NewDirectoryIDs, id)
			directoriesDelta++
		} else {
			allocatedDelta += entry.ownedAllocated
			logicalDelta += entry.logicalSize
			filesDelta++
		}
		*t.records.at(id) = entry
		nodesDelta++
		result.Added++
	}

	for name, oldID := range existing {
		if _, kept := claimed[oldID]; kept {
			continue
		}
		_ = name
		gone := t.detach(oldID)
		allocatedDelta -= gone.allocated
		logicalDelta -= gone.logical
		nodesDelta -= gone.nodes
		filesDelta -= gone.files
		directoriesDelta -= gone.directories
		result.Removed++
	}

	if self.Device != 0 || self.Inode != 0 {
		keepRecord.device, keepRecord.inode = int64(self.Device), int64(self.Inode)
	}
	if !self.ModifiedAt.IsZero() {
		keepRecord.modifiedUnix = self.ModifiedAt.UnixNano()
	}
	if len(children) > 0 || len(volumes) > 0 {
		keepRecord.flags |= flagHasChildren
	} else {
		keepRecord.flags &^= flagHasChildren
	}

	for ancestor := keep; ancestor > 0 && ancestor < t.records.len(); {
		record := t.records.at(ancestor)
		record.ownedAllocated += allocatedDelta
		record.logicalSize += logicalDelta
		if ancestor == t.rootNodeID {
			break
		}
		ancestor = record.parentID
	}
	t.nodeCount += nodesDelta
	t.fileCount += filesDelta
	t.directoryCount += directoriesDelta
	t.bytes += allocatedDelta
	if result.Added > 0 || result.Removed > 0 {
		// Only a change in membership invalidates the child index; a size change
		// alone would re-sort the group, and that is what group() does.
		t.markGroupsStale()
	} else if allocatedDelta != 0 {
		t.markGroupsStale()
	}
	t.version++
	result.AllocatedDelta = allocatedDelta
	result.Version = t.version
	return result, nil
}

// detach takes one node and its subtree out of the tree, in place, and reports
// what left. It is removeSubtree's mechanism without the ancestor roll-up, for
// callers that account for several detachments at once.
func (t *tree) detach(id int64) removal {
	entry := t.records.at(id)
	parent := entry.parentID
	gone := removal{allocated: entry.ownedAllocated, logical: entry.logicalSize}
	stack := []int64{id}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		gone.nodes++
		if t.kinds.value(t.records.at(current).kind) == "directory" {
			gone.directories++
		} else {
			gone.files++
		}
		for _, child := range t.children(current) {
			stack = append(stack, int64(child))
		}
	}
	if parent > 0 && parent < int64(len(t.childStart)) {
		start := t.childStart[parent]
		count := t.childCount[parent]
		group := t.childIDs[start : start+count]
		for index, child := range group {
			if int64(child) != id {
				continue
			}
			copy(group[index:], group[index+1:])
			t.childCount[parent] = count - 1
			break
		}
	}
	entry.parentID = 0
	return gone
}
