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
	TaskID    string
	ID        int64
	State     string
	Root      string
	NodeCount int64
	FileCount int64
	DirCount  int64
	Bytes     int64
	Issues    int64
	Error     string
}

type DirectorySize struct {
	LogicalSize    int64
	AllocatedSize  int64
	OwnedAllocated int64
	Confidence     string
	SizeBasis      string
}

type Emitter func(Node) error

const (
	JobRunning             = "running"
	JobCompleted           = "completed"
	JobCompletedWithIssues = "completed_with_issues"
	JobCancelled           = "cancelled"
	JobInterrupted         = "interrupted"
	JobFailed              = "failed"
)
