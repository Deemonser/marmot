package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"example.com/marmot/internal/domain/scan"
)

type Node = scan.Node
type Issue = scan.Issue
type Result = scan.Result
type DirectorySize = scan.DirectorySize
type Emit = scan.Emitter

type Scanner struct{}

func (Scanner) Scan(ctx context.Context, root string, emit scan.Emitter) (scan.Result, error) {
	return Scan(ctx, root, emit)
}

// WalkDir is deterministic in this first slice. That makes hard-link ownership
// stable while the persistence and UI contracts are proven. The scanner remains
// cancellable and bounded; the worker-pool optimization stays behind this API.
func Scan(ctx context.Context, root string, emit Emit) (Result, error) {
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return Result{}, err
	}
	if !rootInfo.IsDir() {
		return Result{}, fmt.Errorf("scan root is not a directory: %s", root)
	}

	result := Result{Nodes: 1, Directories: 1, DirectorySizes: map[int64]DirectorySize{}}
	rootNode, rootStat := makeNode(1, 0, root, filepath.Base(root), rootInfo, false)
	rootNode.Kind = "directory"
	rootNode.HasChildren = true
	rootNode.LogicalSize = 0
	rootNode.AllocatedSize = 0
	rootNode.OwnedAllocated = 0
	if rootStat != nil {
		rootNode.Device = uint64(rootStat.Dev)
		rootNode.Inode = rootStat.Ino
	}
	if err := emit(rootNode); err != nil {
		return result, err
	}

	parentIDs := map[string]int64{root: rootNode.ID}
	directoryParents := map[int64]int64{rootNode.ID: 0}
	directoryPaths := map[int64]string{rootNode.ID: root}
	directorySizes := map[int64]DirectorySize{rootNode.ID: {Confidence: "exact"}}
	seen := make(map[[2]uint64]struct{})
	var nextID int64 = 1

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			result.Issues = append(result.Issues, Issue{Path: path, Message: walkErr.Error()})
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			result.Issues = append(result.Issues, Issue{Path: path, Message: infoErr.Error()})
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		parentID := parentIDs[filepath.Dir(path)]
		nextID++
		node, stat := makeNode(nextID, parentID, path, entry.Name(), info, entry.Type()&os.ModeSymlink != 0)
		if node.Kind == "directory" {
			directoryParents[node.ID] = parentID
			directoryPaths[node.ID] = path
			directorySizes[node.ID] = DirectorySize{Confidence: "exact"}
			parentIDs[path] = node.ID
		} else {
			result.Files++
			if stat != nil && stat.Nlink > 1 && node.Kind == "file" {
				key := [2]uint64{uint64(stat.Dev), stat.Ino}
				if _, exists := seen[key]; exists {
					node.OwnedAllocated = 0
				} else {
					seen[key] = struct{}{}
				}
			}
			if parentID != 0 {
				total := directorySizes[parentID]
				addDirectorySize(&total, node)
				directorySizes[parentID] = total
			}
		}
		if node.Kind == "directory" {
			result.Directories++
		}
		result.Nodes++
		result.Bytes += node.OwnedAllocated
		return emit(node)
	})
	if err != nil {
		return result, err
	}

	ids := make([]int64, 0, len(directoryPaths))
	for id := range directoryPaths {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return len(directoryPaths[ids[i]]) > len(directoryPaths[ids[j]])
	})
	for _, id := range ids {
		parentID := directoryParents[id]
		if parentID != 0 {
			total := directorySizes[parentID]
			mergeDirectorySize(&total, directorySizes[id])
			directorySizes[parentID] = total
		}
	}
	result.DirectorySizes = directorySizes
	return result, nil
}

func addDirectorySize(total *DirectorySize, node Node) {
	total.LogicalSize += node.LogicalSize
	total.AllocatedSize += node.AllocatedSize
	total.OwnedAllocated += node.OwnedAllocated
	total.Confidence = mergeConfidence(total.Confidence, node.Confidence)
}

func mergeDirectorySize(total *DirectorySize, child DirectorySize) {
	total.LogicalSize += child.LogicalSize
	total.AllocatedSize += child.AllocatedSize
	total.OwnedAllocated += child.OwnedAllocated
	total.Confidence = mergeConfidence(total.Confidence, child.Confidence)
}

func mergeConfidence(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	if left == "unknown" || right == "unknown" {
		return "unknown"
	}
	if left == "partial" || right == "partial" {
		return "partial"
	}
	if left == "estimated" || right == "estimated" {
		return "estimated"
	}
	return "exact"
}

func makeNode(id, parentID int64, path, name string, info fs.FileInfo, symlink bool) (Node, *syscall.Stat_t) {
	node := Node{
		ID:             id,
		ParentID:       parentID,
		Path:           path,
		Name:           name,
		Kind:           "file",
		LogicalSize:    info.Size(),
		AllocatedSize:  info.Size(),
		OwnedAllocated: info.Size(),
		Confidence:     "estimated",
		SizeBasis:      "logical_size",
		ModifiedAt:     info.ModTime(),
	}
	if info.IsDir() && !symlink {
		node.Kind = "directory"
		node.LogicalSize = 0
		node.AllocatedSize = 0
		node.OwnedAllocated = 0
		node.HasChildren = true
	}
	if symlink {
		node.Kind = "symlink"
	}
	stat, _ := info.Sys().(*syscall.Stat_t)
	if stat != nil {
		node.Device = uint64(stat.Dev)
		node.Inode = stat.Ino
	}
	if stat != nil && node.Kind != "directory" {
		allocated := int64(stat.Blocks) * 512
		if allocated > 0 {
			node.AllocatedSize = allocated
			node.OwnedAllocated = allocated
			node.Confidence = "exact"
			node.SizeBasis = "darwin_stat_blocks_512"
		}
	}
	return node, stat
}
