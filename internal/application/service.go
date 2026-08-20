package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
)

const scanBatchSize = 10000

type Service struct {
	store       ports.SnapshotStore
	scanner     ports.Scanner
	files       ports.FileSystem
	permissions ports.PermissionProbe
	trash       ports.Trash
	mu          sync.RWMutex
	tasks       map[string]*scanTask
	plans       map[string]*cleanupPlan
	serial      atomic.Uint64
	emit        func(string, any)
}

type Dependencies struct {
	Store       ports.SnapshotStore
	Scanner     ports.Scanner
	FileSystem  ports.FileSystem
	Permissions ports.PermissionProbe
	Trash       ports.Trash
	Emit        func(string, any)
}

type scanTask struct {
	mu          sync.RWMutex
	taskID      string
	snapshotID  int64
	root        string
	state       string
	nodes       int64
	files       int64
	directories int64
	bytes       int64
	issues      []string
	error       string
	cancel      context.CancelFunc
}

type ScanOptions struct {
	Root string `json:"root"`
}

type ScanStatus struct {
	TaskID      string   `json:"taskId"`
	SnapshotID  int64    `json:"snapshotId"`
	Root        string   `json:"root"`
	State       string   `json:"state"`
	Nodes       int64    `json:"nodes"`
	Files       int64    `json:"files"`
	Directories int64    `json:"directories"`
	Bytes       int64    `json:"bytes"`
	Issues      []string `json:"issues"`
	Error       string   `json:"error"`
}

type ScanProgress struct {
	TaskID      string   `json:"taskId"`
	SnapshotID  int64    `json:"snapshotId"`
	Root        string   `json:"root"`
	State       string   `json:"state"`
	Nodes       int64    `json:"nodes"`
	Files       int64    `json:"files"`
	Directories int64    `json:"directories"`
	Bytes       int64    `json:"bytes"`
	Issues      []string `json:"issues"`
	Error       string   `json:"error"`
}

type PermissionStatus struct {
	Platform string `json:"platform"`
	State    string `json:"state"`
	Message  string `json:"message"`
}

type ChildrenQuery struct {
	SnapshotID int64 `json:"snapshotId"`
	ParentID   int64 `json:"parentId"`
	Limit      int   `json:"limit"`
	Offset     int   `json:"offset"`
}

