package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
)

// A warm start (ADR-0075): instead of walking the volume again, load the seed
// the previous scan left, replay the FSEvents journal from the position it was
// scanned at, bring the directories that changed up to date, and finish. Every
// condition below is a reason to stop and run the full scan instead; none of
// them is an error the user sees.
//
// What the seed is not: a result. It is never queried while it lies on disk, it
// has no UI, and a tree loaded from it is not current until the replay says what
// changed (DDD 37e). The seed is one file with a fixed name, replaced by rename,
// never matched by pattern (ADR-0054 §3).

const (
	seedFileName = "current.seed"
	// warmStartReplayTimeout is the hard ceiling on the replay itself; the budget
	// below normally stops long before this.
	warmStartReplayTimeout = 6 * time.Second
	// warmStartMaxDirectories: above this many changed directories the splice is
	// not worth it regardless of what the prediction said.
	warmStartMaxDirectories = 20_000
	// warmStartBudgetShare is the fraction of the last full scan a warm start may
	// be predicted to cost before it is skipped (ADR-0075 §5).
	warmStartBudgetShare = 1.0 / 3
	// defaultFullScanSeconds stands in when a seed carries no measured full scan.
	defaultFullScanSeconds = 18.0
	// seedLoadSeconds is R-078 §4.5's measured load, added to every prediction.
	seedLoadSeconds = 0.3
)

// defaultCalibration is R-078 / ADR-0075's measurements on the reference
// machine, used until this machine has measured its own.
var defaultCalibration = scan.SeedCalibration{
	EventsPerID:      1.0 / 130,
	DirsPerEvent:     0.25,
	ReplayMsPerEvent: 0.3,
	SpliceMsPerDir:   1.6,
}

func (s *Service) seedPath() string {
	if s.seedDir == "" {
		return ""
	}
	return filepath.Join(s.seedDir, seedFileName)
}

// warmStartPrediction is what a replay from the seed's position is expected to
// cost, from the ID span alone -- free to compute, so the decision to skip the
// replay is made before it starts (ADR-0075 §5).
type warmStartPrediction struct {
	Span    uint64
	Events  float64
	Dirs    float64
	Seconds float64
}

func predictWarmStart(span uint64, calibration scan.SeedCalibration) warmStartPrediction {
	cal := calibration
	if cal.EventsPerID <= 0 {
		cal.EventsPerID = defaultCalibration.EventsPerID
	}
	if cal.DirsPerEvent <= 0 {
		cal.DirsPerEvent = defaultCalibration.DirsPerEvent
	}
	if cal.ReplayMsPerEvent <= 0 {
		cal.ReplayMsPerEvent = defaultCalibration.ReplayMsPerEvent
	}
	if cal.SpliceMsPerDir <= 0 {
		cal.SpliceMsPerDir = defaultCalibration.SpliceMsPerDir
	}
	events := float64(span) * cal.EventsPerID
	dirs := events * cal.DirsPerEvent
	seconds := seedLoadSeconds + (events*cal.ReplayMsPerEvent+dirs*cal.SpliceMsPerDir)/1000
	return warmStartPrediction{Span: span, Events: events, Dirs: dirs, Seconds: seconds}
}

// warmStartBudget is the most a warm start may be predicted to cost.
func warmStartBudget(header scan.SeedHeader) float64 {
	full := header.FullScanSeconds
	if full <= 0 {
		full = defaultFullScanSeconds
	}
	return full * warmStartBudgetShare
}

// seedMatches says whether a seed's identity is this volume's: same root, same
// device, same journal. Any difference means the IDs in it mean nothing here.
func seedMatches(header scan.SeedHeader, root string, device uint64, uuid string) bool {
	return header.Root == root && header.Device == device && header.JournalUUID == uuid && header.EventID > 0
}

// discardSeed deletes the seed and says why in the log. A missing file is fine.
func (s *Service) discardSeed(reason string) {
	path := s.seedPath()
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("seed: 无法删除 %s：%v", path, err)
	}
	log.Printf("seed: 放弃，全扫（%s）", reason)
}

// sweepSeedTemp removes a temporary file a crash left between write and rename.
func (s *Service) sweepSeedTemp() {
	if path := s.seedPath(); path != "" {
		os.Remove(path + ".tmp")
	}
}

