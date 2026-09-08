package application

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/ports"
)

func testService(t *testing.T) *Service {
	return testServiceWithTrash(t, platform.Adapter{})
}

func testServiceWithTrash(t *testing.T, trash ports.Trash) *Service {
	t.Helper()
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	return NewService(Dependencies{Store: store, Scanner: scanner.Scanner{}, FileSystem: adapter, Permissions: adapter, Trash: trash})
}

func TestGetStorageSourcesGroupsAPFSVolumesWithoutAddingMemberUsage(t *testing.T) {
	service := NewService(Dependencies{Volumes: staticVolumeCatalog{items: []ports.Volume{
		{ID: "root", Name: "Macintosh HD", Path: "/", Kind: "system_root", Role: "system", ContainerID: "disk3", VolumeGroupID: "group-a", TotalBytes: 20, UsedBytes: 12, FreeBytes: 8, ContainerTotalBytes: 100, ContainerUsedBytes: 80, ContainerFreeBytes: 20, Permission: "available", Scannable: true},
		{ID: "data", Name: "Macintosh HD - Data", Path: "/System/Volumes/Data", Kind: "data", Role: "data", ContainerID: "disk3", VolumeGroupID: "group-a", TotalBytes: 90, UsedBytes: 70, FreeBytes: 20, ContainerTotalBytes: 100, ContainerUsedBytes: 80, ContainerFreeBytes: 20, Permission: "available", Scannable: true},
		{ID: "external", Name: "Backup", Path: "/Volumes/Backup", Kind: "external", Role: "external", ContainerID: "disk3", TotalBytes: 200, UsedBytes: 50, FreeBytes: 150, ContainerTotalBytes: 200, ContainerUsedBytes: 50, Permission: "available", Scannable: true},
		{ID: "preboot", Name: "Preboot", Path: "/System/Volumes/Preboot", Kind: "system_auxiliary", Role: "system_auxiliary", ContainerID: "disk3", VolumeGroupID: "group-a", Scannable: false},
	}}})

	sources, err := service.GetStorageSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected one APFS source and one external source, got %#v", sources)
	}
	main := sources[0]
	if main.Name != "Macintosh HD" || main.Path != "/" || main.Kind != "apfs_volume_group" {
		t.Fatalf("unexpected main source: %#v", main)
	}
	if main.TotalBytes != 100 || main.UsedBytes != 80 || main.FreeBytes != 20 || len(main.Members) != 2 {
		t.Fatalf("source capacity or members were projected incorrectly: %#v", main)
	}
	if main.UsedBytes == 12+70 {
		t.Fatal("source capacity must not add System/Data volume usage")
	}
	if sources[1].Name != "Backup" || sources[1].Path != "/Volumes/Backup" {
		t.Fatalf("external volume was merged or sorted incorrectly: %#v", sources)
	}
}

type staticVolumeCatalog struct {
	items []ports.Volume
}

func (catalog staticVolumeCatalog) ListVolumes() ([]ports.Volume, error) {
	return catalog.items, nil
}

func TestCreateCleanupPlanRejectsParentChildOverlap(t *testing.T) {
	service := testService(t)
	root := t.TempDir()
	child := filepath.Join(root, "child.txt")
	if err := os.WriteFile(child, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: 1, Paths: []string{root, child}}); err == nil {
		t.Fatal("expected overlapping cleanup plan to be rejected")
	}
}

// mu is not decoration: an item is now deleted by four workers at once, so the
// recorder is written from several goroutines.
type recordingTrash struct {
	mu    sync.Mutex
	paths []string
	// removed is kept apart from paths so a test can tell which of the two
	// irreversibly different things happened.
	removed []string
	failOn  map[string]error
}

func (t *recordingTrash) Trash(path string) (string, error) {
	if err := t.failOn[path]; err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.paths = append(t.paths, path)
	return path, nil
}

// RemoveWithin records the absolute paths it was asked for, so a test reads the
// same shape whether an item went in one call or in chunks.
func (t *recordingTrash) RemoveWithin(item cleanup.Item, names []string) error {
	if err := t.failOn[item.Path]; err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range names {
		t.removed = append(t.removed, filepath.Join(item.Path, name))
	}
	return nil
}

func (t *recordingTrash) RemovePermanently(path string) error {
	if err := t.failOn[path]; err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.removed = append(t.removed, path)
	return nil
}

type blockingScanner struct {
	flushed chan struct{}
}

func (s *blockingScanner) Scan(ctx context.Context, root string, emit scan.Emitter, _ scan.PhaseEmitter) (scan.Result, error) {
	if err := emit(scan.Node{ID: 1, Path: root, Name: filepath.Base(root), Kind: "directory", HasChildren: true}); err != nil {
		return scan.Result{}, err
	}
	for id := int64(2); id <= 10000; id++ {
		if err := emit(scan.Node{ID: id, ParentID: 1, Path: filepath.Join(root, fmt.Sprintf("file-%d", id)), Name: fmt.Sprintf("file-%d", id), Kind: "file", LogicalSize: 1, AllocatedSize: 1, OwnedAllocated: 1, Confidence: "exact", SizeBasis: "test"}); err != nil {
			return scan.Result{}, err
		}
	}
	close(s.flushed)
	<-ctx.Done()
	return scan.Result{}, ctx.Err()
}

type stagedScanner struct {
	topLevelReady chan struct{}
	continueDeep  chan struct{}
}

func (s *stagedScanner) Scan(ctx context.Context, root string, emit scan.Emitter, phase scan.PhaseEmitter) (scan.Result, error) {
	if err := phase(scan.PhaseCatalog); err != nil {
		return scan.Result{}, err
	}
	if err := emit(scan.Node{ID: 1, Path: root, Name: filepath.Base(root), Kind: "directory", HasChildren: true}); err != nil {
		return scan.Result{}, err
	}
	if err := phase(scan.PhaseVolumeOverview); err != nil {
		return scan.Result{}, err
	}
	if err := emit(scan.Node{ID: 2, ParentID: 1, Path: filepath.Join(root, "top-level.txt"), Name: "top-level.txt", Kind: "file", LogicalSize: 1, AllocatedSize: 1, OwnedAllocated: 1, Confidence: "exact", SizeBasis: "test"}); err != nil {
		return scan.Result{}, err
	}
	if err := phase(scan.PhaseTopLevelPublish); err != nil {
		return scan.Result{}, err
	}
	close(s.topLevelReady)
	if err := phase(scan.PhaseDeepScan); err != nil {
		return scan.Result{}, err
	}
	select {
	case <-s.continueDeep:
	case <-ctx.Done():
		return scan.Result{}, ctx.Err()
	}
	if err := phase(scan.PhaseFinalize); err != nil {
		return scan.Result{}, err
	}
	return scan.Result{Nodes: 2, Files: 1, Directories: 1, Bytes: 1, DirectorySizes: map[int64]scan.DirectorySize{1: {LogicalSize: 1, AllocatedSize: 1, OwnedAllocated: 1, Confidence: "exact"}}}, nil
}

// finishTimingStore records how long the scan took to reach a queryable result.
// Used only by the opt-in real-volume smoke tests.
type finishTimingStore struct {
	ports.SnapshotStore
	startedAt time.Time
	finished  chan time.Duration
}

func (s *finishTimingStore) FinishScan(snapshotID int64, state, failure string, nodeCount, fileCount, directoryCount, bytes, issues int64) error {
	err := s.SnapshotStore.FinishScan(snapshotID, state, failure, nodeCount, fileCount, directoryCount, bytes, issues)
	select {
	case s.finished <- time.Since(s.startedAt):
	default:
	}
	return err
}

