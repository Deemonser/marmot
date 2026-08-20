package scan

import "time"

type Node struct {
	ID             int64
	ParentID       int64
	Path           string
	Name           string
	Kind           string
	LogicalSize    int64
	AllocatedSize  int64
	OwnedAllocated int64
	Confidence     string
	SizeBasis      string
	Device         uint64
	Inode          uint64
	ModifiedAt     time.Time
	HasChildren    bool
}

type Issue struct {
	Path    string
	Message string
}

type Result struct {
	Nodes          int64
	Files          int64
	Directories    int64
	Bytes          int64
	DirectorySizes map[int64]DirectorySize
	Issues         []Issue
}

type Snapshot struct {
	TaskID          string
	ID              int64
	State           string
	Phase           string
	Root            string
	SnapshotVersion int64
	NodeCount       int64
	FileCount       int64
	DirCount        int64
	Bytes           int64
	Issues          int64
	Error           string
}

type DirectorySize struct {
	LogicalSize    int64
	AllocatedSize  int64
	OwnedAllocated int64
	Confidence     string
	SizeBasis      string
}

type MapQuery struct {
	SnapshotID      int64
	ParentID        int64
	Limit           int
	Offset          int
	Measure         string
	Depth           int
	ProjectionLimit int
}

type MapEntry struct {
	Kind            string
	Node            Node
	Name            string
	VirtualType     string
	DisplayState    string
	Capabilities    []string
	Count           int64
	LogicalSize     int64
	AllocatedSize   int64
	OwnedAllocated  int64
	Confidence      string
	SizeBasis       string
	Children        []MapEntry
	ChildrenTotal   int
	ChildrenHasMore bool
}

type MapResult struct {
	SnapshotID          int64
	SnapshotVersion     int64
	Parent              Node
	Entries             []MapEntry
	Total               int
	Limit               int
	Offset              int
	HasMore             bool
	Remaining           MapEntry
	Confidence          string
	ProjectionTruncated bool
}

type Emitter func(Node) error
type PhaseEmitter func(Phase) error

type Phase string

const (
	PhaseCatalog         Phase = "catalog"
	PhaseVolumeOverview  Phase = "volume_overview"
	PhaseTopLevelPublish Phase = "top_level_publish"
	PhaseDeepScan        Phase = "deep_scan"
	PhaseFinalize        Phase = "finalize"

	JobRunning             = "running"
	JobCompleted           = "completed"
	JobCompletedWithIssues = "completed_with_issues"
	JobCancelled           = "cancelled"
	JobInterrupted         = "interrupted"
	JobFailed              = "failed"
)
