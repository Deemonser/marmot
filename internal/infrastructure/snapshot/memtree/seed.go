package memtree

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"example.com/marmot/internal/domain/scan"
)

// The seed file (ADR-0075 §1): a finished tree written as it sits in memory --
// the record pages, the name arena pages and the reclaim side table, verbatim --
// behind a small header that says which volume and which FSEvents position it
// belongs to. Nothing here is a second store: the file is either loaded whole
// into a tree and then brought up to date by a replay, or deleted (DDD 37e).
//
// Layout:
//
//	magic            12 bytes  "MARMOTSEED\x00\x00"
//	format version   u32
//	metadata length  u32
//	metadata         JSON (seedMetadata): identity header, code tables, counts,
//	                 issues, page counts -- everything that is not a page
//	record pages     recordPages × recordsPerPage × 64 B
//	arena pages      arenaPages × arenaPageSize
//	reclaim entries  reclaimEntries × 40 B
//
// The format version is pinned to the record and side-table layouts by
// TestSeedFormatPinsLayout: change either and the version must change with it,
// or an old seed would be accepted and read as garbage.
const (
	seedFormatVersion uint32 = 1
	seedMagic                = "MARMOTSEED\x00\x00"
	// seedMaxBytes is ADR-0075's ceiling on what may sit in the cache directory.
	seedMaxBytes int64 = 512 << 20
)

var (
	ErrSeedFormat   = errors.New("seed: not a seed file or unsupported format version")
	ErrSeedTooLarge = errors.New("seed: tree exceeds the seed size ceiling")
	ErrSeedCorrupt  = errors.New("seed: file is truncated or inconsistent")
)

type seedMetadata struct {
	Header scan.SeedHeader `json:"header"`
	// Layout is checked on load in addition to the format version: it is the
	// cheap, explicit statement of what the raw pages mean.
	RecordSize       int `json:"recordSize"`
	ReclaimEntrySize int `json:"reclaimEntrySize"`
	RecordPageShift  int `json:"recordPageShift"`
	ArenaPageSize    int `json:"arenaPageSize"`

	TaskID         string       `json:"taskId"`
	Root           string       `json:"root"`
	RecordLength   int64        `json:"recordLength"`
	RecordPages    int          `json:"recordPages"`
	ArenaNext      uint32       `json:"arenaNext"`
	ArenaPages     int          `json:"arenaPages"`
	ReclaimEntries int          `json:"reclaimEntries"`
	RootNodeID     int64        `json:"rootNodeId"`
	Kinds          []string     `json:"kinds"`
	Volumes        []string     `json:"volumes"`
	Bases          []string     `json:"bases"`
	Confidences    []string     `json:"confidences"`
	VolumeTotal    uint64       `json:"volumeTotal"`
	VolumeUsed     uint64       `json:"volumeUsed"`
	VolumeFree     uint64       `json:"volumeFree"`
	NodeCount      int64        `json:"nodeCount"`
	FileCount      int64        `json:"fileCount"`
	DirectoryCount int64        `json:"directoryCount"`
	Bytes          int64        `json:"bytes"`
	Issues         []scan.Issue `json:"issues"`
}

