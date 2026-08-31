package application

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

type recordingTrash struct {
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
	t.paths = append(t.paths, path)
	return path, nil
}

func (t *recordingTrash) RemovePermanently(path string) error {
	if err := t.failOn[path]; err != nil {
		return err
	}
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
	if err != nil || applied.State != "applied" || len(trash.paths) != 1 || trash.paths[0] != path {
		t.Fatalf("cleanup execution failed: plan=%#v paths=%#v err=%v", applied, trash.paths, err)
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
func runPlan(t *testing.T, service *Service, snapshotID int64, paths []string, permanent bool) CleanupPlan {
	t.Helper()
	plan, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: snapshotID, Paths: paths, Permanent: permanent})
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

// Moving to the trash frees nothing: same volume, so it is a rename. A permanent
// plan has to actually take the other path, not quietly do the safe thing.
func TestPermanentPlanRemovesInsteadOfTrashing(t *testing.T) {
	trash := &recordingTrash{}
	service := testServiceWithTrash(t, trash)
	root := t.TempDir()
	path := filepath.Join(root, "candidate.txt")
	if err := os.WriteFile(path, []byte("candidate"), 0o644); err != nil {
		t.Fatal(err)
	}
	applied := runPlan(t, service, scanned(t, service, root), []string{path}, true)
	if applied.State != "applied" || !applied.Permanent {
		t.Fatalf("permanent plan came back as %#v", applied)
	}
	if len(trash.removed) != 1 || trash.removed[0] != path {
		t.Fatalf("nothing was removed outright: removed=%#v trashed=%#v", trash.removed, trash.paths)
	}
	if len(trash.paths) != 0 {
		t.Fatalf("a permanent plan used the trash anyway: %#v", trash.paths)
	}
	// The result line has to say which of the two happened; they are not
	// interchangeable and only one of them can be undone.
	if applied.Results[0].Reason != "已直接删除" {
		t.Fatalf("the result does not distinguish the two: %q", applied.Results[0].Reason)
	}
}

// The trash makes the irreplaceable guard advisory -- a mistake is a restore
// away. Permanent deletion removes that, so the guard stops being advice.
func TestPermanentDeletionIsRefusedForWhatCannotComeBack(t *testing.T) {
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
	snapshotID := scanned(t, service, root)

	_, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: snapshotID, Paths: []string{repo}, Permanent: true})
	if err == nil {
		t.Fatal("a repository was accepted for permanent deletion")
	}
	if !strings.Contains(err.Error(), "无法恢复") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	// And the reversible path is still open: the guard is a refusal only where
	// there is no way back.
	if _, err := service.CreateCleanupPlan(CleanupPlanRequest{SnapshotID: snapshotID, Paths: []string{repo}}); err != nil {
		t.Fatalf("the same path was refused for the trash too: %v", err)
	}
}

// Permanence is chosen per plan and never inherited. A plan built without the
// flag must not remove anything outright, however the previous one was built.
func TestPermanenceIsNotStickyBetweenPlans(t *testing.T) {
	trash := &recordingTrash{}
	service := testServiceWithTrash(t, trash)
	root := t.TempDir()
	first := filepath.Join(root, "first.txt")
	second := filepath.Join(root, "second.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snapshotID := scanned(t, service, root)
	runPlan(t, service, snapshotID, []string{first}, true)
	applied := runPlan(t, service, snapshotID, []string{second}, false)
	if applied.Permanent {
		t.Fatal("the second plan inherited permanence")
	}
	if len(trash.paths) != 1 || trash.paths[0] != second {
		t.Fatalf("the second plan did not use the trash: %#v", trash.paths)
	}
}