func addTestSnapshot(t *testing.T, service *Service, root, child string) int64 {
	t.Helper()
	snapshotID, err := service.store.CreateSnapshot("test-task", root)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	rootStat := rootInfo.Sys().(*syscall.Stat_t)
	nodes := []scan.Node{{
		ID: 1, Path: root, Name: filepath.Base(root), Kind: "directory", Confidence: "exact", SizeBasis: "test",
		Device: uint64(rootStat.Dev), Inode: rootStat.Ino, ModifiedAt: rootInfo.ModTime(), HasChildren: child != "",
	}}
	if child != "" {
		childInfo, err := os.Lstat(child)
		if err != nil {
			t.Fatal(err)
		}
		childStat := childInfo.Sys().(*syscall.Stat_t)
		nodes = append(nodes, scan.Node{
			ID: 2, ParentID: 1, Path: child, Name: filepath.Base(child), Kind: "file",
			LogicalSize: childInfo.Size(), AllocatedSize: childInfo.Size(), OwnedAllocated: childInfo.Size(),
			Confidence: "exact", SizeBasis: "test", Device: uint64(childStat.Dev), Inode: childStat.Ino,
			ModifiedAt: childInfo.ModTime(),
		})
	}
	if err := service.store.InsertNodes(snapshotID, nodes); err != nil {
		t.Fatal(err)
	}
	return snapshotID
}

func TestValidateCleanupPlanDetectsReplacement(t *testing.T) {
	service := testService(t)
	root := t.TempDir()
	path := filepath.Join(root, "candidate.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshotID := addTestSnapshot(t, service, root, path)
	plan, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: snapshotID, Paths: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	validation, err := service.ValidateCleanupPlan(plan.ID, plan.Version)
	if err != nil {
		t.Fatal(err)
	}
	// The reason is shown to the user verbatim, so it is worded for them rather
	// than for a log grep.
	if validation.Valid || len(validation.Items) != 1 || !strings.Contains(validation.Items[0].Reason, "已被替换") {
		t.Fatalf("expected identity mismatch, got %#v", validation)
	}
}

func TestCreateCleanupPlanRejectsPathOutsideSnapshot(t *testing.T) {
	service := testService(t)
	root := t.TempDir()
	path := filepath.Join(root, "candidate.txt")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshotID := addTestSnapshot(t, service, root, "")
	if _, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: snapshotID, Paths: []string{path}}); err == nil {
		t.Fatal("expected a path outside the snapshot to be rejected")
	}
}

func TestCacheMaintenanceCanBeCancelled(t *testing.T) {
	service := testService(t)
	service.scheduleCacheMaintenance()
	service.cancelCacheMaintenance()
	if service.maintenanceRun.Load() {
		t.Fatal("cache maintenance remained active after cancellation")
	}
}

func TestScanToCleanupVerticalSlice(t *testing.T) {
	trash := &recordingTrash{}
	service := testServiceWithTrash(t, trash)
	root := t.TempDir()
	path := filepath.Join(root, "candidate.txt")
	if err := os.WriteFile(path, []byte("candidate"), 0o644); err != nil {
		t.Fatal(err)
	}

	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 200 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "completed" {
		t.Fatalf("scan did not complete: %#v", status)
	}

	children, err := service.GetChildren(ChildrenQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(children.Nodes) != 1 || children.Nodes[0].Path != path {
		t.Fatalf("unexpected scan children: %#v", children.Nodes)
	}
	plan, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: status.SnapshotID, Paths: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := service.ValidateCleanupPlan(plan.ID, plan.Version)
	if err != nil || !validation.Valid {
		t.Fatalf("cleanup validation failed: valid=%v err=%v result=%#v", validation.Valid, err, validation)
	}
	confirmed, err := service.ConfirmCleanupPlan(plan.ID, plan.Version)
	if err != nil || confirmed.State != "confirmed" {
		t.Fatalf("cleanup confirmation failed: state=%s err=%v", confirmed.State, err)
	}
	applied, err := service.ExecuteCleanupPlan(plan.ID, plan.Version)
	// Deleted, not trashed: a rebuildable object goes outright, because the trash
	// is on the same volume and frees nothing (ADR-0063).
	if err != nil || applied.State != "applied" || len(trash.removed) != 1 || trash.removed[0] != path {
		t.Fatalf("cleanup execution failed: plan=%#v removed=%#v trashed=%#v err=%v", applied, trash.removed, trash.paths, err)
	}
}

// The delete indicator used to sit still for the whole run: progress was emitted
// only between items while each item was one blocking recursive remove, so a
// one-item plan reported 0% from start to finish. This is the shape that broke
// it -- one staged directory, wide and shallow, where no single subtree is large
// enough to become a work unit on its own.
func TestExecuteCleanupPlanReportsProgressInsideOneItem(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "cache")
	nodes := int64(1)
	for pkg := 0; pkg < 30; pkg++ {
		dir := filepath.Join(target, fmt.Sprintf("pkg-%02d", pkg))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		nodes++
		for file := 0; file < 30; file++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f-%02d.js", file)), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			nodes++
		}
	}

	// Off, so every completed chunk is visible. At 5 Hz this tree is deleted
	// inside a single window and only its two ends would be reported.
	restore := cleanupProgressEvery
	cleanupProgressEvery = 0
	t.Cleanup(func() { cleanupProgressEvery = restore })

	var eventMu sync.Mutex
	var events []CleanupProgress
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	service := NewService(Dependencies{
		Store: store, Scanner: scanner.Scanner{}, FileSystem: adapter, Permissions: adapter, Trash: adapter,
		Emit: func(name string, data any) {
			progress, ok := data.(CleanupProgress)
			if !ok {
				return
			}
			eventMu.Lock()
			defer eventMu.Unlock()
			events = append(events, progress)
		},
	})

	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 400 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "completed" {
		t.Fatalf("scan did not complete: %#v", status)
	}

	plan, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: status.SnapshotID, Paths: []string{target}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmCleanupPlan(plan.ID, plan.Version); err != nil {
		t.Fatal(err)
	}
	applied, err := service.ExecuteCleanupPlan(plan.ID, plan.Version)
	if err != nil || applied.State != "applied" {
		t.Fatalf("cleanup execution failed: state=%s err=%v results=%#v", applied.State, err, applied.Results)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("the staged directory survived the deletion: %v", err)
	}

	eventMu.Lock()
	defer eventMu.Unlock()
	if len(events) < 3 {
		t.Fatalf("a %d node item produced %d progress events; the indicator would not move", nodes, len(events))
	}
	// The denominator is fixed before the first unlink, so it may never move: a
	// total that grows during the run makes the fraction go backwards.
	total := events[0].TotalNodes
	if total != nodes {
		t.Errorf("denominator is %d nodes, the tree on disk has %d", total, nodes)
	}
	var last int64 = -1
	for i, event := range events {
		if event.TotalNodes != total {
			t.Fatalf("event %d changed the denominator from %d to %d", i, total, event.TotalNodes)
		}
		if event.DoneNodes < last {
			t.Fatalf("event %d went backwards, %d after %d", i, event.DoneNodes, last)
		}
		if event.DoneNodes > event.TotalNodes {
			t.Fatalf("event %d reports %d of %d nodes done", i, event.DoneNodes, event.TotalNodes)
		}
		last = event.DoneNodes
	}
	// Something in the middle, or the ring still only has its two ends.
	moved := false
	for _, event := range events {
		if event.DoneNodes > 0 && event.DoneNodes < total {
			moved = true
		}
	}
	if !moved {
		t.Error("every event was 0% or 100%: progress never moved inside the item")
	}
	if events[len(events)-1].DoneNodes != total {
		t.Errorf("the run ended at %d of %d nodes", events[len(events)-1].DoneNodes, total)
	}
}

