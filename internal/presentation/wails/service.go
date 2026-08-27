package wails

import (
	"encoding/json"

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
	// CountedBytes and VolumeUsedBytes are the progress bar's numerator and
	// denominator (ADR-0053 §1); Bytes stays the walked total.
	CountedBytes    int64  `json:"countedBytes"`
	VolumeUsedBytes uint64 `json:"volumeUsedBytes"`
}

// ProjectedEntry is one arc below the current level. It carries only what the
// space map draws with; it has no path and no capabilities (ADR-0048).
type ProjectedEntry struct {
	NodeID          int64            `json:"id"`
	Name            string           `json:"name"`
	Kind            string           `json:"kind"`
	OwnedAllocated  int64            `json:"size"`
	Children        []ProjectedEntry `json:"children,omitempty"`
	ChildrenTotal   int              `json:"total,omitempty"`
	ChildrenHasMore bool             `json:"more,omitempty"`
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
	// CountedBytes and VolumeUsedBytes are the progress bar's numerator and
	// denominator (ADR-0053 §1); Bytes stays the walked total.
	CountedBytes    int64  `json:"countedBytes"`
	VolumeUsedBytes uint64 `json:"volumeUsedBytes"`
}

type PermissionStatus struct {
	Platform string `json:"platform"`
	State    string `json:"state"`
	Message  string `json:"message"`
}

type StorageVolumeMember struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Path             string `json:"path"`
	Kind             string `json:"kind"`
	Role             string `json:"role"`
	VolumeTotalBytes uint64 `json:"volumeTotalBytes"`
	VolumeUsedBytes  uint64 `json:"volumeUsedBytes"`
	VolumeFreeBytes  uint64 `json:"volumeFreeBytes"`
	UsageBasis       string `json:"usageBasis"`
	Permission       string `json:"permission"`
	Scannable        bool   `json:"scannable"`
}

type StorageSourceOverview struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Path       string                `json:"path"`
	Kind       string                `json:"kind"`
	TotalBytes uint64                `json:"totalBytes"`
	UsedBytes  uint64                `json:"usedBytes"`
	FreeBytes  uint64                `json:"freeBytes"`
	UsageBasis string                `json:"usageBasis"`
	Permission string                `json:"permission"`
	Message    string                `json:"message"`
	Scannable  bool                  `json:"scannable"`
	Members    []StorageVolumeMember `json:"members"`
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
	VolumeID       string `json:"volumeId"`
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
	SnapshotID      int64     `json:"snapshotId"`
	ParentID        int64     `json:"parentId"`
	Limit           int       `json:"limit"`
	Offset          int       `json:"offset"`
	Measure         string    `json:"measure"`
	Depth           int       `json:"depth"`
	ProjectionLimit int       `json:"projectionLimit"`
	MinSweeps       []float64 `json:"minSweeps"`
}

type MapEntry struct {
	Kind           string   `json:"kind"`
	Node           NodeView `json:"node"`
	Name           string   `json:"name"`
	VirtualType    string   `json:"virtualType"`
	DisplayState   string   `json:"displayState"`
	Capabilities   []string `json:"capabilities"`
	Count          int64    `json:"count"`
	LogicalSize    int64    `json:"logicalSize"`
	AllocatedSize  int64    `json:"allocatedSize"`
	OwnedAllocated int64    `json:"ownedAllocated"`
	Confidence     string   `json:"confidence"`
	SizeBasis      string   `json:"sizeBasis"`
	// Why this object may not be deleted, or empty when it may (ADR-0015: the
	// reason is the application's to state, not the frontend's to guess).
	Protection      string           `json:"protection,omitempty"`
	Children        []ProjectedEntry `json:"children,omitempty"`
	ChildrenTotal   int              `json:"childrenTotal,omitempty"`
	ChildrenHasMore bool             `json:"childrenHasMore,omitempty"`
}

