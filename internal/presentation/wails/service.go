package wails

import (
	"context"
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
	// ExpectedTotalBytes is the previous completed walk's final count for this
	// root — the one denominator on the numerator's own scale (R-067 §2.4).
	// Zero on the first-ever scan of a root.
	ExpectedTotalBytes int64 `json:"expectedTotalBytes"`
	ExpectedTotalNodes int64 `json:"expectedTotalNodes"`
}

// ProjectedEntry is one arc below the current level. It carries only what the
// space map draws with; it has no path and no capabilities (ADR-0048).
type ProjectedEntry struct {
	NodeID         int64  `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	OwnedAllocated int64  `json:"size"`
	// Why this object may not be deleted, or empty when it may. The one thing an
	// arc below the current level says about acting on itself, and it only ever
	// says no -- so the frontend can refuse on the frame a drag starts without
	// gaining anything it could authorise with (ADR-0048).
	Protection      string           `json:"protection,omitempty"`
	Children        []ProjectedEntry `json:"children,omitempty"`
	ChildrenTotal   int              `json:"total,omitempty"`
	ChildrenHasMore bool             `json:"more,omitempty"`
}

// CleanupProgress mirrors the application event. Deleting is O(files) rather than
// the constant-time rename a move to the trash was, so the UI has to be able to
// say where it is.
type CleanupProgress struct {
	PlanID     string `json:"planId"`
	Version    int64  `json:"version"`
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Current    string `json:"current"`
	DoneBytes  int64  `json:"doneBytes"`
	TotalBytes int64  `json:"totalBytes"`
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
	CountedBytes       int64  `json:"countedBytes"`
	VolumeUsedBytes    uint64 `json:"volumeUsedBytes"`
	ExpectedTotalBytes int64  `json:"expectedTotalBytes"`
	ExpectedTotalNodes int64  `json:"expectedTotalNodes"`
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
	Icon       string                `json:"icon"`
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
	Removed    int                 `json:"removed"`
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

// AdviceItem is one suggestion. It carries nodeId as its identity and path for
// display; neither authorises anything, because CreateCleanupPlan re-checks
// every path it is handed (ADR-0061 §1).
type AdviceItem struct {
	NodeID           int64  `json:"nodeId"`
	Name             string `json:"name"`
	Path             string `json:"path"`
	Source           string `json:"source"`
	RuleName         string `json:"ruleName"`
	Category         string `json:"category"`
	ReclaimableBytes int64  `json:"reclaimableBytes"`
	Recovery         string `json:"recovery"`
	Risk             string `json:"risk"`
	// RiskReasons are the codes behind the tier (ADR-0067); the frontend
	// translates them. Activity and IdleDays name the signal, when there is one.
	RiskReasons  []string `json:"riskReasons"`
	Activity     string   `json:"activity"`
	IdleDays     int64    `json:"idleDays"`
	Confidence   float64  `json:"confidence"`
	Evidence     []string `json:"evidence"`
	WhatBreaks   string   `json:"whatBreaks"`
	HowToRestore string   `json:"howToRestore"`
	Manual       bool     `json:"manual"`
	Command      string   `json:"command"`
}

type Advice struct {
	SnapshotID   int64        `json:"snapshotId"`
	Items        []AdviceItem `json:"items"`
	TotalBytes   int64        `json:"totalBytes"`
	RuleItems    int          `json:"ruleItems"`
	AdvisorItems int          `json:"advisorItems"`
	// RejectedSummary says in words what was refused and why. Shown rather than
	// hidden: a tool that reports its own model's bad suggestions is easier to
	// trust than one that quietly shows fewer rows.
	RejectedSummary string `json:"rejectedSummary"`
	// CorrectionSummary reports recoverability claims that were overridden --
	// the one error class that cannot be undone by waiting.
	CorrectionSummary string `json:"correctionSummary"`
	// AdvisorError is a failed round trip. The rule findings still stand.
	AdvisorError string `json:"advisorError"`
	Rounds       int    `json:"rounds"`
	Expanded     int    `json:"expanded"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	// Rejected suggestions are reported, not hidden: a tool that says what it
	// refused is easier to trust than one that quietly shows fewer rows.
	Rejected []AdviceRejection `json:"rejected"`
	// EvidenceNodes and EvidenceBytes describe what an advisor would be sent.
	EvidenceNodes int   `json:"evidenceNodes"`
	EvidenceBytes int   `json:"evidenceBytes"`
	FloorBytes    int64 `json:"floorBytes"`
}

