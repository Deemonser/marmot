package memtree

import (
	"cmp"
	"slices"
	"sort"

	"example.com/marmot/internal/domain/scan"
)

// The reclaim side table (ADR-0074 §3). The record stays 64 bytes; the files
// whose "what comes back" differs from "what it occupies" -- 17.5% of them on
// the reference volume, R-076 §10.2 / ADR-0074 gate 1 -- get one entry here,
// found through a flag on the record and a binary search. 40 bytes each, so
// about 14 MiB for 362k exceptions against 22 MiB for a map, and no per-entry
// allocation.
//
// The table is append-only while the scan runs (IDs are fresh and arrive in
// no particular order across workers) and sealed once at finish: one sort, the
// same shape as group(). After that, re-reads that rewrite a file's record go
// through set, which overwrites in place or appends to a small unsorted tail
// that is folded in when it grows past reclaimPendingLimit.
//
// Stale entries are harmless: the record's flags are authoritative, and a
// detached record (parentID 0) is never walked.
type reclaimEntry struct {
	id        int32
	linkCount uint32
	refCount  uint32
	allocated int64
	private   int64
	cloneID   uint64
}

const reclaimPendingLimit = 16384

type reclaimTable struct {
	sorted  []reclaimEntry
	pending []reclaimEntry
	sealed  bool
	// maxID is the largest ID in either slice. A record ID is never reused, so
	// an entry above it is new and set can append without searching.
	maxID int32
}

// add appends during the scan, when every ID is new. It must not be used once
// the table is sealed; set is.
func (r *reclaimTable) add(entry reclaimEntry) {
	r.pending = append(r.pending, entry)
	if entry.id > r.maxID {
		r.maxID = entry.id
	}
}

