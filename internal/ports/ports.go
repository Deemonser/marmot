package ports

import (
	"context"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/domain/scan"
)

type Scanner interface {
	Scan(context.Context, string, scan.Emitter, scan.PhaseEmitter) (scan.Result, error)
}

type BatchScanner interface {
	ScanBatched(context.Context, string, scan.BatchEmitter, scan.PhaseEmitter) (scan.Result, error)
}

// SnapshotStore holds the scan result. It is memory-only (ADR-0055): there is no
// persistence, so the methods that existed to manage files on disk —
// FinishSnapshot's durable publish, MarkRunningInterrupted, PruneSnapshots,
// CompactCache — are gone rather than stubbed. FinishScan replaces
// FinishSnapshot: it makes the result queryable and nothing else.
type SnapshotStore interface {
	CreateSnapshot(string, string) (int64, error)
	UpdateSnapshotPhase(int64, string) error
	InsertNodes(int64, []scan.Node) error
	UpdateDirectorySizes(int64, map[int64]scan.DirectorySize) error
	NodeByPath(int64, string) (scan.Node, error)
	FinishScan(int64, string, string, int64, int64, int64, int64, int64) error
	Children(int64, int64, int, int) ([]scan.Node, error)
	Map(scan.MapQuery) (scan.MapResult, error)
	NodeByID(int64, int64) (scan.Node, error)
	SnapshotVersion(int64) (int64, error)
	SnapshotByTaskID(string) (scan.Snapshot, error)
	// EvidenceNodes is the bounded skeleton the advice feature is built on: the
	// nodes at or above a size floor, each carrying the subtree facts a
	// suggestion has to be judged against. It is read-only and shares the same
	// in-memory source as Map (ADR-0061 §2).
	EvidenceNodes(recommendation.EvidenceQuery) (recommendation.EvidenceResult, error)
}

type Volume struct {
	ID                  string
	Name                string
	Path                string
	Kind                string
	Role                string
	DeviceProfile       scan.DeviceProfile
	ContainerID         string
	VolumeGroupID       string
	TotalBytes          uint64
	UsedBytes           uint64
	FreeBytes           uint64
	ContainerTotalBytes uint64
	ContainerUsedBytes  uint64
	ContainerFreeBytes  uint64
	UsageBasis          string
	Permission          string
	Message             string
	Scannable           bool
}

type Mount struct {
	ID            string
	Path          string
	DeviceProfile scan.DeviceProfile
}

type VolumeCatalog interface {
	ListVolumes() ([]Volume, error)
}

type MountCatalog interface {
	ListMounts() ([]Mount, error)
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

type PreviewPort interface {
	Preview(string) (string, error)
	Reveal(string) (string, error)
}