// tryWarmStart attempts ADR-0075 on the task's snapshot, which was just created
// empty. It returns true when the task has been brought to its terminal state
// from the seed; false when the caller must run the full scan, in which case the
// snapshot is empty again and the seed is gone.
func (s *Service) tryWarmStart(ctx context.Context, task *scanTask, volumeItems []ports.Volume) bool {
	seedStore, ok := s.store.(ports.SeedStore)
	if !ok || s.historian == nil || s.seedPath() == "" {
		return false
	}
	path := s.seedPath()
	if _, err := os.Stat(path); err != nil {
		return false
	}
	header, err := seedStore.ReadSeedHeader(path)
	if err != nil {
		s.discardSeed("种子头部不可读：" + err.Error())
		return false
	}
	// Whatever happens next, the calibration this machine measured last time is
	// worth carrying into the seed the coming scan writes.
	task.mu.Lock()
	task.seedCalibration = header.Calibration
	task.fullScanSeconds = header.FullScanSeconds
	task.mu.Unlock()

	device, uuid, err := s.historian.FileEventJournalIdentity(task.root)
	if err != nil {
		s.discardSeed("无法读取日志身份：" + err.Error())
		return false
	}
	if !seedMatches(header, task.root, device, uuid) {
		s.discardSeed("种子身份不匹配（根 / 设备 / 日志 UUID）")
		return false
	}
	now := s.historian.CurrentFileEventID()
	if now < header.EventID {
		s.discardSeed("当前 eventID 小于种子的，日志已重置")
		return false
	}
	prediction := predictWarmStart(now-header.EventID, header.Calibration)
	budget := warmStartBudget(header)
	if prediction.Seconds > budget {
		s.discardSeed(warmStartSkipReason(prediction, budget))
		return false
	}
	log.Printf("seed: 尝试热启动 %s：跨度 %d，预计 %.0f 事件 / %.0f 目录 / %.1fs（预算 %.1fs）",
		task.root, prediction.Span, prediction.Events, prediction.Dirs, prediction.Seconds, budget)

	task.mu.Lock()
	task.warm = true
	task.phase = "seed"
	task.mu.Unlock()
	s.emitProgress(task)

	// Load and replay in parallel: the load is 0.3 s of page reads, the replay
	// is fseventsd's time, and neither needs the other.
	loadDone := make(chan error, 1)
	go func() {
		_, err := seedStore.LoadSeed(task.snapshotID, path)
		loadDone <- err
	}()
	replayStarted := time.Now()
	report, replayErr := s.historian.FileEventHistory(task.root, header.EventID, 0, warmStartReplayTimeout)
	replayWall := time.Since(replayStarted)
	loadErr := <-loadDone

	abandon := func(reason string) bool {
		s.discardSeed(reason)
		if err := seedStore.ResetSnapshot(task.snapshotID); err != nil {
			log.Printf("seed: 重置快照失败：%v", err)
		}
		task.mu.Lock()
		task.warm = false
		task.phase = string(scan.PhaseCatalog)
		task.mu.Unlock()
		return false
	}
	if loadErr != nil {
		return abandon("加载种子失败：" + loadErr.Error())
	}
	if replayErr != nil {
		return abandon("回放失败：" + replayErr.Error())
	}
	if ctx.Err() != nil {
		return abandon("扫描已取消")
	}
	if !report.Reliable() {
		return abandon(replayUnreliableReason(report))
	}
	if len(report.Directories) > warmStartMaxDirectories {
		return abandon(fmt.Sprintf("变更目录过多：%d", len(report.Directories)))
	}

	// The seed's volume figures are the old disk; the ones captured at the start
	// of this task are the current disk (ADR-0052 §4).
	s.recordSnapshotVolume(task.snapshotID, task.root, volumeItems)

	pending := make(map[string]bool, len(report.Directories))
	for path := range report.Directories {
		trimmed := strings.TrimSuffix(path, "/")
		if trimmed == "" {
			trimmed = "/"
		}
		_, whole := report.SubtreeRescan[path]
		pending[trimmed] = pending[trimmed] || whole
	}
	spliceStarted := time.Now()
	s.rereadMu.Lock()
	touched, dirty := s.spliceDirectories(task.snapshotID, pending)
	s.rereadMu.Unlock()
	spliceWall := time.Since(spliceStarted)
	if ctx.Err() != nil {
		return abandon("扫描已取消")
	}

	snapshot, err := s.store.SnapshotByTaskID(task.taskID)
	if err != nil {
		return abandon("读取快照摘要失败：" + err.Error())
	}
	state := "completed"
	if snapshot.Issues > 0 || dirty > 0 {
		state = "completed_with_issues"
	}
	if err := s.store.FinishScan(task.snapshotID, state, "", snapshot.NodeCount, snapshot.FileCount, snapshot.DirCount, snapshot.Bytes, snapshot.Issues); err != nil {
		return abandon("收尾失败：" + err.Error())
	}
	task.mu.Lock()
	task.nodes, task.files, task.directories, task.bytes = snapshot.NodeCount, snapshot.FileCount, snapshot.DirCount, snapshot.Bytes
	task.state = state
	task.phase = string(scan.PhaseCatalog)
	task.mu.Unlock()
	s.firstMap.Store(task.snapshotID)
	if s.scanTotals != nil {
		if err := s.scanTotals.StoreScanTotal(task.root, ports.ScanTotal{Bytes: snapshot.Bytes, Nodes: snapshot.NodeCount}); err != nil {
			log.Printf("scan total for %s not stored: %v", task.root, err)
		}
	}
	s.startLiveUpdate(task.snapshotID, task.root)
	log.Printf("seed: 热启动 %s 完成：回放 %d 事件 / %d 目录 %s，拼接 %d 目录（%d 被拒）%s，节点 %d",
		task.root, report.Events, len(report.Directories), replayWall.Round(time.Millisecond),
		touched, dirty, spliceWall.Round(time.Millisecond), snapshot.NodeCount)
	s.emitProgress(task)

	// The seed this start leaves behind: the same tree brought up to date, with
	// the position taken before the replay (so anything after it replays next
	// time) and what this machine just measured.
	next := header
	next.EventID = now
	next.WrittenAt = time.Now()
	next.Calibration = measureCalibration(header.Calibration, prediction.Span, report, replayWall, touched, spliceWall)
	go s.writeSeed(task.snapshotID, next)
	s.scheduleCacheMaintenance()
	return true
}

