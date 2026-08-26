package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
)

type Node = scan.Node
type Issue = scan.Issue
type Result = scan.Result
type DirectorySize = scan.DirectorySize
type Emit = scan.Emitter

const (
	directoryQueueCapacity  = 4096
	directoryFDLimit        = 2048
	ssdDeviceWorkers        = 8
	rotationalDeviceWorkers = 1
	networkDeviceWorkers    = 2
	unknownDeviceWorkers    = 2
)

type directoryTask struct {
	id     int64
	path   string
	fd     int
	fdHeld bool
}

type nodeToEmit struct {
	node      Node
	linkCount uint64
}

type MountResolver func() ([]ports.Mount, error)

type Scanner struct {
	MountResolver MountResolver
}

type mountBoundary struct {
	path   string
	prefix string
}

type mountBoundaries []mountBoundary

func (s Scanner) Scan(ctx context.Context, root string, emit scan.Emitter, phase scan.PhaseEmitter) (scan.Result, error) {
	if emit == nil {
		emit = func(Node) error { return nil }
	}
	return s.ScanBatched(ctx, root, func(nodes []Node) error {
		for _, node := range nodes {
			if err := emit(node); err != nil {
				return err
			}
		}
		return nil
	}, phase)
}

func (s Scanner) ScanBatched(ctx context.Context, root string, emit scan.BatchEmitter, phase scan.PhaseEmitter) (scan.Result, error) {
	if emit == nil {
		emit = func([]Node) error { return nil }
	}
	if s.MountResolver != nil {
		return scanConfiguredTree(ctx, root, emit, phase, s.MountResolver)
	}
	return scanTree(ctx, root, func(node Node) error { return emit([]Node{node}) }, phase, s.MountResolver)
}

func Scan(ctx context.Context, root string, emit Emit, phase scan.PhaseEmitter) (Result, error) {
	return scanTree(ctx, root, emit, phase, nil)
}

