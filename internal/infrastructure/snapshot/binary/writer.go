package binarysnapshot

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"

	"example.com/marmot/internal/domain/scan"
)

type Writer struct {
	directory  string
	paths      snapshotPaths
	data       *os.File
	manifest   Manifest
	records    map[int64]recordRef
	children   map[int64][]int64
	issues     []scan.Issue
	sequence   uint64
	generation uint64
	dataEnd    int64
	failed     bool
	closed     bool
	finished   bool
}

func NewWriter(config Config) (*Writer, error) {
	if config.Directory == "" || config.SnapshotID <= 0 || config.TaskID == "" || config.Root == "" {
		return nil, fmt.Errorf("%w: incomplete writer configuration", ErrInvalidSnapshot)
	}
	if err := os.MkdirAll(config.Directory, 0o700); err != nil {
		return nil, err
	}
	paths := pathsFor(config.Directory, config.SnapshotID)
	data, err := os.OpenFile(paths.data, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	createdAt := nowUnixNano()
	if err := writeFull(data, encodeDataHeader(config.SnapshotID, createdAt)); err != nil {
		_ = data.Close()
		_ = os.Remove(paths.data)
		return nil, err
	}
	if err := data.Sync(); err != nil {
		_ = data.Close()
		_ = os.Remove(paths.data)
		return nil, err
	}

	writer := &Writer{
		directory: config.Directory,
		paths:     paths,
		data:      data,
		records:   make(map[int64]recordRef),
		children:  make(map[int64][]int64),
		sequence:  1,
		dataEnd:   dataHeaderSize,
		manifest: Manifest{
			SchemaVersion:   formatVersion,
			SnapshotID:      config.SnapshotID,
			TaskID:          config.TaskID,
			Root:            filepath.Clean(config.Root),
			State:           scan.JobRunning,
			Phase:           string(scan.PhaseCatalog),
			SnapshotVersion: 1,
			DataFile:        filepath.Base(paths.data),
			CreatedAt:       createdAt,
		},
	}
	if err := writer.writeManifest(writer.manifest); err != nil {
		_ = data.Close()
		_ = os.Remove(paths.data)
		return nil, err
	}
	return writer, nil
}

func (w *Writer) Manifest() Manifest { return w.manifest }

func (w *Writer) AppendBatch(nodes []scan.Node, issues []scan.Issue) error {
	if w.closed {
		return ErrSnapshotClosed
	}
	if w.failed {
		return fmt.Errorf("%w: writer has failed", ErrInvalidSnapshot)
	}
	if w.finished {
		return fmt.Errorf("%w: snapshot already finished", ErrInvalidSnapshot)
	}
	if len(nodes) > maxBatchNodes {
		return fmt.Errorf("%w: batch has %d nodes, maximum is %d", ErrInvalidSnapshot, len(nodes), maxBatchNodes)
	}
	if len(nodes) == 0 && len(issues) == 0 {
		return nil
	}

	localIDs := make(map[int64]struct{}, len(nodes))
	batchRoots := 0
	batchBytes := int64(0)
	for _, node := range nodes {
		if err := validateNode(node); err != nil {
			return err
		}
		if _, exists := localIDs[node.ID]; exists {
			return fmt.Errorf("%w: duplicate node ID %d in batch", ErrInvalidSnapshot, node.ID)
		}
		if _, exists := w.records[node.ID]; exists {
			return fmt.Errorf("%w: duplicate node ID %d", ErrInvalidSnapshot, node.ID)
		}
		if node.ParentID == 0 {
			if w.manifest.RootNodeID != 0 || batchRoots > 0 {
				return fmt.Errorf("%w: multiple root nodes", ErrInvalidSnapshot)
			}
			batchRoots++
		}
		if node.Kind == "file" || node.Kind == "symlink" {
			if math.MaxInt64-batchBytes < node.OwnedAllocated || math.MaxInt64-w.manifest.Bytes-batchBytes < node.OwnedAllocated {
				return fmt.Errorf("%w: byte summary overflow", ErrInvalidSnapshot)
			}
			batchBytes += node.OwnedAllocated
		}
		localIDs[node.ID] = struct{}{}
	}
	if int64(len(nodes)) > math.MaxInt64-w.manifest.NodeCount || int64(len(issues)) > math.MaxInt64-w.manifest.IssueCount {
		return fmt.Errorf("%w: summary overflow", ErrInvalidSnapshot)
	}

	stringsArena := make([]byte, 0, len(nodes)*32+len(issues)*32)
	nodeBytes, _, err := encodeNodes(nodes, &stringsArena)
	if err != nil {
		return err
	}
	issueBytes, _, err := encodeIssues(issues, &stringsArena)
	if err != nil {
		return err
	}
	payloadBytes, err := checkedAdd(uint64(len(nodeBytes)), uint64(len(stringsArena)))
	if err != nil {
		return err
	}
	payloadBytes, err = checkedAdd(payloadBytes, uint64(len(issueBytes)))
	if err != nil || payloadBytes > maxBatchPayload {
		return fmt.Errorf("%w: batch payload exceeds %d bytes", ErrInvalidSnapshot, maxBatchPayload)
	}
	if len(nodeBytes)/nodeRecordSize != len(nodes) || len(issueBytes)/issueRecordSize != len(issues) {
		return fmt.Errorf("%w: encoded record count mismatch", ErrInvalidSnapshot)
	}

	batchStart, err := w.data.Seek(0, io.SeekEnd)
	if err != nil {
		w.failed = true
		return err
	}
	stringBase := batchStart + batchHeaderSize + int64(len(nodeBytes))
	header := encodeBatchHeader(w.sequence, uint32(len(nodes)), uint32(len(issues)), uint64(len(nodeBytes)), uint64(len(stringsArena)), uint64(len(issueBytes)))
	hash := sha256.New()
	_, _ = hash.Write(header)
	_, _ = hash.Write(nodeBytes)
	_, _ = hash.Write(stringsArena)
	_, _ = hash.Write(issueBytes)
	var checksum [sha256.Size]byte
	copy(checksum[:], hash.Sum(nil))
	frameLength := uint64(batchHeaderSize) + payloadBytes + commitFooterSize
	footer := encodeCommitFooter(w.sequence, frameLength, uint32(len(nodes)), uint32(len(issues)), checksum)
	if err := writeFull(w.data, header); err != nil {
		w.failed = true
		return err
	}
	if err := writeFull(w.data, nodeBytes); err != nil {
		w.failed = true
		return err
	}
	if err := writeFull(w.data, stringsArena); err != nil {
		w.failed = true
		return err
	}
	if err := writeFull(w.data, issueBytes); err != nil {
		w.failed = true
		return err
	}
	if err := writeFull(w.data, footer); err != nil {
		w.failed = true
		return err
	}
	if err := w.data.Sync(); err != nil {
		w.failed = true
		return err
	}

	for index, node := range nodes {
		ref := recordRef{
			node:       node,
			dataOffset: batchStart + batchHeaderSize + int64(index*nodeRecordSize),
			stringBase: stringBase,
		}
		w.records[node.ID] = ref
		w.children[node.ParentID] = append(w.children[node.ParentID], node.ID)
		if node.ParentID == 0 {
			w.manifest.RootNodeID = node.ID
		}
		switch node.Kind {
		case "file", "symlink":
			w.manifest.FileCount++
			w.manifest.Bytes += node.OwnedAllocated
		case "directory":
			w.manifest.DirectoryCount++
		}
	}
	w.issues = append(w.issues, issues...)
	w.manifest.NodeCount += int64(len(nodes))
	w.manifest.IssueCount += int64(len(issues))
	w.dataEnd = batchStart + int64(frameLength)
	w.sequence++
	return nil
}

func (w *Writer) Publish(phase string) error {
	return w.publish(scan.JobRunning, phase, "")
}

func (w *Writer) Finish(state, phase, failure string) error {
	if state == "" || state == scan.JobRunning {
		return fmt.Errorf("%w: finish requires a terminal state", ErrInvalidSnapshot)
	}
	if err := w.publish(state, phase, failure); err != nil {
		return err
	}
	w.finished = true
	return nil
}

func (w *Writer) publish(state, phase, failure string) error {
	if w.closed {
		return ErrSnapshotClosed
	}
	if w.failed {
		return fmt.Errorf("%w: writer has failed", ErrInvalidSnapshot)
	}
	if err := w.validateIndexInputs(); err != nil {
		return err
	}
	if phase == "" {
		phase = w.manifest.Phase
	}
	nextGeneration := w.generation + 1
	indexBytes, err := w.buildIndex(nextGeneration)
	if err != nil {
		return err
	}
	indexFile := indexName(w.paths, nextGeneration)
	if err := writeAtomicFile(w.directory, indexFile, indexBytes); err != nil {
		return err
	}
	next := w.manifest
	next.State = state
	next.Phase = phase
	next.Error = failure
	next.IndexGeneration = nextGeneration
	next.IndexFile = filepath.Base(indexFile)
	next.DataEnd = w.dataEnd
	next.SnapshotVersion++
	if state != scan.JobRunning {
		next.FinishedAt = nowUnixNano()
	}
	if err := w.writeManifest(next); err != nil {
		return err
	}
	w.manifest = next
	w.generation = nextGeneration
	return nil
}

func (w *Writer) validateIndexInputs() error {
	if len(w.records) == 0 {
		return fmt.Errorf("%w: snapshot has no nodes", ErrInvalidSnapshot)
	}
	if w.manifest.RootNodeID == 0 {
		return fmt.Errorf("%w: snapshot has no root node", ErrInvalidSnapshot)
	}
	for id, ref := range w.records {
		if ref.node.ParentID != 0 {
			if _, ok := w.records[ref.node.ParentID]; !ok {
				return fmt.Errorf("%w: node %d references missing parent %d", ErrInvalidSnapshot, id, ref.node.ParentID)
			}
		}
	}
	return nil
}

func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return w.data.Close()
}

