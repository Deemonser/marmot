package scanner

import (
	"os"
	"sort"
	"syscall"
	"time"
)

type directoryEntry struct {
	name          string
	logicalSize   int64
	allocatedSize int64
	device        uint64
	inode         uint64
	linkCount     uint64
	modifiedAt    time.Time
	isDirectory   bool
	isSymlink     bool
	mountPoint    bool
	readError     error
	confidence    string
	sizeBasis     string
}

func listDirectoryEntries(path string, fd int) ([]directoryEntry, error) {
	var entries []directoryEntry
	var err error
	if fd >= 0 {
		entries, err = readDirectoryBulkFD(fd)
	} else {
		entries, err = readDirectoryBulk(path)
	}
	if err == nil {
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		return entries, nil
	}
	return readDirectoryEntriesFallback(path)
}

func readDirectoryEntriesFallback(path string) ([]directoryEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]directoryEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			result = append(result, directoryEntry{name: entry.Name(), readError: err})
			continue
		}
		result = append(result, directoryEntryFromFileInfo(entry.Name(), info, entry.Type()&os.ModeSymlink != 0))
	}
	return result, nil
}

func directoryEntryFromFileInfo(name string, info os.FileInfo, symlink bool) directoryEntry {
	entry := directoryEntry{
		name:          name,
		logicalSize:   info.Size(),
		allocatedSize: info.Size(),
		linkCount:     1,
		modifiedAt:    info.ModTime(),
		isDirectory:   info.IsDir() && !symlink,
		isSymlink:     symlink,
		confidence:    "estimated",
		sizeBasis:     "logical_size",
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		entry.device = uint64(stat.Dev)
		entry.inode = stat.Ino
		entry.linkCount = uint64(stat.Nlink)
		allocated := int64(stat.Blocks) * 512
		if allocated > 0 {
			entry.allocatedSize = allocated
			entry.confidence = "exact"
			entry.sizeBasis = "darwin_stat_blocks_512"
		}
	}
	if entry.isDirectory {
		entry.logicalSize = 0
		entry.allocatedSize = 0
	}
	return entry
}

func (entry directoryEntry) node(id, parentID int64, path, volumeID string) Node {
	node := Node{
		ID:             id,
		ParentID:       parentID,
		Path:           path,
		Name:           entry.name,
		Kind:           "file",
		LogicalSize:    entry.logicalSize,
		AllocatedSize:  entry.allocatedSize,
		OwnedAllocated: entry.allocatedSize,
		VolumeID:       volumeID,
		Confidence:     entry.confidence,
		SizeBasis:      entry.sizeBasis,
		Device:         entry.device,
		Inode:          entry.inode,
		ModifiedAt:     entry.modifiedAt,
	}
	if entry.isDirectory {
		node.Kind = "directory"
		node.LogicalSize = 0
		node.AllocatedSize = 0
		node.OwnedAllocated = 0
		node.HasChildren = true
	}
	if entry.isSymlink {
		node.Kind = "symlink"
	}
	if node.Kind != "directory" {
		// ADR-0057 §2: only directories carry a path, on every platform. The
		// portable walk still needs the path to test mount boundaries and to
		// report issues, so it is built either way here — dropping it keeps the
		// emit contract uniform rather than platform-dependent.
		node.Path = ""
	}
	return node
}
