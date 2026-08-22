package binarysnapshot

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"example.com/marmot/internal/domain/scan"
)

type Reader struct {
	directory    string
	manifest     Manifest
	data         *os.File
	index        *os.File
	indexHeader  indexHeader
	dataSize     int64
	validDataEnd int64
}

func Open(directory string, snapshotID int64) (*Reader, error) {
	if directory == "" || snapshotID <= 0 {
		return nil, fmt.Errorf("%w: incomplete reader configuration", ErrInvalidSnapshot)
	}
	paths := pathsFor(directory, snapshotID)
	encodedManifest, err := os.ReadFile(paths.manifest)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(encodedManifest, &manifest); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrInvalidSnapshot, err)
	}
	if err := validateManifest(manifest, snapshotID); err != nil {
		return nil, err
	}
	dataPath, err := safeManifestPath(directory, manifest.DataFile)
	if err != nil {
		return nil, err
	}
	data, err := os.Open(dataPath)
	if err != nil {
		return nil, err
	}
	closeData := true
	defer func() {
		if closeData {
			_ = data.Close()
		}
	}()
	info, err := data.Stat()
	if err != nil {
		return nil, err
	}
	state, err := scanData(data, info.Size(), snapshotID)
	if err != nil {
		return nil, err
	}
	if manifest.DataEnd <= 0 || manifest.DataEnd > state.validEnd || manifest.NodeCount < 0 || uint64(manifest.NodeCount) > state.nodeCount || manifest.IssueCount < 0 || uint64(manifest.IssueCount) > state.issueCount {
		return nil, fmt.Errorf("%w: manifest exceeds committed data", ErrInvalidSnapshot)
	}
	if manifest.IndexFile == "" || manifest.IndexGeneration == 0 {
		return nil, ErrIndexUnavailable
	}
	indexPath, err := safeManifestPath(directory, manifest.IndexFile)
	if err != nil {
		return nil, err
	}
	index, header, err := openIndex(indexPath, snapshotID)
	if err != nil {
		return nil, err
	}
	if header.Generation != manifest.IndexGeneration || int64(header.NodeCount) != manifest.NodeCount || int64(header.IssueCount) != manifest.IssueCount {
		_ = index.Close()
		return nil, fmt.Errorf("%w: manifest and index generation mismatch", ErrInvalidSnapshot)
	}
	if header.NodeCount > state.nodeCount || header.IssueCount > state.issueCount {
		_ = index.Close()
		return nil, fmt.Errorf("%w: index exceeds committed data", ErrInvalidSnapshot)
	}
	reader := &Reader{
		directory:    directory,
		manifest:     manifest,
		data:         data,
		index:        index,
		indexHeader:  header,
		dataSize:     info.Size(),
		validDataEnd: state.validEnd,
	}
	closeData = false
	return reader, nil
}

func (r *Reader) Manifest() Manifest { return r.manifest }

func (r *Reader) Snapshot() scan.Snapshot { return r.manifest.Snapshot() }

func (r *Reader) Close() error {
	if r.index == nil && r.data == nil {
		return nil
	}
	var firstErr error
	if r.index != nil {
		firstErr = r.index.Close()
		r.index = nil
	}
	if r.data != nil {
		if err := r.data.Close(); firstErr == nil {
			firstErr = err
		}
		r.data = nil
	}
	return firstErr
}

func (r *Reader) NodeByID(nodeID int64) (scan.Node, error) {
	indexNode, err := r.findNodeIndex(nodeID)
	if err != nil {
		return scan.Node{}, err
	}
	node, err := r.readRawNode(indexNode)
	if err != nil {
		return scan.Node{}, err
	}
	node.Path, err = r.reconstructPath(node)
	if err != nil {
		return scan.Node{}, err
	}
	return node, nil
}

func (r *Reader) NodeByPath(path string) (scan.Node, error) {
	cleanPath := filepath.Clean(path)
	rootPath := filepath.Clean(r.manifest.Root)
	if cleanPath == rootPath {
		return r.NodeByID(r.manifest.RootNodeID)
	}
	relative, err := filepath.Rel(rootPath, cleanPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return scan.Node{}, ErrNodeNotFound
	}
	currentID := r.manifest.RootNodeID
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		child, ok, err := r.findChildByName(currentID, part)
		if err != nil {
			return scan.Node{}, err
		}
		if !ok {
			return scan.Node{}, ErrNodeNotFound
		}
		currentID = child.ID
	}
	return r.NodeByID(currentID)
}

