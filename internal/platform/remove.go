package platform

import (
	"errors"
	"os"
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