func TestConfiguredScannerPersistsConcurrentBatchesToBinarySnapshot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.txt"), []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "inside.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	service := NewService(Dependencies{
		Store: store,
		Scanner: scanner.Scanner{MountResolver: func() ([]ports.Mount, error) {
			return []ports.Mount{{ID: "test-volume", Path: root, DeviceProfile: scan.DeviceProfileSSD}}, nil
		}},
		FileSystem:  adapter,
		Permissions: adapter,
		Trash:       adapter,
	})
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for attempt := 0; attempt < 400 && status.State == scan.JobRunning; attempt++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != scan.JobCompleted {
		t.Fatalf("configured scanner did not complete: %#v", status)
	}
	// The terminal state now arrives before the durable publish; wait for the
	// snapshot to land before reading it back from the store.
	children, err := store.Children(status.SnapshotID, 1, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("binary snapshot lost concurrent top-level nodes: %#v", children)
	}
	for _, child := range children {
		if child.Path != filepath.Join(root, child.Name) {
			t.Fatalf("binary snapshot reconstructed an invalid path: %#v", child)
		}
	}
}

func TestScanUsesBinarySnapshotStore(t *testing.T) {
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	service := NewService(Dependencies{
		Store:       store,
		Scanner:     scanner.Scanner{},
		FileSystem:  adapter,
		Permissions: adapter,
		Trash:       adapter,
	})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "binary.txt"), []byte("binary snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 200 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != scan.JobCompleted && status.State != scan.JobCompletedWithIssues {
		t.Fatalf("binary snapshot scan did not complete: %#v", status)
	}
	children, err := service.GetChildren(ChildrenQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(children.Nodes) != 1 || children.Nodes[0].Name != "binary.txt" {
		t.Fatalf("binary snapshot children mismatch: %#v", children)
	}
	rootNode, err := store.NodeByID(status.SnapshotID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rootNode.OwnedAllocated == 0 || rootNode.SizeBasis != "descendant_sum_v1" {
		t.Fatalf("binary directory summary was not finalized: %#v", rootNode)
	}
}

func TestScanConfiguredRootWithSnapshotStoreSmoke(t *testing.T) {
	root := os.Getenv("MARMOT_SCAN_ROOT")
	if root == "" {
		t.Skip("set MARMOT_SCAN_ROOT to run the application and SQLite scan smoke test")
	}
	store := memtree.OpenStore()
	defer store.Close()
	timingStore := &finishTimingStore{SnapshotStore: store, finished: make(chan time.Duration, 1)}
	adapter := platform.Adapter{}
	service := NewService(Dependencies{
		Store:       timingStore,
		Scanner:     scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem:  adapter,
		Permissions: adapter,
		Trash:       adapter,
	})
	startedAt := time.Now()
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	deadline := time.After(5 * time.Minute)
	for status.State == "running" {
		select {
		case <-deadline:
			t.Fatalf("application scan did not finish: %#v", status)
		default:
		}
		time.Sleep(200 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("root=%s elapsed=%s state=%s nodes=%d files=%d directories=%d bytes=%d issues=%d", root, time.Since(startedAt), status.State, status.Nodes, status.Files, status.Directories, status.Bytes, len(status.Issues))
	if status.State != "completed" && status.State != "completed_with_issues" {
		t.Fatalf("application scan failed: %#v", status)
	}
}

func TestScanConfiguredRootWithBinarySnapshotStoreSmoke(t *testing.T) {
	root := os.Getenv("MARMOT_SCAN_ROOT")
	if root == "" {
		t.Skip("set MARMOT_SCAN_ROOT to run the application and binary snapshot scan smoke test")
	}
	store := memtree.OpenStore()
	defer store.Close()
	timingStore := &finishTimingStore{SnapshotStore: store, finished: make(chan time.Duration, 1)}
	adapter := platform.Adapter{}
	service := NewService(Dependencies{
		Store:       timingStore,
		Scanner:     scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem:  adapter,
		Permissions: adapter,
		Trash:       adapter,
	})
	startedAt := time.Now()
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	deadline := time.After(10 * time.Minute)
	for status.State == "running" {
		select {
		case <-deadline:
			t.Fatalf("binary application scan did not finish: %#v", status)
		default:
		}
		time.Sleep(200 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	scanDoneAt := time.Now()
	t.Logf("binary root=%s scan=%s state=%s nodes=%d files=%d directories=%d bytes=%d issues=%d", root, scanDoneAt.Sub(startedAt), status.State, status.Nodes, status.Files, status.Directories, status.Bytes, len(status.Issues))
	if status.State != "completed" && status.State != "completed_with_issues" {
		t.Fatalf("binary application scan failed: %#v", status)
	}
	childrenStartedAt := time.Now()
	children, err := service.GetChildren(ChildrenQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	childrenDoneAt := time.Now()
	if len(children.Nodes) == 0 {
		t.Fatal("binary application scan published no root children")
	}
	nodeStartedAt := time.Now()
	rootNode, err := timingStore.NodeByID(status.SnapshotID, 1)
	if err != nil {
		t.Fatal(err)
	}
	nodeDoneAt := time.Now()
	if rootNode.SizeBasis == "" {
		t.Fatalf("binary application root summary has no size basis: %#v", rootNode)
	}
	closeStartedAt := time.Now()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	closeDoneAt := time.Now()
	finalizeDuration := <-timingStore.finished
	t.Logf("binary query timings: finalize=%s getChildren=%s nodeByID=%s storeClose=%s total=%s", finalizeDuration, childrenDoneAt.Sub(childrenStartedAt), nodeDoneAt.Sub(nodeStartedAt), closeDoneAt.Sub(closeStartedAt), closeDoneAt.Sub(startedAt))
}

func TestCancelScanKeepsCommittedPartialResults(t *testing.T) {
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	fakeScanner := &blockingScanner{flushed: make(chan struct{})}
	service := NewService(Dependencies{Store: store, Scanner: fakeScanner, FileSystem: adapter, Permissions: adapter, Trash: adapter})

	root := t.TempDir()
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fakeScanner.flushed:
	case <-time.After(time.Second):
		t.Fatal("scanner did not reach a committed batch")
	}
	if _, err := service.CancelScan(started.TaskID); err != nil {
		t.Fatal(err)
	}

	status := started
	for i := 0; i < 200 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "cancelled" || status.Nodes != 10000 {
		t.Fatalf("unexpected cancelled scan status: %#v", status)
	}
	children, err := service.GetChildren(ChildrenQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(children.Nodes) != 1000 {
		t.Fatalf("expected committed partial children to remain queryable, got %d", len(children.Nodes))
	}
}

func TestTopLevelPublishCommitsBeforeDeepScan(t *testing.T) {
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	fakeScanner := &stagedScanner{topLevelReady: make(chan struct{}), continueDeep: make(chan struct{})}
	service := NewService(Dependencies{Store: store, Scanner: fakeScanner, FileSystem: adapter, Permissions: adapter, Trash: adapter})

	root := t.TempDir()
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fakeScanner.topLevelReady:
	case <-time.After(time.Second):
		t.Fatal("scanner did not publish the top level")
	}
	children, err := service.GetChildren(ChildrenQuery{SnapshotID: started.SnapshotID, ParentID: 1, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(children.Nodes) != 1 || children.Nodes[0].Name != "top-level.txt" {
		t.Fatalf("top-level batch was not committed before deep scan: %#v", children.Nodes)
	}
	close(fakeScanner.continueDeep)
	status := started
	for i := 0; i < 200 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "completed" || status.Phase != string(scan.PhaseFinalize) {
		t.Fatalf("unexpected staged scan status: %#v", status)
	}
}

func TestRootMapBalancesToVolumeUsedBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	// used must exceed the tree so there is a positive gap to balance.
	const total, used, free = uint64(90000), uint64(40000), uint64(50000)
	service := NewService(Dependencies{
		Store: store,
		Scanner: scanner.Scanner{MountResolver: func() ([]ports.Mount, error) {
			return []ports.Mount{{ID: "test-volume", Path: root, DeviceProfile: scan.DeviceProfileSSD}}, nil
		}},
		FileSystem:  adapter,
		Permissions: adapter,
		Trash:       adapter,
		Volumes: staticVolumeCatalog{items: []ports.Volume{{
			ID: "root", Name: "Test", Path: root, Kind: "system_root", Role: "system",
			TotalBytes: total, UsedBytes: used, FreeBytes: free, Permission: "available", Scannable: true,
		}}},
	})
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for attempt := 0; attempt < 400 && status.State == scan.JobRunning; attempt++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != scan.JobCompleted {
		t.Fatalf("scan did not complete: %#v", status)
	}
	result, err := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 64})
	if err != nil {
		t.Fatal(err)
	}
	if result.VolumeTotalBytes != total || result.VolumeUsedBytes != used || result.VolumeFreeBytes != free {
		t.Fatalf("volume state was not recorded on the snapshot: %#v", result)
	}
	if result.VolumeUsedBytes+result.VolumeFreeBytes != result.VolumeTotalBytes {
		t.Fatalf("used + free must equal capacity: %#v", result)
	}
	var sum int64
	hidden := MapEntry{}
	for _, entry := range result.Entries {
		sum += entry.OwnedAllocated
		if entry.VirtualType == "hidden_space" {
			hidden = entry
		}
	}
	if hidden.VirtualType == "" {
		t.Fatalf("the root level has no balancing entry: %#v", result.Entries)
	}
	if hidden.SizeBasis != "volume_statfs_v1" {
		t.Fatalf("the balancing entry must declare where its number came from: %#v", hidden)
	}
	if len(hidden.Capabilities) != 0 {
		t.Fatalf("the balancing entry must not carry capabilities: %#v", hidden)
	}
	if sum != int64(used) {
		t.Fatalf("root entries sum to %d, want the volume's used bytes %d", sum, used)
	}

	// The identity has to hold on a repeat query too: the result is queried many
	// times over a session, and there is no second store to cross-check against
	// any more (ADR-0055).
	again, err := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 64})
	if err != nil {
		t.Fatal(err)
	}
	var againSum int64
	for _, entry := range again.Entries {
		againSum += entry.OwnedAllocated
	}
	if againSum != int64(used) {
		t.Fatalf("a repeat query lost the balance: sum=%d used=%d", againSum, used)
	}
}

// A projected arc on an outer ring carries only a node id (ADR-0048), so
// collecting one goes back to the snapshot for the real entry. The point of this
// test is that the lookup route grants exactly what walking the level grants --
// no more, and never a fabricated capability.
func TestGetNodeEntryResolvesProjectedArcWithTheSameCapabilities(t *testing.T) {
	service := testService(t)
	root := t.TempDir()
	path := filepath.Join(root, "collectable.txt")
	if err := os.WriteFile(path, []byte("collectable"), 0o644); err != nil {
		t.Fatal(err)
	}

	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 200 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "completed" {
		t.Fatalf("scan did not complete: %#v", status)
	}

	walked, err := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var expected MapEntry
	for _, entry := range walked.Entries {
		if entry.Kind == "node" && entry.Node.Path == path {
			expected = entry
		}
	}
	if expected.Node.ID == 0 {
		t.Fatalf("the walked level did not carry the file: %#v", walked.Entries)
	}

	looked, err := service.GetNodeEntry(status.SnapshotID, expected.Node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if looked.Node.Path != expected.Node.Path || looked.Name != expected.Name || looked.OwnedAllocated != expected.OwnedAllocated {
		t.Fatalf("lookup by id described the node differently: %#v vs %#v", looked, expected)
	}
	if fmt.Sprint(looked.Capabilities) != fmt.Sprint(expected.Capabilities) {
		t.Fatalf("lookup by id granted different capabilities: %v vs %v", looked.Capabilities, expected.Capabilities)
	}
	if !slices.Contains(looked.Capabilities, "collect") {
		t.Fatalf("a real file must be collectable: %#v", looked)
	}

	if _, err := service.GetNodeEntry(status.SnapshotID, 0); err == nil {
		t.Fatal("expected a missing node id to be rejected")
	}
	if _, err := service.GetNodeEntry(status.SnapshotID, expected.Node.ID+9999); err == nil {
		t.Fatal("expected an unknown node id to be rejected")
	}
}

// Two answers to "may this be deleted", and they must agree: the space map
// withholds the collect capability and says why, and plan creation refuses the
// path outright. The second one is the gate -- it has to hold even if a frontend
// ignored the first.
func TestProtectedPathsAreNotCollectableAndCannotBeStaged(t *testing.T) {
	for _, path := range []string{"/Users", "/System/Library", "/usr"} {
		entry := mapEntry(scan.MapEntry{
			Kind: "node",
			Node: scan.Node{ID: 2, ParentID: 1, Path: path, Name: filepath.Base(path), Kind: "directory"},
			Name: filepath.Base(path),
		})
		if entry.Protection != cleanup.ProtectionSystemDependency {
			t.Fatalf("%s should carry a protection reason, got %q", path, entry.Protection)
		}
		if slices.Contains(entry.Capabilities, "collect") {
			t.Fatalf("%s must not be collectable: %#v", path, entry.Capabilities)
		}
		// Browsing it is still fine: protection is about deleting, nothing else.
		for _, want := range []string{"enter", "preview", "reveal"} {
			if !slices.Contains(entry.Capabilities, want) {
				t.Fatalf("%s lost %q: %#v", path, want, entry.Capabilities)
			}
		}
	}

	ordinary := mapEntry(scan.MapEntry{
		Kind: "node",
		Node: scan.Node{ID: 3, ParentID: 2, Path: "/Users/alice/Downloads", Name: "Downloads", Kind: "directory"},
		Name: "Downloads",
	})
	if ordinary.Protection != "" || !slices.Contains(ordinary.Capabilities, "collect") {
		t.Fatalf("an ordinary folder lost its collect capability: %#v", ordinary)
	}

	service := testService(t)
	if _, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: 1, Paths: []string{"/Users"}}); err == nil {
		t.Fatal("plan creation must refuse a protected path even when asked directly")
	}
}

// runPlan drives one plan from creation to execution and returns the outcome.
func runPlan(t *testing.T, service *Service, snapshotID int64, paths []string) CleanupPlan {
	t.Helper()
	plan, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: snapshotID, Paths: paths})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := service.ConfirmCleanupPlan(plan.ID, plan.Version); err != nil {
		t.Fatalf("confirm plan: %v", err)
	}
	applied, err := service.ExecuteCleanupPlan(plan.ID, plan.Version)
	if err != nil {
		t.Fatalf("execute plan: %v", err)
	}
	return applied
}

