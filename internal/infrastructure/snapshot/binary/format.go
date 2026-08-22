package binarysnapshot

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"example.com/marmot/internal/domain/scan"
)

const (
	dataMagic   = "MARMOT01"
	batchMagic  = "MBAT"
	commitMagic = "MCOM"
	indexMagic  = "MIDX0001"

	formatVersion = uint32(1)

	dataHeaderSize   = 64
	batchHeaderSize  = 64
	commitFooterSize = 72
	indexHeaderSize  = 160

	nodeRecordSize     = 112
	issueRecordSize    = 16
	nodeIndexSize      = 32
	directoryIndexSize = 48
	childIndexSize     = 56
	issueIndexSize     = 16

	maxBatchNodes   = 32768
	maxBatchPayload = 4 * 1024 * 1024
	maxPageSize     = 1000
	maxStringSize   = 1 << 20
	maxIndexSize    = 512 * 1024 * 1024
)

var (
	ErrInvalidSnapshot  = errors.New("invalid snapshot")
	ErrSnapshotClosed   = errors.New("snapshot is closed")
	ErrIndexUnavailable = errors.New("snapshot index is unavailable")
	ErrNodeNotFound     = errors.New("snapshot node not found")
)

// Config identifies one physical snapshot. The directory is private to the
// application and should not be shared by concurrent writers for one ID.
type Config struct {
	Directory  string
	SnapshotID int64
	TaskID     string
	Root       string
}

// Manifest is the small atomic commit record for a snapshot. Data frames can
// exist beyond DataEnd, but only the index selected here is queryable.
type Manifest struct {
	SchemaVersion   uint32 `json:"schema_version"`
	SnapshotID      int64  `json:"snapshot_id"`
	TaskID          string `json:"task_id"`
	Root            string `json:"root"`
	RootNodeID      int64  `json:"root_node_id"`
	State           string `json:"state"`
	Phase           string `json:"phase"`
	SnapshotVersion int64  `json:"snapshot_version"`
	IndexGeneration uint64 `json:"index_generation"`
	IndexFile       string `json:"index_file"`
	DataFile        string `json:"data_file"`
	CreatedAt       int64  `json:"created_at"`
	FinishedAt      int64  `json:"finished_at,omitempty"`
	DataEnd         int64  `json:"data_end"`
	NodeCount       int64  `json:"node_count"`
	FileCount       int64  `json:"file_count"`
	DirectoryCount  int64  `json:"directory_count"`
	Bytes           int64  `json:"bytes"`
	IssueCount      int64  `json:"issue_count"`
	Error           string `json:"error,omitempty"`
}

func (m Manifest) Snapshot() scan.Snapshot {
	return scan.Snapshot{
		TaskID:          m.TaskID,
		ID:              m.SnapshotID,
		State:           m.State,
		Phase:           m.Phase,
		Root:            m.Root,
		SnapshotVersion: m.SnapshotVersion,
		NodeCount:       m.NodeCount,
		FileCount:       m.FileCount,
		DirCount:        m.DirectoryCount,
		Bytes:           m.Bytes,
		Issues:          m.IssueCount,
		Error:           m.Error,
	}
}

type recordRef struct {
	node       scan.Node
	dataOffset int64
	stringBase int64
}

type dataState struct {
	validEnd     int64
	nodeCount    uint64
	issueCount   uint64
	lastSequence uint64
}

type indexHeader struct {
	Generation      uint64
	NodeCount       uint64
	DirectoryCount  uint64
	ChildCount      uint64
	IssueCount      uint64
	NodeOffset      uint64
	DirectoryOffset uint64
	ChildOffset     uint64
	IssueOffset     uint64
	StringsOffset   uint64
	StringsLength   uint64
	PayloadLength   uint64
}

type indexNode struct {
	nodeID     int64
	dataOffset int64
	stringBase int64
}

type directoryEntry struct {
	parentID       int64
	childStart     uint64
	childCount     uint64
	logicalSize    int64
	allocatedSize  int64
	ownedAllocated int64
}

type childIndexRecord struct {
	nodeID            int64
	logicalSize       int64
	allocatedSize     int64
	ownedAllocated    int64
	prefixLogicalSize int64
	prefixAllocated   int64
	prefixOwned       int64
}

type issueEntry struct {
	pathOffset    uint32
	pathLength    uint32
	messageOffset uint32
	messageLength uint32
}

type snapshotPaths struct {
	data         string
	manifest     string
	indexPattern string
}

func pathsFor(directory string, snapshotID int64) snapshotPaths {
	base := fmt.Sprintf("snapshot-%d", snapshotID)
	return snapshotPaths{
		data:         filepath.Join(directory, base+".data"),
		manifest:     filepath.Join(directory, base+".manifest.json"),
		indexPattern: filepath.Join(directory, base+".index.%020d"),
	}
}

func indexName(paths snapshotPaths, generation uint64) string {
	return fmt.Sprintf(paths.indexPattern, generation)
}

