package memtree

import (
	"fmt"
	"math"
	"path"
	"sort"

	"example.com/marmot/internal/domain/scan"
)

// tree is one scan result, held in the packed form ADR-0056 specifies.
//
// The layout exists because the obvious one does not fit: storing scan.Node per
// node costs 184 bytes of struct plus a 122-byte path plus separate heap
// allocations for every string, which measured 1.9 GiB of RSS on a 2.8M-node
// volume. Here a node is 64 bytes in a flat table, names live in one arena, the
// four fields whose real cardinality is 3/1/2/1 are one byte each, and paths are
// rebuilt from the parent chain instead of stored (R-057).
type tree struct {
	taskID  string
	root    string
	state   string
	phase   string
	version int64

	// The ID is the index into records — see record and recordTable.
	records recordTable
	names   arena

	kinds       *codeTable
	volumes     *codeTable
	bases       *codeTable
	confidences *codeTable

	// Children, grouped by parent. Filled once, at finish, by counting sort over
	// the record table: the (parent, child) pairs are already in the records, so
	// nothing has to be accumulated per parent during the scan and there is no
	// second copy to peak on (ADR-0056 §4).
	childIDs   []int32
	childStart []uint32
	childCount []uint32
	// grouped says whether the three arrays above match the records. Inserting a
	// node or changing a roll-up invalidates them. The grouping is rebuilt on
	// demand rather than maintained per parent during the scan: a map of
	// per-parent slices would hold every child a second time, which is the 2x
	// peak ADR-0056 §4 exists to remove. Backend queries are still allowed
	// between the top-level publish and the end of the walk (ADR-0014/0027);
	// they just pay one O(n) pass.
	grouped bool

	rootNodeID int64

	volumeTotal uint64
	volumeUsed  uint64
	volumeFree  uint64

	nodeCount      int64
	fileCount      int64
	directoryCount int64
	bytes          int64
	issueCount     int64
	issues         []scan.Issue

	finished bool
	failure  string
}

func newTree(taskID, root string) *tree {
	created := &tree{
		taskID:      taskID,
		root:        root,
		state:       scan.JobRunning,
		phase:       string(scan.PhaseCatalog),
		version:     1,
		kinds:       newCodeTable(),
		volumes:     newCodeTable(),
		bases:       newCodeTable(),
		confidences: newCodeTable(),
	}
	// Index 0 must exist and stay zero so a zero node ID reads as invalid.
	created.records.grow(1)
	return created
}

func (t *tree) insert(nodes []scan.Node) error {
	for _, node := range nodes {
		if node.ID <= 0 || node.ID > math.MaxInt32 {
			return fmt.Errorf("%w: node ID %d is out of range", ErrInvalidRequest, node.ID)
		}
		// The ID is the index, so a gap would leave a zero record that reads as a
		// real node. The scanner numbers from 1 without gaps; this enforces it
		// rather than trusting it (ADR-0056 §1).
		// Grow so that index node.ID exists. A left-behind zero record is what a
		// gap in the numbering looks like, and valid() treats it as absent.
		//
		// Paged, so growing never copies what is already there (ADR-0057 §3).
		if t.records.len() <= node.ID {
			t.records.grow(node.ID + 1)
		}
		nameOffset, nameLength, err := t.names.put(node.Name)
		if err != nil {
			return err
		}
		kind, err := t.kinds.code(node.Kind)
		if err != nil {
			return err
		}
		volume, err := t.volumes.code(node.VolumeID)
		if err != nil {
			return err
		}
		basis, err := t.bases.code(node.SizeBasis)
		if err != nil {
			return err
		}
		confidence, err := t.confidences.code(node.Confidence)
		if err != nil {
			return err
		}
		entry := record{
			parentID: node.ParentID, ownedAllocated: node.OwnedAllocated, logicalSize: node.LogicalSize,
			device: int64(node.Device), inode: int64(node.Inode),
			nameOffset: nameOffset, nameLength: nameLength,
			kind: kind, volume: volume, basis: basis, confidence: confidence,
		}
		if !node.ModifiedAt.IsZero() {
			entry.modifiedUnix = node.ModifiedAt.UnixNano()
		}
		if node.HasChildren {
			entry.flags |= flagHasChildren
		}
		*t.records.at(node.ID) = entry
		if node.ParentID == 0 {
			t.rootNodeID = node.ID
		}
	}
	t.grouped = false
	t.version++
	return nil
}

// applySizes writes the rolled-up totals straight into the records. A separate
// map of directory sizes would cost ~50 MiB for no benefit: a directory's record
// is the only place its size is read from.
func (t *tree) applySizes(sizes map[int64]scan.DirectorySize) error {
	for id, size := range sizes {
		if id <= 0 || id >= t.records.len() {
			continue
		}
		entry := t.records.at(id)
		entry.ownedAllocated = size.OwnedAllocated
		entry.logicalSize = size.LogicalSize
		if size.Confidence != "" {
			code, err := t.confidences.code(size.Confidence)
			if err != nil {
				return err
			}
			entry.confidence = code
		}
		if size.SizeBasis != "" {
			code, err := t.bases.code(size.SizeBasis)
			if err != nil {
				return err
			}
			entry.basis = code
		}
	}
	t.grouped = false
	t.version++
	return nil
}