type ChildrenResult struct {
	Nodes  []scan.Node `json:"nodes"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

type CleanupPlanRequest struct {
	SnapshotID int64    `json:"snapshotId"`
	Paths      []string `json:"paths"`
}

type CleanupPlan struct {
	ID         string              `json:"id"`
	SnapshotID int64               `json:"snapshotId"`
	Version    int64               `json:"version"`
	State      string              `json:"state"`
	Items      int                 `json:"items"`
	Results    []CleanupItemResult `json:"results"`
}

type CleanupItemResult struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type CleanupItemValidation struct {
	Path   string `json:"path"`
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

type CleanupValidation struct {
	PlanID  string                  `json:"planId"`
	Version int64                   `json:"version"`
	Valid   bool                    `json:"valid"`
	Items   []CleanupItemValidation `json:"items"`
}

type cleanupPlan struct {
	mu sync.Mutex
	CleanupPlan
	items []cleanup.Item
}

func NewService(deps Dependencies) *Service {
	emit := deps.Emit
	if emit == nil {
		emit = func(string, any) {}
	}
	return &Service{store: deps.Store, scanner: deps.Scanner, files: deps.FileSystem, permissions: deps.Permissions, trash: deps.Trash, tasks: make(map[string]*scanTask), plans: make(map[string]*cleanupPlan), emit: emit}
}

func (s *Service) RecoverInterruptedScans() error {
	return s.store.MarkRunningInterrupted()
}

func (s *Service) GetPermissionStatus() PermissionStatus {
	report := s.permissions.Probe()
	return PermissionStatus{Platform: report.Platform, State: report.State, Message: report.Message}
}

func (s *Service) StartScan(options ScanOptions) (ScanStatus, error) {
	root, err := s.files.NormalizeScanRoot(options.Root)
	if err != nil {
		return ScanStatus{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	taskID := fmt.Sprintf("scan-%d-%d", time.Now().UnixNano(), s.serial.Add(1))
	snapshotID, err := s.store.CreateSnapshot(taskID, root)
	if err != nil {
		cancel()
		return ScanStatus{}, err
	}
	task := &scanTask{taskID: taskID, snapshotID: snapshotID, root: root, state: "running", cancel: cancel}
	s.mu.Lock()
	s.tasks[taskID] = task
	s.mu.Unlock()
	go s.runScan(ctx, task)
	return task.status(), nil
}

func (s *Service) runScan(ctx context.Context, task *scanTask) {
	var batch []scan.Node
	var pendingNodes, pendingFiles, pendingDirectories, pendingBytes int64
	parents := make(map[int64]int64)
	committedDirectorySizes := make(map[int64]scan.DirectorySize)
	pendingDirectorySizes := make(map[int64]scan.DirectorySize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.store.InsertNodes(task.snapshotID, batch); err != nil {
			return err
		}
		batch = batch[:0]
		pendingNodes = 0
		pendingFiles = 0
		pendingDirectories = 0
		pendingBytes = 0

		changed := make(map[int64]scan.DirectorySize, len(pendingDirectorySizes))
		for nodeID, delta := range pendingDirectorySizes {
			total := committedDirectorySizes[nodeID]
			total.LogicalSize += delta.LogicalSize
			total.AllocatedSize += delta.AllocatedSize
			total.OwnedAllocated += delta.OwnedAllocated
			total.Confidence = "partial"
			total.SizeBasis = "descendant_sum_v1_partial"
			committedDirectorySizes[nodeID] = total
			changed[nodeID] = total
		}
		pendingDirectorySizes = make(map[int64]scan.DirectorySize)
		if len(changed) > 0 {
			return s.store.UpdateDirectorySizes(task.snapshotID, changed)
		}
		return nil
	}
	lastProgress := time.Time{}
	result, scanErr := s.scanner.Scan(ctx, task.root, func(node scan.Node) error {
		batch = append(batch, node)
		parents[node.ID] = node.ParentID
		if node.Kind == "directory" {
			pendingDirectorySizes[node.ID] = scan.DirectorySize{Confidence: "partial", SizeBasis: "descendant_sum_v1_partial"}
		} else {
			for parentID := node.ParentID; parentID != 0; parentID = parents[parentID] {
				total := pendingDirectorySizes[parentID]
				total.LogicalSize += node.LogicalSize
				total.AllocatedSize += node.AllocatedSize
				total.OwnedAllocated += node.OwnedAllocated
				total.Confidence = "partial"
				total.SizeBasis = "descendant_sum_v1_partial"
				pendingDirectorySizes[parentID] = total
			}
		}
		task.mu.Lock()
		task.nodes++
		if node.Kind == "file" || node.Kind == "symlink" {
			task.files++
		}
		if node.Kind == "directory" {
			task.directories++
			pendingDirectories++
		}
		task.bytes += node.OwnedAllocated
		task.mu.Unlock()
		pendingNodes++
		if node.Kind == "file" || node.Kind == "symlink" {
			pendingFiles++
		}
		pendingBytes += node.OwnedAllocated
		if len(batch) >= scanBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
		if time.Since(lastProgress) > 100*time.Millisecond {
			s.emitProgress(task)
			lastProgress = time.Now()
		}
		return nil
	})
	if scanErr == nil {
		if err := flush(); err != nil {
			scanErr = err
		}
	}
	if scanErr != nil && (pendingNodes > 0 || pendingFiles > 0 || pendingDirectories > 0 || pendingBytes > 0) {
		task.mu.Lock()
		task.nodes -= pendingNodes
		task.files -= pendingFiles
		task.directories -= pendingDirectories
		task.bytes -= pendingBytes
		task.mu.Unlock()
	}
	if scanErr == nil && len(result.DirectorySizes) > 0 {
		directorySizes := make(map[int64]scan.DirectorySize, len(result.DirectorySizes))
		for nodeID, size := range result.DirectorySizes {
			confidence := size.Confidence
			basis := "descendant_sum_v1"
			if len(result.Issues) > 0 {
				confidence = "partial"
				basis = "descendant_sum_v1_partial"
			}
			directorySizes[nodeID] = scan.DirectorySize{LogicalSize: size.LogicalSize, AllocatedSize: size.AllocatedSize, OwnedAllocated: size.OwnedAllocated, Confidence: confidence, SizeBasis: basis}
		}
		if err := s.store.UpdateDirectorySizes(task.snapshotID, directorySizes); err != nil {
			scanErr = err
		}
	}
	for _, issue := range result.Issues {
		task.mu.Lock()
		task.issues = append(task.issues, issue.Path+": "+issue.Message)
		task.mu.Unlock()
	}
	state := "completed"
	failure := ""
	if errors.Is(scanErr, context.Canceled) {
		state = "cancelled"
	} else if scanErr != nil {
		state = "failed"
		failure = scanErr.Error()
		task.mu.Lock()
		task.error = failure
		task.mu.Unlock()
	}
	if len(result.Issues) > 0 && state == "completed" {
		state = "completed_with_issues"
	}
	task.mu.Lock()
	task.state = state
	status := task.statusLocked()
	task.mu.Unlock()
	_ = s.store.FinishSnapshot(task.snapshotID, state, failure, status.Nodes, status.Files, status.Directories, status.Bytes, int64(len(result.Issues)))
	s.emitProgress(task)
}

func (s *Service) GetScanStatus(taskID string) (ScanStatus, error) {
	s.mu.RLock()
	task := s.tasks[taskID]
	s.mu.RUnlock()
	if task == nil {
		snapshot, err := s.store.SnapshotByTaskID(taskID)
		if err != nil {
			return ScanStatus{}, fmt.Errorf("scan task not found: %s", taskID)
		}
		issues := []string(nil)
		if snapshot.Issues > 0 {
			issues = []string{fmt.Sprintf("%d scan issues preserved", snapshot.Issues)}
		}
		return ScanStatus{TaskID: snapshot.TaskID, SnapshotID: snapshot.ID, Root: snapshot.Root, State: snapshot.State, Nodes: snapshot.NodeCount, Files: snapshot.FileCount, Directories: snapshot.DirCount, Bytes: snapshot.Bytes, Issues: issues, Error: snapshot.Error}, nil
	}
	return task.status(), nil
}

func (s *Service) CancelScan(taskID string) (ScanStatus, error) {
	s.mu.RLock()
	task := s.tasks[taskID]
	s.mu.RUnlock()
	if task == nil {
		return ScanStatus{}, fmt.Errorf("scan task not found: %s", taskID)
	}
	task.cancel()
	return task.status(), nil
}

func (s *Service) GetChildren(query ChildrenQuery) (ChildrenResult, error) {
	if query.Limit <= 0 || query.Limit > 1000 {
		query.Limit = 1000
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	nodes, err := s.store.Children(query.SnapshotID, query.ParentID, query.Limit, query.Offset)
	if err != nil {
		return ChildrenResult{}, err
	}
	return ChildrenResult{Limit: query.Limit, Offset: query.Offset, Nodes: nodes}, nil
}

func (s *Service) CreateCleanupPlan(request CleanupPlanRequest) (CleanupPlan, error) {
	if request.SnapshotID <= 0 || len(request.Paths) == 0 {
		return CleanupPlan{}, errors.New("snapshot and paths are required")
	}
	paths := make([]string, 0, len(request.Paths))
	seenPaths := make(map[string]struct{}, len(request.Paths))
	for _, rawPath := range request.Paths {
		path, err := cleanup.NormalizePath(rawPath)
		if err != nil {
			return CleanupPlan{}, err
		}
		if _, exists := seenPaths[path]; exists {
			return CleanupPlan{}, errors.New("duplicate cleanup items are not allowed")
		}
		seenPaths[path] = struct{}{}
		paths = append(paths, path)
	}
	if cleanup.HasOverlappingPaths(paths) {
		return CleanupPlan{}, errors.New("parent and child cleanup items cannot overlap")
	}
	items := make([]cleanup.Item, 0, len(paths))
	for _, path := range paths {
		node, err := s.store.NodeByPath(request.SnapshotID, path)
		if err != nil {
			return CleanupPlan{}, fmt.Errorf("cleanup path is not in snapshot: %s: %w", path, err)
		}
		if node.ParentID == 0 {
			return CleanupPlan{}, errors.New("scan roots cannot be cleaned")
		}
		item, err := s.files.CaptureCleanupItem(path)
		if err != nil {
			return CleanupPlan{}, err
		}
		if !matchesSnapshotNode(node, item) {
			return CleanupPlan{}, fmt.Errorf("cleanup path changed since scan: %s", path)
		}
		items = append(items, item)
	}
	plan := &cleanupPlan{CleanupPlan: CleanupPlan{ID: fmt.Sprintf("plan-%d-%d", time.Now().UnixNano(), s.serial.Add(1)), SnapshotID: request.SnapshotID, Version: 1, State: "draft", Items: len(items), Results: make([]CleanupItemResult, 0, len(items))}, items: items}
	s.mu.Lock()
	s.plans[plan.ID] = plan
	s.mu.Unlock()
	return plan.CleanupPlan, nil
}

func (s *Service) ValidateCleanupPlan(planID string, version int64) (CleanupValidation, error) {
	plan, err := s.getPlan(planID, version)
	if err != nil {
		return CleanupValidation{}, err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	return s.validatePlanLocked(plan), nil
}

func (s *Service) ConfirmCleanupPlan(planID string, version int64) (CleanupPlan, error) {
	plan, err := s.getPlan(planID, version)
	if err != nil {
		return CleanupPlan{}, err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.State != "draft" && plan.State != "validated" {
		return CleanupPlan{}, errors.New("cleanup plan cannot be confirmed in its current state")
	}
	validation := s.validatePlanLocked(plan)
	if !validation.Valid {
		return CleanupPlan{}, errors.New("cleanup plan is invalid")
	}
	plan.State = "confirmed"
	return plan.publicLocked(), nil
}

func (s *Service) ExecuteCleanupPlan(planID string, version int64) (CleanupPlan, error) {
	plan, err := s.getPlan(planID, version)
	if err != nil {
		return CleanupPlan{}, err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.State != "confirmed" {
		return CleanupPlan{}, errors.New("cleanup plan must be confirmed")
	}
	plan.Results = plan.Results[:0]
	failed := false
	for _, item := range plan.items {
		if valid, reason := s.validateCleanupItem(item); !valid {
			failed = true
			plan.Results = append(plan.Results, CleanupItemResult{Path: item.Path, State: "skipped", Reason: reason})
			continue
		}
		if _, err := s.trash.Trash(item.Path); err != nil {
			failed = true
			plan.Results = append(plan.Results, CleanupItemResult{Path: item.Path, State: "failed", Reason: err.Error()})
			continue
		}
		plan.Results = append(plan.Results, CleanupItemResult{Path: item.Path, State: "applied", Reason: "moved to Trash"})
	}
	if failed {
		plan.State = "failed"
	} else {
		plan.State = "applied"
	}
	return plan.publicLocked(), nil
}

func (s *Service) emitProgress(task *scanTask) {
	status := task.status()
	s.emit("scan-progress", ScanProgress{TaskID: status.TaskID, SnapshotID: status.SnapshotID, Root: status.Root, State: status.State, Nodes: status.Nodes, Files: status.Files, Directories: status.Directories, Bytes: status.Bytes, Issues: status.Issues, Error: status.Error})
}

func (t *scanTask) status() ScanStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.statusLocked()
}

func (t *scanTask) statusLocked() ScanStatus {
	return ScanStatus{TaskID: t.taskID, SnapshotID: t.snapshotID, Root: t.root, State: t.state, Nodes: t.nodes, Files: t.files, Directories: t.directories, Bytes: t.bytes, Issues: append([]string(nil), t.issues...), Error: t.error}
}

func matchesSnapshotNode(node scan.Node, item cleanup.Item) bool {
	if node.Path != item.Path || node.Kind != item.Kind || node.Device != item.Device || node.Inode != item.Inode || !node.ModifiedAt.Equal(item.Modified) {
		return false
	}
	return item.Kind == "directory" || node.LogicalSize == item.Size
}

func (s *Service) validateCleanupItem(item cleanup.Item) (bool, string) {
	current, err := s.files.CaptureCleanupItem(item.Path)
	if err != nil {
		return false, err.Error()
	}
	if current.Device != item.Device || current.Inode != item.Inode {
		return false, "file identity changed"
	}
	if current.Size != item.Size || current.Mode != item.Mode || !current.Modified.Equal(item.Modified) {
		return false, "file metadata changed"
	}
	return true, "ready"
}

func (s *Service) getPlan(planID string, version int64) (*cleanupPlan, error) {
	s.mu.RLock()
	plan := s.plans[planID]
	s.mu.RUnlock()
	if plan == nil || plan.Version != version {
		return nil, errors.New("cleanup plan not found or version mismatch")
	}
	return plan, nil
}

func (s *Service) validatePlanLocked(plan *cleanupPlan) CleanupValidation {
	validation := CleanupValidation{PlanID: plan.ID, Version: plan.Version, Valid: true, Items: make([]CleanupItemValidation, 0, len(plan.items))}
	for _, item := range plan.items {
		valid, reason := s.validateCleanupItem(item)
		validation.Items = append(validation.Items, CleanupItemValidation{Path: item.Path, Valid: valid, Reason: reason})
		if !valid {
			validation.Valid = false
		}
	}
	if validation.Valid && plan.State == "draft" {
		plan.State = "validated"
	}
	return validation
}

func (plan *cleanupPlan) publicLocked() CleanupPlan {
	public := plan.CleanupPlan
	public.Results = append([]CleanupItemResult(nil), plan.Results...)
	return public
}
