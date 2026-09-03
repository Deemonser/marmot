package ports

import (
	"context"
	"errors"

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

// AdviceRequest is what an advisor is asked. It is two blocks of text and
// nothing else: the adapter is transport, and knows nothing about what counts as
// cleanable. Building these blocks is the domain's job (recommendation.
// SystemPrompt / TriagePrompt / ExpandPrompt), so switching providers cannot
// change what the model is asked.
type AdviceRequest struct {
	// System is the fixed instruction block. Keeping it byte-identical across
	// requests is what makes a provider's prompt cache work at all.
	System string
	User   string
	// MaxOutputTokens bounds the reply. Zero lets the adapter choose.
	MaxOutputTokens int
}

// Advisor turns evidence into raw suggestions. Everything it returns is
// untrusted and must go through recommendation.Validate before it can become a
// Recommendation (ADR-0061 §7).
//
// The context carries cancellation: an analysis the user stopped must stop the
// request in flight, not just discard its result.
type Advisor interface {
	Advise(context.Context, AdviceRequest) (recommendation.AdvisorResult, error)
	// Describe names the endpoint and model for display, and must never include
	// the credential.
	Describe() string
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

// ErrCredentialNotFound lets "nothing stored yet" -- the shipping state -- be
// told apart from "the store refused us", which is a fault worth reporting. It
// lives here rather than in platform so the application layer can compare
// against it without importing a platform package, which the dependency rule
// forbids.
var ErrCredentialNotFound = errors.New("credential not found")

// CredentialStore holds the advisor's API key and the endpoint configuration it
// is useless without. The implementation encrypts them into the app's own
// support directory; see internal/platform for exactly what that does and does
// not protect against.
type CredentialStore interface {
	StoreCredential(account, secret string) error
	LoadCredential(account string) (string, error)
	DeleteCredential(account string) error
}

type Trash interface {
	Trash(string) (string, error)
	// RemovePermanently deletes without a trash step, and without any way back.
	// Offered because a move to the trash frees no space: same volume, so it is a
	// rename. What may be removed this way is decided in the application layer,
	// never here.
	RemovePermanently(string) error
}

type PreviewPort interface {
	Preview(string) (string, error)
	Reveal(string) (string, error)
}

// VolumeIcons returns the icon the system itself shows for a mounted volume,
// as PNG bytes at the requested pixel size: the internal drive with the Apple
// mark for the boot volume, the orange external-drive icon for a USB disk, a
// user's own custom icon where one is set. These are the operating system's
// icons, not a copy of any other application's artwork (SDD §5.2 rule 6).
type VolumeIcons interface {
	VolumeIcon(path string, pixels int) ([]byte, error)
}

// ScanTotals remembers, per scan root, how many bytes the last completed walk
// counted. The progress numerator is lstat-allocated bytes, and no statfs
// figure shares that basis — "volume used" runs ~6% high (purgeable, local
// snapshots, container overhead), which pins the bar's ceiling around 94%.
// The one number on the same scale is the previous run's own final count
// (R-067 §2.4). History, not configuration: losing the file only means the
// next scan falls back to the statfs denominator once.
type ScanTotals interface {
	// LoadScanTotal returns the last completed walk's final counted bytes for
	// this root, or 0 when there is no history.
	LoadScanTotal(root string) int64
	StoreScanTotal(root string, bytes int64) error
}
