package memtree

import (
	"fmt"
	"math"

	"example.com/marmot/internal/domain/scan"
)

// mapSource is the minimal set of primitives the space-map assembly needs. There
// is now exactly one implementation — the in-memory tree — because there is no
// second store to disagree with (ADR-0055). The interface stays because it is
// what keeps the assembly independent of the storage layout that ADR-0056
// rewrites.
type mapSource interface {
	mapSnapshotID() int64
	mapSnapshotVersion() int64
	// mapRootNodeID and mapVolume let the root level balance to the disk the
	// snapshot came from (ADR-0052 §4). Zero values mean the snapshot did not
	// record the volume: the balancing entry is then omitted, never guessed.
	mapRootNodeID() int64
	mapVolume() (total, used, free uint64)
	mapNodeByID(nodeID int64) (scan.Node, error)
	mapChildCount(parentID int64) (int, error)
	mapChildren(parentID int64, limit, offset int) ([]scan.Node, error)
	mapAggregateChildren(parentID int64, offset int, basis string) (scan.MapEntry, error)
	// mapProjectedChildren returns the slim form used below the current level.
	// It must not reconstruct paths and is not bound by the paging page limit:
	// the projection budget bounds it instead (ADR-0048).
	mapProjectedChildren(parentID int64, limit int, minSize int64) ([]scan.ProjectedEntry, error)
}

func buildMap(source mapSource, query scan.MapQuery) (scan.MapResult, error) {
	if query.SnapshotID != source.mapSnapshotID() {
		return scan.MapResult{}, fmt.Errorf("%w: snapshot ID mismatch", ErrInvalidRequest)
	}
	if query.Offset < 0 {
		return scan.MapResult{}, fmt.Errorf("%w: negative map offset", ErrInvalidRequest)
	}
	parent, err := source.mapNodeByID(query.ParentID)
	if err != nil {
		return scan.MapResult{}, err
	}
	total, err := source.mapChildCount(query.ParentID)
	if err != nil {
		return scan.MapResult{}, err
	}
	if query.Offset > total {
		return scan.MapResult{}, fmt.Errorf("%w: map offset exceeds child count", ErrInvalidRequest)
	}
	requestedLimit := query.Limit
	visibleLimit, err := normalizePageLimit(requestedLimit)
	if err != nil {
		return scan.MapResult{}, err
	}
	if total > query.Offset+visibleLimit && visibleLimit > 1 {
		visibleLimit--
	}
	nodes, err := source.mapChildren(query.ParentID, visibleLimit, query.Offset)
	if err != nil {
		return scan.MapResult{}, err
	}
	entries := make([]scan.MapEntry, 0, len(nodes)+1)
	for _, node := range nodes {
		entries = append(entries, nodeMapEntry(node))
	}
	tailOffset := query.Offset + len(nodes)
	remaining := emptyRemainingEntry("map_remaining_v1")
	if total > tailOffset {
		remaining, err = source.mapAggregateChildren(query.ParentID, tailOffset, "map_remaining_v1")
		if err != nil {
			return scan.MapResult{}, err
		}
		entries = append(entries, remaining)
	}

	densityTruncated := false
	if query.Depth > 0 {
		// Allocate the budget proportionally instead of letting the first
		// entries drain it: a wide branch would otherwise take every arc and
		// leave its siblings without teeth.
		allotments := shareBudget(entries, normalizeProjectionBudget(query.ProjectionLimit))
		entrySweeps := sweepsFor(entries)
		for index := range entries {
			if entries[index].Kind != "node" || entries[index].Node.Kind != "directory" {
				continue
			}
			if allotments[index] <= 0 {
				if entries[index].Node.HasChildren {
					densityTruncated = true
				}
				continue
			}
			children, childTotal, childHasMore, childTruncated, err := projectChildrenFrom(
				source, entries[index].Node.ID, query.Depth-1, allotments[index],
				entrySweeps[index], entries[index].OwnedAllocated, query.MinSweeps, 0)
			if err != nil {
				return scan.MapResult{}, err
			}
			entries[index].Children = children
			entries[index].ChildrenTotal = childTotal
			entries[index].ChildrenHasMore = childHasMore
			densityTruncated = densityTruncated || childTruncated
		}
	}
	confidence := parent.Confidence
	if total > tailOffset {
		confidence = mergeMapConfidence(confidence, remaining.Confidence)
	}
	volumeTotal, volumeUsed, volumeFree := source.mapVolume()
	// The root level must add up to the disk's used space. Whatever the walk
	// could not account for — an unmounted Recovery volume, areas permission
	// denied us, APFS metadata, the gap between allocated and physical size —
	// becomes one visible, explainable entry instead of silently missing space
	// (ADR-0052 §4). A negative gap is a basis defect: report zero and leave the
	// tree total alone rather than trimming it to fit.
	if query.ParentID == source.mapRootNodeID() && volumeUsed > 0 && query.Offset == 0 {
		if gap := int64(volumeUsed) - parent.OwnedAllocated; gap > 0 {
			entries = append(entries, scan.MapEntry{
				Kind: "aggregate", Name: "隐藏空间", VirtualType: "hidden_space", DisplayState: "partial",
				LogicalSize: gap, AllocatedSize: gap, OwnedAllocated: gap,
				Confidence: "estimated", SizeBasis: "volume_statfs_v1",
			})
		}
	}
	return scan.MapResult{
		SnapshotID:       source.mapSnapshotID(),
		SnapshotVersion:  source.mapSnapshotVersion(),
		VolumeTotalBytes: volumeTotal,
		VolumeUsedBytes:  volumeUsed,
		VolumeFreeBytes:  volumeFree,
		Parent:           parent,
		Entries:          entries,
		Total:            total,
		Limit:            requestedLimit,
		Offset:           query.Offset,
		HasMore:          total > tailOffset,
		Remaining:        remaining,
		Confidence:       confidence,
		DensityTruncated: densityTruncated,
	}, nil
}

