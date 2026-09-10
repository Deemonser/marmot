package application

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
)

// A finished result follows the disk (ADR-0072). The system reports directories
// whose contents changed; each one is listed again and its direct children
// brought up to date in the tree, without descending. Directories that arrive
// new, and directories the system says it lost track of, are read in depth by
// the same path ⌘R uses. Everything is applied in batches, one child-index
// rebuild per batch, and the frontend is told once per batch.
//
// What this is not: a promise of consistency across the whole tree at one
// instant. Each directory is as of its own last event; the numbers users act on
// are checked once more, by identity, when a deletion runs (ADR-0071).
const (
	// liveUpdateLatency is FSEvents' own coalescing window. Measured on this
	// machine the stream delivers at most 32 events per callback whatever the
	// latency, so the application batches on top of it (R-074 §2).
	liveUpdateLatency = 2 * time.Second
	// liveUpdateBatch is how long the application collects callbacks before
	// applying them as one change to the tree.
	liveUpdateBatchDefault = 2 * time.Second
)

// liveUpdateBatch is a variable so tests can shorten the wait.
var liveUpdateBatch = liveUpdateBatchDefault

// LiveUpdateEvent is the name of the frontend event, sent once per applied batch.
const LiveUpdateEvent = "snapshot-updated"

// LiveUpdate is the event's payload: which snapshot moved to which version, and
// how many directories the batch touched.
type LiveUpdate struct {
	SnapshotID  int64 `json:"snapshotId"`
	Version     int64 `json:"version"`
	Directories int64 `json:"directories"`
}

// LiveUpdateStatus is what the frontend asks before it decides whether the tree
// it shows is current (ADR-0072 §4).
type LiveUpdateStatus struct {
	Active      bool   `json:"active"`
	SnapshotID  int64  `json:"snapshotId"`
	Root        string `json:"root"`
	Batches     int64  `json:"batches"`
	Directories int64  `json:"directories"`
	// Dropped counts batches in which the system reported lost events. The tree
	// is then behind in ways nobody knows; a re-scan is the honest fix.
	Dropped int64 `json:"dropped"`
	// Dirty counts directories that could not be brought up to date: a deep
	// read was needed and refused (too large, or holding attached volumes).
	Dirty         int64 `json:"dirty"`
	LastAppliedMs int64 `json:"lastAppliedMs"`
	Version       int64 `json:"version"`
}

type directoryRefreshingStore interface {
	RefreshDirectory(int64, int64, scan.Node, []scan.Node) (scan.DirectoryRefresh, error)
}

type liveUpdater struct {
	snapshotID int64
	root       string
	stop       func()

	mu       sync.Mutex
	pending  map[string]bool // directory path -> must scan its subtree
	dropped  bool
	timer    *time.Timer
	stopping bool

	batches     int64
	directories int64
	droppedRuns int64
	dirty       int64
	lastApplied time.Time
	version     int64
}

// startLiveUpdate begins following the disk for a finished result. Missing
// capabilities -- no watcher, a store or scanner without the shallow methods --
// leave it off silently: the result is then what it was, as before ADR-0072.
func (s *Service) startLiveUpdate(snapshotID int64, root string) {
	if s.fileEvents == nil {
		return
	}
	if _, ok := s.store.(directoryRefreshingStore); !ok {
		return
	}
	if _, ok := s.scanner.(ports.DirectoryReader); !ok {
		return
	}
	s.StopLiveUpdate()
	updater := &liveUpdater{snapshotID: snapshotID, root: root, pending: map[string]bool{}}
	stop, err := s.fileEvents.WatchFileEvents(root, liveUpdateLatency, func(events []ports.FileEvent) {
		s.liveEvents(updater, events)
	})
	if err != nil {
		log.Printf("live: 无法监听 %s：%v", root, err)
		return
	}
	updater.stop = stop
	s.liveMu.Lock()
	s.live = updater
	s.liveMu.Unlock()
	log.Printf("live: 开始跟随 %s（快照 %d）", root, snapshotID)
}

// StopLiveUpdate ends the stream and drops whatever was pending. Called before
// a new scan replaces the tree, and at shutdown.
func (s *Service) StopLiveUpdate() {
	s.liveMu.Lock()
	updater := s.live
	s.live = nil
	s.liveMu.Unlock()
	if updater == nil {
		return
	}
	updater.mu.Lock()
	updater.stopping = true
	if updater.timer != nil {
		updater.timer.Stop()
		updater.timer = nil
	}
	updater.pending = map[string]bool{}
	updater.mu.Unlock()
	if updater.stop != nil {
		updater.stop()
	}
	log.Printf("live: 停止跟随 %s", updater.root)
}

// GetLiveUpdateStatus reports whether, and how well, the current result follows
// the disk.
func (s *Service) GetLiveUpdateStatus() LiveUpdateStatus {
	s.liveMu.Lock()
	updater := s.live
	s.liveMu.Unlock()
	if updater == nil {
		return LiveUpdateStatus{}
	}
	updater.mu.Lock()
	defer updater.mu.Unlock()
	status := LiveUpdateStatus{
		Active: true, SnapshotID: updater.snapshotID, Root: updater.root,
		Batches: updater.batches, Directories: updater.directories,
		Dropped: updater.droppedRuns, Dirty: updater.dirty, Version: updater.version,
	}
	if !updater.lastApplied.IsZero() {
		status.LastAppliedMs = updater.lastApplied.UnixMilli()
	}
	return status
}

