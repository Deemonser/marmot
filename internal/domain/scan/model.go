package scan

import "time"

type DeviceProfile string

const (
	DeviceProfileSSD              DeviceProfile = "ssd"
	DeviceProfileRotational       DeviceProfile = "rotational"
	DeviceProfileNetworkOrVirtual DeviceProfile = "network_or_virtual"
	DeviceProfileUnknown          DeviceProfile = "unknown"
)

type Node struct {
	ID             int64
	ParentID       int64
	Path           string
	Name           string
	Kind           string
	LogicalSize    int64
	AllocatedSize  int64
	OwnedAllocated int64
	VolumeID       string
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
	// MinSweeps is the smallest angle, in radians, a descendant must span to be
	// worth projecting, one entry per projected level. The renderer knows each
	// ring's radius and the narrowest arc a person can see, so it computes this
	// and the store prunes at the source (ADR-0059 §3).
	//
	// Culling here rather than in the renderer is the point: the previous build
	// serialised, transferred and parsed arcs that were then discarded for being
	// sub-pixel, which is why four levels already hit the payload ceiling. An
	// empty slice disables it.
	MinSweeps []float64
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
	Children        []ProjectedEntry
	ChildrenTotal   int
	ChildrenHasMore bool
}

// NodeEntry is the space map's one way of turning a snapshot node into a map
// entry, so a node reached by walking a level and the same node looked up by ID
// describe themselves identically. Capabilities are deliberately absent: they
// are granted a layer above, by the application, for both routes alike.
func NodeEntry(node Node) MapEntry {
	return MapEntry{
		Kind: "node", Node: node, Name: node.Name,
		LogicalSize: node.LogicalSize, AllocatedSize: node.AllocatedSize, OwnedAllocated: node.OwnedAllocated,
		Confidence: node.Confidence, SizeBasis: node.SizeBasis,
	}
}

// ProjectedEntry is one arc of the space map below the current level. It carries
// only what drawing an arc needs: identity, name, size and kind. It deliberately
// omits Path, Device, Inode, ModifiedAt, VolumeID and the per-entry confidence
// and size-basis strings — the space map never reads them, they dominated the
// payload, and reconstructing a path costs one record read per ancestor.
//
// Omitting the path also makes ADR-0013/0015 and DDD invariant 17 structural:
// a projected descendant cannot be used to authorise a file operation, because
// it does not carry one. Acting on an arc requires looking the node up by ID.
// The JSON keys are deliberately short. At the target density the projection
// carries thousands of arcs and repeated field names dominate the payload; short
// keys are what keep it inside the 256 KB ceiling (ADR-0048).
type ProjectedEntry struct {
	NodeID int64  `json:"id"`
	Name   string `json:"name"`
	// Kind is "directory", "file" or "aggregate".
	Kind           string `json:"kind"`
	OwnedAllocated int64  `json:"size"`
	// Why this object may not be deleted, or empty when it may. The one thing a
	// projection says about acting on itself, and it only ever says no -- so it
	// cannot be used to authorise anything, which is what ADR-0048 and DDD
	// invariant 17 are protecting. Carried here so the UI can refuse on the frame
	// the drag starts instead of a round trip later.
	Protection      string           `json:"protection,omitempty"`
	Children        []ProjectedEntry `json:"children"`
	ChildrenTotal   int              `json:"total,omitempty"`
	ChildrenHasMore bool             `json:"more,omitempty"`
}

type MapResult struct {
	SnapshotID      int64
	SnapshotVersion int64
	Parent          Node
	Entries         []MapEntry
	Total           int
	Limit           int
	Offset          int
	HasMore         bool
	Remaining       MapEntry
	Confidence      string
	// Volume state captured when the scan started. The root level balances to
	// VolumeUsedBytes; the free-space rows come from VolumeFreeBytes. Zero means
	// the snapshot did not record it (ADR-0052 §4).
	VolumeTotalBytes uint64
	VolumeUsedBytes  uint64
	VolumeFreeBytes  uint64
	// DensityTruncated reports that the arc budget ran out before every
	// projected subtree got an allowance. It is a limit of how much the wheel
	// can draw, never a limit of what the store knows: the query source retains
	// every child, so any entry the caller asks for directly is answerable.
	DensityTruncated bool
}

type Emitter func(Node) error

// BatchEmitter delivers one scanner-produced batch to the consumer. The slice
// and the strings inside it are valid only until the call returns: the scanner
// recycles the backing arrays across batches, so a consumer that needs to keep
// anything must copy it before returning (ADR-0057 §1).
//
// This is the opposite of the previous contract, which transferred ownership.
// It changed because 45.7% of batches carry a single node and the fixed cost of
// allocating a batch the consumer could own dominated the scan's allocation
// total (R-058 §4.1). A consumer that retains the slice will see it overwritten
// by a later batch, and the emitter is called concurrently from scanner worker
// threads, so retaining it is also a data race.
type BatchEmitter func([]Node) error
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

// SubtreeRemoval is what leaving the tree cost, reported so the caller can log it
// and the UI can say what changed without another scan.
type SubtreeRemoval struct {
	Nodes          int64
	Files          int64
	Directories    int64
	AllocatedBytes int64
	// Version is the snapshot version after the removal, so a client can tell
	// this apart from the state it last drew.
	Version int64
}