// WriteSeed writes the snapshot's tree to path as a seed with the given
// identity header. It writes to path+".tmp", fsyncs, then renames, so a crash
// leaves either the previous seed or none. Returns the file size.
//
// The store is locked for the duration (about 0.3 s for a 2.8M-node tree,
// R-078 §4.5): the pages must not move while they are being copied out.
func (s *Store) WriteSeed(snapshotID int64, path string, header scan.SeedHeader) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tr, err := s.treeFor(snapshotID)
	if err != nil {
		return 0, err
	}
	if !tr.finished {
		return 0, errors.New("seed: only a finished result can be written")
	}
	tr.reclaim.seal()
	recordSize := int(unsafe.Sizeof(record{}))
	entrySize := int(unsafe.Sizeof(reclaimEntry{}))
	estimate := int64(len(tr.records.pages))*int64(recordsPerPage)*int64(recordSize) +
		int64(len(tr.names.pages))*int64(arenaPageSize) +
		int64(len(tr.reclaim.sorted))*int64(entrySize)
	if estimate > seedMaxBytes {
		return 0, fmt.Errorf("%w: %d bytes", ErrSeedTooLarge, estimate)
	}

	meta := seedMetadata{
		Header:           header,
		RecordSize:       recordSize,
		ReclaimEntrySize: entrySize,
		RecordPageShift:  recordPageShift,
		ArenaPageSize:    arenaPageSize,
		TaskID:           tr.taskID,
		Root:             tr.root,
		RecordLength:     tr.records.len(),
		RecordPages:      len(tr.records.pages),
		ArenaNext:        tr.names.next,
		ArenaPages:       len(tr.names.pages),
		ReclaimEntries:   len(tr.reclaim.sorted),
		RootNodeID:       tr.rootNodeID,
		Kinds:            append([]string(nil), tr.kinds.values...),
		Volumes:          append([]string(nil), tr.volumes.values...),
		Bases:            append([]string(nil), tr.bases.values...),
		Confidences:      append([]string(nil), tr.confidences.values...),
		VolumeTotal:      tr.volumeTotal,
		VolumeUsed:       tr.volumeUsed,
		VolumeFree:       tr.volumeFree,
		NodeCount:        tr.nodeCount,
		FileCount:        tr.fileCount,
		DirectoryCount:   tr.directoryCount,
		Bytes:            tr.bytes,
		Issues:           append([]scan.Issue(nil), tr.issues...),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return 0, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	abandon := func(err error) (int64, error) {
		file.Close()
		os.Remove(temporary)
		return 0, err
	}
	writer := bufio.NewWriterSize(file, 4<<20)
	head := make([]byte, len(seedMagic)+8)
	copy(head, seedMagic)
	binary.LittleEndian.PutUint32(head[len(seedMagic):], seedFormatVersion)
	binary.LittleEndian.PutUint32(head[len(seedMagic)+4:], uint32(len(metaBytes)))
	if _, err := writer.Write(head); err != nil {
		return abandon(err)
	}
	if _, err := writer.Write(metaBytes); err != nil {
		return abandon(err)
	}
	for _, page := range tr.records.pages {
		if _, err := writer.Write(unsafe.Slice((*byte)(unsafe.Pointer(&page[0])), len(page)*recordSize)); err != nil {
			return abandon(err)
		}
	}
	for _, page := range tr.names.pages {
		if _, err := writer.Write(page); err != nil {
			return abandon(err)
		}
	}
	if len(tr.reclaim.sorted) > 0 {
		entries := tr.reclaim.sorted
		if _, err := writer.Write(unsafe.Slice((*byte)(unsafe.Pointer(&entries[0])), len(entries)*entrySize)); err != nil {
			return abandon(err)
		}
	}
	if err := writer.Flush(); err != nil {
		return abandon(err)
	}
	if err := file.Sync(); err != nil {
		return abandon(err)
	}
	if err := file.Close(); err != nil {
		os.Remove(temporary)
		return 0, err
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// ReadSeedHeader reads only the identity header, without touching the pages:
// what the application needs to decide whether the seed is worth loading.
func (s *Store) ReadSeedHeader(path string) (scan.SeedHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return scan.SeedHeader{}, err
	}
	defer file.Close()
	meta, _, err := readSeedMetadata(bufio.NewReaderSize(file, 64<<10))
	if err != nil {
		return scan.SeedHeader{}, err
	}
	return meta.Header, nil
}

func readSeedMetadata(reader io.Reader) (seedMetadata, uint32, error) {
	head := make([]byte, len(seedMagic)+8)
	if _, err := io.ReadFull(reader, head); err != nil {
		return seedMetadata{}, 0, fmt.Errorf("%w: %v", ErrSeedFormat, err)
	}
	if string(head[:len(seedMagic)]) != seedMagic {
		return seedMetadata{}, 0, ErrSeedFormat
	}
	version := binary.LittleEndian.Uint32(head[len(seedMagic):])
	if version != seedFormatVersion {
		return seedMetadata{}, version, fmt.Errorf("%w: version %d, want %d", ErrSeedFormat, version, seedFormatVersion)
	}
	metaLength := binary.LittleEndian.Uint32(head[len(seedMagic)+4:])
	if metaLength == 0 || metaLength > 64<<20 {
		return seedMetadata{}, version, ErrSeedCorrupt
	}
	metaBytes := make([]byte, metaLength)
	if _, err := io.ReadFull(reader, metaBytes); err != nil {
		return seedMetadata{}, version, fmt.Errorf("%w: %v", ErrSeedCorrupt, err)
	}
	var meta seedMetadata
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return seedMetadata{}, version, fmt.Errorf("%w: %v", ErrSeedCorrupt, err)
	}
	if meta.RecordSize != int(unsafe.Sizeof(record{})) || meta.ReclaimEntrySize != int(unsafe.Sizeof(reclaimEntry{})) ||
		meta.RecordPageShift != recordPageShift || meta.ArenaPageSize != arenaPageSize {
		return seedMetadata{}, version, fmt.Errorf("%w: layout differs from this build", ErrSeedFormat)
	}
	return meta, version, nil
}