type MapResult struct {
	SnapshotID       int64      `json:"snapshotId"`
	SnapshotVersion  int64      `json:"snapshotVersion"`
	Parent           NodeView   `json:"parent"`
	Entries          []MapEntry `json:"entries"`
	Total            int        `json:"total"`
	Limit            int        `json:"limit"`
	Offset           int        `json:"offset"`
	HasMore          bool       `json:"hasMore"`
	Remaining        MapEntry   `json:"remaining"`
	Confidence       string     `json:"confidence"`
	VolumeTotalBytes uint64     `json:"volumeTotalBytes"`
	VolumeUsedBytes  uint64     `json:"volumeUsedBytes"`
	VolumeFreeBytes  uint64     `json:"volumeFreeBytes"`
	DensityTruncated bool       `json:"densityTruncated"`
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

func (s *Service) GetStorageSources() ([]StorageSourceOverview, error) {
	sources, err := s.application.GetStorageSources()
	if err != nil {
		return nil, err
	}
	result := make([]StorageSourceOverview, 0, len(sources))
	for _, source := range sources {
		members := make([]StorageVolumeMember, 0, len(source.Members))
		for _, member := range source.Members {
			members = append(members, StorageVolumeMember{
				ID:               member.ID,
				Name:             member.Name,
				Path:             member.Path,
				Kind:             member.Kind,
				Role:             member.Role,
				VolumeTotalBytes: member.VolumeTotalBytes,
				VolumeUsedBytes:  member.VolumeUsedBytes,
				VolumeFreeBytes:  member.VolumeFreeBytes,
				UsageBasis:       member.UsageBasis,
				Permission:       member.Permission,
				Scannable:        member.Scannable,
			})
		}
		result = append(result, StorageSourceOverview{
			ID:         source.ID,
			Name:       source.Name,
			Path:       source.Path,
			Kind:       source.Kind,
			TotalBytes: source.TotalBytes,
			UsedBytes:  source.UsedBytes,
			FreeBytes:  source.FreeBytes,
			UsageBasis: source.UsageBasis,
			Permission: source.Permission,
			Message:    source.Message,
			Scannable:  source.Scannable,
			Members:    members,
		})
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
	result, err := s.application.GetMap(application.MapQuery{SnapshotID: query.SnapshotID, ParentID: query.ParentID, Limit: query.Limit, Offset: query.Offset, Measure: query.Measure, Depth: query.Depth, ProjectionLimit: query.ProjectionLimit, MinSweeps: query.MinSweeps})
	if err != nil {
		return MapResult{}, err
	}
	entries := make([]MapEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, mapEntry(entry))
	}
	return trimMapPayload(MapResult{SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, Parent: nodeView(result.Parent), Entries: entries, Total: result.Total, Limit: result.Limit, Offset: result.Offset, HasMore: result.HasMore, Remaining: mapEntry(result.Remaining), Confidence: result.Confidence, VolumeTotalBytes: result.VolumeTotalBytes, VolumeUsedBytes: result.VolumeUsedBytes, VolumeFreeBytes: result.VolumeFreeBytes, DensityTruncated: result.DensityTruncated}), nil
}

const maxMapPayloadBytes = 256 * 1024

func trimMapPayload(result MapResult) MapResult {
	encoded, err := json.Marshal(result)
	if err == nil && len(encoded) <= maxMapPayloadBytes {
		return result
	}
	result.DensityTruncated = true
	for index := range result.Entries {
		if len(result.Entries[index].Children) == 0 {
			continue
		}
		result.Entries[index].Children = nil
		result.Entries[index].ChildrenHasMore = true
	}
	encoded, err = json.Marshal(result)
	if err == nil && len(encoded) <= maxMapPayloadBytes {
		return result
	}

	realEntries := make([]MapEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		if entry.Kind != "aggregate" {
			realEntries = append(realEntries, entry)
		}
	}
	low, high, best := 0, len(realEntries), -1
	for low <= high {
		middle := (low + high) / 2
		candidate := compactMapResult(result, realEntries, middle)
		encoded, err := json.Marshal(candidate)
		if err == nil && len(encoded) <= maxMapPayloadBytes {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if best >= 0 {
		return compactMapResult(result, realEntries, best)
	}
	result.Entries = nil
	return result
}

func compactMapResult(result MapResult, entries []MapEntry, keep int) MapResult {
	candidate := result
	candidate.Entries = append([]MapEntry(nil), entries[:keep]...)
	omitted := append([]MapEntry(nil), entries[keep:]...)
	if result.Remaining.Count > 0 || len(omitted) > 0 {
		tail := append(omitted, result.Remaining)
		aggregate := aggregateMapEntries(tail)
		candidate.Entries = append(candidate.Entries, aggregate)
		candidate.Remaining = aggregate
		candidate.HasMore = true
	}
	return candidate
}

func aggregateMapEntries(entries []MapEntry) MapEntry {
	aggregate := MapEntry{Kind: "aggregate", Name: "较小对象", VirtualType: "smaller_objects", DisplayState: "partial", Capabilities: []string{"enter"}, Confidence: "estimated", SizeBasis: "map_payload_trim_v1"}
	for _, entry := range entries {
		if entry.Kind == "aggregate" && entry.Count == 0 {
			continue
		}
		aggregate.Count += entry.Count
		aggregate.LogicalSize += entry.LogicalSize
		aggregate.AllocatedSize += entry.AllocatedSize
		aggregate.OwnedAllocated += entry.OwnedAllocated
		if entry.Kind == "node" {
			aggregate.Count++
		}
	}
	return aggregate
}

// GetNodeEntry is how the frontend collects an arc from a ring below the current
// level: the projection it drew that arc from carries no path and no
// capabilities, so the node has to be looked up by ID before anything may act on
// it (ADR-0048).
func (s *Service) GetNodeEntry(snapshotID, nodeID int64) (MapEntry, error) {
	entry, err := s.application.GetNodeEntry(snapshotID, nodeID)
	if err != nil {
		return MapEntry{}, err
	}
	return mapEntry(entry), nil
}

func (s *Service) PreviewNode(snapshotID, nodeID int64) (NodeActionResult, error) {
	result, err := s.application.PreviewNode(snapshotID, nodeID)
	return NodeActionResult{OK: result.OK, Code: result.Code, Message: result.Message, Path: result.Path}, err
}

func (s *Service) RevealNode(snapshotID, nodeID int64) (NodeActionResult, error) {
	result, err := s.application.RevealNode(snapshotID, nodeID)
	return NodeActionResult{OK: result.OK, Code: result.Code, Message: result.Message, Path: result.Path}, err
}

// RevealStorageSource takes the source's identity, not a path: the path comes
// from the volume catalog (ADR-0051 §5, DDD invariant 17).
func (s *Service) RevealStorageSource(sourceID string) (NodeActionResult, error) {
	result, err := s.application.RevealStorageSource(sourceID)
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
	return ScanStatus{TaskID: status.TaskID, SnapshotID: status.SnapshotID, Root: status.Root, State: status.State, Phase: status.Phase, Nodes: status.Nodes, Files: status.Files, Directories: status.Directories, Bytes: status.Bytes, Issues: append([]string{}, status.Issues...), Error: status.Error, CountedBytes: status.CountedBytes, VolumeUsedBytes: status.VolumeUsedBytes}
}

func ScanProgressView(progress application.ScanProgress) ScanProgress {
	return ScanProgress{TaskID: progress.TaskID, SnapshotID: progress.SnapshotID, Root: progress.Root, State: progress.State, Phase: progress.Phase, Nodes: progress.Nodes, Files: progress.Files, Directories: progress.Directories, Bytes: progress.Bytes, Issues: append([]string{}, progress.Issues...), Error: progress.Error, SnapshotVersion: progress.SnapshotVersion, AffectedParentIDs: append([]int64{}, progress.AffectedParentIDs...), CountedBytes: progress.CountedBytes, VolumeUsedBytes: progress.VolumeUsedBytes}
}

func nodeView(node scan.Node) NodeView {
	return NodeView{ID: node.ID, ParentID: node.ParentID, Path: node.Path, Name: node.Name, Kind: node.Kind, LogicalSize: node.LogicalSize, AllocatedSize: node.AllocatedSize, OwnedAllocated: node.OwnedAllocated, VolumeID: node.VolumeID, Confidence: node.Confidence, SizeBasis: node.SizeBasis, HasChildren: node.HasChildren}
}

func cleanupPlan(plan application.CleanupPlan) CleanupPlan {
	results := make([]CleanupItemResult, 0, len(plan.Results))
	for _, item := range plan.Results {
		results = append(results, CleanupItemResult{Path: item.Path, State: item.State, Reason: item.Reason})
	}
	return CleanupPlan{ID: plan.ID, SnapshotID: plan.SnapshotID, Version: plan.Version, State: plan.State, Items: plan.Items, Results: results}
}

func mapEntry(entry application.MapEntry) MapEntry {
	children := projectedEntries(entry.Children)
	return MapEntry{Kind: entry.Kind, Node: nodeView(entry.Node), Name: entry.Name, VirtualType: entry.VirtualType, DisplayState: entry.DisplayState, Capabilities: append([]string(nil), entry.Capabilities...), Count: entry.Count, LogicalSize: entry.LogicalSize, AllocatedSize: entry.AllocatedSize, OwnedAllocated: entry.OwnedAllocated, Confidence: entry.Confidence, SizeBasis: entry.SizeBasis, Protection: entry.Protection, Children: children, ChildrenTotal: entry.ChildrenTotal, ChildrenHasMore: entry.ChildrenHasMore}
}

func projectedEntries(entries []application.ProjectedEntry) []ProjectedEntry {
	out := make([]ProjectedEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ProjectedEntry{
			NodeID: entry.NodeID, Name: entry.Name, Kind: entry.Kind,
			OwnedAllocated: entry.OwnedAllocated, Children: projectedEntries(entry.Children),
			ChildrenTotal: entry.ChildrenTotal, ChildrenHasMore: entry.ChildrenHasMore,
		})
	}
	return out
}
