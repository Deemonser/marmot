package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"example.com/marmot/internal/domain/scan"
)

type Node = scan.Node
type Issue = scan.Issue
type Result = scan.Result
type DirectorySize = scan.DirectorySize
type Emit = scan.Emitter

const (
	unknownDeviceWorkers   = 2
	directoryQueueCapacity = 4096
)

type directoryTask struct {
	id   int64
	path string
}

type Scanner struct{}

func (Scanner) Scan(ctx context.Context, root string, emit scan.Emitter, phase scan.PhaseEmitter) (scan.Result, error) {
	return Scan(ctx, root, emit, phase)
}

func Scan(ctx context.Context, root string, emit Emit, phase scan.PhaseEmitter) (Result, error) {
	if emit == nil {
		emit = func(Node) error { return nil }
	}
	if phase == nil {
		phase = func(scan.Phase) error { return nil }
	}
	scanCtx, stop := context.WithCancel(ctx)
	defer stop()

	if err := phase(scan.PhaseCatalog); err != nil {
		return Result{}, err
	}
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return Result{}, err
	}
	if !rootInfo.IsDir() {
		return Result{}, fmt.Errorf("scan root is not a directory: %s", root)
	}

	result := Result{DirectorySizes: map[int64]DirectorySize{}}
	var stateMu sync.Mutex
	var nextID int64
	seen := make(map[[2]uint64]struct{})
	directoryParents := make(map[int64]int64)
	directoryPaths := make(map[int64]string)
	directorySizes := make(map[int64]DirectorySize)
	appendIssue := func(path string, issueErr error) {
		stateMu.Lock()
		result.Issues = append(result.Issues, Issue{Path: path, Message: issueErr.Error()})
		stateMu.Unlock()
	}

	emitNode := func(node Node, stat *syscall.Stat_t) (Node, error) {
		stateMu.Lock()
		defer stateMu.Unlock()
		nextID++
		node.ID = nextID
		if stat != nil && stat.Nlink > 1 && node.Kind == "file" {
			key := [2]uint64{uint64(stat.Dev), stat.Ino}
			if _, exists := seen[key]; exists {
				node.OwnedAllocated = 0
			} else {
				seen[key] = struct{}{}
			}
		}
		result.Nodes++
		if node.Kind == "directory" {
			result.Directories++
			directoryParents[node.ID] = node.ParentID
			directoryPaths[node.ID] = node.Path
			directorySizes[node.ID] = DirectorySize{Confidence: "exact"}
		} else {
			if node.Kind == "file" || node.Kind == "symlink" {
				result.Files++
			}
			result.Bytes += node.OwnedAllocated
			total := directorySizes[node.ParentID]
			addDirectorySize(&total, node)
			directorySizes[node.ParentID] = total
		}
		if err := emit(node); err != nil {
			return node, err
		}
		return node, nil
	}

	rootNode, rootStat := makeNode(0, 0, root, filepath.Base(root), rootInfo, false)
	rootNode.Kind = "directory"
	rootNode.HasChildren = true
	rootNode.LogicalSize = 0
	rootNode.AllocatedSize = 0
	rootNode.OwnedAllocated = 0
	rootNode, err = emitNode(rootNode, rootStat)
	if err != nil {
		return result, err
	}

	if err := phase(scan.PhaseVolumeOverview); err != nil {
		return result, err
	}

	readDirectory := func(task directoryTask) ([]directoryTask, error) {
		if err := scanCtx.Err(); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(task.path)
		if err != nil {
			appendIssue(task.path, err)
			return nil, nil
		}
		children := make([]directoryTask, 0)
		for _, entry := range entries {
			if err := scanCtx.Err(); err != nil {
				return nil, err
			}
			path := filepath.Join(task.path, entry.Name())
			info, infoErr := entry.Info()
			if infoErr != nil {
				appendIssue(path, infoErr)
				continue
			}
			node, stat := makeNode(0, task.id, path, entry.Name(), info, entry.Type()&os.ModeSymlink != 0)
			node, err = emitNode(node, stat)
			if err != nil {
				return nil, err
			}
			if node.Kind == "directory" {
				children = append(children, directoryTask{id: node.ID, path: path})
			}
		}
		return children, nil
	}

	topLevel, err := readDirectory(directoryTask{id: rootNode.ID, path: root})
	if err != nil {
		return result, err
	}
	if err := phase(scan.PhaseTopLevelPublish); err != nil {
		return result, err
	}
	if err := phase(scan.PhaseDeepScan); err != nil {
		return result, err
	}

	queue := make(chan directoryTask, directoryQueueCapacity)
	var pending sync.WaitGroup
	pending.Add(len(topLevel))
	var workers sync.WaitGroup
	var firstErrMu sync.Mutex
	var firstErr error
	reportError := func(err error) {
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		firstErrMu.Lock()
		if firstErr == nil {
			firstErr = err
			stop()
		}
		firstErrMu.Unlock()
	}
	enqueue := func(task directoryTask) error {
		pending.Add(1)
		select {
		case queue <- task:
			return nil
		case <-scanCtx.Done():
			pending.Done()
			return scanCtx.Err()
		}
	}

	for i := 0; i < unknownDeviceWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range queue {
				children, err := readDirectory(task)
				if err != nil {
					reportError(err)
				} else {
					for _, child := range children {
						if err := enqueue(child); err != nil {
							reportError(err)
							break
						}
					}
				}
				pending.Done()
			}
		}()
	}
	go func() {
		pending.Wait()
		close(queue)
	}()

	for _, task := range topLevel {
		select {
		case queue <- task:
		case <-scanCtx.Done():
			pending.Done()
		}
	}
	workers.Wait()
	firstErrMu.Lock()
	scanErr := firstErr
	firstErrMu.Unlock()
	if scanErr != nil {
		return result, scanErr
	}
	if err := scanCtx.Err(); err != nil {
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
	if err := phase(scan.PhaseFinalize); err != nil {
		return result, err
	}
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

func makeNode(id, parentID int64, path, name string, info os.FileInfo, symlink bool) (Node, *syscall.Stat_t) {
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