// LoadSeed fills the snapshot's (empty, just created) tree from the seed at
// path. The tree comes back finished but ungrouped; the caller splices the
// replayed changes and calls FinishScan, after which nothing can tell it from a
// scanned tree. On any error the snapshot's tree is left empty and the caller
// falls back to a full scan.
func (s *Store) LoadSeed(snapshotID int64, path string) (scan.SeedHeader, error) {
	file, err := os.Open(path)
	if err != nil {
		return scan.SeedHeader{}, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 4<<20)
	meta, _, err := readSeedMetadata(reader)
	if err != nil {
		return scan.SeedHeader{}, err
	}
	if meta.RecordLength <= 0 || meta.RecordPages <= 0 || int64(meta.RecordPages)*recordsPerPage < meta.RecordLength ||
		meta.RootNodeID <= 0 || meta.RootNodeID >= meta.RecordLength {
		return scan.SeedHeader{}, ErrSeedCorrupt
	}
	recordSize := int(unsafe.Sizeof(record{}))
	entrySize := int(unsafe.Sizeof(reclaimEntry{}))

	// Read into fresh structures first; the store's tree is replaced only once
	// the whole file has been read, so a truncated file cannot leave a half tree.
	var records recordTable
	records.grow(int64(meta.RecordPages) * recordsPerPage)
	records.length = meta.RecordLength
	for _, page := range records.pages {
		if _, err := io.ReadFull(reader, unsafe.Slice((*byte)(unsafe.Pointer(&page[0])), len(page)*recordSize)); err != nil {
			return scan.SeedHeader{}, fmt.Errorf("%w: records: %v", ErrSeedCorrupt, err)
		}
	}
	names := arena{next: meta.ArenaNext}
	for range meta.ArenaPages {
		page := make([]byte, arenaPageSize)
		if _, err := io.ReadFull(reader, page); err != nil {
			return scan.SeedHeader{}, fmt.Errorf("%w: names: %v", ErrSeedCorrupt, err)
		}
		names.pages = append(names.pages, page)
	}
	reclaim := reclaimTable{sealed: true}
	if meta.ReclaimEntries > 0 {
		reclaim.sorted = make([]reclaimEntry, meta.ReclaimEntries)
		if _, err := io.ReadFull(reader, unsafe.Slice((*byte)(unsafe.Pointer(&reclaim.sorted[0])), meta.ReclaimEntries*entrySize)); err != nil {
			return scan.SeedHeader{}, fmt.Errorf("%w: reclaim: %v", ErrSeedCorrupt, err)
		}
	}
	codes := func(values []string) (*codeTable, error) {
		table := newCodeTable()
		for _, value := range values {
			if _, err := table.code(value); err != nil {
				return nil, err
			}
		}
		return table, nil
	}
	kinds, err := codes(meta.Kinds)
	if err != nil {
		return scan.SeedHeader{}, err
	}
	volumes, err := codes(meta.Volumes)
	if err != nil {
		return scan.SeedHeader{}, err
	}
	bases, err := codes(meta.Bases)
	if err != nil {
		return scan.SeedHeader{}, err
	}
	confidences, err := codes(meta.Confidences)
	if err != nil {
		return scan.SeedHeader{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tr, err := s.treeFor(snapshotID)
	if err != nil {
		return scan.SeedHeader{}, err
	}
	if tr.records.len() > 1 {
		return scan.SeedHeader{}, errors.New("seed: the snapshot already holds nodes")
	}
	tr.records = records
	tr.names = names
	tr.reclaim = reclaim
	tr.kinds, tr.volumes, tr.bases, tr.confidences = kinds, volumes, bases, confidences
	tr.rootNodeID = meta.RootNodeID
	tr.volumeTotal, tr.volumeUsed, tr.volumeFree = meta.VolumeTotal, meta.VolumeUsed, meta.VolumeFree
	tr.nodeCount, tr.fileCount, tr.directoryCount, tr.bytes = meta.NodeCount, meta.FileCount, meta.DirectoryCount, meta.Bytes
	tr.issues = append(tr.issues, meta.Issues...)
	tr.issueCount = int64(len(meta.Issues))
	tr.grouped = false
	tr.version++
	return meta.Header, nil
}
