package application

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/ports"
)

func testService(t *testing.T) *Service {
	return testServiceWithTrash(t, platform.Adapter{})
}

func testServiceWithTrash(t *testing.T, trash ports.Trash) *Service {
	t.Helper()
	store, err := snapshot.Open(filepath.Join(t.TempDir(), "snapshots.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	return NewService(Dependencies{Store: store, Scanner: scanner.Scanner{}, FileSystem: adapter, Permissions: adapter, Trash: trash})
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
	if validation.Valid || len(validation.Items) != 1 || validation.Items[0].Reason != "file identity changed" {
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

func TestGetScanStatusRecoversInterruptedSnapshot(t *testing.T) {
	service := testService(t)
	root := t.TempDir()
	snapshotID := addTestSnapshot(t, service, root, "")
	if err := service.RecoverInterruptedScans(); err != nil {
		t.Fatal(err)
	}
	status, err := service.GetScanStatus("test-task")
	if err != nil {
		t.Fatal(err)
	}
	if status.SnapshotID != snapshotID || status.State != "interrupted" || status.Root != root {
		t.Fatalf("unexpected recovered status: %#v", status)
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

func TestCancelScanKeepsCommittedPartialResults(t *testing.T) {
	store, err := snapshot.Open(filepath.Join(t.TempDir(), "snapshots.db"))
	if err != nil {
		t.Fatal(err)
	}
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

type recordingTrash struct {
	paths []string
}

func (t *recordingTrash) Trash(path string) (string, error) {
	t.paths = append(t.paths, path)
	return path, nil
}

type blockingScanner struct {
	flushed chan struct{}
}

func (s *blockingScanner) Scan(ctx context.Context, root string, emit scan.Emitter) (scan.Result, error) {
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