// measureCalibration folds this replay's numbers into the calibration, so the
// next prediction uses what this machine actually does.
func measureCalibration(previous scan.SeedCalibration, span uint64, report ports.FileEventHistoryReport, replay time.Duration, touched int64, splice time.Duration) scan.SeedCalibration {
	next := previous
	if span > 0 && report.Events > 0 {
		next.EventsPerID = float64(report.Events) / float64(span)
		next.DirsPerEvent = float64(len(report.Directories)) / float64(report.Events)
		next.ReplayMsPerEvent = float64(replay.Milliseconds()) / float64(report.Events)
	}
	if touched > 0 {
		next.SpliceMsPerDir = float64(splice.Milliseconds()) / float64(touched)
	}
	return next
}

// writeSeed persists a finished result as the seed for its root. Runs in the
// background after a scan; a failure is logged, never shown.
func (s *Service) writeSeed(snapshotID int64, header scan.SeedHeader) {
	seedStore, ok := s.store.(ports.SeedStore)
	if !ok || s.seedPath() == "" {
		return
	}
	started := time.Now()
	size, err := seedStore.WriteSeed(snapshotID, s.seedPath(), header)
	if err != nil {
		log.Printf("seed: 未写入（%v）", err)
		// A tree too large to seed must not leave an older, smaller seed of the
		// same root behind to be accepted next time.
		os.Remove(s.seedPath())
		return
	}
	log.Printf("seed: 已写入 %s（%.1f MiB，%s，eventID %d）", s.seedPath(), float64(size)/float64(1<<20), time.Since(started).Round(time.Millisecond), header.EventID)
}

// seedHeaderForScan is the identity a full scan's seed carries: the journal
// position taken before the walk started, and the calibration inherited from
// the seed that was there before, if any.
func (s *Service) seedHeaderForScan(task *scanTask, elapsed time.Duration) (scan.SeedHeader, bool) {
	if s.historian == nil || s.seedPath() == "" {
		return scan.SeedHeader{}, false
	}
	if _, ok := s.store.(ports.SeedStore); !ok {
		return scan.SeedHeader{}, false
	}
	task.mu.RLock()
	eventID := task.seedEventID
	calibration := task.seedCalibration
	task.mu.RUnlock()
	if eventID == 0 {
		return scan.SeedHeader{}, false
	}
	device, uuid, err := s.historian.FileEventJournalIdentity(task.root)
	if err != nil {
		log.Printf("seed: 无法读取日志身份，不写种子：%v", err)
		return scan.SeedHeader{}, false
	}
	return scan.SeedHeader{
		Root: task.root, Device: device, JournalUUID: uuid, EventID: eventID,
		WrittenAt: time.Now(), Calibration: calibration, FullScanSeconds: elapsed.Seconds(),
	}, true
}

func warmStartSkipReason(prediction warmStartPrediction, budget float64) string {
	return fmt.Sprintf("预计 %.1fs 超过预算 %.1fs（跨度 %d，约 %.0f 事件）", prediction.Seconds, budget, prediction.Span, prediction.Events)
}

func replayUnreliableReason(report ports.FileEventHistoryReport) string {
	switch {
	case !report.HistoryDone:
		return "回放超时"
	case report.IDsWrapped > 0:
		return "事件 ID 回绕"
	case report.UserDropped > 0 || report.KernelDropped > 0:
		return "系统报告丢失事件"
	case report.RootChanged > 0:
		return "扫描根已变更"
	}
	return "回放不可靠"
}