// scanned builds a snapshot over a temp dir and returns its id.
func scanned(t *testing.T, service *Service, root string) int64 {
	t.Helper()
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 400 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		if status, err = service.GetScanStatus(started.TaskID); err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "completed" {
		t.Fatalf("scan did not complete: %#v", status)
	}
	return status.SnapshotID
}

// Moving to the trash frees nothing: same volume, so it is a rename. Deletion is
// now the only mode, so it has to actually delete rather than quietly do the
// reversible thing.
func TestDeletionRemovesInsteadOfTrashing(t *testing.T) {
	trash := &recordingTrash{}
	service := testServiceWithTrash(t, trash)
	root := t.TempDir()
	path := filepath.Join(root, "candidate.txt")
	if err := os.WriteFile(path, []byte("candidate"), 0o644); err != nil {
		t.Fatal(err)
	}
	applied := runPlan(t, service, scanned(t, service, root), []string{path})
	if applied.State != "applied" {
		t.Fatalf("plan came back as %#v", applied)
	}
	if len(trash.removed) != 1 || trash.removed[0] != path {
		t.Fatalf("nothing was removed outright: removed=%#v trashed=%#v", trash.removed, trash.paths)
	}
	if len(trash.paths) != 0 {
		t.Fatalf("the trash was used anyway: %#v", trash.paths)
	}
	if applied.Results[0].Reason != "已删除" {
		t.Fatalf("the result does not say what happened: %q", applied.Results[0].Reason)
	}
}

