package wails

import (
	"example.com/marmot/internal/application"
	"example.com/marmot/internal/domain/scan"
)

type Service struct {
	application *application.Service
}

func NewService(applicationService *application.Service) *Service {
	return &Service{application: applicationService}
}

type ScanOptions struct {
	Root string `json:"root"`
}

type ScanStatus struct {
	TaskID      string   `json:"taskId"`
	SnapshotID  int64    `json:"snapshotId"`
	Root        string   `json:"root"`
	State       string   `json:"state"`
	Phase       string   `json:"phase"`
	Nodes       int64    `json:"nodes"`
	Files       int64    `json:"files"`
	Directories int64    `json:"directories"`
	Bytes       int64    `json:"bytes"`
	Issues      []string `json:"issues"`
	Error       string   `json:"error"`
}

type ScanProgress struct {
	TaskID            string   `json:"taskId"`
	SnapshotID        int64    `json:"snapshotId"`
	Root              string   `json:"root"`
	State             string   `json:"state"`
	Phase             string   `json:"phase"`
	Nodes             int64    `json:"nodes"`
	Files             int64    `json:"files"`
	Directories       int64    `json:"directories"`
	Bytes             int64    `json:"bytes"`
	Issues            []string `json:"issues"`
	Error             string   `json:"error"`
	SnapshotVersion   int64    `json:"snapshotVersion"`
	AffectedParentIDs []int64  `json:"affectedParentIds"`
}

type PermissionStatus struct {
	Platform string `json:"platform"`
	State    string `json:"state"`
	Message  string `json:"message"`
}

type VolumeOverview struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
	Permission string `json:"permission"`
	Message    string `json:"message"`
	Scannable  bool   `json:"scannable"`
}

type ChildrenQuery struct {
	SnapshotID int64 `json:"snapshotId"`
	ParentID   int64 `json:"parentId"`
	Limit      int   `json:"limit"`
	Offset     int   `json:"offset"`
}

