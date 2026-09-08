package scanner

import (
	"os"
	"path/filepath"
	"syscall"

	"example.com/marmot/internal/domain/scan"
)

// ReadDirectoryShallow lists one directory's direct children as nodes, the way
// the walk would have emitted them, without descending (ADR-0072). It is what a
// file-system event for that directory resolves to. IDs and ParentIDs are left
// zero: the caller splices the nodes into a tree that has its own numbering.
//
// Mount points are left out, as the walk leaves them out (they are attached as
// volume nodes by the application). Hard links are not deduplicated: the walk's
// link table is per scan and a shallow read has no access to it, so a linked
// file counts its full size here. Firmlink children take the identity a path
// lookup finds (SDD §13f), as they do in the walk.
func (Scanner) ReadDirectory(path, volumeID string) (scan.Node, []scan.Node, error) {
	return ReadDirectoryShallow(path, volumeID)
}

func ReadDirectoryShallow(path, volumeID string) (scan.Node, []scan.Node, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return scan.Node{}, nil, err
	}
	if !info.IsDir() {
		return scan.Node{}, nil, &os.PathError{Op: "read directory", Path: path, Err: syscall.ENOTDIR}
	}
	self := scan.Node{Path: path, Name: filepath.Base(path), Kind: "directory", ModifiedAt: info.ModTime(), HasChildren: true}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		self.Device, self.Inode = uint64(stat.Dev), stat.Ino
	}
	entries, err := listDirectoryEntries(path, -1)
	if err != nil {
		return scan.Node{}, nil, err
	}
	firmlinks := loadFirmlinkSources()
	nodes := make([]scan.Node, 0, len(entries))
	for _, entry := range entries {
		if entry.mountPoint || entry.readError != nil {
			continue
		}
		childPath := filepath.Join(path, entry.name)
		node := entry.node(0, 0, childPath, volumeID)
		node.Path = childPath
		if node.Kind == "directory" {
			if _, isFirmlink := firmlinks[childPath]; isFirmlink {
				if device, inode, ok := lookupIdentity(childPath); ok {
					node.Device, node.Inode = device, inode
				}
			}
		}
		nodes = append(nodes, node)
	}
	return self, nodes, nil
}