// set overwrites the entry for an ID or appends one. Only after seal.
func (r *reclaimTable) set(entry reclaimEntry) {
	if !r.sealed {
		r.add(entry)
		return
	}
	if entry.id > r.maxID {
		// A fresh record: nothing to overwrite anywhere.
		r.add(entry)
	} else if index, ok := r.find(entry.id); ok {
		r.sorted[index] = entry
		return
	} else {
		replaced := false
		for i := range r.pending {
			if r.pending[i].id == entry.id {
				r.pending[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			r.pending = append(r.pending, entry)
		}
	}
	if len(r.pending) > reclaimPendingLimit {
		r.seal()
	}
}

func (r *reclaimTable) get(id int64) (reclaimEntry, bool) {
	// Latest write wins, and the tail is where the latest writes are.
	for i := len(r.pending) - 1; i >= 0; i-- {
		if int64(r.pending[i].id) == id {
			return r.pending[i], true
		}
	}
	if index, ok := r.find(int32(id)); ok {
		return r.sorted[index], true
	}
	return reclaimEntry{}, false
}

func (r *reclaimTable) find(id int32) (int, bool) {
	index := sort.Search(len(r.sorted), func(i int) bool { return r.sorted[i].id >= id })
	return index, index < len(r.sorted) && r.sorted[index].id == id
}

// seal folds the pending tail into the sorted body: sort the tail (small), then
// one linear merge. Later writes for the same ID win -- the tail is sorted
// stably and a tail entry beats a body entry. Sorting the whole table instead
// cost ~150 ms per seal on 400k entries, which a directory re-read adding a few
// thousand exception files paid several times over.
func (r *reclaimTable) seal() {
	r.sealed = true
	if len(r.pending) == 0 {
		return
	}
	slices.SortStableFunc(r.pending, func(a, b reclaimEntry) int { return cmp.Compare(a.id, b.id) })
	merged := make([]reclaimEntry, 0, len(r.sorted)+len(r.pending))
	i, j := 0, 0
	for i < len(r.sorted) || j < len(r.pending) {
		switch {
		case j >= len(r.pending):
			merged = append(merged, r.sorted[i])
			i++
		case i >= len(r.sorted) || r.pending[j].id < r.sorted[i].id:
			entry := r.pending[j]
			j++
			// Several tail writes to one ID: the last one stands.
			for j < len(r.pending) && r.pending[j].id == entry.id {
				entry = r.pending[j]
				j++
			}
			merged = append(merged, entry)
		case r.pending[j].id == r.sorted[i].id:
			// The tail overrides the body for this ID.
			i++
		default:
			merged = append(merged, r.sorted[i])
			i++
		}
	}
	r.sorted = merged
	r.pending = nil
}

func (r *reclaimTable) len() int { return len(r.sorted) + len(r.pending) }

// reclaimFlags decides, for one file node, whether it needs a side entry and
// which flags its record carries. Directories and volumes carry neither.
func reclaimFlags(node scan.Node) (flags uint8, entry reclaimEntry, exception bool) {
	if node.Kind == "directory" || node.Kind == "volume" {
		return 0, reclaimEntry{}, false
	}
	if !node.HasPrivate {
		return flagReclaimUnknown, reclaimEntry{}, false
	}
	if node.PrivateSize == node.AllocatedSize && node.CloneRefCount <= 1 && node.LinkCount <= 1 {
		return 0, reclaimEntry{}, false
	}
	return flagReclaimException, reclaimEntry{
		linkCount: node.LinkCount, refCount: node.CloneRefCount,
		allocated: node.AllocatedSize, private: node.PrivateSize, cloneID: node.CloneID,
	}, true
}

// noteReclaim records what a node contributes to the side table, for a record
// about to be stored under id. It clears and sets the two reclaim flags on the
// record; the rest of the record is untouched.
func (t *tree) noteReclaim(id int64, node scan.Node, entry *record) {
	flags, side, exception := reclaimFlags(node)
	entry.flags = entry.flags&^(flagReclaimUnknown|flagReclaimException) | flags
	if !exception {
		return
	}
	side.id = int32(id)
	if t.reclaim.sealed {
		t.reclaim.set(side)
	} else {
		t.reclaim.add(side)
	}
}

// reclaimable is ADR-0074 §2 for a set of node IDs: walk every subtree once,
// add up what certainly comes back, and account shared blocks by group.
//
// Two kinds of group. A hardlink group is one inode reached by several paths
// (LinkCount > 1): the inode's blocks come back only when every path is in the
// set. A clone group is several inodes sharing one data stream (CloneRefCount >
// 1): the shared blocks come back only when every inode is in the set -- and,
// for an inode that is itself hardlinked, only when all its paths are. Group
// sizes come from the attributes, not from what the walk happened to see, so a
// member outside the scan keeps its group incomplete. Conservative by design:
// an incomplete group's shared blocks land in SharedExcluded, never in Bytes.
func (t *tree) reclaimable(ids []int64) scan.Reclaimable {
	t.ensureGrouped()
	t.reclaim.seal()

	type inodeKey struct{ device, inode int64 }
	type hardGroup struct {
		paths   uint32
		size    uint32
		shared  int64
		cloneID uint64
		cloned  bool
	}
	type cloneGroup struct {
		inodes map[inodeKey]struct{}
		size   uint32
		shared int64
	}
	var result scan.Reclaimable
	hardGroups := map[inodeKey]*hardGroup{}
	cloneGroups := map[uint64]*cloneGroup{}
	visited := map[int64]struct{}{}
	// An inode reached once through a path in a plain (non-hardlinked) file is
	// counted on that visit; a clone member reached through two hardlinks is
	// one clone member, which the hard group tracks.
	inodeSeen := map[inodeKey]struct{}{}

	stack := make([]int64, 0, 64)
	for _, id := range ids {
		if !t.valid(id) {
			continue
		}
		stack = append(stack, id)
	}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, done := visited[current]; done {
			continue
		}
		visited[current] = struct{}{}
		rec := t.records.at(current)
		switch t.kinds.value(rec.kind) {
		case "directory":
			for _, child := range t.children(current) {
				stack = append(stack, int64(child))
			}
			continue
		case "volume":
			continue
		}
		result.Files++
		if rec.flags&flagReclaimUnknown != 0 {
			result.Unknown = true
			result.UnknownBytes += rec.ownedAllocated
			continue
		}
		if rec.flags&flagReclaimException == 0 {
			result.Bytes += rec.ownedAllocated
			continue
		}
		side, ok := t.reclaim.get(current)
		if !ok {
			// The flag promised an entry; treat the file as unshared rather than
			// invent a number. Only a bug in the bookkeeping gets here.
			result.Bytes += rec.ownedAllocated
			continue
		}
		key := inodeKey{rec.device, rec.inode}
		private := max(side.private, 0)
		if side.allocated < private {
			private = side.allocated
		}
		sharedWithClones := side.allocated - private
		if side.linkCount > 1 {
			group := hardGroups[key]
			if group == nil {
				group = &hardGroup{size: side.linkCount, shared: private, cloneID: side.cloneID, cloned: side.refCount > 1}
				hardGroups[key] = group
			}
			group.paths++
			// The clone-shared part is the clone group's business; register the
			// inode there once.
			if side.refCount > 1 {
				if _, seen := inodeSeen[key]; !seen {
					inodeSeen[key] = struct{}{}
					cg := cloneGroups[side.cloneID]
					if cg == nil {
						cg = &cloneGroup{inodes: map[inodeKey]struct{}{}, size: side.refCount, shared: sharedWithClones}
						cloneGroups[side.cloneID] = cg
					}
					cg.inodes[key] = struct{}{}
				}
			} else if sharedWithClones > 0 {
				// Shared with something no deletion here reaches (a snapshot).
				result.SharedExcluded += sharedWithClones
			}
			continue
		}
		// Not hardlinked: the private part is this file's alone.
		result.Bytes += private
		if side.refCount > 1 {
			cg := cloneGroups[side.cloneID]
			if cg == nil {
				cg = &cloneGroup{inodes: map[inodeKey]struct{}{}, size: side.refCount, shared: sharedWithClones}
				cloneGroups[side.cloneID] = cg
			}
			cg.inodes[key] = struct{}{}
		} else if sharedWithClones > 0 {
			result.SharedExcluded += sharedWithClones
		}
	}

	// Hardlink groups: the inode's own blocks come back when every path is here.
	completeInodes := map[inodeKey]bool{}
	for key, group := range hardGroups {
		complete := group.paths >= group.size
		completeInodes[key] = complete
		if complete {
			result.Bytes += group.shared
		} else {
			result.SharedExcluded += group.shared
		}
	}
	// Clone groups: shared blocks come back when every clone is here, each of
	// them wholly (an inode reached through a hardlink group must be complete).
	for _, group := range cloneGroups {
		complete := uint32(len(group.inodes)) >= group.size
		if complete {
			for key := range group.inodes {
				if done, tracked := completeInodes[key]; tracked && !done {
					complete = false
					break
				}
			}
		}
		if complete {
			result.Bytes += group.shared
		} else {
			result.SharedExcluded += group.shared
		}
	}
	result.UpperBound = result.Bytes + result.SharedExcluded + result.UnknownBytes
	return result
}