// liveEvents is the watcher's callback: record, and arm the batch timer.
func (s *Service) liveEvents(updater *liveUpdater, events []ports.FileEvent) {
	updater.mu.Lock()
	defer updater.mu.Unlock()
	if updater.stopping {
		return
	}
	for _, event := range events {
		path := strings.TrimSuffix(event.Path, "/")
		if path == "" {
			path = "/"
		}
		updater.pending[path] = updater.pending[path] || event.MustScanSubDirs
		if event.Dropped {
			updater.dropped = true
		}
	}
	if updater.timer == nil {
		updater.timer = time.AfterFunc(liveUpdateBatch, func() { s.applyLiveBatch(updater) })
	}
}

// applyLiveBatch brings every directory named since the last batch up to date.
func (s *Service) applyLiveBatch(updater *liveUpdater) {
	updater.mu.Lock()
	pending := updater.pending
	dropped := updater.dropped
	updater.pending = map[string]bool{}
	updater.dropped = false
	updater.timer = nil
	stopping := updater.stopping
	updater.mu.Unlock()
	if stopping || len(pending) == 0 {
		return
	}
	// Under the same lock as ⌘R: two writers to one tree would interleave.
	// A scan in progress means this result is about to be replaced; the batch
	// is not worth applying.
	if s.scanRunning() {
		return
	}
	s.rereadMu.Lock()
	defer s.rereadMu.Unlock()
	touched, dirty := s.spliceDirectories(updater.snapshotID, pending)

	version, _ := s.store.SnapshotVersion(updater.snapshotID)
	if touched > 0 && s.volumes != nil {
		if items, err := s.volumes.ListVolumes(); err == nil {
			s.refreshSnapshotVolume(updater.snapshotID, items)
		}
	}
	updater.mu.Lock()
	updater.batches++
	updater.directories += touched
	updater.dirty += dirty
	if dropped {
		updater.droppedRuns++
	}
	updater.lastApplied = time.Now()
	updater.version = version
	updater.mu.Unlock()
	if dropped {
		log.Printf("live: 系统报告丢失事件，结果可能落后，建议重新扫描")
	}
	if touched > 0 {
		s.emit(LiveUpdateEvent, LiveUpdate{SnapshotID: updater.snapshotID, Version: version, Directories: touched})
	}
}

// spliceDirectories brings every directory in pending up to date in one pass:
// the body of a live batch (ADR-0072 §3), also run by a warm start on the
// directories the FSEvents replay named (ADR-0075 §4). The caller holds
// rereadMu. pending maps a directory path to whether its whole subtree must be
// re-read. Returns how many directories were brought up to date and how many
// deep reads were refused.
type refreshBatchStore interface {
	BeginRefreshBatch(int64) error
	EndRefreshBatch(int64) error
}

func (s *Service) spliceDirectories(snapshotID int64, pending map[string]bool) (touched, dirty int64) {
	// One child-index rebuild for the whole batch, not one per changed directory.
	if batcher, ok := s.store.(refreshBatchStore); ok {
		if err := batcher.BeginRefreshBatch(snapshotID); err == nil {
			defer batcher.EndRefreshBatch(snapshotID)
		}
	}
	refresher := s.store.(directoryRefreshingStore)
	reader := s.scanner.(ports.DirectoryReader)
	paths := make([]string, 0, len(pending))
	for path := range pending {
		paths = append(paths, path)
	}
	// Parents before children: a new subdirectory is added by its parent's
	// refresh and read in depth right there, so its own event finds it present.
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) != len(paths[j]) {
			return len(paths[i]) < len(paths[j])
		}
		return paths[i] < paths[j]
	})

	deepRead := make(map[int64]struct{})
	// A directory that takes a noticeable fraction of a second is worth a log
	// line: it is what a slow batch or a slow warm start is made of. Timed from
	// one iteration to the next rather than with a defer, which would only fire
	// when the whole batch is done.
	var slowPath string
	var slowStarted time.Time
	noteSlow := func() {
		if slowPath == "" {
			return
		}
		if elapsed := time.Since(slowStarted); elapsed > 300*time.Millisecond {
			log.Printf("live: %s 用了 %s（深读=%v）", slowPath, elapsed.Round(time.Millisecond), pending[slowPath])
		}
		slowPath = ""
	}
	defer noteSlow()
	for _, path := range paths {
		noteSlow()
		slowPath, slowStarted = path, time.Now()
		node, err := s.store.NodeByPath(snapshotID, path)
		if err != nil || node.Kind != "directory" {
			// Outside the result (another volume, a boundary), or a directory
			// that is itself gone -- its parent's refresh removes it.
			continue
		}
		if _, done := deepRead[node.ID]; done {
			continue
		}
		if pending[path] {
			// The system lost track below this directory: read the whole subtree.
			if _, code, err := s.rereadSubtree(snapshotID, node); code != "" {
				dirty++
				log.Printf("live: %s 需要深读但被拒绝（%s）：%v", path, code, err)
			} else {
				touched++
				deepRead[node.ID] = struct{}{}
			}
			continue
		}
		self, children, err := reader.ReadDirectory(node.Path, node.VolumeID)
		if err != nil {
			continue
		}
		refresh, err := refresher.RefreshDirectory(snapshotID, node.ID, self, children)
		if err != nil {
			log.Printf("live: %s 刷新失败：%v", path, err)
			continue
		}
		touched++
		for _, newID := range refresh.NewDirectoryIDs {
			fresh, err := s.store.NodeByID(snapshotID, newID)
			if err != nil {
				continue
			}
			if _, code, err := s.rereadSubtree(snapshotID, fresh); code != "" {
				dirty++
				log.Printf("live: 新目录 %s 深读被拒绝（%s）：%v", fresh.Path, code, err)
			} else {
				deepRead[newID] = struct{}{}
			}
		}
	}

	return touched, dirty
}
