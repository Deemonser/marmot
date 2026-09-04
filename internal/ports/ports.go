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
	// SubtreeChunks plans one staged item's deletion: how many nodes are under
	// it, and disjoint units of work that can be removed concurrently. It is
	// arithmetic on the tree already in memory, so it costs no I/O and is asked
	// for at the moment of deletion rather than carried in the plan.
	SubtreeChunks(int64, string, int) (cleanup.Subtree, error)
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

// ErrItemReplaced is returned when the directory a removal was about to work
// inside is no longer the one the plan validated. It is a refusal, not a
// failure: the caller must stop rather than retry, because what is there now was
// never staged.
var ErrItemReplaced = errors.New("cleanup item was replaced after it was validated")

type Trash interface {
	Trash(string) (string, error)
	// RemovePermanently deletes without a trash step, and without any way back.
	// Offered because a move to the trash frees no space: same volume, so it is a
	// rename. What may be removed this way is decided in the application layer,
	// never here.
	RemovePermanently(string) error
	// RemoveWithin deletes names beneath one staged item, and is the only way the
	// pieces of an item may be removed.
	//
	// It exists because a path is not a durable reference to an object. The names
	// come from the snapshot and are relative on purpose: resolving absolute
	// child paths from "/" again at the moment of deletion means a directory that
	// became a symlink since the plan was validated redirects the removal out of
	// the item entirely. The implementation pins the item by descriptor, confirms
	// it is still the device and inode the plan captured, and resolves every name
	// inside it -- so a swap is caught (ErrItemReplaced) instead of followed, and
	// a name that leaves the item is refused.
	RemoveWithin(cleanup.Item, []string) error
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

// VolumeWatcher reports that the set of mounted volumes changed -- a disk was
// plugged in, ejected or renamed -- so the source page can re-read the catalog
// instead of showing the list from launch (SDD §5.2 rule 7). onChange may be
// called from any thread and more than once per event; the application layer
// coalesces. stop unregisters the observer.
type VolumeWatcher interface {
	WatchVolumes(onChange func()) (stop func(), err error)
}

// ScanTotals remembers, per scan root, how many bytes the last completed walk
// counted. The progress numerator is lstat-allocated bytes, and no statfs
// figure shares that basis — "volume used" runs ~6% high (purgeable, local
// snapshots, container overhead), which pins the bar's ceiling around 94%.
// The one number on the same scale is the previous run's own final count
// (R-067 §2.4). History, not configuration: losing the file only means the
// next scan falls back to the statfs denominator once.
type ScanTotals interface {
	// LoadScanTotal returns the last completed walk's final counts for this
	// root, or the zero value when there is no history.
	LoadScanTotal(root string) ScanTotal
	StoreScanTotal(root string, total ScanTotal) error
}

// ScanTotal is what a completed walk leaves behind for the next one's progress
// bar: its final counted bytes and its final node count. Both are needed. Bytes
// alone pin the bar early -- the walk is breadth-first, so its tail is the
// file-dense, byte-poor leaves (node_modules, caches, .git objects), where the
// byte fraction stops moving while the node fraction still climbs
// (ADR-0053 second amendment). Nodes is 0 for history written before it was
// recorded; the bar then falls back to bytes alone.
type ScanTotal struct {
	Bytes int64 `json:"bytes"`
	Nodes int64 `json:"nodes"`
}