func (r *Reader) Children(parentID int64, limit, offset int) ([]scan.Node, error) {
	if offset < 0 {
		return nil, fmt.Errorf("%w: negative child offset", ErrInvalidSnapshot)
	}
	limit, err := normalizePageLimit(limit)
	if err != nil {
		return nil, err
	}
	entry, found, err := r.findDirectory(parentID)
	if err != nil || !found || uint64(offset) >= entry.childCount {
		return []scan.Node{}, err
	}
	remaining := entry.childCount - uint64(offset)
	if remaining < uint64(limit) {
		limit = int(remaining)
	}
	ids, err := r.readChildIDs(entry.childStart+uint64(offset), uint64(limit))
	if err != nil {
		return nil, err
	}
	nodes := make([]scan.Node, 0, len(ids))
	for _, id := range ids {
		node, err := r.NodeByID(id)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (r *Reader) Map(query scan.MapQuery) (scan.MapResult, error) {
	if query.SnapshotID != r.manifest.SnapshotID {
		return scan.MapResult{}, fmt.Errorf("%w: snapshot ID mismatch", ErrInvalidSnapshot)
	}
	if query.Offset < 0 {
		return scan.MapResult{}, fmt.Errorf("%w: negative map offset", ErrInvalidSnapshot)
	}
	parent, err := r.NodeByID(query.ParentID)
	if err != nil {
		return scan.MapResult{}, err
	}
	total, err := r.childCount(query.ParentID)
	if err != nil {
		return scan.MapResult{}, err
	}
	if query.Offset > total {
		return scan.MapResult{}, fmt.Errorf("%w: map offset exceeds child count", ErrInvalidSnapshot)
	}
	requestedLimit := query.Limit
	visibleLimit, err := normalizePageLimit(requestedLimit)
	if err != nil {
		return scan.MapResult{}, err
	}
	if total > query.Offset+visibleLimit && visibleLimit > 1 {
		visibleLimit--
	}
	nodes, err := r.Children(query.ParentID, visibleLimit, query.Offset)
	if err != nil {
		return scan.MapResult{}, err
	}
	entries := make([]scan.MapEntry, 0, len(nodes)+1)
	for _, node := range nodes {
		entries = append(entries, nodeMapEntry(node))
	}
	tailOffset := query.Offset + len(nodes)
	remaining := scan.MapEntry{Kind: "aggregate", Name: "较小对象", VirtualType: "smaller_objects", DisplayState: "partial", Capabilities: []string{"enter"}, SizeBasis: "map_remaining_v1"}
	if total > tailOffset {
		remaining, err = r.aggregateChildren(query.ParentID, tailOffset, "map_remaining_v1")
		if err != nil {
			return scan.MapResult{}, err
		}
		entries = append(entries, remaining)
	}

	projectionTruncated := false
	if query.Depth > 0 {
		budget := query.ProjectionLimit
		if budget <= 0 {
			budget = 384
		}
		if budget > 512 {
			budget = 512
		}
		for index := range entries {
			if budget <= 0 {
				projectionTruncated = true
				break
			}
			if entries[index].Kind != "node" || entries[index].Node.Kind != "directory" {
				continue
			}
			children, childTotal, childHasMore, childTruncated, err := r.projectChildren(entries[index].Node.ID, query.Depth-1, &budget)
			if err != nil {
				return scan.MapResult{}, err
			}
			entries[index].Children = children
			entries[index].ChildrenTotal = childTotal
			entries[index].ChildrenHasMore = childHasMore
			projectionTruncated = projectionTruncated || childTruncated
		}
	}
	confidence := parent.Confidence
	if total > tailOffset {
		confidence = mergeMapConfidence(confidence, remaining.Confidence)
	}
	return scan.MapResult{
		SnapshotID:          r.manifest.SnapshotID,
		SnapshotVersion:     r.manifest.SnapshotVersion,
		Parent:              parent,
		Entries:             entries,
		Total:               total,
		Limit:               requestedLimit,
		Offset:              query.Offset,
		HasMore:             total > tailOffset,
		Remaining:           remaining,
		Confidence:          confidence,
		ProjectionTruncated: projectionTruncated,
	}, nil
}

func (r *Reader) Issues() ([]scan.Issue, error) {
	issues := make([]scan.Issue, 0, r.indexHeader.IssueCount)
	for index := uint64(0); index < r.indexHeader.IssueCount; index++ {
		entry, err := r.readIssueEntry(index)
		if err != nil {
			return nil, err
		}
		path, err := readIndexString(r.index, r.indexHeader, entry.pathOffset, entry.pathLength)
		if err != nil {
			return nil, err
		}
		message, err := readIndexString(r.index, r.indexHeader, entry.messageOffset, entry.messageLength)
		if err != nil {
			return nil, err
		}
		issues = append(issues, scan.Issue{Path: path, Message: message})
	}
	return issues, nil
}

func (r *Reader) childCount(parentID int64) (int, error) {
	entry, found, err := r.findDirectory(parentID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	if entry.childCount > math.MaxInt {
		return 0, fmt.Errorf("%w: child count overflows int", ErrInvalidSnapshot)
	}
	return int(entry.childCount), nil
}

func (r *Reader) aggregateChildren(parentID int64, offset int, basis string) (scan.MapEntry, error) {
	directory, found, err := r.findDirectory(parentID)
	if err != nil {
		return scan.MapEntry{}, err
	}
	if !found {
		return scan.MapEntry{Kind: "aggregate", Name: "较小对象", VirtualType: "smaller_objects", DisplayState: "partial", Capabilities: []string{"enter"}, Confidence: "estimated", SizeBasis: basis}, nil
	}
	if offset < 0 || uint64(offset) > directory.childCount {
		return scan.MapEntry{}, fmt.Errorf("%w: invalid aggregate offset", ErrInvalidSnapshot)
	}
	prefix, err := r.childPrefix(directory, uint64(offset))
	if err != nil {
		return scan.MapEntry{}, err
	}
	entry := scan.MapEntry{
		Kind: "aggregate", Name: "较小对象", VirtualType: "smaller_objects", DisplayState: "partial", Capabilities: []string{"enter"},
		Count:          int64(directory.childCount) - int64(offset),
		LogicalSize:    directory.logicalSize - prefix.prefixLogicalSize,
		AllocatedSize:  directory.allocatedSize - prefix.prefixAllocated,
		OwnedAllocated: directory.ownedAllocated - prefix.prefixOwned,
		Confidence:     "estimated", SizeBasis: basis,
	}
	if entry.Count < 0 || entry.LogicalSize < 0 || entry.AllocatedSize < 0 || entry.OwnedAllocated < 0 {
		return scan.MapEntry{}, fmt.Errorf("%w: aggregate prefix exceeds total", ErrInvalidSnapshot)
	}
	return entry, nil
}

func (r *Reader) projectChildren(parentID int64, depth int, budget *int) ([]scan.MapEntry, int, bool, bool, error) {
	total, err := r.childCount(parentID)
	if err != nil {
		return nil, 0, false, false, err
	}
	if total == 0 {
		return nil, 0, false, false, nil
	}
	if *budget <= 0 {
		return nil, total, true, true, nil
	}
	visibleLimit := total
	if visibleLimit > *budget {
		visibleLimit = *budget
		if visibleLimit > 1 {
			visibleLimit--
		}
	}
	nodes, err := r.Children(parentID, visibleLimit, 0)
	if err != nil {
		return nil, 0, false, false, err
	}
	entries := make([]scan.MapEntry, 0, len(nodes)+1)
	truncated := false
	for _, node := range nodes {
		if *budget <= 0 {
			truncated = true
			break
		}
		*budget = *budget - 1
		entry := nodeMapEntry(node)
		if node.Kind == "directory" {
			if depth > 0 {
				childEntries, childTotal, childHasMore, childTruncated, err := r.projectChildren(node.ID, depth-1, budget)
				if err != nil {
					return nil, 0, false, false, err
				}
				entry.Children = childEntries
				entry.ChildrenTotal = childTotal
				entry.ChildrenHasMore = childHasMore
				truncated = truncated || childTruncated
			} else if node.HasChildren {
				truncated = true
			}
		}
		entries = append(entries, entry)
	}
	hasMore := total > len(nodes)
	if hasMore {
		remaining, err := r.aggregateChildren(parentID, len(nodes), "map_projection_remaining_v1")
		if err != nil {
			return nil, 0, false, false, err
		}
		entries = append(entries, remaining)
		truncated = true
	}
	return entries, total, hasMore, truncated, nil
}

func (r *Reader) findChildByName(parentID int64, name string) (scan.Node, bool, error) {
	total, err := r.childCount(parentID)
	if err != nil {
		return scan.Node{}, false, err
	}
	for offset := 0; offset < total; offset += maxPageSize {
		nodes, err := r.Children(parentID, maxPageSize, offset)
		if err != nil {
			return scan.Node{}, false, err
		}
		for _, node := range nodes {
			if node.Name == name {
				return node, true, nil
			}
		}
		if len(nodes) == 0 {
			break
		}
	}
	return scan.Node{}, false, nil
}

func (r *Reader) reconstructPath(node scan.Node) (string, error) {
	if node.ID == r.manifest.RootNodeID {
		return r.manifest.Root, nil
	}
	parts := []string{node.Name}
	seen := map[int64]struct{}{node.ID: {}}
	current := node
	for current.ID != r.manifest.RootNodeID {
		if current.ParentID == 0 {
			return "", fmt.Errorf("%w: node %d is outside root", ErrInvalidSnapshot, current.ID)
		}
		if _, exists := seen[current.ParentID]; exists {
			return "", fmt.Errorf("%w: parent cycle at node %d", ErrInvalidSnapshot, current.ParentID)
		}
		seen[current.ParentID] = struct{}{}
		parent, err := r.readRawNodeByID(current.ParentID)
		if err != nil {
			return "", err
		}
		if parent.ID != r.manifest.RootNodeID {
			parts = append(parts, parent.Name)
		}
		current = parent
	}
	path := r.manifest.Root
	for index := len(parts) - 1; index >= 0; index-- {
		path = filepath.Join(path, parts[index])
	}
	return path, nil
}

func (r *Reader) readRawNodeByID(nodeID int64) (scan.Node, error) {
	indexNode, err := r.findNodeIndex(nodeID)
	if err != nil {
		return scan.Node{}, err
	}
	return r.readRawNode(indexNode)
}

func (r *Reader) readRawNode(indexNode indexNode) (scan.Node, error) {
	if indexNode.dataOffset < dataHeaderSize || int64(nodeRecordSize) > r.validDataEnd-indexNode.dataOffset {
		return scan.Node{}, fmt.Errorf("%w: node record outside committed data", ErrInvalidSnapshot)
	}
	record := make([]byte, nodeRecordSize)
	if err := readAtFull(r.data, record, indexNode.dataOffset); err != nil {
		return scan.Node{}, err
	}
	nodeID := int64(binary.LittleEndian.Uint64(record[0:8]))
	parentID := int64(binary.LittleEndian.Uint64(record[8:16]))
	if nodeID != indexNode.nodeID {
		return scan.Node{}, fmt.Errorf("%w: node index identity mismatch", ErrInvalidSnapshot)
	}
	name, err := r.readDataString(indexNode.stringBase, record, 16)
	if err != nil {
		return scan.Node{}, err
	}
	kind, err := r.readDataString(indexNode.stringBase, record, 24)
	if err != nil {
		return scan.Node{}, err
	}
	volume, err := r.readDataString(indexNode.stringBase, record, 32)
	if err != nil {
		return scan.Node{}, err
	}
	confidence, err := r.readDataString(indexNode.stringBase, record, 40)
	if err != nil {
		return scan.Node{}, err
	}
	sizeBasis, err := r.readDataString(indexNode.stringBase, record, 48)
	if err != nil {
		return scan.Node{}, err
	}
	logicalSize, err := signedValue(record[56:64])
	if err != nil {
		return scan.Node{}, err
	}
	allocatedSize, err := signedValue(record[64:72])
	if err != nil {
		return scan.Node{}, err
	}
	ownedAllocated, err := signedValue(record[72:80])
	if err != nil {
		return scan.Node{}, err
	}
	if logicalSize < 0 || allocatedSize < 0 || ownedAllocated < 0 {
		return scan.Node{}, fmt.Errorf("%w: negative node size", ErrInvalidSnapshot)
	}
	modifiedAt, err := signedValue(record[96:104])
	if err != nil {
		return scan.Node{}, err
	}
	return scan.Node{
		ID: nodeID, ParentID: parentID, Name: name, Kind: kind, VolumeID: volume,
		Confidence: confidence, SizeBasis: sizeBasis, LogicalSize: logicalSize,
		AllocatedSize: allocatedSize, OwnedAllocated: ownedAllocated,
		Device: binary.LittleEndian.Uint64(record[80:88]), Inode: binary.LittleEndian.Uint64(record[88:96]),
		ModifiedAt: time.Unix(0, modifiedAt), HasChildren: record[104] != 0,
	}, nil
}

func signedValue(encoded []byte) (int64, error) {
	if len(encoded) != 8 {
		return 0, fmt.Errorf("%w: signed field width", ErrInvalidSnapshot)
	}
	return int64(binary.LittleEndian.Uint64(encoded)), nil
}

func (r *Reader) readDataString(base int64, record []byte, offset int) (string, error) {
	stringOffset := binary.LittleEndian.Uint32(record[offset : offset+4])
	stringLength := binary.LittleEndian.Uint32(record[offset+4 : offset+8])
	return readString(r.data, base, stringOffset, stringLength, r.validDataEnd)
}

func (r *Reader) findNodeIndex(nodeID int64) (indexNode, error) {
	if nodeID <= 0 {
		return indexNode{}, ErrNodeNotFound
	}
	low, high := uint64(0), r.indexHeader.NodeCount
	for low < high {
		middle := low + (high-low)/2
		entry, err := r.readNodeIndex(middle)
		if err != nil {
			return indexNode{}, err
		}
		if entry.nodeID < nodeID {
			low = middle + 1
		} else {
			high = middle
		}
	}
	if low >= r.indexHeader.NodeCount {
		return indexNode{}, ErrNodeNotFound
	}
	entry, err := r.readNodeIndex(low)
	if err != nil {
		return indexNode{}, err
	}
	if entry.nodeID != nodeID {
		return indexNode{}, ErrNodeNotFound
	}
	return entry, nil
}

func (r *Reader) readNodeIndex(index uint64) (indexNode, error) {
	if index >= r.indexHeader.NodeCount {
		return indexNode{}, ErrNodeNotFound
	}
	offset, err := offsetFor(r.indexHeader.NodeOffset, index, nodeIndexSize)
	if err != nil {
		return indexNode{}, err
	}
	buf := make([]byte, nodeIndexSize)
	if err := readAtFull(r.index, buf, offset); err != nil {
		return indexNode{}, err
	}
	return indexNode{
		nodeID:     int64(binary.LittleEndian.Uint64(buf[0:8])),
		dataOffset: int64(binary.LittleEndian.Uint64(buf[8:16])),
		stringBase: int64(binary.LittleEndian.Uint64(buf[16:24])),
	}, nil
}

func (r *Reader) findDirectory(parentID int64) (directoryEntry, bool, error) {
	low, high := uint64(0), r.indexHeader.DirectoryCount
	for low < high {
		middle := low + (high-low)/2
		entry, err := r.readDirectoryIndex(middle)
		if err != nil {
			return directoryEntry{}, false, err
		}
		if entry.parentID < parentID {
			low = middle + 1
		} else {
			high = middle
		}
	}
	if low >= r.indexHeader.DirectoryCount {
		return directoryEntry{}, false, nil
	}
	entry, err := r.readDirectoryIndex(low)
	if err != nil {
		return directoryEntry{}, false, err
	}
	return entry, entry.parentID == parentID, nil
}

func (r *Reader) readDirectoryIndex(index uint64) (directoryEntry, error) {
	offset, err := offsetFor(r.indexHeader.DirectoryOffset, index, directoryIndexSize)
	if err != nil {
		return directoryEntry{}, err
	}
	buf := make([]byte, directoryIndexSize)
	if err := readAtFull(r.index, buf, offset); err != nil {
		return directoryEntry{}, err
	}
	entry := directoryEntry{
		parentID:       int64(binary.LittleEndian.Uint64(buf[0:8])),
		childStart:     binary.LittleEndian.Uint64(buf[8:16]),
		childCount:     binary.LittleEndian.Uint64(buf[16:24]),
		logicalSize:    int64(binary.LittleEndian.Uint64(buf[24:32])),
		allocatedSize:  int64(binary.LittleEndian.Uint64(buf[32:40])),
		ownedAllocated: int64(binary.LittleEndian.Uint64(buf[40:48])),
	}
	if entry.logicalSize < 0 || entry.allocatedSize < 0 || entry.ownedAllocated < 0 {
		return directoryEntry{}, fmt.Errorf("%w: negative directory summary", ErrInvalidSnapshot)
	}
	return entry, nil
}

func (r *Reader) readChildIDs(start, count uint64) ([]int64, error) {
	if count == 0 {
		return []int64{}, nil
	}
	if count > maxPageSize {
		return nil, fmt.Errorf("%w: child page exceeds limit", ErrInvalidSnapshot)
	}
	ids := make([]int64, int(count))
	for index := uint64(0); index < count; index++ {
		childIndex, err := r.readChildIndex(start + index)
		if err != nil {
			return nil, err
		}
		ids[index] = childIndex.nodeID
	}
	return ids, nil
}

func (r *Reader) readChildIndex(index uint64) (childIndexRecord, error) {
	if index >= r.indexHeader.ChildCount {
		return childIndexRecord{}, fmt.Errorf("%w: child index out of bounds", ErrInvalidSnapshot)
	}
	offset, err := offsetFor(r.indexHeader.ChildOffset, index, childIndexSize)
	if err != nil {
		return childIndexRecord{}, err
	}
	buf := make([]byte, childIndexSize)
	if err := readAtFull(r.index, buf, offset); err != nil {
		return childIndexRecord{}, err
	}
	entry := childIndexRecord{
		nodeID:            int64(binary.LittleEndian.Uint64(buf[0:8])),
		logicalSize:       int64(binary.LittleEndian.Uint64(buf[8:16])),
		allocatedSize:     int64(binary.LittleEndian.Uint64(buf[16:24])),
		ownedAllocated:    int64(binary.LittleEndian.Uint64(buf[24:32])),
		prefixLogicalSize: int64(binary.LittleEndian.Uint64(buf[32:40])),
		prefixAllocated:   int64(binary.LittleEndian.Uint64(buf[40:48])),
		prefixOwned:       int64(binary.LittleEndian.Uint64(buf[48:56])),
	}
	if entry.nodeID <= 0 || entry.logicalSize < 0 || entry.allocatedSize < 0 || entry.ownedAllocated < 0 || entry.prefixLogicalSize < 0 || entry.prefixAllocated < 0 || entry.prefixOwned < 0 {
		return childIndexRecord{}, fmt.Errorf("%w: invalid child index record", ErrInvalidSnapshot)
	}
	return entry, nil
}

func (r *Reader) childPrefix(directory directoryEntry, consumed uint64) (childIndexRecord, error) {
	if consumed == 0 {
		return childIndexRecord{}, nil
	}
	if consumed > directory.childCount {
		return childIndexRecord{}, fmt.Errorf("%w: child prefix out of bounds", ErrInvalidSnapshot)
	}
	return r.readChildIndex(directory.childStart + consumed - 1)
}

func (r *Reader) readIssueEntry(index uint64) (issueEntry, error) {
	offset, err := offsetFor(r.indexHeader.IssueOffset, index, issueIndexSize)
	if err != nil {
		return issueEntry{}, err
	}
	buf := make([]byte, issueIndexSize)
	if err := readAtFull(r.index, buf, offset); err != nil {
		return issueEntry{}, err
	}
	return issueEntry{
		pathOffset: binary.LittleEndian.Uint32(buf[0:4]), pathLength: binary.LittleEndian.Uint32(buf[4:8]),
		messageOffset: binary.LittleEndian.Uint32(buf[8:12]), messageLength: binary.LittleEndian.Uint32(buf[12:16]),
	}, nil
}

func normalizePageLimit(limit int) (int, error) {
	if limit <= 0 {
		return 256, nil
	}
	if limit > maxPageSize {
		return 0, fmt.Errorf("%w: page limit %d exceeds %d", ErrInvalidSnapshot, limit, maxPageSize)
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

func validateManifest(manifest Manifest, snapshotID int64) error {
	if manifest.SchemaVersion != formatVersion || manifest.SnapshotID != snapshotID || manifest.TaskID == "" || manifest.Root == "" || manifest.DataFile == "" || manifest.SnapshotVersion <= 0 {
		return fmt.Errorf("%w: manifest fields", ErrInvalidSnapshot)
	}
	if manifest.NodeCount < 0 || manifest.FileCount < 0 || manifest.DirectoryCount < 0 || manifest.Bytes < 0 || manifest.IssueCount < 0 {
		return fmt.Errorf("%w: negative manifest summary", ErrInvalidSnapshot)
	}
	if manifest.NodeCount > 0 && manifest.RootNodeID <= 0 {
		return fmt.Errorf("%w: missing root node", ErrInvalidSnapshot)
	}
	return nil
}

func safeManifestPath(directory, name string) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", fmt.Errorf("%w: unsafe manifest path", ErrInvalidSnapshot)
	}
	return filepath.Join(directory, name), nil
}

func offsetFor(base, index uint64, width int) (int64, error) {
	product, err := checkedMul(index, uint64(width))
	if err != nil {
		return 0, err
	}
	value, err := checkedAdd(base, product)
	if err != nil || value > math.MaxInt64 {
		return 0, fmt.Errorf("%w: index offset overflow", ErrInvalidSnapshot)
	}
	return int64(value), nil
}

func openIndex(path string, snapshotID int64) (*os.File, indexHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, indexHeader{}, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, indexHeader{}, err
	}
	if info.Size() < indexHeaderSize || info.Size() > maxIndexSize {
		return nil, indexHeader{}, fmt.Errorf("%w: index file size", ErrInvalidSnapshot)
	}
	headerBytes := make([]byte, indexHeaderSize)
	if err := readAtFull(file, headerBytes, 0); err != nil {
		return nil, indexHeader{}, err
	}
	if string(headerBytes[0:8]) != indexMagic || binary.LittleEndian.Uint32(headerBytes[8:12]) != formatVersion || binary.LittleEndian.Uint32(headerBytes[12:16]) != indexHeaderSize {
		return nil, indexHeader{}, fmt.Errorf("%w: index header", ErrInvalidSnapshot)
	}
	if int64(binary.LittleEndian.Uint64(headerBytes[24:32])) != snapshotID {
		return nil, indexHeader{}, fmt.Errorf("%w: index snapshot ID mismatch", ErrInvalidSnapshot)
	}
	header := indexHeader{
		Generation:      binary.LittleEndian.Uint64(headerBytes[16:24]),
		NodeCount:       binary.LittleEndian.Uint64(headerBytes[32:40]),
		DirectoryCount:  binary.LittleEndian.Uint64(headerBytes[40:48]),
		ChildCount:      binary.LittleEndian.Uint64(headerBytes[48:56]),
		IssueCount:      binary.LittleEndian.Uint64(headerBytes[56:64]),
		NodeOffset:      binary.LittleEndian.Uint64(headerBytes[64:72]),
		DirectoryOffset: binary.LittleEndian.Uint64(headerBytes[72:80]),
		ChildOffset:     binary.LittleEndian.Uint64(headerBytes[80:88]),
		IssueOffset:     binary.LittleEndian.Uint64(headerBytes[88:96]),
		StringsOffset:   binary.LittleEndian.Uint64(headerBytes[96:104]),
		StringsLength:   binary.LittleEndian.Uint64(headerBytes[104:112]),
		PayloadLength:   binary.LittleEndian.Uint64(headerBytes[112:120]),
	}
	if err := validateIndexRanges(header, uint64(info.Size())); err != nil {
		return nil, indexHeader{}, err
	}
	if err := verifyIndexChecksum(file, info.Size(), headerBytes); err != nil {
		return nil, indexHeader{}, err
	}
	closeOnError = false
	return file, header, nil
}

func validateIndexRanges(header indexHeader, fileSize uint64) error {
	if header.Generation == 0 || header.NodeOffset != indexHeaderSize {
		return fmt.Errorf("%w: index offsets", ErrInvalidSnapshot)
	}
	nodeBytes, err := checkedMul(header.NodeCount, nodeIndexSize)
	if err != nil {
		return err
	}
	directoryBytes, err := checkedMul(header.DirectoryCount, directoryIndexSize)
	if err != nil {
		return err
	}
	childBytes, err := checkedMul(header.ChildCount, childIndexSize)
	if err != nil {
		return err
	}
	issueBytes, err := checkedMul(header.IssueCount, issueIndexSize)
	if err != nil {
		return err
	}
	directoryOffset, err := checkedAdd(header.NodeOffset, nodeBytes)
	if err != nil || header.DirectoryOffset != directoryOffset {
		return fmt.Errorf("%w: index table offsets", ErrInvalidSnapshot)
	}
	childOffset, err := checkedAdd(header.DirectoryOffset, directoryBytes)
	if err != nil || header.ChildOffset != childOffset {
		return fmt.Errorf("%w: index table offsets", ErrInvalidSnapshot)
	}
	issueOffset, err := checkedAdd(header.ChildOffset, childBytes)
	if err != nil || header.IssueOffset != issueOffset {
		return fmt.Errorf("%w: index table offsets", ErrInvalidSnapshot)
	}
	stringsOffset, err := checkedAdd(header.IssueOffset, issueBytes)
	if err != nil || header.StringsOffset != stringsOffset {
		return fmt.Errorf("%w: index table offsets", ErrInvalidSnapshot)
	}
	end, err := checkedAdd(header.StringsOffset, header.StringsLength)
	if err != nil || end != fileSize || header.PayloadLength != fileSize-indexHeaderSize {
		return fmt.Errorf("%w: index table bounds", ErrInvalidSnapshot)
	}
	return nil
}

func verifyIndexChecksum(file *os.File, fileSize int64, header []byte) error {
	if len(header) != indexHeaderSize || fileSize < indexHeaderSize {
		return fmt.Errorf("%w: index checksum input", ErrInvalidSnapshot)
	}
	headerCopy := append([]byte(nil), header...)
	for index := 120; index < 152; index++ {
		headerCopy[index] = 0
	}
	hash := sha256.New()
	_, _ = hash.Write(headerCopy)
	remaining := fileSize - indexHeaderSize
	offset := int64(indexHeaderSize)
	buffer := make([]byte, 64*1024)
	for remaining > 0 {
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining
		}
		if err := readAtFull(file, buffer[:readSize], offset); err != nil {
			return err
		}
		_, _ = hash.Write(buffer[:readSize])
		offset += readSize
		remaining -= readSize
	}
	var expected [sha256.Size]byte
	copy(expected[:], header[120:152])
	actual := hash.Sum(nil)
	for index := range expected {
		if expected[index] != actual[index] {
			return fmt.Errorf("%w: index checksum mismatch", ErrInvalidSnapshot)
		}
	}
	return nil
}

func scanData(file *os.File, fileSize int64, snapshotID int64) (dataState, error) {
	if fileSize < dataHeaderSize {
		return dataState{}, fmt.Errorf("%w: data file too small", ErrInvalidSnapshot)
	}
	header := make([]byte, dataHeaderSize)
	if err := readAtFull(file, header, 0); err != nil {
		return dataState{}, err
	}
	if err := decodeDataHeader(header, snapshotID); err != nil {
		return dataState{}, err
	}
	state := dataState{validEnd: dataHeaderSize}
	offset := int64(dataHeaderSize)
	expectedSequence := uint64(1)
	for offset <= fileSize-batchHeaderSize {
		batchHeader := make([]byte, batchHeaderSize)
		if err := readAtFull(file, batchHeader, offset); err != nil {
			break
		}
		if string(batchHeader[0:4]) != batchMagic || binary.LittleEndian.Uint32(batchHeader[4:8]) != formatVersion || binary.LittleEndian.Uint64(batchHeader[8:16]) != expectedSequence {
			break
		}
		nodeCount := binary.LittleEndian.Uint32(batchHeader[16:20])
		issueCount := binary.LittleEndian.Uint32(batchHeader[20:24])
		nodeBytes := binary.LittleEndian.Uint64(batchHeader[24:32])
		stringBytes := binary.LittleEndian.Uint64(batchHeader[32:40])
		issueBytes := binary.LittleEndian.Uint64(batchHeader[40:48])
		payloadBytes := binary.LittleEndian.Uint64(batchHeader[48:56])
		if nodeCount > maxBatchNodes || nodeBytes != uint64(nodeCount)*nodeRecordSize || issueBytes != uint64(issueCount)*issueRecordSize {
			break
		}
		calculatedPayload, err := checkedAdd(nodeBytes, stringBytes)
		if err != nil {
			break
		}
		calculatedPayload, err = checkedAdd(calculatedPayload, issueBytes)
		if err != nil || calculatedPayload != payloadBytes || payloadBytes > maxBatchPayload {
			break
		}
		frameLength, err := checkedAdd(batchHeaderSize, payloadBytes)
		if err != nil {
			break
		}
		frameLength, err = checkedAdd(frameLength, commitFooterSize)
		if err != nil || frameLength > math.MaxInt64 {
			break
		}
		frameEnd := offset + int64(frameLength)
		if frameEnd < offset || frameEnd > fileSize {
			break
		}
		footer := make([]byte, commitFooterSize)
		footerOffset := offset + batchHeaderSize + int64(payloadBytes)
		if err := readAtFull(file, footer, footerOffset); err != nil {
			break
		}
		if string(footer[0:4]) != commitMagic || binary.LittleEndian.Uint32(footer[4:8]) != formatVersion || binary.LittleEndian.Uint64(footer[8:16]) != expectedSequence || binary.LittleEndian.Uint64(footer[16:24]) != frameLength || binary.LittleEndian.Uint64(footer[24:32]) != uint64(nodeCount) || binary.LittleEndian.Uint64(footer[32:40]) != uint64(issueCount) {
			break
		}
		actual, err := hashFrame(file, offset, batchHeader, payloadBytes)
		if err != nil {
			break
		}
		if string(actual[:]) != string(footer[40:72]) {
			break
		}
		state.validEnd = frameEnd
		state.nodeCount += uint64(nodeCount)
		state.issueCount += uint64(issueCount)
		state.lastSequence = expectedSequence
		expectedSequence++
		offset = frameEnd
	}
	return state, nil
}

func hashFrame(file *os.File, offset int64, header []byte, payloadLength uint64) ([sha256.Size]byte, error) {
	hash := sha256.New()
	_, _ = hash.Write(header)
	remaining := int64(payloadLength)
	readOffset := offset + batchHeaderSize
	buffer := make([]byte, 64*1024)
	for remaining > 0 {
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining
		}
		if err := readAtFull(file, buffer[:readSize], readOffset); err != nil {
			return [sha256.Size]byte{}, err
		}
		_, _ = hash.Write(buffer[:readSize])
		readOffset += readSize
		remaining -= readSize
	}
	var checksum [sha256.Size]byte
	copy(checksum[:], hash.Sum(nil))
	return checksum, nil
}
