package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
)

const (
	scanBatchSize            = 50000
	scanBatchBufferFloor     = 4096
	scanPersistQueueCapacity = 2
	maxAffectedParents       = 256
	cacheMaintenanceDelay    = 60 * time.Second
)

type Service struct {
	store       ports.SnapshotStore
	scanner     ports.Scanner
	files       ports.FileSystem
	permissions ports.PermissionProbe
	trash       ports.Trash
	volumes     ports.VolumeCatalog
	preview     ports.PreviewPort
	// icons is optional: without it the source rows draw their built-in glyph.
	icons     ports.VolumeIcons
	iconMu    sync.Mutex
	iconCache map[string]string
	// volumeWatcher is optional: without it the source list is what it was at
	// launch and after each scan.
	volumeWatcher    ports.VolumeWatcher
	volumeWatchMu    sync.Mutex
	volumeWatchStop  func()
	volumeWatchTimer *time.Timer
	volumeWatchArmed bool
	legacyCacheDir   string
	mu               sync.RWMutex
	tasks            map[string]*scanTask
	plans            map[string]*cleanupPlan
	serial           atomic.Uint64
	emit             func(string, any)
	recoveryMu       sync.Mutex
	recoveryDone     chan struct{}
	recoveryErr      error
	recoveryFinished bool
	maintenanceRun   atomic.Bool
	maintenanceMu    sync.Mutex
	maintenanceStop  context.CancelFunc
	maintenanceDone  chan struct{}
	memoryLimiter    scanMemoryLimiter
	// advisor is optional and replaceable at runtime: it is configured by the
	// user, and a nil one means the advice feature is the rule layer alone.
	advisorMu      sync.RWMutex
	advisor        ports.Advisor
	advisorFault   string
	credentials    ports.CredentialStore
	scanTotals     ports.ScanTotals
	advisorFactory AdvisorFactory
	// batchSize and maxParallel shape the triage round; zero means the default.
	batchSize   int
	maxParallel int
	// firstMap holds the snapshot whose first root map has not been logged yet, so
	// the one line that answers "what happened between the scan finishing and the
	// result appearing" is always written, however fast it was.
	firstMap atomic.Int64
}

type Dependencies struct {
	// LegacyCacheDir is the app's own cache directory, used once to remove the
	// superseded SQLite store (ADR-0054). Empty disables the cleanup. It is passed
	// in rather than derived from the snapshot directory so the cleanup never
	// climbs out of a directory it was given.
	LegacyCacheDir string
	Store          ports.SnapshotStore
	Scanner        ports.Scanner
	FileSystem     ports.FileSystem
	Permissions    ports.PermissionProbe
	Trash          ports.Trash
	Volumes        ports.VolumeCatalog
	Preview        ports.PreviewPort
	// Icons supplies the system's icon per mounted volume for the source page.
	// Optional; nil leaves StorageSourceOverview.Icon empty.
	Icons ports.VolumeIcons
	// VolumeWatcher tells the service when the mounted set changes. Optional.
	VolumeWatcher ports.VolumeWatcher
	Credentials   ports.CredentialStore
	ScanTotals    ports.ScanTotals
	// AdvisorFactory is injected rather than imported so the application layer
	// stays free of transport code (PROJECT-STRUCTURE dependency rule).
	AdvisorFactory AdvisorFactory
	Emit           func(string, any)
}

type scanTask struct {
	mu          sync.RWMutex
	taskID      string
	snapshotID  int64
	root        string
	state       string
	phase       string
	nodes       int64
	files       int64
	directories int64
	bytes       int64
	issues      []string
	error       string
	// volumeUsed is the progress bar's denominator, captured at scan start.
	// preCounted is the volume-group auxiliary volumes' statfs usage: known before
	// the walk and certain to land in the tree, so the bar counts it from t=0
	// instead of taking a 12-point jump when they are attached at the end
	// (ADR-0053 §1). It feeds the bar only — never the persisted summary, which
	// must keep matching the nodes actually written.
	// expectedTotal is the previous completed walk's final counted bytes for
	// this root — the only denominator on the same scale as the numerator
	// (R-067 §2.4). Zero when this root has never completed a walk.
	volumeUsed      uint64
	preCounted      int64
	expectedTotal   int64
	cancel          context.CancelFunc
	affectedParents map[int64]struct{}
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
	// denominator (ADR-0053 §1). CountedBytes adds the volume-group auxiliary
	// volumes to the walked bytes while the scan runs; Bytes stays the walked
	// total that gets persisted. A zero denominator means the bar must stay
	// indeterminate rather than guess.
	CountedBytes    int64  `json:"countedBytes"`
	VolumeUsedBytes uint64 `json:"volumeUsedBytes"`
	// ExpectedTotalBytes is the previous completed walk's final count for this
	// root: the one denominator on the numerator's own scale. Zero on the
	// first-ever scan of a root (R-067 §2.4).
	ExpectedTotalBytes int64 `json:"expectedTotalBytes"`
}