func validateNode(node scan.Node) error {
	if node.ID <= 0 || node.ParentID < 0 {
		return fmt.Errorf("%w: invalid node identity", ErrInvalidSnapshot)
	}
	if node.LogicalSize < 0 || node.AllocatedSize < 0 || node.OwnedAllocated < 0 {
		return fmt.Errorf("%w: negative node size", ErrInvalidSnapshot)
	}
	for label, value := range map[string]string{
		"name": node.Name, "kind": node.Kind, "volume": node.VolumeID, "confidence": node.Confidence, "size_basis": node.SizeBasis,
	} {
		if len(value) > maxStringSize {
			return fmt.Errorf("%w: %s exceeds string limit", ErrInvalidSnapshot, label)
		}
	}
	return nil
}

type stringRef struct{ offset, length uint32 }

type nodeStringRefs struct {
	name, kind, volume, confidence, sizeBasis stringRef
}

func encodeNodes(nodes []scan.Node, arena *[]byte) ([]byte, []nodeStringRefs, error) {
	bytesLength, err := checkedMul(uint64(len(nodes)), nodeRecordSize)
	if err != nil || bytesLength > math.MaxInt {
		return nil, nil, fmt.Errorf("%w: node record size", ErrInvalidSnapshot)
	}
	encoded := make([]byte, int(bytesLength))
	refs := make([]nodeStringRefs, len(nodes))
	for index, node := range nodes {
		values := []*stringRef{&refs[index].name, &refs[index].kind, &refs[index].volume, &refs[index].confidence, &refs[index].sizeBasis}
		strings := []string{node.Name, node.Kind, node.VolumeID, node.Confidence, node.SizeBasis}
		for valueIndex, value := range strings {
			offset, length, err := appendString(arena, value)
			if err != nil {
				return nil, nil, err
			}
			values[valueIndex].offset = offset
			values[valueIndex].length = length
		}
		base := index * nodeRecordSize
		binary.LittleEndian.PutUint64(encoded[base:base+8], uint64(node.ID))
		binary.LittleEndian.PutUint64(encoded[base+8:base+16], uint64(node.ParentID))
		putStringPair(encoded, base+16, refs[index].name.offset, refs[index].name.length)
		putStringPair(encoded, base+24, refs[index].kind.offset, refs[index].kind.length)
		putStringPair(encoded, base+32, refs[index].volume.offset, refs[index].volume.length)
		putStringPair(encoded, base+40, refs[index].confidence.offset, refs[index].confidence.length)
		putStringPair(encoded, base+48, refs[index].sizeBasis.offset, refs[index].sizeBasis.length)
		binary.LittleEndian.PutUint64(encoded[base+56:base+64], uint64(node.LogicalSize))
		binary.LittleEndian.PutUint64(encoded[base+64:base+72], uint64(node.AllocatedSize))
		binary.LittleEndian.PutUint64(encoded[base+72:base+80], uint64(node.OwnedAllocated))
		binary.LittleEndian.PutUint64(encoded[base+80:base+88], node.Device)
		binary.LittleEndian.PutUint64(encoded[base+88:base+96], node.Inode)
		binary.LittleEndian.PutUint64(encoded[base+96:base+104], uint64(node.ModifiedAt.UnixNano()))
		if node.HasChildren {
			encoded[base+104] = 1
		}
	}
	return encoded, refs, nil
}