// Delete means delete. The user asked three times, and the last time explicitly
// included what cannot be rebuilt -- so a repository staged by hand goes the same
// way as a build directory, and nothing is quietly rerouted to the trash.
//
// What still refuses is cleanup.DeleteBlock, and it refuses for a different
// reason: it protects the machine from being broken, not the user from losing
// their own files.
func TestNothingIsReroutedToTheTrash(t *testing.T) {
	trash := &recordingTrash{}
	service := testServiceWithTrash(t, trash)
	root := t.TempDir()
	repo := filepath.Join(root, "project", ".git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "HEAD"), []byte("ref: refs/heads/main"), 0o644); err != nil {
		t.Fatal(err)
	}
	applied := runPlan(t, service, scanned(t, service, root), []string{repo})
	if applied.State != "applied" {
		t.Fatalf("the plan did not complete: %#v", applied)
	}
	if len(trash.paths) != 0 {
		t.Fatalf("a staged path was rerouted to the trash: %#v", trash.paths)
	}
	if len(trash.removed) != 1 || trash.removed[0] != repo {
		t.Fatalf("the repository was not deleted: %#v", trash.removed)
	}
}

// The machine-integrity guard is untouched, and it is the one refusal left. A
// home folder root or a system tree is refused whatever the user staged, because
// losing those is not "the user lost a file", it is a broken install.
func TestMachineIntegrityGuardStillRefuses(t *testing.T) {
	service := testService(t)
	snapshotID := addTestSnapshot(t, service, "/", "")
	for _, path := range []string{"/", "/System", "/Users/alice"} {
		if _, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: snapshotID, Paths: []string{path}}); err == nil {
			t.Errorf("%s was accepted for deletion", path)
		}
	}
}

// The volume list of a real Mac carries mounts nested inside other mounts:
// /System/Volumes/Update/mnt1 is the unsealed system volume staged under the
// update mount — the same bytes the walk already counts at / — and
// /System/Volumes/Update/SFR/mnt1 is the recovery system from another
// container. The walk never crosses into /System/Volumes/Update, so attach can
// never place them; pre-counting them put 19.4 GB of phantom bytes in the
// progress numerator, and the bar sat pinned at 100% for the last seconds of
// the walk (R-067 §2.3).
func TestGroupVolumesInRootExcludesMountsNestedInOtherVolumes(t *testing.T) {
	service := NewService(Dependencies{})
	items := []ports.Volume{
		{ID: "root", Path: "/", Kind: "system_root", UsedBytes: 17e9},
		{ID: "data", Path: "/System/Volumes/Data", Kind: "data", UsedBytes: 149e9},
		{ID: "preboot", Path: "/System/Volumes/Preboot", Kind: "system_auxiliary", UsedBytes: 18e9},
		{ID: "update", Path: "/System/Volumes/Update", Kind: "system_auxiliary", UsedBytes: 770e6},
		{ID: "vm", Path: "/System/Volumes/VM", Kind: "system_auxiliary", UsedBytes: 10e9},
		{ID: "staged-system", Path: "/System/Volumes/Update/mnt1", Kind: "system_auxiliary", UsedBytes: 17e9},
		{ID: "recovery", Path: "/System/Volumes/Update/SFR/mnt1", Kind: "system_auxiliary", UsedBytes: 2e9},
	}

	volumes, watched := service.groupVolumesInRoot("/", items)

	got := map[string]bool{}
	var preCounted int64
	for _, volume := range volumes {
		got[volume.id] = true
		preCounted += volume.usedBytes
	}
	for _, id := range []string{"preboot", "update", "vm"} {
		if !got[id] {
			t.Errorf("volume %s should be pre-counted and attachable", id)
		}
	}
	for _, id := range []string{"staged-system", "recovery"} {
		if got[id] {
			t.Errorf("volume %s is nested inside another mount: the walk can never reach its parent, so it must not be pre-counted", id)
		}
	}
	if want := int64(18e9 + 770e6 + 10e9); preCounted != want {
		t.Errorf("preCounted = %d, want %d", preCounted, want)
	}
	if watched == nil {
		t.Fatal("watched paths missing")
	}
	if _, ok := watched["/System/Volumes"]; !ok {
		t.Error("the attach parent /System/Volumes should be watched")
	}
}

// A volume nested in another mount is still pre-counted when the scan root IS
// the outer mount: from inside /System/Volumes/Update the walk does reach
// mnt1's parent.
func TestGroupVolumesInRootKeepsNestedMountWhenRootIsTheOuterVolume(t *testing.T) {
	service := NewService(Dependencies{})
	items := []ports.Volume{
		{ID: "update", Path: "/System/Volumes/Update", Kind: "system_auxiliary", UsedBytes: 770e6},
		{ID: "staged-system", Path: "/System/Volumes/Update/mnt1", Kind: "system_auxiliary", UsedBytes: 17e9},
	}
	volumes, _ := service.groupVolumesInRoot("/System/Volumes/Update", items)
	if len(volumes) != 1 || volumes[0].id != "staged-system" {
		t.Fatalf("scanning the outer mount itself should keep the nested volume, got %+v", volumes)
	}
}

type memoryScanTotals struct {
	mu     sync.Mutex
	totals map[string]ports.ScanTotal
}

func (m *memoryScanTotals) LoadScanTotal(root string) ports.ScanTotal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totals[root]
}

func (m *memoryScanTotals) StoreScanTotal(root string, total ports.ScanTotal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.totals == nil {
		m.totals = map[string]ports.ScanTotal{}
	}
	m.totals[root] = total
	return nil
}