// projectChildrenFrom fills one subtree from its own arc allowance. The
// allowance is subdivided among children in proportion to size, so a wide branch
// cannot starve its siblings and no single directory is asked for thousands of
// children.
// sweepsFor gives each entry the angle it will occupy, in radians. The renderer
// lays the level out proportionally over a full circle, so this is the same
// number it will use.
func sweepsFor(entries []scan.MapEntry) []float64 {
	total := int64(0)
	for _, entry := range entries {
		if entry.OwnedAllocated > 0 {
			total += entry.OwnedAllocated
		}
	}
	sweeps := make([]float64, len(entries))
	if total == 0 {
		return sweeps
	}
	for index, entry := range entries {
		if entry.OwnedAllocated > 0 {
			sweeps[index] = 2 * math.Pi * float64(entry.OwnedAllocated) / float64(total)
		}
	}
	return sweeps
}

func projectChildrenFrom(source mapSource, parentID int64, depth, allot int, sweep float64, parentSize int64, minSweeps []float64, level int) ([]scan.ProjectedEntry, int, bool, bool, error) {
	total, err := source.mapChildCount(parentID)
	if err != nil {
		return nil, 0, false, false, err
	}
	if total == 0 {
		return nil, 0, false, false, nil
	}
	if allot <= 0 {
		return nil, total, true, true, nil
	}
	visibleLimit := total
	if visibleLimit > allot {
		visibleLimit = allot
		if visibleLimit > 1 {
			visibleLimit--
		}
	}
	// A ring cannot show more arcs than its circumference divided by the
	// narrowest visible one, so fetching more than that is pure waste. Without
	// this bound the shallow levels materialised up to the whole budget and then
	// culled almost all of it, which cost 67ms of assembly.
	if minSweep, ok := minSweepAt(minSweeps, level); ok {
		if drawable := int(2 * math.Pi / minSweep); drawable > 0 && visibleLimit > drawable {
			visibleLimit = drawable
		}
	}
	// The smallest child worth fetching: below this its arc is narrower than a
	// pixel at this ring's radius.
	minSize := int64(0)
	if minSweep, ok := minSweepAt(minSweeps, level); ok && sweep > 0 && parentSize > 0 {
		minSize = int64(float64(parentSize) * minSweep / sweep)
	}
	children, err := source.mapProjectedChildren(parentID, visibleLimit, minSize)
	if err != nil {
		return nil, 0, false, false, err
	}
	entries := make([]scan.ProjectedEntry, 0, len(children)+1)
	// Deliberately not seeded from the min-sweep cull. That flag means "there is
	// more here than the budget let us send"; an arc below one pixel is not
	// withheld information, it is below the display's resolution. Conflating the
	// two made the flag permanently true.
	truncated := false
	shares := shareProjected(children, allot-len(children))
	for index, child := range children {
		childSweep := 0.0
		if parentSize > 0 {
			childSweep = sweep * float64(child.OwnedAllocated) / float64(parentSize)
		}
		if child.Kind == "directory" && depth > 0 && shares[index] > 0 {
			grandChildren, grandTotal, grandHasMore, grandTruncated, err := projectChildrenFrom(
				source, child.NodeID, depth-1, shares[index], childSweep, child.OwnedAllocated, minSweeps, level+1)
			if err != nil {
				return nil, 0, false, false, err
			}
			child.Children = grandChildren
			child.ChildrenTotal = grandTotal
			child.ChildrenHasMore = grandHasMore
			truncated = truncated || grandTruncated
		} else if child.Kind == "directory" && child.ChildrenHasMore {
			truncated = true
		}
		child.ChildrenHasMore = child.ChildrenHasMore && len(child.Children) < child.ChildrenTotal
		entries = append(entries, child)
	}
	hasMore := total > len(children)
	if hasMore {
		remaining, err := source.mapAggregateChildren(parentID, len(children), "map_projection_remaining_v1")
		if err != nil {
			return nil, 0, false, false, err
		}
		entries = append(entries, scan.ProjectedEntry{
			Name: remaining.Name, Kind: "aggregate", OwnedAllocated: remaining.OwnedAllocated,
		})
		truncated = true
	}
	return entries, total, hasMore, truncated, nil
}