func scanTree(ctx context.Context, root string, emit Emit, phase scan.PhaseEmitter, resolveMounts MountResolver) (Result, error) {
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
	var mounts []ports.Mount
	if resolveMounts != nil {
		mounts, err = resolveMounts()
		if err != nil {
			return Result{}, fmt.Errorf("resolve mount boundaries: %w", err)
		}
	}
	volumeID, deviceProfile := mountForRoot(root, mounts)
	boundaries := newMountBoundaries(root, mounts)
	fdSlots := make(chan struct{}, directoryFDLimit)
	acquireFD := func() bool {
		select {
		case fdSlots <- struct{}{}:
			return true
		default:
			return false
		}
	}
	releaseTaskFD := func(task directoryTask) {
		if !task.fdHeld {
			return
		}
		closeDirectoryFD(task.fd)
		<-fdSlots
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

	emitNodes := func(items []nodeToEmit) ([]Node, error) {
		stateMu.Lock()
		defer stateMu.Unlock()
		emitted := make([]Node, 0, len(items))
		for _, item := range items {
			node := item.node
			nextID++
			node.ID = nextID
			if item.linkCount > 1 && node.Kind == "file" {
				key := [2]uint64{node.Device, node.Inode}
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
				return emitted, err
			}
			emitted = append(emitted, node)
		}
		return emitted, nil
	}
	emitNode := func(node Node, linkCount uint64) (Node, error) {
		emitted, err := emitNodes([]nodeToEmit{{node: node, linkCount: linkCount}})
		if len(emitted) == 0 {
			return node, err
		}
		return emitted[0], err
	}

	rootNode := directoryEntryFromFileInfo(filepath.Base(root), rootInfo, false).node(0, 0, root, volumeID)
	rootNode.Kind = "directory"
	rootNode.HasChildren = true
	rootNode.LogicalSize = 0
	rootNode.AllocatedSize = 0
	rootNode.OwnedAllocated = 0
	rootNode, err = emitNode(rootNode, 0)
	if err != nil {
		return result, err
	}

	if err := phase(scan.PhaseVolumeOverview); err != nil {
		return result, err
	}

	readDirectory := func(task directoryTask) ([]directoryTask, error) {
		defer releaseTaskFD(task)
		if err := scanCtx.Err(); err != nil {
			return nil, err
		}
		if boundaries.contains(task.path) {
			return nil, nil
		}
		fd := -1
		if task.fdHeld {
			fd = task.fd
		}
		entries, err := listDirectoryEntries(task.path, fd)
		if err != nil {
			appendIssue(task.path, err)
			return nil, nil
		}
		items := make([]nodeToEmit, 0, len(entries))
		for _, entry := range entries {
			if err := scanCtx.Err(); err != nil {
				return nil, err
			}
			path := filepath.Join(task.path, entry.name)
			if entry.mountPoint || boundaries.contains(path) {
				continue
			}
			if entry.readError != nil {
				appendIssue(path, entry.readError)
				continue
			}
			items = append(items, nodeToEmit{node: entry.node(0, task.id, path, volumeID), linkCount: entry.linkCount})
		}
		nodes, err := emitNodes(items)
		if err != nil {
			return nil, err
		}
		children := make([]directoryTask, 0, len(nodes))
		for _, node := range nodes {
			if node.Kind == "directory" {
				child := directoryTask{id: node.ID, path: node.Path}
				if acquireFD() {
					var childFD int
					var childErr error
					if task.fdHeld {
						childFD, childErr = openDirectoryAt(task.fd, node.Name)
					} else {
						childFD, childErr = openDirectoryPath(node.Path)
					}
					if childErr == nil {
						child.fd = childFD
						child.fdHeld = true
					} else {
						<-fdSlots
					}
				}
				children = append(children, child)
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
	workerCount := workersForProfile(deviceProfile)
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range queue {
				processDirectory(task, scanCtx, queue, &pending, readDirectory, releaseTaskFD, reportError)
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
			releaseTaskFD(task)
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

// processDirectory keeps the global queue bounded without allowing a full queue
// to deadlock every worker. Work that cannot be queued is walked by the current
// worker using a local stack.
func processDirectory(
	initial directoryTask,
	ctx context.Context,
	queue chan<- directoryTask,
	pending *sync.WaitGroup,
	read func(directoryTask) ([]directoryTask, error),
	release func(directoryTask),
	reportError func(error),
) {
	local := []directoryTask{initial}
	defer func() {
		for _, task := range local {
			release(task)
		}
	}()
	for len(local) > 0 {
		last := len(local) - 1
		task := local[last]
		local = local[:last]
		if err := ctx.Err(); err != nil {
			release(task)
			return
		}
		children, err := read(task)
		if err != nil {
			reportError(err)
			continue
		}
		for index, child := range children {
			if err := ctx.Err(); err != nil {
				release(child)
				for _, remaining := range children[index+1:] {
					release(remaining)
				}
				return
			}
			pending.Add(1)
			select {
			case queue <- child:
				continue
			case <-ctx.Done():
				pending.Done()
				release(child)
				for _, remaining := range children[index+1:] {
					release(remaining)
				}
				return
			default:
				pending.Done()
				local = append(local, child)
			}
		}
	}
}

func workersForProfile(profile scan.DeviceProfile) int {
	switch profile {
	case scan.DeviceProfileSSD:
		return ssdDeviceWorkers
	case scan.DeviceProfileRotational:
		return rotationalDeviceWorkers
	case scan.DeviceProfileNetworkOrVirtual:
		return networkDeviceWorkers
	default:
		return unknownDeviceWorkers
	}
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

func mountForRoot(root string, mounts []ports.Mount) (string, scan.DeviceProfile) {
	best := ""
	profile := scan.DeviceProfileUnknown
	bestLength := -1
	for _, mount := range mounts {
		mountPath := filepath.Clean(mount.Path)
		if !pathWithin(mountPath, root) || len(mountPath) <= bestLength {
			continue
		}
		best = mount.ID
		profile = mount.DeviceProfile
		bestLength = len(mountPath)
	}
	if best != "" {
		return best, profile
	}
	return "mount:" + filepath.Clean(root), profile
}

func newMountBoundaries(root string, mounts []ports.Mount) mountBoundaries {
	root = filepath.Clean(root)
	boundaries := make(mountBoundaries, 0, len(mounts))
	for _, mount := range mounts {
		mountPath := filepath.Clean(mount.Path)
		if mountPath == root {
			continue
		}
		if pathWithin(root, mountPath) {
			boundaries = append(boundaries, mountBoundary{path: mountPath, prefix: mountPath + string(filepath.Separator)})
		}
	}
	return boundaries
}

func (boundaries mountBoundaries) contains(path string) bool {
	for _, boundary := range boundaries {
		if path == boundary.path || strings.HasPrefix(path, boundary.prefix) {
			return true
		}
	}
	return false
}

func pathWithin(base, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