// The progress numerator is lstat-allocated bytes; no statfs figure shares
// that basis, so the denominator on the same scale is the previous completed
// walk's own final count. First scan of a root: no history, ExpectedTotalBytes
// is zero and the UI falls back to statfs. Second scan: the first walk's final
// bytes ride along from the first status on (R-067 §2.4).
func TestCompletedScanTeachesTheNextScanItsDenominator(t *testing.T) {
	totals := &memoryScanTotals{}
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	service := NewService(Dependencies{Store: store, Scanner: scanner.Scanner{}, FileSystem: adapter, Permissions: adapter, Trash: adapter, ScanTotals: totals})

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}

	runScan := func() ScanStatus {
		t.Helper()
		started, err := service.StartScan(ScanOptions{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if history := totals.LoadScanTotal(started.Root); started.ExpectedTotalBytes != history.Bytes || started.ExpectedTotalNodes != history.Nodes {
			t.Fatalf("the very first status should already carry the history: got %d/%d, want %+v", started.ExpectedTotalBytes, started.ExpectedTotalNodes, history)
		}
		status := started
		for i := 0; i < 400 && status.State == "running"; i++ {
			time.Sleep(5 * time.Millisecond)
			status, err = service.GetScanStatus(started.TaskID)
			if err != nil {
				t.Fatal(err)
			}
		}
		if status.State != "completed" {
			t.Fatalf("scan did not complete: %#v", status)
		}
		return status
	}

	first := runScan()
	if first.ExpectedTotalBytes != 0 {
		t.Fatalf("a root never scanned before has no history, got %d", first.ExpectedTotalBytes)
	}
	if recorded := totals.LoadScanTotal(first.Root); recorded.Bytes != first.Bytes || recorded.Nodes != first.Nodes {
		t.Fatalf("the completed walk should have recorded its final counts: recorded %+v, walked %d bytes / %d nodes", recorded, first.Bytes, first.Nodes)
	}

	second := runScan()
	if second.ExpectedTotalBytes != first.Bytes {
		t.Fatalf("the second scan's denominator should be the first walk's final count %d, got %d", first.Bytes, second.ExpectedTotalBytes)
	}
}

// A cancelled walk counted less than the truth: it must not overwrite the
// history a completed walk left behind.
func TestCancelledScanDoesNotTeachATotal(t *testing.T) {
	totals := &memoryScanTotals{totals: map[string]ports.ScanTotal{}}
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	service := NewService(Dependencies{Store: store, Scanner: scanner.Scanner{}, FileSystem: adapter, Permissions: adapter, Trash: adapter, ScanTotals: totals})

	root := t.TempDir()
	for i := 0; i < 40; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.bin", i)), make([]byte, 4096), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	totals.totals[root] = ports.ScanTotal{Bytes: 12345}

	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelScan(started.TaskID); err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 400 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		status, err = service.GetScanStatus(started.TaskID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := totals.LoadScanTotal(root); got.Bytes != 12345 {
		t.Fatalf("a %s scan overwrote the learned total: %+v", status.State, got)
	}
}

// recordingPreview stands in for the macOS bridge and writes down what it was
// handed, so the test can see which path each action would have given AppKit.
type recordingPreview struct {
	terminal []string
}

func (recordingPreview) Preview(path string) (string, error) { return path, nil }
func (recordingPreview) Reveal(path string) (string, error)  { return path, nil }
func (r *recordingPreview) OpenTerminal(path string) (string, error) {
	r.terminal = append(r.terminal, path)
	return path, nil
}

// Terminal.app runs a file URL as a script, so a file node must reach the port
// as its parent directory and a directory node as itself (ADR-0069 §5). Both go
// through the same node checks as Preview and Reveal: an id that is not in the
// snapshot is refused before the port hears about it.
func TestOpenTerminalNodeHandsTheDirectoryToThePort(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "build")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "output.bin")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	preview := &recordingPreview{}
	service := NewService(Dependencies{Store: store, Scanner: scanner.Scanner{}, FileSystem: adapter, Permissions: adapter, Trash: adapter, Preview: preview})

	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 200 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		if status, err = service.GetScanStatus(started.TaskID); err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "completed" {
		t.Fatalf("scan did not complete: %#v", status)
	}

	level, err := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var dirID int64
	for _, entry := range level.Entries {
		if entry.Kind == "node" && entry.Node.Path == dir {
			dirID = entry.Node.ID
		}
	}
	if dirID == 0 {
		t.Fatalf("the walked level did not carry the directory: %#v", level.Entries)
	}
	inner, err := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: dirID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var fileID int64
	for _, entry := range inner.Entries {
		if entry.Kind == "node" && entry.Node.Path == file {
			fileID = entry.Node.ID
		}
	}
	if fileID == 0 {
		t.Fatalf("the directory level did not carry the file: %#v", inner.Entries)
	}

	if result, err := service.OpenTerminalNode(status.SnapshotID, dirID); err != nil || !result.OK {
		t.Fatalf("directory: %#v %v", result, err)
	}
	if result, err := service.OpenTerminalNode(status.SnapshotID, fileID); err != nil || !result.OK {
		t.Fatalf("file: %#v %v", result, err)
	}
	if fmt.Sprint(preview.terminal) != fmt.Sprint([]string{dir, dir}) {
		t.Fatalf("the port must only ever see directories: %v", preview.terminal)
	}

	if result, err := service.OpenTerminalNode(status.SnapshotID, fileID+9999); err != nil || result.OK || result.Code != "stale_node" {
		t.Fatalf("an unknown node must be refused before the port: %#v %v", result, err)
	}
	if result, err := service.OpenTerminalNode(0, fileID); err != nil || result.OK || result.Code != "invalid_request" {
		t.Fatalf("a missing snapshot must be refused: %#v %v", result, err)
	}
	if len(preview.terminal) != 2 {
		t.Fatalf("refused calls must not reach the port: %v", preview.terminal)
	}
}

// vaultedFS is the file system as a macOS data vault presents it: the parent
// listing shows the entry, but lstat on it is refused even with Full Disk
// Access. Everything else is the real adapter.
type vaultedFS struct {
	platform.Adapter
	vaulted string
}

func (v vaultedFS) CaptureCleanupItem(path string) (cleanup.Item, error) {
	if path == v.vaulted {
		return cleanup.Item{}, &fs.PathError{Op: "lstat", Path: path, Err: syscall.EPERM}
	}
	return v.Adapter.CaptureCleanupItem(path)
}

// "Not allowed to look" must not be reported as "moved or deleted": the first
// is a fact about this process, the second a claim about the disk that nothing
// supports. Either way the port is not called (ADR-0015 error semantics).
func TestNodeActionReportsPermissionNotStalenessForVaultedPaths(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "com.apple.vault")
	if err := os.Mkdir(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plain.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	preview := &recordingPreview{}
	service := NewService(Dependencies{Store: store, Scanner: scanner.Scanner{}, FileSystem: vaultedFS{Adapter: adapter, vaulted: vault}, Permissions: adapter, Trash: adapter, Preview: preview})

	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 200 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		if status, err = service.GetScanStatus(started.TaskID); err != nil {
			t.Fatal(err)
		}
	}
	level, err := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var vaultID, plainID int64
	for _, entry := range level.Entries {
		if entry.Kind != "node" {
			continue
		}
		switch entry.Node.Path {
		case vault:
			vaultID = entry.Node.ID
		case filepath.Join(root, "plain.bin"):
			plainID = entry.Node.ID
		}
	}
	if vaultID == 0 || plainID == 0 {
		t.Fatalf("level did not carry both nodes: %#v", level.Entries)
	}

	result, err := service.RevealNode(status.SnapshotID, vaultID)
	if err != nil || result.OK || result.Code != "permission_denied" {
		t.Fatalf("vaulted path: want permission_denied, got %#v %v", result, err)
	}
	if result, err := service.OpenTerminalNode(status.SnapshotID, vaultID); err != nil || result.OK || result.Code != "permission_denied" {
		t.Fatalf("vaulted path (terminal): want permission_denied, got %#v %v", result, err)
	}
	if len(preview.terminal) != 0 {
		t.Fatalf("a refused node must not reach the port: %v", preview.terminal)
	}
	if result, err := service.RevealNode(status.SnapshotID, plainID); err != nil || !result.OK {
		t.Fatalf("the plain file must still reveal: %#v %v", result, err)
	}
}