// shareBudget splits an arc budget across current-level directories in
// proportion to size, with a floor so small wedges still show depth.
// minSweepAt reads the threshold for a projected level, holding the last entry
// for anything deeper.
func minSweepAt(minSweeps []float64, level int) (float64, bool) {
	if len(minSweeps) == 0 {
		return 0, false
	}
	if level >= len(minSweeps) {
		level = len(minSweeps) - 1
	}
	if minSweeps[level] <= 0 {
		return 0, false
	}
	return minSweeps[level], true
}

func shareBudget(entries []scan.MapEntry, budget int) []int {
	sizes := make([]int64, len(entries))
	for index, entry := range entries {
		if entry.Kind == "node" && entry.Node.Kind == "directory" {
			sizes[index] = entry.OwnedAllocated
		} else {
			sizes[index] = -1
		}
	}
	return shareBySize(sizes, budget)
}

func shareProjected(children []scan.ProjectedEntry, budget int) []int {
	sizes := make([]int64, len(children))
	for index, child := range children {
		if child.Kind == "directory" {
			sizes[index] = child.OwnedAllocated
		} else {
			sizes[index] = -1
		}
	}
	return shareBySize(sizes, budget)
}

// shareBySize distributes budget over the entries whose size is non-negative.
func shareBySize(sizes []int64, budget int) []int {
	shares := make([]int, len(sizes))
	eligible := 0
	var totalSize int64
	for _, size := range sizes {
		if size < 0 {
			continue
		}
		eligible++
		totalSize += size
	}
	if eligible == 0 || budget <= 0 {
		return shares
	}
	floor := projectionShareFloor
	if eligible*floor > budget {
		floor = budget / eligible
	}
	remaining := budget - eligible*floor
	for index, size := range sizes {
		if size < 0 {
			continue
		}
		share := floor
		if remaining > 0 && totalSize > 0 && size > 0 {
			share += int((int64(remaining) * size) / totalSize)
		}
		shares[index] = share
	}
	return shares
}

// Budget for the whole projection, in arcs. ADR-0048 sets the target density at
// 2000; the slim entry shape keeps that inside the 256 KB payload ceiling.
const (
	projectionBudgetDefault = 2000
	projectionBudgetMax     = 2000
	// Minimum arcs any real directory gets, so a small wedge still shows depth
	// instead of being starved by the largest one.
	projectionShareFloor = 2
)

func normalizeProjectionBudget(limit int) int {
	if limit <= 0 {
		return projectionBudgetDefault
	}
	if limit > projectionBudgetMax {
		return projectionBudgetMax
	}
	return limit
}

func emptyRemainingEntry(basis string) scan.MapEntry {
	return scan.MapEntry{
		Kind:         "aggregate",
		Name:         "较小对象",
		VirtualType:  "smaller_objects",
		DisplayState: "partial",
		Capabilities: []string{"enter"},
		SizeBasis:    basis,
	}
}

func normalizePageLimit(limit int) (int, error) {
	if limit <= 0 {
		return 256, nil
	}
	if limit > maxPageSize {
		return 0, fmt.Errorf("%w: page limit %d exceeds %d", ErrInvalidRequest, limit, maxPageSize)
	}
	return limit, nil
}

func nodeMapEntry(node scan.Node) scan.MapEntry {
	return scan.MapEntry{Kind: "node", Node: node, Name: node.Name, LogicalSize: node.LogicalSize, AllocatedSize: node.AllocatedSize, OwnedAllocated: node.OwnedAllocated, Confidence: node.Confidence, SizeBasis: node.SizeBasis}
}

func mergeMapConfidence(parent, remaining string) string {
	if remaining == "" {
		return parent
	}
	if parent == "unknown" || remaining == "unknown" {
		return "unknown"
	}
	if parent == "partial" || remaining == "partial" {
		return "partial"
	}
	return "estimated"
}