// finish groups the children and orders each group the way the space map reads
// them. Counting sort, in place: one pass to count, a prefix sum, one pass to
// place. Nothing is duplicated, so the peak is one child array (4 bytes per
// node), not a second copy of every entry (ADR-0056 §4).
func (t *tree) finish(state, failure string, nodeCount, fileCount, directoryCount, bytes, issues int64) {
	// No trim here any more: the table and the arena are paged, so the slack a
	// finished result carries is at most one page each rather than up to half the
	// table, and buying it back with a full copy is no longer worth 225.55 MB of
	// allocation (ADR-0057 §3).
	t.group()
	t.state = state
	t.failure = failure
	t.nodeCount = nodeCount
	t.fileCount = fileCount
	t.directoryCount = directoryCount
	t.bytes = bytes
	t.issueCount = issues
	t.finished = true
	t.version++
}

// group is the counting sort: one pass to count, a prefix sum, one pass to place,
// then each parent's own range sorted in place. Nothing is duplicated.
func (t *tree) group() {
	count := int(t.records.len())
	t.childStart = make([]uint32, count)
	t.childCount = make([]uint32, count)
	total := 0
	for id := 1; id < count; id++ {
		parent := t.records.at(int64(id)).parentID
		if parent > 0 && parent < int64(count) {
			t.childCount[parent]++
			total++
		}
	}
	cursor := make([]uint32, count)
	running := uint32(0)
	for id := 1; id < count; id++ {
		t.childStart[id] = running
		cursor[id] = running
		running += t.childCount[id]
	}
	t.childIDs = make([]int32, total)
	for id := 1; id < count; id++ {
		parent := t.records.at(int64(id)).parentID
		if parent > 0 && parent < int64(count) {
			t.childIDs[cursor[parent]] = int32(id)
			cursor[parent]++
		}
	}
	for id := 1; id < count; id++ {
		if t.childCount[id] < 2 {
			continue
		}
		group := t.childIDs[t.childStart[id] : t.childStart[id]+t.childCount[id]]
		sort.Slice(group, func(i, j int) bool {
			left, right := t.records.at(int64(group[i])).ownedAllocated, t.records.at(int64(group[j])).ownedAllocated
			if left != right {
				return left > right
			}
			return group[i] < group[j]
		})
	}
	t.grouped = true
}

func (t *tree) ensureGrouped() {
	if !t.grouped {
		t.group()
	}
}

func (t *tree) children(parentID int64) []int32 {
	t.ensureGrouped()
	if parentID <= 0 || parentID >= int64(len(t.childStart)) {
		return nil
	}
	start, length := t.childStart[parentID], t.childCount[parentID]
	return t.childIDs[start : start+length]
}

func (t *tree) valid(id int64) bool {
	return id > 0 && id < t.records.len() && (id == t.rootNodeID || t.records.at(int64(id)).parentID != 0 || t.records.at(id).nameLength > 0)
}

// path rebuilds an absolute path by walking to the root. Paths are not stored:
// they average 122 bytes of mostly repeated prefixes, 325 MiB on a full volume
// (R-057). Only the current level needs them, and only a page at a time.
func (t *tree) path(id int64) string {
	if id == t.rootNodeID {
		return t.root
	}
	var segments []string
	for cursor := id; cursor > 0 && cursor < t.records.len(); {
		entry := *t.records.at(cursor)
		if cursor == t.rootNodeID {
			break
		}
		segments = append(segments, t.names.get(entry.nameOffset, entry.nameLength))
		if entry.parentID == 0 {
			break
		}
		cursor = entry.parentID
	}
	result := t.root
	for i := len(segments) - 1; i >= 0; i-- {
		result = path.Join(result, segments[i])
	}
	return result
}

func (t *tree) node(id int64) (scan.Node, error) {
	if !t.valid(id) {
		return scan.Node{}, ErrNodeNotFound
	}
	entry := *t.records.at(id)
	return scan.Node{
		ID: id, ParentID: entry.parentID, Path: t.path(id),
		Name: t.names.get(entry.nameOffset, entry.nameLength),
		Kind: t.kinds.value(entry.kind),
		// allocated and owned are the same measurement (R-053); one field carries
		// both. A second size basis — privatesize, left open by ADR-0052 §2 —
		// would need its own field, not a reinterpretation of this one.
		LogicalSize: entry.logicalSize, AllocatedSize: entry.ownedAllocated, OwnedAllocated: entry.ownedAllocated,
		VolumeID:   t.volumes.value(entry.volume),
		Confidence: t.confidences.value(entry.confidence), SizeBasis: t.bases.value(entry.basis),
		Device: uint64(entry.device), Inode: uint64(entry.inode),
		ModifiedAt:  modifiedTime(entry.modifiedUnix),
		HasChildren: entry.flags&flagHasChildren != 0,
	}, nil
}