func scanForTest(t *testing.T, service *Service, root string) ScanStatus {
	t.Helper()
	started, err := service.StartScan(ScanOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	status := started
	for i := 0; i < 400 && status.State == "running"; i++ {
		time.Sleep(5 * time.Millisecond)
		if status, err = service.GetScanStatus(started.TaskID); err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "completed" {
		t.Fatalf("scan did not complete: %#v", status)
	}
	return status
}

func mapEntryByPath(t *testing.T, service *Service, snapshotID, parentID int64, path string) MapEntry {
	t.Helper()
	level, err := service.GetMap(MapQuery{SnapshotID: snapshotID, ParentID: parentID, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range level.Entries {
		if entry.Kind == "node" && entry.Node.Path == path {
			return entry
		}
	}
	t.Fatalf("%s is not in level %d: %#v", path, parentID, level.Entries)
	return MapEntry{}
}

// The disk changes under a finished scan. Re-reading the one directory that
// changed brings that directory -- and every ancestor's total -- up to date,
// keeps the IDs of the objects that are still there, and leaves the rest of the
// snapshot alone (ADR-0070).
func TestRereadDirectoryReplacesOneSubtreeInPlace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "build")
	other := filepath.Join(root, "src")
	for _, path := range []string{dir, other} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, size int) {
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "keep.bin"), 4096)
	write(filepath.Join(dir, "gone.bin"), 8192)
	write(filepath.Join(other, "main.go"), 4096)

	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	service := NewService(Dependencies{Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts}, FileSystem: adapter, Permissions: adapter, Trash: adapter, Preview: &recordingPreview{}})
	status := scanForTest(t, service, root)

	buildBefore := mapEntryByPath(t, service, status.SnapshotID, 1, dir)
	keepBefore := mapEntryByPath(t, service, status.SnapshotID, buildBefore.Node.ID, filepath.Join(dir, "keep.bin"))
	goneBefore := mapEntryByPath(t, service, status.SnapshotID, buildBefore.Node.ID, filepath.Join(dir, "gone.bin"))
	srcBefore := mapEntryByPath(t, service, status.SnapshotID, 1, other)
	rootBefore, err := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	// The world moves on: one file removed, one grown, one added, one new subdirectory.
	if err := os.Remove(filepath.Join(dir, "gone.bin")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "keep.bin"), 16384)
	write(filepath.Join(dir, "new.bin"), 4096)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "sub", "deep.bin"), 4096)

	// Re-reading through the file resolves to its directory.
	result, err := service.RereadDirectory(status.SnapshotID, keepBefore.Node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.NodeID != buildBefore.Node.ID || result.Path != dir {
		t.Fatalf("re-read did not land on the directory: %#v", result)
	}
	// build, keep.bin, new.bin, sub, sub/deep.bin
	if result.Nodes != 5 || result.Kept != 1 || result.Added != 3 || result.Removed != 1 {
		t.Fatalf("nodes/kept/added/removed = %d/%d/%d/%d: %#v", result.Nodes, result.Kept, result.Added, result.Removed, result)
	}

	buildAfter := mapEntryByPath(t, service, status.SnapshotID, 1, dir)
	if buildAfter.Node.ID != buildBefore.Node.ID {
		t.Fatalf("the directory must keep its ID: %d -> %d", buildBefore.Node.ID, buildAfter.Node.ID)
	}
	keepAfter := mapEntryByPath(t, service, status.SnapshotID, buildAfter.Node.ID, filepath.Join(dir, "keep.bin"))
	if keepAfter.Node.ID != keepBefore.Node.ID {
		t.Fatalf("an unchanged-in-place file must keep its ID: %d -> %d", keepBefore.Node.ID, keepAfter.Node.ID)
	}
	if keepAfter.OwnedAllocated <= keepBefore.OwnedAllocated {
		t.Fatalf("keep.bin grew on disk but not in the snapshot: %d -> %d", keepBefore.OwnedAllocated, keepAfter.OwnedAllocated)
	}
	newEntry := mapEntryByPath(t, service, status.SnapshotID, buildAfter.Node.ID, filepath.Join(dir, "new.bin"))
	subEntry := mapEntryByPath(t, service, status.SnapshotID, buildAfter.Node.ID, filepath.Join(dir, "sub"))
	mapEntryByPath(t, service, status.SnapshotID, subEntry.Node.ID, filepath.Join(dir, "sub", "deep.bin"))
	if newEntry.Node.ID <= goneBefore.Node.ID || subEntry.Node.ID <= goneBefore.Node.ID {
		t.Fatalf("new objects must take fresh IDs, not recycle old ones: new=%d sub=%d old max=%d", newEntry.Node.ID, subEntry.Node.ID, goneBefore.Node.ID)
	}
	if _, err := service.GetNodeEntry(status.SnapshotID, goneBefore.Node.ID); err == nil {
		t.Fatal("the removed file's ID still resolves")
	}

	// The directory's total is the sum of what is in it now, and the root moved
	// by exactly the same difference. The sibling directory did not move at all.
	wantBuild := keepAfter.OwnedAllocated + newEntry.OwnedAllocated + subEntry.OwnedAllocated
	if buildAfter.OwnedAllocated != wantBuild {
		t.Fatalf("build holds %d, its contents sum to %d", buildAfter.OwnedAllocated, wantBuild)
	}
	rootAfter, err := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if rootAfter.Parent.OwnedAllocated-rootBefore.Parent.OwnedAllocated != buildAfter.OwnedAllocated-buildBefore.OwnedAllocated {
		t.Fatalf("root moved by %d, build by %d", rootAfter.Parent.OwnedAllocated-rootBefore.Parent.OwnedAllocated, buildAfter.OwnedAllocated-buildBefore.OwnedAllocated)
	}
	if srcAfter := mapEntryByPath(t, service, status.SnapshotID, 1, other); srcAfter.Node.ID != srcBefore.Node.ID || srcAfter.OwnedAllocated != srcBefore.OwnedAllocated {
		t.Fatalf("the sibling was disturbed: %#v vs %#v", srcAfter, srcBefore)
	}
	if version, _ := store.SnapshotVersion(status.SnapshotID); version != result.Version {
		t.Fatalf("version %d does not match the result's %d", version, result.Version)
	}

	// After the re-read every action on the kept file works again.
	if action, err := service.RevealNode(status.SnapshotID, keepAfter.Node.ID); err != nil || !action.OK {
		t.Fatalf("reveal after re-read: %#v %v", action, err)
	}
}

func TestRereadDirectoryRefusesTheRootAndAGoneDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "build")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	service := NewService(Dependencies{Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts}, FileSystem: adapter, Permissions: adapter, Trash: adapter})
	status := scanForTest(t, service, root)

	if result, err := service.RereadDirectory(status.SnapshotID, 1); err != nil || result.OK || result.Code != "scan_root" {
		t.Fatalf("the scan root must be refused: %#v %v", result, err)
	}
	if result, err := service.RereadDirectory(0, 1); err != nil || result.OK || result.Code != "invalid_request" {
		t.Fatalf("a missing snapshot must be refused: %#v %v", result, err)
	}
	build := mapEntryByPath(t, service, status.SnapshotID, 1, dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if result, err := service.RereadDirectory(status.SnapshotID, build.Node.ID); err != nil || result.OK || result.Code != "stale_node" {
		t.Fatalf("a directory that is gone must be reported stale, not read: %#v %v", result, err)
	}
	if _, err := service.GetNodeEntry(status.SnapshotID, build.Node.ID); err != nil {
		t.Fatalf("a refused re-read must leave the snapshot alone: %v", err)
	}
}