// ProjectedEntry is one arc below the current level. It carries only what the
// space map draws with; it has no path and no capabilities (ADR-0048).
type ProjectedEntry struct {
	NodeID int64  `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	// Why it may not be deleted, or empty. Passed through from the store, which
	// is the only layer with the path to ask about (ADR-0048).
	Protection      string
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
	CountedBytes       int64  `json:"countedBytes"`
	VolumeUsedBytes    uint64 `json:"volumeUsedBytes"`
	ExpectedTotalBytes int64  `json:"expectedTotalBytes"`
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
	// Icon is the system's icon for the source's mount point as a PNG data URL,
	// or empty when no icon port is wired or the lookup failed. Presentation
	// only: it is not part of the capacity or identity contract.
	Icon string `json:"icon"`
}

type MapQuery struct {
	SnapshotID      int64  `json:"snapshotId"`
	ParentID        int64  `json:"parentId"`
	Limit           int    `json:"limit"`
	Offset          int    `json:"offset"`
	Measure         string `json:"measure"`
	Depth           int    `json:"depth"`
	ProjectionLimit int    `json:"projectionLimit"`
	// One minimum arc angle per projected level, in radians. The renderer knows
	// each ring's radius; the store prunes what would be sub-pixel (ADR-0059 §3).
	MinSweeps []float64 `json:"minSweeps"`
}

type MapEntry struct {
	Kind           string    `json:"kind"`
	Node           scan.Node `json:"node"`
	Name           string    `json:"name"`
	VirtualType    string    `json:"virtualType"`
	DisplayState   string    `json:"displayState"`
	Capabilities   []string  `json:"capabilities"`
	Count          int64     `json:"count"`
	LogicalSize    int64     `json:"logicalSize"`
	AllocatedSize  int64     `json:"allocatedSize"`
	OwnedAllocated int64     `json:"ownedAllocated"`
	Confidence     string    `json:"confidence"`
	SizeBasis      string    `json:"sizeBasis"`
	// Why this object may not be deleted, or empty when it may. Set together with
	// withholding the collect capability, so the UI can say why instead of just
	// refusing (cleanup.DeleteBlock). omitempty: it is empty for almost every
	// entry, and the space map payload is capped.
	Protection      string           `json:"protection,omitempty"`
	Children        []ProjectedEntry `json:"children,omitempty"`
	ChildrenTotal   int              `json:"childrenTotal,omitempty"`
	ChildrenHasMore bool             `json:"childrenHasMore,omitempty"`
}

type MapResult struct {
	SnapshotID       int64      `json:"snapshotId"`
	SnapshotVersion  int64      `json:"snapshotVersion"`
	Parent           scan.Node  `json:"parent"`
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

type ChildrenQuery struct {
	SnapshotID int64 `json:"snapshotId"`
	ParentID   int64 `json:"parentId"`
	Limit      int   `json:"limit"`
	Offset     int   `json:"offset"`
}

type ChildrenResult struct {
	Nodes  []scan.Node `json:"nodes"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

type CleanupPlanRequest struct {
	SnapshotID int64    `json:"snapshotId"`
	Paths      []string `json:"paths"`
}

type CleanupPlan struct {
	ID         string `json:"id"`
	SnapshotID int64  `json:"snapshotId"`
	Version    int64  `json:"version"`
	State      string `json:"state"`
	Items      int    `json:"items"`
	// Removed is how many applied items were also taken out of the in-memory
	// result, so the client knows whether the view is already in step or still
	// needs a re-scan.
	Removed int                 `json:"removed"`
	Results []CleanupItemResult `json:"results"`
}

// CleanupProgress is emitted before each item and once at the end. Deleting is
// O(files), not O(1) like a move to the trash, so a plan can take minutes and
// needs to say so while it does.
type CleanupProgress struct {
	PlanID  string `json:"planId"`
	Version int64  `json:"version"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"`
	// DoneBytes and TotalBytes are what the indicator should actually be drawn
	// from. See cleanupPlan.weights for why item counts will not do.
	DoneBytes  int64 `json:"doneBytes"`
	TotalBytes int64 `json:"totalBytes"`
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

type cleanupPlan struct {
	mu sync.Mutex
	CleanupPlan
	items []cleanup.Item
	// weights is each item's subtree size, taken from the snapshot node rather
	// than from lstat: a directory's own Size is a dirent, not what it holds.
	//
	// Progress is reported by bytes, not by item index, because the items are
	// nowhere near equal. A real plan here was twelve items of which one held 18.5
	// GB and the rest a few hundred MB each; counting items would have parked the
	// indicator at one twelfth for most of the run and then jumped it. An
	// indicator that spends most of its time wrong is worse than none.
	weights []int64
}

func NewService(deps Dependencies) *Service {
	emit := deps.Emit
	if emit == nil {
		emit = func(string, any) {}
	}
	return &Service{store: deps.Store, scanner: deps.Scanner, files: deps.FileSystem, permissions: deps.Permissions, trash: deps.Trash, volumes: deps.Volumes, preview: deps.Preview, icons: deps.Icons, iconCache: make(map[string]string), volumeWatcher: deps.VolumeWatcher, credentials: deps.Credentials, scanTotals: deps.ScanTotals, advisorFactory: deps.AdvisorFactory, legacyCacheDir: deps.LegacyCacheDir, tasks: make(map[string]*scanTask), plans: make(map[string]*cleanupPlan), emit: emit}
}

// BeginRecovery lets the Wails window become visible before large legacy cache
// maintenance starts. Scan and snapshot queries wait for the recovery barrier.
func (s *Service) BeginRecovery() {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if s.recoveryDone == nil {
		s.recoveryDone = make(chan struct{})
	}
}

func (s *Service) finishRecovery(err error) {
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	if s.recoveryDone == nil || s.recoveryFinished {
		return
	}
	s.recoveryErr = err
	s.recoveryFinished = true
	close(s.recoveryDone)
}

func (s *Service) waitForRecovery() error {
	s.recoveryMu.Lock()
	done := s.recoveryDone
	s.recoveryMu.Unlock()
	if done == nil {
		return nil
	}
	<-done
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	return s.recoveryErr
}

func (s *Service) scheduleCacheMaintenance() {
	if !s.maintenanceRun.CompareAndSwap(false, true) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.maintenanceMu.Lock()
	s.maintenanceStop = cancel
	s.maintenanceDone = done
	s.maintenanceMu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		defer func() {
			s.maintenanceMu.Lock()
			s.maintenanceStop = nil
			s.maintenanceDone = nil
			s.maintenanceMu.Unlock()
			s.maintenanceRun.Store(false)
		}()
		timer := time.NewTimer(cacheMaintenanceDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
		if s.hasRunningScan() {
			return
		}
		// The only cache work left: remove what the superseded SQLite store left
		// behind (ADR-0054). There is no snapshot cache to prune or compact any
		// more (ADR-0055).
		s.removeLegacyCache()
	}()
}

func (s *Service) cancelCacheMaintenance() {
	s.maintenanceMu.Lock()
	stop := s.maintenanceStop
	done := s.maintenanceDone
	s.maintenanceMu.Unlock()
	if stop == nil {
		return
	}
	stop()
	<-done
}

func (s *Service) hasRunningScan() bool {
	s.mu.RLock()
	tasks := make([]*scanTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		tasks = append(tasks, task)
	}
	s.mu.RUnlock()
	for _, task := range tasks {
		if task.status().State == "running" {
			return true
		}
	}
	return false
}

func (s *Service) GetPermissionStatus() PermissionStatus {
	report := s.permissions.Probe()
	return PermissionStatus{Platform: report.Platform, State: report.State, Message: report.Message}
}

func (s *Service) GetStorageSources() ([]StorageSourceOverview, error) {
	if s.volumes == nil {
		return []StorageSourceOverview{}, nil
	}
	items, err := s.volumes.ListVolumes()
	if err != nil {
		return nil, err
	}
	sources := projectStorageSources(items)
	for index := range sources {
		sources[index].Icon = s.volumeIcon(sources[index].Path)
	}
	return sources, nil
}

// StorageSourcesChangedEvent is emitted, without a payload (nil: it is
// registered as a Void event, and Wails drops a Void event carrying data), when
// the set of mounted volumes changed; the frontend answers by calling
// GetStorageSources again. A signal rather than the list, so there is one
// shape for the list.
const StorageSourcesChangedEvent = "storage-sources-changed"

// volumeChangeDebounce coalesces the burst a single disk produces: a mount
// notification, then diskutil settling, sometimes a rename. A var so tests can
// shorten it.
var volumeChangeDebounce = 350 * time.Millisecond

// StartVolumeWatch subscribes to mount changes for the life of the process.
// Without a watcher this is a no-op: the reference updates its disk list live
// when a drive is plugged in, and so does this, but the source page is still
// correct at launch and after each scan without it.
func (s *Service) StartVolumeWatch() {
	if s.volumeWatcher == nil {
		return
	}
	s.volumeWatchMu.Lock()
	defer s.volumeWatchMu.Unlock()
	if s.volumeWatchStop != nil {
		return
	}
	stop, err := s.volumeWatcher.WatchVolumes(s.volumesChanged)
	if err != nil {
		log.Printf("volume watch unavailable: %v", err)
		return
	}
	s.volumeWatchStop = stop
}

// StopVolumeWatch unregisters the observer and drops a pending notification.
// Wired to the app's OnShutdown. A notification already in flight when the
// observer is removed finds the watch disarmed and does nothing.
func (s *Service) StopVolumeWatch() {
	s.volumeWatchMu.Lock()
	defer s.volumeWatchMu.Unlock()
	s.volumeWatchArmed = false
	if s.volumeWatchTimer != nil {
		s.volumeWatchTimer.Stop()
	}
	if s.volumeWatchStop != nil {
		s.volumeWatchStop()
		s.volumeWatchStop = nil
	}
}

// volumesChanged is the watcher's callback: it (re)arms the debounce. One
// timer for the life of the service, reset per notification.
func (s *Service) volumesChanged() {
	s.volumeWatchMu.Lock()
	defer s.volumeWatchMu.Unlock()
	if s.volumeWatchStop == nil {
		return // stopped; a late notification changes nothing
	}
	s.volumeWatchArmed = true
	if s.volumeWatchTimer == nil {
		s.volumeWatchTimer = time.AfterFunc(volumeChangeDebounce, s.flushVolumeChange)
		return
	}
	s.volumeWatchTimer.Reset(volumeChangeDebounce)
}

// flushVolumeChange runs once the burst is over: the icon cache goes (a
// re-inserted disk may carry a different icon, and a path that failed while
// unmounted may work now) and one event is emitted -- unless the watch was
// stopped while the timer was pending.
func (s *Service) flushVolumeChange() {
	s.volumeWatchMu.Lock()
	armed := s.volumeWatchArmed
	s.volumeWatchArmed = false
	s.volumeWatchMu.Unlock()
	if !armed {
		return
	}
	s.iconMu.Lock()
	s.iconCache = make(map[string]string)
	s.iconMu.Unlock()
	s.emit(StorageSourcesChangedEvent, nil)
}

// volumeIconPixels is the rendered size of a source row's icon: the row shows
// it at 32pt, so 64px covers a Retina display without upscaling.
const volumeIconPixels = 64

// volumeIcon returns the data URL for a mount point's system icon, cached for
// the life of the process: the icon changes about as often as the volume's
// name does, and the source list is re-read after every scan.
func (s *Service) volumeIcon(path string) string {
	if s.icons == nil || path == "" {
		return ""
	}
	s.iconMu.Lock()
	icon, ok := s.iconCache[path]
	s.iconMu.Unlock()
	if ok {
		return icon
	}
	// The lookup runs outside the lock: one stalled mount (a dead network share)
	// must cost only its own row, not every concurrent caller.
	png, err := s.icons.VolumeIcon(path, volumeIconPixels)
	icon = ""
	if err == nil && len(png) > 0 {
		icon = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	// A failure is cached too: retrying on every refresh would only repeat it.
	// The cache is dropped whenever the mounted set changes.
	s.iconMu.Lock()
	s.iconCache[path] = icon
	s.iconMu.Unlock()
	return icon
}

// RevealStorageSource shows a storage source in Finder. It takes the source's
// identity, not a path: the path comes from the volume catalog, so a caller
// cannot use this to reveal an arbitrary location (DDD invariant 17, ADR-0051).
// recordSnapshotVolume stores the scanned volume's capacity, used and free bytes
// on the snapshot. A failure is not fatal: the space map then omits the balancing
// entry rather than guessing one.
func (s *Service) recordSnapshotVolume(snapshotID int64, root string, items []ports.Volume) uint64 {
	sources := projectStorageSources(items)
	root = filepath.Clean(root)
	for _, source := range sources {
		if filepath.Clean(source.Path) != root {
			continue
		}
		if volumeStore, ok := s.store.(snapshotVolumeStore); ok {
			_ = volumeStore.SetSnapshotVolume(snapshotID, source.TotalBytes, source.UsedBytes, source.FreeBytes)
		}
		return source.UsedBytes
	}
	return 0
}

// snapshotIssueStore and snapshotVolumeStore are the two optional store
// capabilities that survive ADR-0055: recording the paths the walk could not read,
// and recording the volume state the space map balances against (ADR-0052 §4).
type snapshotIssueStore interface {
	InsertIssues(int64, []scan.Issue) error
}

// subtreeRemovingStore lets a deletion be taken out of the result in place. An
// optional capability like the two above: a store that cannot do it leaves the
// result stale, and the user is told to re-scan rather than shown a wrong map.
type subtreeRemovingStore interface {
	RemoveSubtree(int64, string) (scan.SubtreeRemoval, error)
}

type snapshotVolumeStore interface {
	SetSnapshotVolume(snapshotID int64, total, used, free uint64) error
}

// legacyCacheRemover is implemented by adapters that can delete the superseded
// SQLite snapshot cache (ADR-0054).
type legacyCacheRemover interface {
	RemoveLegacySnapshotCache(string) (int64, []string, error)
}

// removeLegacyCache deletes the cache the SQLite store left behind. It runs once
// per launch in the background maintenance stage and never affects startup or a
// scan: a cleanup failure is our problem, not the user's (ADR-0054 §5). It is
// idempotent — a missing file is the normal case.
func (s *Service) removeLegacyCache() {
	if s.legacyCacheDir == "" {
		return
	}
	remover, ok := s.files.(legacyCacheRemover)
	if !ok {
		return
	}
	freed, removed, err := remover.RemoveLegacySnapshotCache(s.legacyCacheDir)
	if len(removed) > 0 {
		// The app deleted files on the user's disk; that must never be silent
		// (ADR-0054 §6).
		log.Printf("legacy snapshot cache removed: %v, freed %d bytes", removed, freed)
	}
	if err != nil {
		log.Printf("legacy snapshot cache cleanup: %v", err)
	}
}

// groupVolume is one APFS volume-group member that the walk cannot enter: it is
// a mount point inside the scan root, so the scanner skips it at the mount
// boundary, but its space is real space in the same container (ADR-0052 §3).
//
// Its size comes from statfs, not from a walk. Preboot is mostly APFS clones of
// the system volume, so summing allocated sizes there reports 27.60 GiB where
// the volume physically holds 8.36 GiB (R-053 §3.4) — enough to push the tree
// total past the disk's used space.
type groupVolume struct {
	id        string
	path      string
	name      string
	usedBytes int64
}

// groupVolumesInRoot lists the auxiliary volumes of the scan root's volume group
// together with the ancestor paths whose roll-up must include them. The data
// volume is deliberately absent: its content is already reached through the
// firmlinks at /Users, /Applications and friends, so counting the mount point
// again would double it.
func (s *Service) groupVolumesInRoot(root string, items []ports.Volume) ([]groupVolume, map[string]int64) {
	if len(items) == 0 {
		return nil, nil
	}
	root = filepath.Clean(root)
	volumes := make([]groupVolume, 0, len(items))
	watched := map[string]int64{root: 0}
	for _, item := range items {
		path := filepath.Clean(item.Path)
		if item.Kind != "system_auxiliary" || item.UsedBytes == 0 || path == root || !pathWithinRoot(root, path) {
			continue
		}
		// A volume mounted inside another volume's mount point — e.g.
		// /System/Volumes/Update/mnt1, the unsealed system volume staged under
		// the update mount, whose bytes are the same bytes the walk already
		// counts at / — is unreachable by the walk (it never crosses into the
		// outer volume), so attachGroupVolumes can never place it. Pre-counting
		// it puts phantom bytes in the numerator from t=0 that fall out again
		// at the terminal state: measured 19.4 GB and the bar pinned at 100%
		// for the last seconds of the walk (R-067 §2.3).
		if nestedInAnotherVolume(root, path, items) {
			continue
		}
		volumes = append(volumes, groupVolume{id: item.ID, path: path, name: filepath.Base(path), usedBytes: int64(item.UsedBytes)})
		for ancestor := filepath.Dir(path); ; ancestor = filepath.Dir(ancestor) {
			watched[ancestor] = 0
			if ancestor == root || ancestor == "/" || ancestor == "." {
				break
			}
		}
	}
	if len(volumes) == 0 {
		return nil, nil
	}
	return volumes, watched
}

// nestedInAnotherVolume reports whether path sits inside some OTHER volume's
// mount point (the scan root itself does not count as "another volume").
func nestedInAnotherVolume(root, path string, items []ports.Volume) bool {
	for _, other := range items {
		otherPath := filepath.Clean(other.Path)
		if otherPath == path || otherPath == root {
			continue
		}
		if pathWithinRoot(otherPath, path) {
			return true
		}
	}
	return false
}

func pathWithinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// attachGroupVolumes writes one non-walkable node per auxiliary volume and folds
// its bytes into every ancestor's roll-up, so the space map adds up to the same
// container the volume row reports. The nodes carry kind "volume", which grants
// no capabilities: they must never be previewable, revealable or cleanable
// (ADR-0052 §3, DDD invariants 9 and 17).
func (s *Service) attachGroupVolumes(snapshotID int64, firstNodeID int64, volumes []groupVolume, watched map[string]int64, directorySizes map[int64]scan.DirectorySize) (nodes int64, bytes int64) {
	attached := make([]scan.Node, 0, len(volumes))
	nextID := firstNodeID
	for _, volume := range volumes {
		parentID := watched[filepath.Dir(volume.path)]
		if parentID == 0 {
			continue
		}
		attached = append(attached, scan.Node{
			ID: nextID, ParentID: parentID, Path: volume.path, Name: volume.name, Kind: "volume",
			LogicalSize: volume.usedBytes, AllocatedSize: volume.usedBytes, OwnedAllocated: volume.usedBytes,
			VolumeID: volume.id, Confidence: "estimated", SizeBasis: "volume_statfs_v1", HasChildren: false,
		})
		nextID++
		for ancestor := filepath.Dir(volume.path); ; ancestor = filepath.Dir(ancestor) {
			if ancestorID := watched[ancestor]; ancestorID != 0 {
				size := directorySizes[ancestorID]
				size.LogicalSize += volume.usedBytes
				size.AllocatedSize += volume.usedBytes
				size.OwnedAllocated += volume.usedBytes
				if size.Confidence == "" || size.Confidence == "exact" {
					size.Confidence = "estimated"
				}
				directorySizes[ancestorID] = size
			}
			if ancestor == "/" || ancestor == "." {
				break
			}
			if _, ok := watched[ancestor]; !ok {
				break
			}
		}
		bytes += volume.usedBytes
	}
	if len(attached) == 0 {
		return 0, 0
	}
	if err := s.store.InsertNodes(snapshotID, attached); err != nil {
		return 0, 0
	}
	return int64(len(attached)), bytes
}

func (s *Service) RevealStorageSource(sourceID string) (NodeActionResult, error) {
	if sourceID == "" {
		return NodeActionResult{Code: "invalid_request", Message: "storage source is required"}, nil
	}
	if s.preview == nil {
		return NodeActionResult{Code: "platform_error", Message: "finder reveal is unavailable"}, nil
	}
	sources, err := s.GetStorageSources()
	if err != nil {
		return NodeActionResult{}, err
	}
	for _, source := range sources {
		if source.ID != sourceID {
			continue
		}
		path, revealErr := s.preview.Reveal(source.Path)
		if revealErr != nil {
			return NodeActionResult{Code: "platform_error", Message: revealErr.Error()}, nil
		}
		return NodeActionResult{OK: true, Code: "ok", Message: "操作已交给 macOS", Path: path}, nil
	}
	return NodeActionResult{Code: "not_found", Message: "storage source not found: " + sourceID}, nil
}

func projectStorageSources(items []ports.Volume) []StorageSourceOverview {
	groups := make(map[string][]ports.Volume)
	for _, item := range items {
		if item.Kind == "system_auxiliary" || item.Role == "system_auxiliary" {
			continue
		}
		key := "volume:" + item.ID
		if item.VolumeGroupID != "" {
			key = "group:" + item.VolumeGroupID
		} else if item.ID == "" {
			key = "path:" + item.Path
		}
		groups[key] = append(groups[key], item)
	}

	result := make([]StorageSourceOverview, 0, len(groups))
	for key, members := range groups {
		sort.SliceStable(members, func(i, j int) bool {
			leftRank, rightRank := storageMemberSortRank(members[i].Path), storageMemberSortRank(members[j].Path)
			if leftRank != rightRank {
				return leftRank < rightRank
			}
			return members[i].Path < members[j].Path
		})
		result = append(result, storageSourceFromVolumes(key, members))
	}
	sort.SliceStable(result, func(i, j int) bool {
		leftRank, rightRank := storageSourceSortRank(result[i].Path), storageSourceSortRank(result[j].Path)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func storageSourceFromVolumes(key string, items []ports.Volume) StorageSourceOverview {
	rootIndex := -1
	for index := range items {
		if items[index].Path == "/" {
			rootIndex = index
			break
		}
	}
	primaryIndex := 0
	if rootIndex >= 0 {
		primaryIndex = rootIndex
	}
	primary := items[primaryIndex]
	grouped := strings.HasPrefix(key, "group:")

	result := StorageSourceOverview{
		ID:         "storage-source:" + key,
		Name:       primary.Name,
		Path:       primary.Path,
		Kind:       primary.Kind,
		Permission: storagePermission(items),
		Scannable:  primary.Scannable,
		Members:    make([]StorageVolumeMember, 0, len(items)),
	}
	if rootIndex >= 0 {
		result.Name = items[rootIndex].Name
		result.Path = "/"
	}
	if grouped {
		result.Kind = "apfs_volume_group"
	}
	result.TotalBytes, result.UsedBytes, result.FreeBytes, result.UsageBasis = storageSourceCapacity(items, primaryIndex, grouped)
	if grouped && len(items) > 1 {
		result.Message = "System/Data 属于同一 APFS 卷组；入口使用共享容器容量，成员占用单独保留"
	} else {
		result.Message = primary.Message
	}
	if result.Permission == "partial" {
		result.Message = "部分技术卷不可访问；" + result.Message
	}
	for _, item := range items {
		role := item.Role
		if role == "" {
			role = item.Kind
		}
		result.Members = append(result.Members, StorageVolumeMember{
			ID:               item.ID,
			Name:             item.Name,
			Path:             item.Path,
			Kind:             item.Kind,
			Role:             role,
			VolumeTotalBytes: item.TotalBytes,
			VolumeUsedBytes:  item.UsedBytes,
			VolumeFreeBytes:  item.FreeBytes,
			UsageBasis:       item.UsageBasis,
			Permission:       item.Permission,
			Scannable:        item.Scannable,
		})
	}
	return result
}

func storageSourceCapacity(items []ports.Volume, primaryIndex int, grouped bool) (uint64, uint64, uint64, string) {
	capacityIndex := primaryIndex
	for index := range items {
		if items[index].ContainerTotalBytes > 0 {
			capacityIndex = index
			if items[index].Path == "/" {
				break
			}
		}
	}
	item := items[capacityIndex]
	if item.ContainerTotalBytes > 0 {
		basis := "apfs_container_v1"
		if grouped {
			basis = "apfs_container_shared_v1"
		}
		free := item.ContainerFreeBytes
		if free == 0 {
			free = item.FreeBytes
		}
		return item.ContainerTotalBytes, item.ContainerUsedBytes, free, basis
	}
	return item.TotalBytes, item.UsedBytes, item.FreeBytes, item.UsageBasis
}

func storagePermission(items []ports.Volume) string {
	available, unavailable := false, false
	for _, item := range items {
		switch item.Permission {
		case "available":
			available = true
		case "unavailable":
			unavailable = true
		}
	}
	if available && unavailable {
		return "partial"
	}
	if unavailable {
		return "unavailable"
	}
	if available {
		return "available"
	}
	return "unknown"
}

func storageMemberSortRank(path string) int {
	switch {
	case path == "/":
		return 0
	case path == "/System/Volumes/Data":
		return 1
	case strings.HasPrefix(path, "/Volumes/"):
		return 2
	default:
		return 3
	}
}

func storageSourceSortRank(path string) int {
	switch {
	case path == "/":
		return 0
	case strings.HasPrefix(path, "/Volumes/"):
		return 1
	default:
		return 2
	}
}

func (s *Service) StartScan(options ScanOptions) (ScanStatus, error) {
	if err := s.waitForRecovery(); err != nil {
		return ScanStatus{}, err
	}
	s.cancelCacheMaintenance()
	root, err := s.files.NormalizeScanRoot(options.Root)
	if err != nil {
		return ScanStatus{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	taskID := fmt.Sprintf("scan-%d-%d", time.Now().UnixNano(), s.serial.Add(1))
	snapshotID, err := s.store.CreateSnapshot(taskID, root)
	if err != nil {
		cancel()
		return ScanStatus{}, err
	}
	task := &scanTask{taskID: taskID, snapshotID: snapshotID, root: root, state: "running", phase: string(scan.PhaseCatalog), cancel: cancel, affectedParents: make(map[int64]struct{})}
	if s.scanTotals != nil {
		task.expectedTotal = s.scanTotals.LoadScanTotal(root)
	}
	s.mu.Lock()
	s.tasks[taskID] = task
	s.mu.Unlock()
	go s.runScan(ctx, task)
	return task.status(), nil
}

func (s *Service) handleScanPhase(ctx context.Context, task *scanTask, phase scan.Phase, persistBarrier func() error, emitProgressIfDue func()) error {
	if phase == scan.PhaseTopLevelPublish {
		if err := persistBarrier(); err != nil {
			return err
		}
	}
	task.mu.Lock()
	task.phase = string(phase)
	task.mu.Unlock()
	if err := s.store.UpdateSnapshotPhase(task.snapshotID, string(phase)); err != nil {
		return err
	}
	emitProgressIfDue()
	return ctx.Err()
}

func (s *Service) runScan(ctx context.Context, task *scanTask) {
	// Held for the whole scan and released on every exit — normal, cancelled and
	// failed alike, which is why it is a defer rather than a call on the success
	// path (ADR-0058 §1).
	defer s.memoryLimiter.hold()()
	scanStarted := time.Now()

	// One enumeration, shared by both users of it below. It used to run twice —
	// once for the snapshot's volume record, once for the group volumes — and each
	// pass spawns `diskutil info` per volume, so the walk could not start for
	// about 1.5s after the user clicked scan.
	//
	// Captured at the start of the walk, not at the end: the source page's numbers
	// come from the same enumeration, so both pages describe one disk state
	// (ADR-0052 §4). It runs here rather than in StartScan because StartScan is
	// what the UI waits on before it can leave the source page — the capture only
	// has to precede the walk, not the call's return.
	var volumeItems []ports.Volume
	if s.volumes != nil {
		volumeItems, _ = s.volumes.ListVolumes()
	}
	if volumeUsed := s.recordSnapshotVolume(task.snapshotID, task.root, volumeItems); volumeUsed > 0 {
		task.mu.Lock()
		task.volumeUsed = volumeUsed
		task.mu.Unlock()
	}

	type persistEvent struct {
		nodes   []scan.Node
		barrier chan error
	}

	// Keep scanning and snapshot writes overlapped without allowing an
	// unbounded in-memory node queue. Barrier events preserve the top-level
	// publish boundary while the deep scan continues in parallel with
	// persistence.
	events := make(chan persistEvent, scanPersistQueueCapacity)
	// Recycled batch buffers, see enqueueBatch below (ADR-0057 §1). Sized above
	// the queue so a producer normally finds a free buffer without allocating.
	freeBatches := make(chan []scan.Node, scanPersistQueueCapacity+2)
	for index := 0; index < cap(freeBatches); index++ {
		freeBatches <- make([]scan.Node, 0, scanBatchBufferFloor)
	}
	writerDone := make(chan struct{})
	var writerMu sync.Mutex
	var writerErr error
	setWriterErr := func(err error) {
		if err == nil {
			return
		}
		writerMu.Lock()
		if writerErr == nil {
			writerErr = err
		}
		writerMu.Unlock()
	}
	getWriterErr := func() error {
		writerMu.Lock()
		defer writerMu.Unlock()
		return writerErr
	}
	persistenceStopped := func() error {
		if err := getWriterErr(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return errors.New("scan persistence stopped")
	}

	// Sized once: growing this by append copies roughly five times its final
	// size over a scan, which measured 46 MB for a buffer that ends at 9 MiB.
	batch := make([]scan.Node, 0, scanBatchSize)
	var pendingNodes, pendingFiles, pendingDirectories, pendingBytes int64
	topLevelDirectory := make(map[int64]int64)
	committedProgressSizes := make(map[int64]scan.DirectorySize)
	pendingProgressSizes := make(map[int64]scan.DirectorySize)
	var rootID int64
	var progressMu sync.Mutex
	addProgressSize := func(nodeID int64, node scan.Node) {
		if nodeID == 0 {
			return
		}
		total := pendingProgressSizes[nodeID]
		total.LogicalSize += node.LogicalSize
		total.AllocatedSize += node.AllocatedSize
		total.OwnedAllocated += node.OwnedAllocated
		total.Confidence = "partial"
		total.SizeBasis = "descendant_sum_v1_partial"
		pendingProgressSizes[nodeID] = total
	}
	flush := func(force bool) error {
		if len(batch) == 0 {
			return nil
		}
		if !force {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := s.store.InsertNodes(task.snapshotID, batch); err != nil {
			return err
		}
		batch = batch[:0]
		pendingNodes = 0
		pendingFiles = 0
		pendingDirectories = 0
		pendingBytes = 0

		changed := make(map[int64]scan.DirectorySize, len(pendingProgressSizes))
		for nodeID, delta := range pendingProgressSizes {
			total := committedProgressSizes[nodeID]
			total.LogicalSize += delta.LogicalSize
			total.AllocatedSize += delta.AllocatedSize
			total.OwnedAllocated += delta.OwnedAllocated
			total.Confidence = "partial"
			total.SizeBasis = "descendant_sum_v1_partial"
			committedProgressSizes[nodeID] = total
			changed[nodeID] = total
		}
		pendingProgressSizes = make(map[int64]scan.DirectorySize)
		if len(changed) > 0 {
			return s.store.UpdateDirectorySizes(task.snapshotID, changed)
		}
		return nil
	}
	lastProgress := time.Time{}
	emitProgressIfDue := func() {
		progressMu.Lock()
		defer progressMu.Unlock()
		if lastProgress.IsZero() || time.Since(lastProgress) >= 200*time.Millisecond {
			s.emitProgress(task)
			lastProgress = time.Now()
		}
	}
	// The walk skips mount points, so the volume group's auxiliary volumes are
	// attached afterwards (ADR-0052 §3). Their roll-up needs the node IDs of the
	// ancestor directories, which only the emit stream knows.
	groupVolumes, watchedPaths := s.groupVolumesInRoot(task.root, volumeItems)
	// Counted from t=0, not when the volumes are attached at the end: their sizes
	// are already known from statfs and they are certain to land in the tree, so
	// deferring them puts a 12-point jump in the last tenth of the scan
	// (ADR-0053 §1, R-054 §3.2). attachGroupVolumes must not add them again.
	var preCountedBytes int64
	for _, volume := range groupVolumes {
		preCountedBytes += volume.usedBytes
	}
	if preCountedBytes > 0 {
		task.mu.Lock()
		task.preCounted = preCountedBytes
		task.mu.Unlock()
	}
	appendNode := func(node scan.Node) error {
		batch = append(batch, node)
		if watchedPaths != nil {
			if _, watched := watchedPaths[node.Path]; watched {
				watchedPaths[node.Path] = node.ID
			}
		}
		if node.ParentID == 0 {
			rootID = node.ID
			topLevelDirectory[node.ID] = node.ID
		} else if node.Kind == "directory" {
			topLevelDirectory[node.ID] = topLevelDirectory[node.ParentID]
		}
		if node.Kind != "directory" {
			addProgressSize(rootID, node)
			topLevelID := topLevelDirectory[node.ParentID]
			if topLevelID != 0 && topLevelID != rootID {
				addProgressSize(topLevelID, node)
			}
		}
		task.mu.Lock()
		if node.ParentID != 0 && len(task.affectedParents) < maxAffectedParents {
			task.affectedParents[node.ParentID] = struct{}{}
		}
		task.nodes++
		if node.Kind == "file" || node.Kind == "symlink" {
			task.files++
		}
		if node.Kind == "directory" {
			task.directories++
			pendingDirectories++
		}
		task.bytes += node.OwnedAllocated
		task.mu.Unlock()
		pendingNodes++
		if node.Kind == "file" || node.Kind == "symlink" {
			pendingFiles++
		}
		pendingBytes += node.OwnedAllocated
		if len(batch) >= scanBatchSize {
			if err := flush(false); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
		emitProgressIfDue()
		return nil
	}
	go func() {
		defer close(writerDone)
		for event := range events {
			if event.barrier != nil {
				err := flush(false)
				event.barrier <- err
				if err != nil && !errors.Is(err, context.Canceled) {
					setWriterErr(err)
					return
				}
				continue
			}
			for _, node := range event.nodes {
				if err := appendNode(node); err != nil {
					setWriterErr(err)
					return
				}
			}
			select {
			case freeBatches <- event.nodes:
			default:
			}
		}
		if err := flush(true); err != nil {
			setWriterErr(err)
		}
	}()
	// ADR-0057 §1: the scanner's batch is only valid until the callback returns,
	// so it is copied here before crossing to the writer goroutine. The copies go
	// into recycled buffers rather than a fresh slice per batch — there are
	// 421,701 batches, and allocating one each is exactly the cost the new
	// contract exists to remove (R-058 §4.1).
	enqueueBatch := func(nodes []scan.Node) error {
		if len(nodes) == 0 {
			return nil
		}
		// Waits for a buffer instead of allocating one. The free list is normally
		// empty — the scanner outruns the writer — so allocating on that path
		// meant allocating per batch, which is the cost this exists to remove.
		// Blocking here bounds the buffers to the free list and costs nothing the
		// bounded events queue was not already costing.
		var buffer []scan.Node
		select {
		case buffer = <-freeBatches:
			buffer = buffer[:0]
		case <-ctx.Done():
			return ctx.Err()
		case <-writerDone:
			return persistenceStopped()
		}
		buffer = append(buffer, nodes...)
		select {
		case events <- persistEvent{nodes: buffer}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-writerDone:
			return persistenceStopped()
		}
	}
	enqueue := func(node scan.Node) error {
		return enqueueBatch([]scan.Node{node})
	}
	persistBarrier := func() error {
		ack := make(chan error, 1)
		event := persistEvent{barrier: ack}
		select {
		case events <- event:
		case <-ctx.Done():
			return ctx.Err()
		case <-writerDone:
			return persistenceStopped()
		}
		select {
		case err := <-ack:
			return err
		case <-writerDone:
			return persistenceStopped()
		}
	}
	var result scan.Result
	var scanErr error
	if batchScanner, ok := s.scanner.(ports.BatchScanner); ok {
		result, scanErr = batchScanner.ScanBatched(ctx, task.root, enqueueBatch, func(phase scan.Phase) error {
			return s.handleScanPhase(ctx, task, phase, persistBarrier, emitProgressIfDue)
		})
	} else {
		result, scanErr = s.scanner.Scan(ctx, task.root, enqueue, func(phase scan.Phase) error {
			return s.handleScanPhase(ctx, task, phase, persistBarrier, emitProgressIfDue)
		})
	}
	// Stamped where the bar stops moving: everything after this is roll-up and
	// publish, which the user sees as the progress standing still.
	//
	// Note what `walk` therefore measures. enqueueBatch is bounded, so when the
	// store writer falls behind the walk it blocks and the scanner runs at the
	// writer's pace. Measured on this machine: the scanner alone over `/` is 13.8s
	// with a no-op emitter, and the same walk inside the application is 29.4s with
	// a 128ms tail. So `walk` is the walk AND the insert, and the insert is the
	// scan's bottleneck -- not the filesystem (R-067).
	walkEnded := time.Now()
	close(events)
	<-writerDone
	if scanErr == nil {
		scanErr = getWriterErr()
	}
	if scanErr != nil && (pendingNodes > 0 || pendingFiles > 0 || pendingDirectories > 0 || pendingBytes > 0) {
		task.mu.Lock()
		task.nodes -= pendingNodes
		task.files -= pendingFiles
		task.directories -= pendingDirectories
		task.bytes -= pendingBytes
		task.mu.Unlock()
	}
	// Rewritten in place rather than copied into a second map of the same size:
	// result is local to this scan and nothing else reads DirectorySizes, and a
	// second 589k-entry map cost 79 MB (ADR-0057 §3, R-058 §4.3).
	directorySizes := result.DirectorySizes
	if directorySizes == nil {
		directorySizes = map[int64]scan.DirectorySize{}
	}
	if scanErr == nil && len(directorySizes) > 0 {
		basis := "descendant_sum_v1"
		partial := len(result.Issues) > 0
		if partial {
			basis = "descendant_sum_v1_partial"
		}
		for nodeID, size := range directorySizes {
			if partial {
				size.Confidence = "partial"
			}
			size.SizeBasis = basis
			directorySizes[nodeID] = size
		}
	}
	if scanErr == nil && len(groupVolumes) > 0 {
		// result.Nodes is also the highest ID the scanner handed out: it numbers
		// nodes from 1 without gaps.
		attachedNodes, attachedBytes := s.attachGroupVolumes(task.snapshotID, result.Nodes+1, groupVolumes, watchedPaths, directorySizes)
		if attachedNodes > 0 {
			result.Nodes += attachedNodes
			result.Bytes += attachedBytes
			task.mu.Lock()
			task.nodes += attachedNodes
			task.bytes += attachedBytes
			// These bytes are now inside task.bytes, so the pre-count of the same
			// volumes has to give way here rather than at the terminal state.
			// countedBytesLocked only dropped it when the state changed, and the
			// gap between the two is a window where both are counted: measured, the
			// numerator jumped to 118% of the denominator in it.
			//
			// Subtracted rather than zeroed, and floored at zero: attach can still
			// skip a volume whose parent directory the walk failed to reach (a
			// permission hole, a race with an unmount), and what it did not add
			// stays in the numerator. Since nested mounts are excluded up front
			// (nestedInAnotherVolume), the two normally now agree to the byte.
			task.preCounted -= attachedBytes
			if task.preCounted < 0 {
				task.preCounted = 0
			}
			task.mu.Unlock()
		}
	}
	for _, issue := range result.Issues {
		task.mu.Lock()
		task.issues = append(task.issues, issue.Path+": "+issue.Message)
		task.mu.Unlock()
	}
	state := "completed"
	failure := ""
	if errors.Is(scanErr, context.Canceled) {
		state = "cancelled"
	} else if scanErr != nil {
		state = "failed"
		failure = scanErr.Error()
		task.mu.Lock()
		task.error = failure
		task.mu.Unlock()
	}
	if len(result.Issues) > 0 && state == "completed" {
		state = "completed_with_issues"
	}

	// One terminal state, not two. Without a durable copy there is nothing to
	// publish in the background, so the result becomes queryable here and the
	// scan is over (ADR-0055, superseding ADR-0047's split and DDD invariant 36).
	finishErr := error(nil)
	if len(directorySizes) > 0 {
		finishErr = s.store.UpdateDirectorySizes(task.snapshotID, directorySizes)
	}
	if finishErr == nil {
		if issueStore, ok := s.store.(snapshotIssueStore); ok && len(result.Issues) > 0 {
			finishErr = issueStore.InsertIssues(task.snapshotID, result.Issues)
		}
	}

	task.mu.Lock()
	status := task.statusLocked()
	task.mu.Unlock()

	if finishErr == nil {
		finishErr = s.store.FinishScan(task.snapshotID, state, failure, status.Nodes, status.Files, status.Directories, status.Bytes, int64(len(result.Issues)))
	}
	task.mu.Lock()
	if finishErr != nil {
		task.state = "failed"
		task.error = finishErr.Error()
	} else {
		task.state = state
	}
	finalState := task.state
	task.mu.Unlock()
	// The only record of how long a scan took. Without it the question "how long
	// did that actually take" has no answer after the fact, which is where this
	// line came from. Reachable with:
	//   ./bin/marmot.app/Contents/MacOS/marmot
	// stderr from a bundle launched by Finder or `open` goes nowhere.
	// Arm the one map line that matters: the first root map for this snapshot,
	// whose timestamp minus this one is the wait the user sees after the bar stops.
	s.firstMap.Store(task.snapshotID)
	// A completed walk's final count becomes the next scan's denominator; a
	// cancelled or failed walk counted less than the truth and teaches nothing.
	if s.scanTotals != nil && (finalState == "completed" || finalState == "completed_with_issues") {
		if err := s.scanTotals.StoreScanTotal(task.root, status.Bytes); err != nil {
			log.Printf("scan total for %s not stored: %v", task.root, err)
		}
	}
	log.Printf("scan %s finished: state=%s elapsed=%s walk=%s tail=%s nodes=%d files=%d dirs=%d bytes=%d issues=%d",
		task.taskID, finalState, time.Since(scanStarted).Round(time.Millisecond),
		walkEnded.Sub(scanStarted).Round(time.Millisecond),
		time.Since(walkEnded).Round(time.Millisecond),
		status.Nodes, status.Files, status.Directories, status.Bytes, len(result.Issues))
	s.emitProgress(task)
	s.scheduleCacheMaintenance()
}

func (s *Service) GetScanStatus(taskID string) (ScanStatus, error) {
	if err := s.waitForRecovery(); err != nil {
		return ScanStatus{}, err
	}
	s.mu.RLock()
	task := s.tasks[taskID]
	s.mu.RUnlock()
	if task == nil {
		snapshot, err := s.store.SnapshotByTaskID(taskID)
		if err != nil {
			return ScanStatus{}, fmt.Errorf("scan task not found: %s", taskID)
		}
		issues := []string(nil)
		if snapshot.Issues > 0 {
			issues = []string{fmt.Sprintf("%d scan issues preserved", snapshot.Issues)}
		}
		// The task record is gone but the result is still in memory — the store
		// keeps it until the next scan replaces it. Nothing is ever recovered from
		// disk (ADR-0055).
		return ScanStatus{TaskID: snapshot.TaskID, SnapshotID: snapshot.ID, Root: snapshot.Root, State: snapshot.State, Phase: snapshot.Phase, Nodes: snapshot.NodeCount, Files: snapshot.FileCount, Directories: snapshot.DirCount, Bytes: snapshot.Bytes, Issues: issues, Error: snapshot.Error}, nil
	}
	return task.status(), nil
}

func (s *Service) CancelScan(taskID string) (ScanStatus, error) {
	s.mu.RLock()
	task := s.tasks[taskID]
	s.mu.RUnlock()
	if task == nil {
		return ScanStatus{}, fmt.Errorf("scan task not found: %s", taskID)
	}
	task.cancel()
	return task.status(), nil
}

func (s *Service) GetChildren(query ChildrenQuery) (ChildrenResult, error) {
	if err := s.waitForRecovery(); err != nil {
		return ChildrenResult{}, err
	}
	if query.Limit <= 0 || query.Limit > 1000 {
		query.Limit = 1000
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	nodes, err := s.store.Children(query.SnapshotID, query.ParentID, query.Limit, query.Offset)
	if err != nil {
		return ChildrenResult{}, err
	}
	return ChildrenResult{Limit: query.Limit, Offset: query.Offset, Nodes: nodes}, nil
}

// Logged because this is the gap nobody could account for: the scan's own line
// lands when the walk ends, and then the result page appears some time later with
// nothing written down in between. Twice that question could only be answered by
// re-running things under a probe.
func (s *Service) GetMap(query MapQuery) (MapResult, error) {
	if err := s.waitForRecovery(); err != nil {
		return MapResult{}, err
	}
	if query.SnapshotID <= 0 || query.ParentID <= 0 {
		return MapResult{}, errors.New("snapshot and parent are required")
	}
	if query.Limit <= 0 {
		query.Limit = 256
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	if query.Measure == "" {
		query.Measure = "owned_allocated"
	}
	if query.Measure != "owned_allocated" {
		return MapResult{}, errors.New("unsupported map measure")
	}
	if query.Depth < 0 {
		return MapResult{}, errors.New("map depth cannot be negative")
	}
	// ADR-0059 §3: twelve levels, matching the deepest the reference was observed
	// to draw. It stays affordable because MinSweeps prunes sub-pixel arcs in the
	// store rather than in the renderer.
	if query.Depth > 11 {
		query.Depth = 11
	}
	// ADR-0048 raised the density target to 2000 arcs; the slim projected entry
	// shape keeps that inside the 256 KB payload ceiling.
	if query.ProjectionLimit <= 0 {
		query.ProjectionLimit = 2000
	}
	if query.ProjectionLimit > 2000 {
		query.ProjectionLimit = 2000
	}
	started := time.Now()
	result, err := s.store.Map(scan.MapQuery{SnapshotID: query.SnapshotID, ParentID: query.ParentID, Limit: query.Limit, Offset: query.Offset, Measure: query.Measure, Depth: query.Depth, ProjectionLimit: query.ProjectionLimit, MinSweeps: query.MinSweeps})
	if err != nil {
		log.Printf("map snapshot=%d parent=%d 失败: %v", query.SnapshotID, query.ParentID, err)
		return MapResult{}, err
	}
	// Only the root of a snapshot, and only when it is slow or the first one:
	// logging every drill-down would bury the line that matters. The line that
	// matters is the first root map after a scan, because the wait between "scan
	// finished" and the result appearing had no record at all.
	elapsed := time.Since(started)
	if query.Offset == 0 && (elapsed > 50*time.Millisecond || s.firstMap.CompareAndSwap(query.SnapshotID, 0)) {
		log.Printf("map snapshot=%d parent=%d 用时 %.0fms，%d 条目",
			query.SnapshotID, query.ParentID, float64(elapsed.Microseconds())/1000, len(result.Entries))
	}
	return mapResult(result), nil
}

func (s *Service) PreviewNode(snapshotID, nodeID int64) (NodeActionResult, error) {
	return s.nodeAction(snapshotID, nodeID, func(path string) (string, error) {
		if s.preview == nil {
			return "", errors.New("preview is unavailable")
		}
		return s.preview.Preview(path)
	})
}

func (s *Service) RevealNode(snapshotID, nodeID int64) (NodeActionResult, error) {
	return s.nodeAction(snapshotID, nodeID, func(path string) (string, error) {
		if s.preview == nil {
			return "", errors.New("finder reveal is unavailable")
		}
		return s.preview.Reveal(path)
	})
}

// GetNodeEntry resolves one node ID into the map entry the current level would
// have carried for it. The space map's outer rings are slim projections with no
// path and no capabilities by design, so acting on one means looking its node up
// by ID (ADR-0048, DDD invariant 17) -- this is that lookup, and it is the only
// way an arc below the current level can be collected. Capabilities come from
// mapEntry, the same place the walked level gets them, so a projected arc gains
// nothing the current level would not already grant.
func (s *Service) GetNodeEntry(snapshotID, nodeID int64) (MapEntry, error) {
	if snapshotID <= 0 || nodeID <= 0 {
		return MapEntry{}, errors.New("snapshot and node are required")
	}
	node, err := s.store.NodeByID(snapshotID, nodeID)
	if err != nil {
		return MapEntry{}, fmt.Errorf("node %d is no longer in the current scan result: %w", nodeID, err)
	}
	return mapEntry(scan.NodeEntry(node)), nil
}

func (s *Service) nodeAction(snapshotID, nodeID int64, action func(string) (string, error)) (NodeActionResult, error) {
	if snapshotID <= 0 || nodeID <= 0 {
		return NodeActionResult{Code: "invalid_request", Message: "snapshot and node are required"}, nil
	}
	node, err := s.store.NodeByID(snapshotID, nodeID)
	if err != nil {
		return NodeActionResult{Code: "stale_node", Message: "该对象已不在当前扫描结果中"}, nil
	}
	if node.Kind != "file" && node.Kind != "directory" && node.Kind != "symlink" {
		return NodeActionResult{Code: "unsupported_node", Message: "该对象不能执行此操作"}, nil
	}
	current, err := s.files.CaptureCleanupItem(node.Path)
	if err != nil || current.Device != node.Device || current.Inode != node.Inode {
		return NodeActionResult{Code: "stale_node", Message: "对象已移动或删除，请重新扫描"}, nil
	}
	path, err := action(node.Path)
	if err != nil {
		return NodeActionResult{Code: "platform_error", Message: err.Error()}, nil
	}
	return NodeActionResult{OK: true, Code: "ok", Message: "操作已交给 macOS", Path: path}, nil
}

func (s *Service) CreateCleanupPlan(request CleanupPlanRequest) (CleanupPlan, error) {
	if request.SnapshotID <= 0 || len(request.Paths) == 0 {
		return CleanupPlan{}, errors.New("snapshot and paths are required")
	}
	paths := make([]string, 0, len(request.Paths))
	seenPaths := make(map[string]struct{}, len(request.Paths))
	for _, rawPath := range request.Paths {
		path, err := cleanup.NormalizePath(rawPath)
		if err != nil {
			return CleanupPlan{}, err
		}
		if _, exists := seenPaths[path]; exists {
			return CleanupPlan{}, errors.New("duplicate cleanup items are not allowed")
		}
		// Checked here, not only where the collect capability is granted: that
		// answer is advisory and travels through the frontend, this one is the
		// gate. Pure policy, no I/O, so it runs before the store is touched.
		if reason := cleanup.DeleteBlock(path); reason != "" {
			log.Printf("cleanup: 拒绝建立计划，%s 受保护（%s）", path, reason)
			return CleanupPlan{}, fmt.Errorf("该对象不允许删除 (%s): %s", reason, path)
		}
		seenPaths[path] = struct{}{}
		paths = append(paths, path)
	}
	if cleanup.HasOverlappingPaths(paths) {
		return CleanupPlan{}, errors.New("parent and child cleanup items cannot overlap")
	}
	items := make([]cleanup.Item, 0, len(paths))
	weights := make([]int64, 0, len(paths))
	for _, path := range paths {
		node, err := s.store.NodeByPath(request.SnapshotID, path)
		if err != nil {
			return CleanupPlan{}, fmt.Errorf("该路径不在当前扫描结果中: %s: %w", path, err)
		}
		if node.ParentID == 0 {
			return CleanupPlan{}, errors.New("scan roots cannot be cleaned")
		}
		item, err := s.files.CaptureCleanupItem(path)
		if err != nil {
			return CleanupPlan{}, err
		}
		weights = append(weights, node.OwnedAllocated)
		if !matchesSnapshotNode(node, item) {
			log.Printf("cleanup: 拒绝建立计划，%s 在扫描之后发生了变化", path)
			return CleanupPlan{}, fmt.Errorf("该对象在扫描之后发生了变化，重新扫描后再试: %s", path)
		}
		items = append(items, item)
	}
	plan := &cleanupPlan{CleanupPlan: CleanupPlan{ID: fmt.Sprintf("plan-%d-%d", time.Now().UnixNano(), s.serial.Add(1)), SnapshotID: request.SnapshotID, Version: 1, State: "draft", Items: len(items), Results: make([]CleanupItemResult, 0, len(items))}, items: items, weights: weights}
	log.Printf("cleanup %s: 建立计划，%d 项", plan.ID, len(items))
	s.mu.Lock()
	s.plans[plan.ID] = plan
	s.mu.Unlock()
	return plan.CleanupPlan, nil
}

func (s *Service) ValidateCleanupPlan(planID string, version int64) (CleanupValidation, error) {
	plan, err := s.getPlan(planID, version)
	if err != nil {
		return CleanupValidation{}, err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	return s.validatePlanLocked(plan), nil
}

func (s *Service) ConfirmCleanupPlan(planID string, version int64) (CleanupPlan, error) {
	plan, err := s.getPlan(planID, version)
	if err != nil {
		return CleanupPlan{}, err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.State != "draft" && plan.State != "validated" {
		return CleanupPlan{}, errors.New("cleanup plan cannot be confirmed in its current state")
	}
	validation := s.validatePlanLocked(plan)
	if !validation.Valid {
		return CleanupPlan{}, errors.New("cleanup plan is invalid")
	}
	plan.State = "confirmed"
	return plan.publicLocked(), nil
}

func (s *Service) ExecuteCleanupPlan(planID string, version int64) (CleanupPlan, error) {
	plan, err := s.getPlan(planID, version)
	if err != nil {
		return CleanupPlan{}, err
	}
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.State != "confirmed" {
		return CleanupPlan{}, errors.New("cleanup plan must be confirmed")
	}
	plan.Results = plan.Results[:0]
	failed := false
	applied := 0
	// Deleting is not what moving to the trash was. The trash is a rename inside
	// one volume -- constant time, whatever the size. Deleting unlinks every file,
	// so a 18.5 GB cache of hundreds of thousands of small files takes as long as
	// it takes, and the call used to return only when all of it was done. With no
	// progress that reads as a hang, on exactly the operation where a user most
	// wants to know something is still happening.
	var totalWeight, doneWeight int64
	for _, weight := range plan.weights {
		totalWeight += weight
	}
	for index, item := range plan.items {
		s.emit("cleanup-progress", CleanupProgress{
			PlanID: plan.ID, Version: plan.Version, Done: index, Total: len(plan.items),
			Current: item.Path, DoneBytes: doneWeight, TotalBytes: totalWeight,
		})
		// Credited immediately after its event, so every report says how many bytes
		// were finished BEFORE the item it names. Done here rather than at the
		// bottom of the loop because the skip and fail paths continue past it, and
		// the item's turn is over either way -- an indicator that stalls on a
		// skipped item is telling the user the wrong thing about a run that is
		// still moving.
		if index < len(plan.weights) {
			doneWeight += plan.weights[index]
		}
		if valid, reason := s.validateCleanupItem(item); !valid {
			failed = true
			log.Printf("cleanup %s: 跳过 %s：%s", plan.ID, item.Path, reason)
			plan.Results = append(plan.Results, CleanupItemResult{Path: item.Path, State: "skipped", Reason: reason})
			continue
		}
		// Deleted outright, with no exception. The trash is on the same volume, so
		// moving there reclaims nothing, and the user asked three times for delete
		// to mean delete -- including for what cannot be rebuilt (ADR-0063 rev.
		// 2026-08-31).
		//
		// What still refuses is cleanup.DeleteBlock, checked when the plan is
		// built: system trees, home folder roots, volume roots, "/". That guard
		// protects the machine from being broken, which is a different question
		// from whether the user may lose their own files.
		//
		// recommendation.IrreplaceableReason keeps its job on the advice side
		// (ADR-0061 §7): the tool will not RECOMMEND deleting a repository. It no
		// longer overrides a path the user staged themselves. What the tool
		// suggests and what the user is permitted to do are not the same question.
		outcome := "已删除"
		// The log line is the only remaining record that this object existed.
		log.Printf("cleanup %s: 删除 %s", plan.ID, item.Path)
		err := s.trash.RemovePermanently(item.Path)
		if err != nil {
			failed = true
			log.Printf("cleanup %s: 失败 %s：%v", plan.ID, item.Path, err)
			plan.Results = append(plan.Results, CleanupItemResult{Path: item.Path, State: "failed", Reason: err.Error()})
			continue
		}
		applied++
		log.Printf("cleanup %s: %s %s", plan.ID, outcome, item.Path)
		plan.Results = append(plan.Results, CleanupItemResult{Path: item.Path, State: "applied", Reason: outcome})
	}
	// Written whatever the outcome. This is the record that was missing when a
	// run "failed" after moving 33 GB: the plan state is one word for a per-item
	// outcome, and without the per-item lines nobody could tell which it was.
	// Take the deleted subtrees out of the result rather than re-scanning the
	// disk to discover something already known exactly. A full re-scan here was
	// 9.5s on 1.8M nodes; this is O(depth + siblings) per item (ADR-0064).
	//
	// ADR-0009 forbade writing a cleanup result back as a scan fact, and it was
	// right while cleanup meant the trash: the bytes were still on the volume, so
	// subtracting them would have understated what the disk held. Deletion is
	// real now, and the subtraction is the accurate statement.
	plan.Removed = s.applyRemovalsToSnapshot(plan.SnapshotID, plan.Results)
	log.Printf("cleanup %s 完成：%d 项，成功 %d，未执行 %d，快照已就地更新 %d 项",
		plan.ID, len(plan.items), applied, len(plan.items)-applied, plan.Removed)
	s.emit("cleanup-progress", CleanupProgress{
		PlanID: plan.ID, Version: plan.Version, Done: len(plan.items), Total: len(plan.items),
		DoneBytes: totalWeight, TotalBytes: totalWeight,
	})
	if failed {
		plan.State = "failed"
	} else {
		plan.State = "applied"
	}
	return plan.publicLocked(), nil
}

func (s *Service) emitProgress(task *scanTask) {
	status := task.status()
	version, _ := s.store.SnapshotVersion(status.SnapshotID)
	task.mu.Lock()
	affected := make([]int64, 0, maxAffectedParents)
	for parentID := range task.affectedParents {
		if len(affected) >= maxAffectedParents {
			break
		}
		affected = append(affected, parentID)
	}
	task.affectedParents = make(map[int64]struct{})
	task.mu.Unlock()
	s.emit("scan-progress", ScanProgress{TaskID: status.TaskID, SnapshotID: status.SnapshotID, Root: status.Root, State: status.State, Phase: status.Phase, Nodes: status.Nodes, Files: status.Files, Directories: status.Directories, Bytes: status.Bytes, Issues: status.Issues, Error: status.Error, SnapshotVersion: version, AffectedParentIDs: affected, CountedBytes: status.CountedBytes, VolumeUsedBytes: status.VolumeUsedBytes, ExpectedTotalBytes: status.ExpectedTotalBytes})
}

func (t *scanTask) status() ScanStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.statusLocked()
}

// countedBytesLocked is the progress bar's numerator. Once the scan is terminal
// the auxiliary volumes are already inside bytes, so the pre-count has to drop
// out or it would be counted twice (ADR-0053 §1).
func (t *scanTask) countedBytesLocked() int64 {
	if t.state == scan.JobRunning {
		return t.bytes + t.preCounted
	}
	return t.bytes
}

func (t *scanTask) statusLocked() ScanStatus {
	return ScanStatus{TaskID: t.taskID, SnapshotID: t.snapshotID, Root: t.root, State: t.state, Phase: t.phase, Nodes: t.nodes, Files: t.files, Directories: t.directories, Bytes: t.bytes, Issues: append([]string(nil), t.issues...), Error: t.error, CountedBytes: t.countedBytesLocked(), VolumeUsedBytes: t.volumeUsed, ExpectedTotalBytes: t.expectedTotal}
}

func mapResult(result scan.MapResult) MapResult {
	entries := make([]MapEntry, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, mapEntry(entry))
	}
	return MapResult{SnapshotID: result.SnapshotID, SnapshotVersion: result.SnapshotVersion, Parent: result.Parent, Entries: entries, Total: result.Total, Limit: result.Limit, Offset: result.Offset, HasMore: result.HasMore, Remaining: mapEntry(result.Remaining), Confidence: result.Confidence, VolumeTotalBytes: result.VolumeTotalBytes, VolumeUsedBytes: result.VolumeUsedBytes, VolumeFreeBytes: result.VolumeFreeBytes, DensityTruncated: result.DensityTruncated}
}

func mapEntry(entry scan.MapEntry) MapEntry {
	displayState := entry.DisplayState
	if displayState == "" {
		displayState = "current"
		if entry.Confidence == "partial" {
			displayState = "partial"
		}
	}
	virtualType := entry.VirtualType
	capabilities := append([]string(nil), entry.Capabilities...)
	if entry.Kind == "aggregate" {
		if virtualType == "" {
			virtualType = "smaller_objects"
		}
		// hidden_space has nothing to enter: it is the space the walk could not
		// account for, so we cannot break it down. Granting "enter" would open an
		// empty level (ADR-0052 §4).
		if len(capabilities) == 0 && virtualType != "hidden_space" {
			capabilities = []string{"enter"}
		}
		if displayState == "current" {
			displayState = "partial"
		}
	}
	// Why this object may not be deleted. Browsing, previewing and revealing are
	// untouched by it -- only collecting is, because only collecting leads to a
	// delete. The reason travels with the entry so the UI can explain the refusal
	// rather than silently dropping the affordance.
	protection := ""
	if entry.Kind == "node" && entry.Node.Path != "" {
		protection = cleanup.DeleteBlock(entry.Node.Path)
	}
	if entry.Kind == "node" && len(capabilities) == 0 {
		if entry.Node.Kind == "directory" {
			capabilities = append(capabilities, "enter")
		}
		if entry.Node.Kind == "file" || entry.Node.Kind == "directory" || entry.Node.Kind == "symlink" {
			capabilities = append(capabilities, "preview", "reveal")
			if protection == "" {
				capabilities = append(capabilities, "collect")
			}
		}
	}
	children := projectedEntries(entry.Children)
	return MapEntry{Kind: entry.Kind, Node: entry.Node, Name: entry.Name, VirtualType: virtualType, DisplayState: displayState, Capabilities: capabilities, Count: entry.Count, LogicalSize: entry.LogicalSize, AllocatedSize: entry.AllocatedSize, OwnedAllocated: entry.OwnedAllocated, Confidence: entry.Confidence, SizeBasis: entry.SizeBasis, Protection: protection, Children: children, ChildrenTotal: entry.ChildrenTotal, ChildrenHasMore: entry.ChildrenHasMore}
}

// projectedEntries passes the slim arcs through unchanged. They carry no path
// and gain no capabilities: acting on an arc requires looking its node up by ID
// (ADR-0048, DDD invariant 17).
func projectedEntries(entries []scan.ProjectedEntry) []ProjectedEntry {
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

// applyRemovalsToSnapshot subtracts every applied item from the in-memory result
// and refreshes the volume figures from the OS. Returns how many were applied.
//
// The two numbers come from different places on purpose. The tree roll-ups are
// arithmetic on what the scan measured, so they are as good as the scan was; the
// volume total, used and free come from statfs, so the headline free space is
// exact whatever the roll-ups do.
func (s *Service) applyRemovalsToSnapshot(snapshotID int64, results []CleanupItemResult) int {
	remover, ok := s.store.(subtreeRemovingStore)
	if !ok {
		return 0
	}
	removed := 0
	for _, result := range results {
		if result.State != "applied" {
			continue
		}
		gone, err := remover.RemoveSubtree(snapshotID, result.Path)
		if err != nil {
			// Not fatal and not silent: the deletion happened, only the view is
			// behind, and a re-scan fixes it.
			log.Printf("cleanup: 快照未能就地移除 %s：%v", result.Path, err)
			continue
		}
		removed++
		log.Printf("cleanup: 快照移除 %s（%d 节点 / %d 字节），版本 %d",
			result.Path, gone.Nodes, gone.AllocatedBytes, gone.Version)
	}
	if removed > 0 && s.volumes != nil {
		if items, err := s.volumes.ListVolumes(); err == nil {
			s.refreshSnapshotVolume(snapshotID, items)
		}
	}
	return removed
}

// refreshSnapshotVolume re-reads the volume behind a finished result. Space freed
// by a deletion shows up here immediately, and from the OS rather than from
// arithmetic.
func (s *Service) refreshSnapshotVolume(snapshotID int64, items []ports.Volume) {
	volumeStore, ok := s.store.(snapshotVolumeStore)
	if !ok {
		return
	}
	for _, source := range projectStorageSources(items) {
		node, err := s.store.NodeByPath(snapshotID, filepath.Clean(source.Path))
		if err != nil || node.ParentID != 0 {
			continue
		}
		_ = volumeStore.SetSnapshotVolume(snapshotID, source.TotalBytes, source.UsedBytes, source.FreeBytes)
		return
	}
}

func matchesSnapshotNode(node scan.Node, item cleanup.Item) bool {
	if node.Path != item.Path || node.Kind != item.Kind || node.Device != item.Device || node.Inode != item.Inode || !node.ModifiedAt.Equal(item.Modified) {
		return false
	}
	return item.Kind == "directory" || node.LogicalSize == item.Size
}

func (s *Service) validateCleanupItem(item cleanup.Item) (bool, string) {
	current, err := s.files.CaptureCleanupItem(item.Path)
	if err != nil {
		return false, err.Error()
	}
	if current.Device != item.Device || current.Inode != item.Inode {
		return false, "对象已被替换（不是扫描时的那一个）"
	}
	if current.Size != item.Size || current.Mode != item.Mode || !current.Modified.Equal(item.Modified) {
		// The common case by far, and it is not an error: a cache directory that
		// something is still writing to changes between the scan and the delete.
		// Saying "metadata changed" left the user with nothing to do about it.
		return false, "扫描之后内容有变化（多半是仍在被写入的缓存），重新扫描后再试"
	}
	return true, "ready"
}

func (s *Service) getPlan(planID string, version int64) (*cleanupPlan, error) {
	s.mu.RLock()
	plan := s.plans[planID]
	s.mu.RUnlock()
	if plan == nil || plan.Version != version {
		return nil, errors.New("cleanup plan not found or version mismatch")
	}
	return plan, nil
}

func (s *Service) validatePlanLocked(plan *cleanupPlan) CleanupValidation {
	validation := CleanupValidation{PlanID: plan.ID, Version: plan.Version, Valid: true, Items: make([]CleanupItemValidation, 0, len(plan.items))}
	for _, item := range plan.items {
		valid, reason := s.validateCleanupItem(item)
		validation.Items = append(validation.Items, CleanupItemValidation{Path: item.Path, Valid: valid, Reason: reason})
		if !valid {
			validation.Valid = false
		}
	}
	if validation.Valid && plan.State == "draft" {
		plan.State = "validated"
	}
	return validation
}

func (plan *cleanupPlan) publicLocked() CleanupPlan {
	public := plan.CleanupPlan
	public.Results = append([]CleanupItemResult(nil), plan.Results...)
	return public
}