func encodeIssues(issues []scan.Issue, arena *[]byte) ([]byte, []issueEntry, error) {
	bytesLength, err := checkedMul(uint64(len(issues)), issueRecordSize)
	if err != nil || bytesLength > math.MaxInt {
		return nil, nil, fmt.Errorf("%w: issue record size", ErrInvalidSnapshot)
	}
	encoded := make([]byte, int(bytesLength))
	refs := make([]issueEntry, len(issues))
	for index, issue := range issues {
		pathOffset, pathLength, err := appendString(arena, issue.Path)
		if err != nil {
			return nil, nil, err
		}
		messageOffset, messageLength, err := appendString(arena, issue.Message)
		if err != nil {
			return nil, nil, err
		}
		refs[index] = issueEntry{pathOffset, pathLength, messageOffset, messageLength}
		base := index * issueRecordSize
		binary.LittleEndian.PutUint32(encoded[base:base+4], pathOffset)
		binary.LittleEndian.PutUint32(encoded[base+4:base+8], pathLength)
		binary.LittleEndian.PutUint32(encoded[base+8:base+12], messageOffset)
		binary.LittleEndian.PutUint32(encoded[base+12:base+16], messageLength)
	}
	return encoded, refs, nil
}

func (w *Writer) buildIndex(generation uint64) ([]byte, error) {
	ids := make([]int64, 0, len(w.records))
	for id := range w.records {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	parents := make([]int64, 0, len(w.children))
	for parentID := range w.children {
		parents = append(parents, parentID)
	}
	sort.Slice(parents, func(i, j int) bool { return parents[i] < parents[j] })
	childEntries := make([]childIndexRecord, 0, len(ids))
	orderedChildren := make(map[int64][]int64, len(parents))
	directoryTotals := make(map[int64][3]int64, len(parents))
	for _, parentID := range parents {
		children := append([]int64(nil), w.children[parentID]...)
		sort.Slice(children, func(i, j int) bool {
			left := w.records[children[i]].node
			right := w.records[children[j]].node
			if left.OwnedAllocated != right.OwnedAllocated {
				return left.OwnedAllocated > right.OwnedAllocated
			}
			return left.ID < right.ID
		})
		orderedChildren[parentID] = children
		var logicalTotal, allocatedTotal, ownedTotal int64
		for _, childID := range children {
			node := w.records[childID].node
			var err error
			logicalTotal, err = addSummary(logicalTotal, node.LogicalSize)
			if err != nil {
				return nil, err
			}
			allocatedTotal, err = addSummary(allocatedTotal, node.AllocatedSize)
			if err != nil {
				return nil, err
			}
			ownedTotal, err = addSummary(ownedTotal, node.OwnedAllocated)
			if err != nil {
				return nil, err
			}
			childEntries = append(childEntries, childIndexRecord{
				nodeID:            childID,
				logicalSize:       node.LogicalSize,
				allocatedSize:     node.AllocatedSize,
				ownedAllocated:    node.OwnedAllocated,
				prefixLogicalSize: logicalTotal,
				prefixAllocated:   allocatedTotal,
				prefixOwned:       ownedTotal,
			})
		}
		directoryTotals[parentID] = [3]int64{logicalTotal, allocatedTotal, ownedTotal}
	}

	indexStrings := make([]byte, 0, len(w.issues)*32)
	issueRefs := make([]issueEntry, len(w.issues))
	for index, issue := range w.issues {
		pathOffset, pathLength, err := appendString(&indexStrings, issue.Path)
		if err != nil {
			return nil, err
		}
		messageOffset, messageLength, err := appendString(&indexStrings, issue.Message)
		if err != nil {
			return nil, err
		}
		issueRefs[index] = issueEntry{pathOffset, pathLength, messageOffset, messageLength}
	}

	nodeBytes, err := checkedMul(uint64(len(ids)), nodeIndexSize)
	if err != nil {
		return nil, err
	}
	directoryBytes, err := checkedMul(uint64(len(parents)), directoryIndexSize)
	if err != nil {
		return nil, err
	}
	childBytes, err := checkedMul(uint64(len(childEntries)), childIndexSize)
	if err != nil {
		return nil, err
	}
	issueBytes, err := checkedMul(uint64(len(issueRefs)), issueIndexSize)
	if err != nil {
		return nil, err
	}
	nodeOffset := uint64(indexHeaderSize)
	directoryOffset, err := checkedAdd(nodeOffset, nodeBytes)
	if err != nil {
		return nil, err
	}
	childOffset, err := checkedAdd(directoryOffset, directoryBytes)
	if err != nil {
		return nil, err
	}
	issueOffset, err := checkedAdd(childOffset, childBytes)
	if err != nil {
		return nil, err
	}
	stringsOffset, err := checkedAdd(issueOffset, issueBytes)
	if err != nil {
		return nil, err
	}
	totalSize, err := checkedAdd(stringsOffset, uint64(len(indexStrings)))
	if err != nil || totalSize > maxIndexSize || totalSize > math.MaxInt {
		return nil, fmt.Errorf("%w: index size exceeds limit", ErrInvalidSnapshot)
	}
	buf := make([]byte, int(totalSize))
	copy(buf[0:8], indexMagic)
	binary.LittleEndian.PutUint32(buf[8:12], formatVersion)
	binary.LittleEndian.PutUint32(buf[12:16], indexHeaderSize)
	binary.LittleEndian.PutUint64(buf[16:24], generation)
	binary.LittleEndian.PutUint64(buf[24:32], uint64(w.manifest.SnapshotID))
	binary.LittleEndian.PutUint64(buf[32:40], uint64(len(ids)))
	binary.LittleEndian.PutUint64(buf[40:48], uint64(len(parents)))
	binary.LittleEndian.PutUint64(buf[48:56], uint64(len(childEntries)))
	binary.LittleEndian.PutUint64(buf[56:64], uint64(len(issueRefs)))
	binary.LittleEndian.PutUint64(buf[64:72], nodeOffset)
	binary.LittleEndian.PutUint64(buf[72:80], directoryOffset)
	binary.LittleEndian.PutUint64(buf[80:88], childOffset)
	binary.LittleEndian.PutUint64(buf[88:96], issueOffset)
	binary.LittleEndian.PutUint64(buf[96:104], stringsOffset)
	binary.LittleEndian.PutUint64(buf[104:112], uint64(len(indexStrings)))
	binary.LittleEndian.PutUint64(buf[112:120], totalSize-uint64(indexHeaderSize))

	for index, id := range ids {
		base := int(nodeOffset) + index*nodeIndexSize
		ref := w.records[id]
		binary.LittleEndian.PutUint64(buf[base:base+8], uint64(id))
		binary.LittleEndian.PutUint64(buf[base+8:base+16], uint64(ref.dataOffset))
		binary.LittleEndian.PutUint64(buf[base+16:base+24], uint64(ref.stringBase))
	}
	childStart := uint64(0)
	for index, parentID := range parents {
		base := int(directoryOffset) + index*directoryIndexSize
		children := orderedChildren[parentID]
		totals := directoryTotals[parentID]
		binary.LittleEndian.PutUint64(buf[base:base+8], uint64(parentID))
		binary.LittleEndian.PutUint64(buf[base+8:base+16], childStart)
		binary.LittleEndian.PutUint64(buf[base+16:base+24], uint64(len(children)))
		binary.LittleEndian.PutUint64(buf[base+24:base+32], uint64(totals[0]))
		binary.LittleEndian.PutUint64(buf[base+32:base+40], uint64(totals[1]))
		binary.LittleEndian.PutUint64(buf[base+40:base+48], uint64(totals[2]))
		childStart += uint64(len(children))
	}
	for index, child := range childEntries {
		base := int(childOffset) + index*childIndexSize
		binary.LittleEndian.PutUint64(buf[base:base+8], uint64(child.nodeID))
		binary.LittleEndian.PutUint64(buf[base+8:base+16], uint64(child.logicalSize))
		binary.LittleEndian.PutUint64(buf[base+16:base+24], uint64(child.allocatedSize))
		binary.LittleEndian.PutUint64(buf[base+24:base+32], uint64(child.ownedAllocated))
		binary.LittleEndian.PutUint64(buf[base+32:base+40], uint64(child.prefixLogicalSize))
		binary.LittleEndian.PutUint64(buf[base+40:base+48], uint64(child.prefixAllocated))
		binary.LittleEndian.PutUint64(buf[base+48:base+56], uint64(child.prefixOwned))
	}
	for index, ref := range issueRefs {
		base := int(issueOffset) + index*issueIndexSize
		binary.LittleEndian.PutUint32(buf[base:base+4], ref.pathOffset)
		binary.LittleEndian.PutUint32(buf[base+4:base+8], ref.pathLength)
		binary.LittleEndian.PutUint32(buf[base+8:base+12], ref.messageOffset)
		binary.LittleEndian.PutUint32(buf[base+12:base+16], ref.messageLength)
	}
	copy(buf[stringsOffset:], indexStrings)

	checksum := sha256.Sum256(buf)
	copy(buf[120:152], checksum[:])
	return buf, nil
}

func addSummary(total, value int64) (int64, error) {
	if value < 0 || total < 0 || math.MaxInt64-total < value {
		return 0, fmt.Errorf("%w: summary overflow", ErrInvalidSnapshot)
	}
	return total + value, nil
}

func (w *Writer) writeManifest(manifest Manifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicFile(w.directory, w.paths.manifest, encoded)
}

func writeAtomicFile(directory, target string, content []byte) error {
	temporary, err := os.CreateTemp(directory, filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryName)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := writeFull(temporary, content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, target)
}
