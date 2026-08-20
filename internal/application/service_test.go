package application

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot"
	"example.com/marmot/internal/platform"
)

func testService(t *testing.T) *Service {
	t.Helper()
	store, err := snapshot.Open(filepath.Join(t.TempDir(), "snapshots.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	adapter := platform.Adapter{}
	return NewService(Dependencies{Store: store, Scanner: scanner.Scanner{}, FileSystem: adapter, Permissions: adapter, Trash: adapter})
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
