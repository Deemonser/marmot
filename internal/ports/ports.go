package ports

import (
	"context"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/domain/scan"
)

type Scanner interface {
	Scan(context.Context, string, scan.Emitter, scan.PhaseEmitter) (scan.Result, error)
}

type SnapshotStore interface {
	CreateSnapshot(string, string) (int64, error)
	UpdateSnapshotPhase(int64, string) error
	InsertNodes(int64, []scan.Node) error
	UpdateDirectorySizes(int64, map[int64]scan.DirectorySize) error
	NodeByPath(int64, string) (scan.Node, error)
	FinishSnapshot(int64, string, string, int64, int64, int64, int64, int64) error
	Children(int64, int64, int, int) ([]scan.Node, error)
	SnapshotByTaskID(string) (scan.Snapshot, error)
	MarkRunningInterrupted() error
}

type PermissionReport struct {
	Platform string
	State    string
	Message  string
}

type PermissionProbe interface {
	Probe() PermissionReport
}

type FileSystem interface {
	NormalizeScanRoot(string) (string, error)
	CaptureCleanupItem(string) (cleanup.Item, error)
}

type Trash interface {
	Trash(string) (string, error)
}