func encodeDataHeader(snapshotID, createdAt int64) []byte {
	buf := make([]byte, dataHeaderSize)
	copy(buf[0:8], dataMagic)
	binary.LittleEndian.PutUint32(buf[8:12], formatVersion)
	binary.LittleEndian.PutUint32(buf[12:16], dataHeaderSize)
	binary.LittleEndian.PutUint64(buf[16:24], uint64(snapshotID))
	binary.LittleEndian.PutUint64(buf[24:32], uint64(createdAt))
	return buf
}

func decodeDataHeader(buf []byte, snapshotID int64) error {
	if len(buf) != dataHeaderSize || string(buf[0:8]) != dataMagic {
		return fmt.Errorf("%w: data header magic", ErrInvalidSnapshot)
	}
	if binary.LittleEndian.Uint32(buf[8:12]) != formatVersion || binary.LittleEndian.Uint32(buf[12:16]) != dataHeaderSize {
		return fmt.Errorf("%w: data header version", ErrInvalidSnapshot)
	}
	if int64(binary.LittleEndian.Uint64(buf[16:24])) != snapshotID {
		return fmt.Errorf("%w: snapshot ID mismatch", ErrInvalidSnapshot)
	}
	return nil
}

func encodeBatchHeader(sequence uint64, nodeCount, issueCount uint32, nodeBytes, stringBytes, issueBytes uint64) []byte {
	buf := make([]byte, batchHeaderSize)
	copy(buf[0:4], batchMagic)
	binary.LittleEndian.PutUint32(buf[4:8], formatVersion)
	binary.LittleEndian.PutUint64(buf[8:16], sequence)
	binary.LittleEndian.PutUint32(buf[16:20], nodeCount)
	binary.LittleEndian.PutUint32(buf[20:24], issueCount)
	binary.LittleEndian.PutUint64(buf[24:32], nodeBytes)
	binary.LittleEndian.PutUint64(buf[32:40], stringBytes)
	binary.LittleEndian.PutUint64(buf[40:48], issueBytes)
	binary.LittleEndian.PutUint64(buf[48:56], nodeBytes+stringBytes+issueBytes)
	return buf
}

func encodeCommitFooter(sequence, frameLength uint64, nodeCount, issueCount uint32, checksum [sha256.Size]byte) []byte {
	buf := make([]byte, commitFooterSize)
	copy(buf[0:4], commitMagic)
	binary.LittleEndian.PutUint32(buf[4:8], formatVersion)
	binary.LittleEndian.PutUint64(buf[8:16], sequence)
	binary.LittleEndian.PutUint64(buf[16:24], frameLength)
	binary.LittleEndian.PutUint64(buf[24:32], uint64(nodeCount))
	binary.LittleEndian.PutUint64(buf[32:40], uint64(issueCount))
	copy(buf[40:72], checksum[:])
	return buf
}

func checkedAdd(a, b uint64) (uint64, error) {
	if b > math.MaxUint64-a {
		return 0, fmt.Errorf("%w: integer overflow", ErrInvalidSnapshot)
	}
	return a + b, nil
}

func checkedMul(a, b uint64) (uint64, error) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, fmt.Errorf("%w: integer overflow", ErrInvalidSnapshot)
	}
	return a * b, nil
}

func readAtFull(file *os.File, buf []byte, offset int64) error {
	if offset < 0 {
		return fmt.Errorf("%w: negative file offset", ErrInvalidSnapshot)
	}
	read, err := file.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return err
	}
	if read != len(buf) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func writeFull(file *os.File, buf []byte) error {
	for len(buf) > 0 {
		written, err := file.Write(buf)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		buf = buf[written:]
	}
	return nil
}

func appendString(arena *[]byte, value string) (uint32, uint32, error) {
	if len(value) > maxStringSize || len(*arena) > math.MaxUint32-len(value) {
		return 0, 0, fmt.Errorf("%w: string arena limit", ErrInvalidSnapshot)
	}
	offset := uint32(len(*arena))
	*arena = append(*arena, value...)
	return offset, uint32(len(value)), nil
}

func putStringPair(dst []byte, offset int, valueOffset, valueLength uint32) {
	binary.LittleEndian.PutUint32(dst[offset:offset+4], valueOffset)
	binary.LittleEndian.PutUint32(dst[offset+4:offset+8], valueLength)
}

func readString(file *os.File, base int64, offset, length uint32, fileSize int64) (string, error) {
	if length > maxStringSize || base < 0 || offset > math.MaxInt32 {
		return "", fmt.Errorf("%w: string bounds", ErrInvalidSnapshot)
	}
	start := base + int64(offset)
	if start < base || int64(length) > fileSize-start {
		return "", fmt.Errorf("%w: string outside data file", ErrInvalidSnapshot)
	}
	buf := make([]byte, int(length))
	if err := readAtFull(file, buf, start); err != nil {
		return "", err
	}
	return string(buf), nil
}

func readIndexString(file *os.File, header indexHeader, offset, length uint32) (string, error) {
	if length > maxStringSize || uint64(offset)+uint64(length) > header.StringsLength {
		return "", fmt.Errorf("%w: index string bounds", ErrInvalidSnapshot)
	}
	start := int64(header.StringsOffset + uint64(offset))
	buf := make([]byte, int(length))
	if err := readAtFull(file, buf, start); err != nil {
		return "", err
	}
	return string(buf), nil
}

func nowUnixNano() int64 { return time.Now().UnixNano() }