// Identity pins the object; its contents are allowed to move. A cache directory
// that is still being written to changes its size and mtime between the scan
// and the delete, and that used to block the delete for no gain (ADR-0071).
func TestValidateCleanupPlanIgnoresContentChangesButNotReplacement(t *testing.T) {
	service := testService(t)
	root := t.TempDir()
	dir := filepath.Join(root, "Cache")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "entry.bin")
	if err := os.WriteFile(file, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshotID := scanForTest(t, service, root).SnapshotID
	plan, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: snapshotID, Paths: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}

	// The cache keeps working: a file grows, another appears, the directory's
	// mtime and size move. None of that changes which object "Cache" is.
	if err := os.WriteFile(file, []byte("after, and longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(dir, future, future); err != nil {
		t.Fatal(err)
	}
	validation, err := service.ValidateCleanupPlan(plan.ID, plan.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !validation.Valid {
		t.Fatalf("content changes must not block the delete: %#v", validation)
	}

	// Replacing the directory with a different one at the same path is a
	// different object, and that still blocks.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	validation, err = service.ValidateCleanupPlan(plan.ID, plan.Version)
	if err != nil {
		t.Fatal(err)
	}
	if validation.Valid || !strings.Contains(validation.Items[0].Reason, "已被替换") {
		t.Fatalf("a replaced object must still be refused: %#v", validation)
	}
}

// fakeFileEvents hands the test the callback the service registered, so events
// can be injected without a real FSEvents stream.
type fakeFileEvents struct {
	mu       sync.Mutex
	root     string
	onEvents func([]ports.FileEvent)
	stopped  int
}

func (f *fakeFileEvents) WatchFileEvents(root string, _ time.Duration, onEvents func([]ports.FileEvent)) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.root = root
	f.onEvents = onEvents
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopped++
	}, nil
}

func (f *fakeFileEvents) fire(events ...ports.FileEvent) {
	f.mu.Lock()
	callback := f.onEvents
	f.mu.Unlock()
	if callback != nil {
		callback(events)
	}
}

// The result follows the disk (ADR-0072): a directory's event lists it again,
// files that grew take their new size, new files and directories appear (the
// directory read in depth), removed ones go, and the ancestors move by the
// difference. Unchanged objects keep their IDs. One event is emitted per batch.
func TestLiveUpdateFollowsDirectoryEvents(t *testing.T) {
	previous := liveUpdateBatch
	liveUpdateBatch = 20 * time.Millisecond
	t.Cleanup(func() { liveUpdateBatch = previous })

	root := t.TempDir()
	dir := filepath.Join(root, "Cache")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, size int) {
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "keep.bin"), 4096)
	write(filepath.Join(dir, "gone.bin"), 8192)

	var emitted []LiveUpdate
	var emitMu sync.Mutex
	watcher := &fakeFileEvents{}
	store := memtree.OpenStore()
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	service := NewService(Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts}, FileSystem: adapter, Permissions: adapter, Trash: adapter,
		FileEvents: watcher,
		Emit: func(name string, data any) {
			if name != LiveUpdateEvent {
				return
			}
			emitMu.Lock()
			emitted = append(emitted, data.(LiveUpdate))
			emitMu.Unlock()
		},
	})
	t.Cleanup(service.StopLiveUpdate)
	status := scanForTest(t, service, root)
	if live := service.GetLiveUpdateStatus(); !live.Active || live.Root != root || live.SnapshotID != status.SnapshotID {
		t.Fatalf("a finished scan must start following its root: %#v", live)
	}

	cacheBefore := mapEntryByPath(t, service, status.SnapshotID, 1, dir)
	keepBefore := mapEntryByPath(t, service, status.SnapshotID, cacheBefore.Node.ID, filepath.Join(dir, "keep.bin"))
	goneBefore := mapEntryByPath(t, service, status.SnapshotID, cacheBefore.Node.ID, filepath.Join(dir, "gone.bin"))
	rootBefore, _ := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 10})

	// The disk moves: keep grows, gone goes, new appears, a subdirectory with
	// content is created. The system reports the directory (twice, with the
	// trailing slash FSEvents uses, and as a nested path arriving out of order).
	write(filepath.Join(dir, "keep.bin"), 16384)
	if err := os.Remove(filepath.Join(dir, "gone.bin")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "new.bin"), 4096)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "sub", "deep.bin"), 4096)
	watcher.fire(ports.FileEvent{Path: filepath.Join(dir, "sub") + "/"})
	watcher.fire(ports.FileEvent{Path: dir + "/"}, ports.FileEvent{Path: "/nowhere/outside/the/result/"})

	deadline := time.Now().Add(3 * time.Second)
	for {
		emitMu.Lock()
		count := len(emitted)
		emitMu.Unlock()
		if count > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	emitMu.Lock()
	got := append([]LiveUpdate(nil), emitted...)
	emitMu.Unlock()
	if len(got) != 1 || got[0].SnapshotID != status.SnapshotID || got[0].Directories != 1 {
		t.Fatalf("expected one update event for one directory, got %#v", got)
	}

	cacheAfter := mapEntryByPath(t, service, status.SnapshotID, 1, dir)
	if cacheAfter.Node.ID != cacheBefore.Node.ID {
		t.Fatalf("the directory must keep its ID: %d -> %d", cacheBefore.Node.ID, cacheAfter.Node.ID)
	}
	keepAfter := mapEntryByPath(t, service, status.SnapshotID, cacheAfter.Node.ID, filepath.Join(dir, "keep.bin"))
	if keepAfter.Node.ID != keepBefore.Node.ID || keepAfter.OwnedAllocated <= keepBefore.OwnedAllocated {
		t.Fatalf("keep.bin must keep its ID and take its new size: %#v -> %#v", keepBefore, keepAfter)
	}
	newEntry := mapEntryByPath(t, service, status.SnapshotID, cacheAfter.Node.ID, filepath.Join(dir, "new.bin"))
	subEntry := mapEntryByPath(t, service, status.SnapshotID, cacheAfter.Node.ID, filepath.Join(dir, "sub"))
	deepEntry := mapEntryByPath(t, service, status.SnapshotID, subEntry.Node.ID, filepath.Join(dir, "sub", "deep.bin"))
	if subEntry.OwnedAllocated != deepEntry.OwnedAllocated || subEntry.OwnedAllocated == 0 {
		t.Fatalf("a new directory must be read in depth: sub=%d deep=%d", subEntry.OwnedAllocated, deepEntry.OwnedAllocated)
	}
	if _, err := service.GetNodeEntry(status.SnapshotID, goneBefore.Node.ID); err == nil {
		t.Fatal("the removed file's ID still resolves")
	}
	wantCache := keepAfter.OwnedAllocated + newEntry.OwnedAllocated + subEntry.OwnedAllocated
	if cacheAfter.OwnedAllocated != wantCache {
		t.Fatalf("Cache holds %d, its contents sum to %d", cacheAfter.OwnedAllocated, wantCache)
	}
	rootAfter, _ := service.GetMap(MapQuery{SnapshotID: status.SnapshotID, ParentID: 1, Limit: 10})
	if rootAfter.Parent.OwnedAllocated-rootBefore.Parent.OwnedAllocated != cacheAfter.OwnedAllocated-cacheBefore.OwnedAllocated {
		t.Fatalf("root moved by %d, Cache by %d", rootAfter.Parent.OwnedAllocated-rootBefore.Parent.OwnedAllocated, cacheAfter.OwnedAllocated-cacheBefore.OwnedAllocated)
	}
	live := service.GetLiveUpdateStatus()
	if live.Batches != 1 || live.Directories != 1 || live.Dropped != 0 || live.Dirty != 0 || live.Version != got[0].Version {
		t.Fatalf("status: %#v (event %#v)", live, got[0])
	}

	// A new scan stops the stream before the tree is replaced.
	service.StopLiveUpdate()
	watcher.mu.Lock()
	stopped := watcher.stopped
	watcher.mu.Unlock()
	if stopped != 1 || service.GetLiveUpdateStatus().Active {
		t.Fatalf("stop must end the stream exactly once: stopped=%d status=%#v", stopped, service.GetLiveUpdateStatus())
	}
}