type NodeView struct {
	ID             int64  `json:"id"`
	ParentID       int64  `json:"parentId"`
	Path           string `json:"path"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	LogicalSize    int64  `json:"logicalSize"`
	AllocatedSize  int64  `json:"allocatedSize"`
	OwnedAllocated int64  `json:"ownedAllocated"`
	Confidence     string `json:"confidence"`
	SizeBasis      string `json:"sizeBasis"`
	HasChildren    bool   `json:"hasChildren"`
}

type ChildrenResult struct {
	Nodes  []NodeView `json:"nodes"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

type MapQuery struct {
	SnapshotID int64  `json:"snapshotId"`
	ParentID   int64  `json:"parentId"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	Measure    string `json:"measure"`
}

type MapEntry struct {
	Kind           string   `json:"kind"`
	Node           NodeView `json:"node"`
	Name           string   `json:"name"`
	Count          int64    `json:"count"`
	LogicalSize    int64    `json:"logicalSize"`
	AllocatedSize  int64    `json:"allocatedSize"`
	OwnedAllocated int64    `json:"ownedAllocated"`
	Confidence     string   `json:"confidence"`
	SizeBasis      string   `json:"sizeBasis"`
}

type MapResult struct {
	SnapshotID      int64      `json:"snapshotId"`
	SnapshotVersion int64      `json:"snapshotVersion"`
	Parent          NodeView   `json:"parent"`
	Entries         []MapEntry `json:"entries"`
	Total           int        `json:"total"`
	Limit           int        `json:"limit"`
	Offset          int        `json:"offset"`
	HasMore         bool       `json:"hasMore"`
	Remaining       MapEntry   `json:"remaining"`
	Confidence      string     `json:"confidence"`
}

type NodeActionResult struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
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

func (s *Service) GetPermissionStatus() PermissionStatus {
	status := s.application.GetPermissionStatus()
	return PermissionStatus{Platform: status.Platform, State: status.State, Message: status.Message}
}

func (s *Service) GetVolumes() ([]VolumeOverview, error) {
	volumes, err := s.application.GetVolumes()
	if err != nil {
		return nil, err
	}
	result := make([]VolumeOverview, 0, len(volumes))
	for _, volume := range volumes {
		result = append(result, VolumeOverview{ID: volume.ID, Name: volume.Name, Path: volume.Path, Kind: volume.Kind, TotalBytes: volume.TotalBytes, UsedBytes: volume.UsedBytes, FreeBytes: volume.FreeBytes, Permission: volume.Permission, Message: volume.Message, Scannable: volume.Scannable})
	}
	return result, nil
}

func (s *Service) StartScan(options ScanOptions) (ScanStatus, error) {
	status, err := s.application.StartScan(application.ScanOptions{Root: options.Root})
	return scanStatus(status), err
}

func (s *Service) GetScanStatus(taskID string) (ScanStatus, error) {
	status, err := s.application.GetScanStatus(taskID)
	return scanStatus(status), err
}

func (s *Service) CancelScan(taskID string) (ScanStatus, error) {
	status, err := s.application.CancelScan(taskID)
	return scanStatus(status), err
}

func (s *Service) GetChildren(query ChildrenQuery) (ChildrenResult, error) {
	result, err := s.application.GetChildren(application.ChildrenQuery{SnapshotID: query.SnapshotID, ParentID: query.ParentID, Limit: query.Limit, Offset: query.Offset})
	if err != nil {
		return ChildrenResult{}, err
	}
	view := ChildrenResult{Limit: result.Limit, Offset: result.Offset, Nodes: make([]NodeView, 0, len(result.Nodes))}
	for _, node := range result.Nodes {
		view.Nodes = append(view.Nodes, nodeView(node))
	}
	return view, nil
}

func (s *Service) GetMap(query MapQuery) (MapResult, error) {
	result, err := s.application.GetMap(application.MapQuery{SnapshotID: query.SnapshotID, ParentID: query.ParentID, Limit: query.Limit, Offset: query.Offset, Measure: query.Measure})
	if err != nil {
		return MapResult{}, err
	}
	entries := make([]MapEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, mapEntry(entry))
	}
	return MapResult{SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, Parent: nodeView(result.Parent), Entries: entries, Total: result.Total, Limit: result.Limit, Offset: result.Offset, HasMore: result.HasMore, Remaining: mapEntry(result.Remaining), Confidence: result.Confidence}, nil
}

func (s *Service) PreviewNode(snapshotID, nodeID int64) (NodeActionResult, error) {
	result, err := s.application.PreviewNode(snapshotID, nodeID)
	return NodeActionResult{OK: result.OK, Code: result.Code, Message: result.Message, Path: result.Path}, err
}

func (s *Service) RevealNode(snapshotID, nodeID int64) (NodeActionResult, error) {
	result, err := s.application.RevealNode(snapshotID, nodeID)
	return NodeActionResult{OK: result.OK, Code: result.Code, Message: result.Message, Path: result.Path}, err
}

func (s *Service) CreateCleanupPlan(request CleanupPlanRequest) (CleanupPlan, error) {
	plan, err := s.application.CreateCleanupPlan(application.CleanupPlanRequest{SnapshotID: request.SnapshotID, Paths: request.Paths})
	return cleanupPlan(plan), err
}

func (s *Service) ValidateCleanupPlan(planID string, version int64) (CleanupValidation, error) {
	validation, err := s.application.ValidateCleanupPlan(planID, version)
	if err != nil {
		return CleanupValidation{}, err
	}
	result := CleanupValidation{PlanID: validation.PlanID, Version: validation.Version, Valid: validation.Valid, Items: make([]CleanupItemValidation, 0, len(validation.Items))}
	for _, item := range validation.Items {
		result.Items = append(result.Items, CleanupItemValidation{Path: item.Path, Valid: item.Valid, Reason: item.Reason})
	}
	return result, nil
}

func (s *Service) ConfirmCleanupPlan(planID string, version int64) (CleanupPlan, error) {
	plan, err := s.application.ConfirmCleanupPlan(planID, version)
	return cleanupPlan(plan), err
}

func (s *Service) ExecuteCleanupPlan(planID string, version int64) (CleanupPlan, error) {
	plan, err := s.application.ExecuteCleanupPlan(planID, version)
	return cleanupPlan(plan), err
}

func scanStatus(status application.ScanStatus) ScanStatus {
	return ScanStatus{TaskID: status.TaskID, SnapshotID: status.SnapshotID, Root: status.Root, State: status.State, Phase: status.Phase, Nodes: status.Nodes, Files: status.Files, Directories: status.Directories, Bytes: status.Bytes, Issues: append([]string{}, status.Issues...), Error: status.Error}
}

func ScanProgressView(progress application.ScanProgress) ScanProgress {
	return ScanProgress{TaskID: progress.TaskID, SnapshotID: progress.SnapshotID, Root: progress.Root, State: progress.State, Phase: progress.Phase, Nodes: progress.Nodes, Files: progress.Files, Directories: progress.Directories, Bytes: progress.Bytes, Issues: append([]string{}, progress.Issues...), Error: progress.Error, SnapshotVersion: progress.SnapshotVersion, AffectedParentIDs: append([]int64{}, progress.AffectedParentIDs...)}
}

func nodeView(node scan.Node) NodeView {
	return NodeView{ID: node.ID, ParentID: node.ParentID, Path: node.Path, Name: node.Name, Kind: node.Kind, LogicalSize: node.LogicalSize, AllocatedSize: node.AllocatedSize, OwnedAllocated: node.OwnedAllocated, Confidence: node.Confidence, SizeBasis: node.SizeBasis, HasChildren: node.HasChildren}
}

func cleanupPlan(plan application.CleanupPlan) CleanupPlan {
	results := make([]CleanupItemResult, 0, len(plan.Results))
	for _, item := range plan.Results {
		results = append(results, CleanupItemResult{Path: item.Path, State: item.State, Reason: item.Reason})
	}
	return CleanupPlan{ID: plan.ID, SnapshotID: plan.SnapshotID, Version: plan.Version, State: plan.State, Items: plan.Items, Results: results}
}

func mapEntry(entry application.MapEntry) MapEntry {
	return MapEntry{Kind: entry.Kind, Node: nodeView(entry.Node), Name: entry.Name, Count: entry.Count, LogicalSize: entry.LogicalSize, AllocatedSize: entry.AllocatedSize, OwnedAllocated: entry.OwnedAllocated, Confidence: entry.Confidence, SizeBasis: entry.SizeBasis}
}
