package platform

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/ports"
)

// RemovePermanently deletes a path outright, with no trash and no undo.
//
// It exists because moving to the trash does not free space -- the trash is on
// the same volume, so the move is a rename and the bytes are still there. A tool
// whose purpose is to reclaim space and which cannot reclaim any is not finished.
//
// Nothing here decides WHAT may be removed. That is policy and lives in the
// application layer, which refuses this path for anything the irreplaceable guard
// recognises: the trash makes that guard advisory, and this call makes it final.
func RemovePermanently(path string) error {
	if path == "" || path == "/" {
		return errors.New("拒绝删除空路径或根目录")
	}
	return os.RemoveAll(path)
}

// RemoveWithin deletes names beneath item, resolving every one of them through a
// descriptor for item rather than from "/" again.
//
// Two things have to be true and neither is true of a bare path. The directory
// must still be the object the plan validated -- os.OpenRoot follows symlinks in
// the name it is given, so opening the item path is not by itself proof that the
// item is what it was -- and every name must resolve inside it. os.Root gives the
// second (a name that escapes, by "..", by an absolute path, or through a symlink
// pointing out of the tree, is refused) and the device and inode check gives the
// first. After the check the descriptor is what is used, so the object cannot be
// swapped out from under the removal afterwards.
//
// Symlinks inside the tree are unlinked, not followed: a cache full of them --
// node_modules/.bin is nothing else -- is removed completely and whatever they
// point at is untouched.
func RemoveWithin(item cleanup.Item, names []string) error {
	if item.Path == "" || item.Path == "/" {
		return errors.New("拒绝删除空路径或根目录")
	}
	root, err := os.OpenRoot(item.Path)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("file identity is unavailable")
	}
	if uint64(stat.Dev) != item.Device || stat.Ino != item.Inode {
		return fmt.Errorf("%w: %s", ports.ErrItemReplaced, item.Path)
	}
	var first error
	for _, name := range names {
		if err := root.RemoveAll(name); err != nil && first == nil {
			first = err
		}
	}
	return first
}