type AdviceRejection struct {
	NodeID      int64  `json:"nodeId"`
	ClaimedName string `json:"claimedName"`
	Reason      string `json:"reason"`
}

// EvidencePreview is what "查看发送内容" shows. Text is the same rendering that
// would be sent, so the preview cannot drift from the payload.
type EvidencePreview struct {
	SnapshotID int64  `json:"snapshotId"`
	Root       string `json:"root"`
	FloorBytes int64  `json:"floorBytes"`
	Nodes      int    `json:"nodes"`
	Bytes      int    `json:"bytes"`
	Text       string `json:"text"`
}

// AdvisorSettings is the non-secret half of the advisor configuration.
type AdvisorSettings struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"baseUrl"`
	Model           string `json:"model"`
	JSONMode        string `json:"jsonMode"`
	ReasoningEffort string `json:"reasoningEffort"`
}

// AdvisorStatus never carries the key -- only whether one is stored.
type AdvisorStatus struct {
	Configured  bool            `json:"configured"`
	HasKey      bool            `json:"hasKey"`
	Description string          `json:"description"`
	Settings    AdvisorSettings `json:"settings"`
	// Fault is why a saved configuration did not come back at startup. Falling
	// back to the rule layer is a working state; leaving the user to guess why
	// the AI they configured is off is not.
	Fault string `json:"fault"`
}

func (s *Service) GetAdvisorStatus() AdvisorStatus {
	return advisorStatus(s.application.GetAdvisorStatus())
}

// ConfigureAdvisor stores the endpoint, model and key, encrypted in the app's
// support directory. An empty apiKey keeps whatever is already stored, so
// editing the endpoint does not require pasting the credential again.
func (s *Service) ConfigureAdvisor(settings AdvisorSettings, apiKey string) (AdvisorStatus, error) {
	status, err := s.application.ConfigureAdvisor(application.AdvisorSettings{
		Provider: settings.Provider, BaseURL: settings.BaseURL, Model: settings.Model,
		JSONMode: settings.JSONMode, ReasoningEffort: settings.ReasoningEffort,
	}, apiKey)
	if err != nil {
		return AdvisorStatus{}, err
	}
	return advisorStatus(status), nil
}

func (s *Service) ClearAdvisor() error {
	return s.application.ClearAdvisor()
}

// RunAdvisorAnalysis is the full flow: rule layer, then the advisor if one is
// configured. The context is the frontend's -- cancelling the promise cancels
// the request in flight rather than merely discarding its result.
func (s *Service) RunAdvisorAnalysis(ctx context.Context, snapshotID int64) (Advice, error) {
	advice, err := s.application.RunAdvisorAnalysis(ctx, snapshotID)
	if err != nil {
		return Advice{}, err
	}
	return adviceView(advice), nil
}

func advisorStatus(status application.AdvisorStatus) AdvisorStatus {
	return AdvisorStatus{
		Configured: status.Configured, HasKey: status.HasKey, Description: status.Description, Fault: status.Fault,
		Settings: AdvisorSettings{
			Provider: status.Settings.Provider, BaseURL: status.Settings.BaseURL,
			Model: status.Settings.Model, JSONMode: status.Settings.JSONMode,
			ReasoningEffort: status.Settings.ReasoningEffort,
		},
	}
}

// GetCleanupAdvice is the rule layer alone, with no network request of any kind.
func (s *Service) GetCleanupAdvice(snapshotID int64) (Advice, error) {
	advice, err := s.application.GetCleanupAdvice(snapshotID)
	if err != nil {
		return Advice{}, err
	}
	return adviceView(advice), nil
}

func adviceView(advice application.Advice) Advice {
	items := make([]AdviceItem, 0, len(advice.Items))
	for _, item := range advice.Items {
		items = append(items, AdviceItem{
			NodeID: item.NodeID, Name: item.Name, Path: item.Path,
			Source: string(item.Source), RuleName: item.RuleName, Category: item.Category,
			ReclaimableBytes: item.ReclaimableBytes,
			Recovery:         string(item.Recovery), Risk: string(item.Risk),
			RiskReasons: append([]string{}, item.RiskReasons...),
			Activity:    string(item.Activity), IdleDays: item.IdleDays,
			Confidence: item.Confidence, Evidence: append([]string{}, item.Evidence...),
			WhatBreaks: item.WhatBreaks, HowToRestore: item.HowToRestore,
			Manual: item.Manual, Command: item.Command,
		})
	}
	rejected := make([]AdviceRejection, 0, len(advice.Rejected))
	for _, item := range advice.Rejected {
		rejected = append(rejected, AdviceRejection{NodeID: item.NodeID, ClaimedName: item.ClaimedName, Reason: item.Reason})
	}
	return Advice{
		SnapshotID: advice.SnapshotID, Items: items, TotalBytes: advice.TotalBytes,
		RuleItems: advice.RuleItems, AdvisorItems: advice.AdvisorItems, Rejected: rejected,
		RejectedSummary: advice.RejectedSummary, CorrectionSummary: advice.CorrectionSummary,
		AdvisorError: advice.AdvisorError,
		Rounds:       advice.Rounds, Expanded: advice.Expanded,
		InputTokens: advice.InputTokens, OutputTokens: advice.OutputTokens,
		EvidenceNodes: advice.EvidenceNodes, EvidenceBytes: advice.EvidenceBytes, FloorBytes: advice.FloorBytes,
	}
}

// PreviewEvidence renders exactly what an advisor would receive.
func (s *Service) PreviewEvidence(snapshotID int64) (EvidencePreview, error) {
	pack, err := s.application.BuildEvidencePack(snapshotID)
	if err != nil {
		return EvidencePreview{}, err
	}
	text := pack.Text()
	return EvidencePreview{
		SnapshotID: snapshotID, Root: pack.Root, FloorBytes: pack.FloorBytes,
		Nodes: len(pack.Nodes), Bytes: len(text), Text: text,
	}, nil
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
			Icon:       source.Icon,
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
	plan, err := s.application.CreateCleanupPlan(application.CleanupPlanRequest{
		SnapshotID: request.SnapshotID, Paths: request.Paths,
	})
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
	return ScanStatus{TaskID: status.TaskID, SnapshotID: status.SnapshotID, Root: status.Root, State: status.State, Phase: status.Phase, Nodes: status.Nodes, Files: status.Files, Directories: status.Directories, Bytes: status.Bytes, Issues: append([]string{}, status.Issues...), Error: status.Error, CountedBytes: status.CountedBytes, VolumeUsedBytes: status.VolumeUsedBytes, ExpectedTotalBytes: status.ExpectedTotalBytes, ExpectedTotalNodes: status.ExpectedTotalNodes}
}

func ScanProgressView(progress application.ScanProgress) ScanProgress {
	return ScanProgress{TaskID: progress.TaskID, SnapshotID: progress.SnapshotID, Root: progress.Root, State: progress.State, Phase: progress.Phase, Nodes: progress.Nodes, Files: progress.Files, Directories: progress.Directories, Bytes: progress.Bytes, Issues: append([]string{}, progress.Issues...), Error: progress.Error, SnapshotVersion: progress.SnapshotVersion, AffectedParentIDs: append([]int64{}, progress.AffectedParentIDs...), CountedBytes: progress.CountedBytes, VolumeUsedBytes: progress.VolumeUsedBytes, ExpectedTotalBytes: progress.ExpectedTotalBytes, ExpectedTotalNodes: progress.ExpectedTotalNodes}
}

func nodeView(node scan.Node) NodeView {
	return NodeView{ID: node.ID, ParentID: node.ParentID, Path: node.Path, Name: node.Name, Kind: node.Kind, LogicalSize: node.LogicalSize, AllocatedSize: node.AllocatedSize, OwnedAllocated: node.OwnedAllocated, VolumeID: node.VolumeID, Confidence: node.Confidence, SizeBasis: node.SizeBasis, HasChildren: node.HasChildren}
}

func cleanupPlan(plan application.CleanupPlan) CleanupPlan {
	results := make([]CleanupItemResult, 0, len(plan.Results))
	for _, item := range plan.Results {
		results = append(results, CleanupItemResult{Path: item.Path, State: item.State, Reason: item.Reason})
	}
	return CleanupPlan{ID: plan.ID, SnapshotID: plan.SnapshotID, Version: plan.Version, State: plan.State, Items: plan.Items, Removed: plan.Removed, Results: results}
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
			OwnedAllocated: entry.OwnedAllocated, Protection: entry.Protection,
			Children:      projectedEntries(entry.Children),
			ChildrenTotal: entry.ChildrenTotal, ChildrenHasMore: entry.ChildrenHasMore,
		})
	}
	return out
}