// slim is the arc form: no path, so a projected descendant cannot authorise a
// file operation (ADR-0048, DDD invariant 17).
func (t *tree) slim(id int64) (scan.ProjectedEntry, bool) {
	if !t.valid(id) {
		return scan.ProjectedEntry{}, false
	}
	entry := *t.records.at(id)
	return scan.ProjectedEntry{
		NodeID: id, Name: t.names.get(entry.nameOffset, entry.nameLength),
		Kind: t.kinds.value(entry.kind), OwnedAllocated: entry.ownedAllocated,
	}, true
}

// nodeIDByPath walks down from the root. Building a path index would put the
// 325 MiB of paths back into memory to serve the handful of lookups a cleanup
// plan makes.
func (t *tree) nodeIDByPath(target string) (int64, bool) {
	t.ensureGrouped()
	if target == t.root {
		return t.rootNodeID, true
	}
	relative, ok := trimRoot(t.root, target)
	if !ok {
		return 0, false
	}
	cursor := t.rootNodeID
	for _, segment := range splitSegments(relative) {
		found := int64(0)
		for _, child := range t.children(cursor) {
			entry := *t.records.at(int64(child))
			if t.names.get(entry.nameOffset, entry.nameLength) == segment {
				found = int64(child)
				break
			}
		}
		if found == 0 {
			return 0, false
		}
		cursor = found
	}
	return cursor, true
}

func (t *tree) snapshot(id int64) scan.Snapshot {
	return scan.Snapshot{
		TaskID: t.taskID, ID: id, State: t.state, Phase: t.phase, Root: t.root,
		SnapshotVersion: t.version, NodeCount: t.nodeCount, FileCount: t.fileCount,
		DirCount: t.directoryCount, Bytes: t.bytes, Issues: t.issueCount, Error: t.failure,
	}
}

// coverage stays as ADR-0049's assertion hook: the tree is the store, so there is
// nothing to trim and coverage must always be full.
func (t *tree) coverage() (int, int) {
	directoryCode, ok := t.kinds.index["directory"]
	if !ok {
		return 0, 0
	}
	total := 0
	for id := 1; id < int(t.records.len()); id++ {
		if t.records.at(int64(id)).kind == directoryCode {
			total++
		}
	}
	return total, total
}

// removal is what came out of the tree, so the caller can report it and the
// counters can be corrected by the same numbers that left.
type removal struct {
	nodes       int64
	files       int64
	directories int64
	allocated   int64
	logical     int64
}

// removeSubtree detaches one node and rolls the space it held out of its
// ancestors, in place.
//
// This exists because the alternative was a full re-scan after every deletion --
// 9.5 seconds on a 1.8M-node disk to learn something already known exactly: that
// one subtree is gone. The work here is O(depth + siblings of the parent +
// size of the removed subtree), and the last term is only a counter walk.
//
// Detaching is done in two places on purpose. The child index is patched so
// queries stop seeing it immediately, AND the record's parentID is cleared so a
// later regroup -- group() rebuilds the index from parentID alone -- cannot
// resurrect it. Patching only the index would work until the next insert
// invalidated the grouping, which is the kind of bug that reappears months later
// looking like corruption.
//
// Descendants keep pointing at the detached node and are left alone: nothing
// reaches them from the root any more, and rewriting half a million parent
// pointers to prove it would be work for its own sake.
func (t *tree) removeSubtree(id int64) (removal, bool) {
	t.ensureGrouped()
	if id <= 0 || id >= t.records.len() || id == t.rootNodeID {
		return removal{}, false
	}
	entry := t.records.at(id)
	parent := entry.parentID
	if parent <= 0 || parent >= t.records.len() {
		return removal{}, false
	}

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

	// Out of the parent's group. The groups are contiguous and sorted by size
	// descending, so closing the gap by shifting left keeps that order.
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
	if t.childCount[parent] == 0 {
		record := t.records.at(parent)
		record.flags &^= flagHasChildren
	}
	t.records.at(id).parentID = 0

	for ancestor := parent; ancestor > 0 && ancestor < t.records.len(); {
		record := t.records.at(ancestor)
		record.ownedAllocated -= gone.allocated
		record.logicalSize -= gone.logical
		if ancestor == t.rootNodeID {
			break
		}
		ancestor = record.parentID
	}

	t.nodeCount -= gone.nodes
	t.fileCount -= gone.files
	t.directoryCount -= gone.directories
	t.bytes -= gone.allocated
	t.version++
	return gone, true
}
