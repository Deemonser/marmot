package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/platform"
	"example.com/marmot/internal/ports"
)

func TestScanIsDeterministicAndDeduplicatesHardlinks(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "one.bin")
	if err := os.WriteFile(file, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(root, "one-link.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "two.bin"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}

	var nodes []Node
	var phases []scan.Phase
	result, err := Scan(context.Background(), root, func(node Node) error {
		nodes = append(nodes, node)
		return nil
	}, func(phase scan.Phase) error {
		phases = append(phases, phase)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 5 || result.Files != 3 || result.Directories != 2 {
		t.Fatalf("unexpected scan result: nodes=%d files=%d dirs=%d", len(nodes), result.Files, result.Directories)
	}
	if nodes[1].Name != "nested" || nodes[2].Name != "one-link.bin" || nodes[3].Name != "one.bin" || nodes[4].Name != "two.bin" {
		t.Fatalf("unexpected staged scan order: %#v", nodes)
	}
	if nodes[2].OwnedAllocated == 0 || nodes[3].OwnedAllocated != 0 {
		t.Fatalf("hardlink ownership was not deterministic: link=%d original=%d", nodes[2].OwnedAllocated, nodes[3].OwnedAllocated)
	}
	expectedPhases := []scan.Phase{scan.PhaseCatalog, scan.PhaseVolumeOverview, scan.PhaseTopLevelPublish, scan.PhaseDeepScan, scan.PhaseFinalize}
	if len(phases) != len(expectedPhases) {
		t.Fatalf("unexpected phase sequence: %#v", phases)
	}
	for i, expected := range expectedPhases {
		if phases[i] != expected {
			t.Fatalf("unexpected phase at %d: got %s want %s", i, phases[i], expected)
		}
	}
	rootSize := result.DirectorySizes[1]
	if rootSize.LogicalSize <= rootSize.OwnedAllocated {
		t.Fatalf("logical and owned sizes were collapsed: %#v", rootSize)
	}
}

func TestScanCanBeCancelled(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(root, filepath.Base(t.Name())+string(rune('a'+i))), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Scan(ctx, root, func(Node) error { return nil }, nil)
	if err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestScanCanBeCancelledAfterDirectoryEnumeration(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 256; index++ {
		child := filepath.Join(root, fmt.Sprintf("directory-%03d", index))
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(child, "entry.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := false
	_, err := Scan(ctx, root, func(node Node) error {
		if !cancelled && node.ParentID != 0 && node.Kind == "directory" {
			cancelled = true
			cancel()
		}
		return nil
	}, nil)
	if err != context.Canceled {
		t.Fatalf("expected cancellation after directory enumeration, got %v", err)
	}
}

func TestConfiguredScanCanBeCancelledDuringNativeEnumeration(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 512; index++ {
		child := filepath.Join(root, fmt.Sprintf("native-directory-%03d", index))
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(child, "entry.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := false
	_, err := (Scanner{MountResolver: func() ([]ports.Mount, error) {
		return []ports.Mount{{ID: "cancel-volume", Path: root, DeviceProfile: scan.DeviceProfileSSD}}, nil
	}}).Scan(ctx, root, func(node Node) error {
		if !cancelled && node.ParentID != 0 {
			cancelled = true
			cancel()
		}
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected configured scan cancellation, got %v", err)
	}
}

func TestScanDoesNotDeadlockWhenDirectoryQueueFills(t *testing.T) {
	root := t.TempDir()
	fanout := directoryQueueCapacity + 128
	for _, parentName := range []string{"first", "second"} {
		parent := filepath.Join(root, parentName)
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < fanout; index++ {
			if err := os.Mkdir(filepath.Join(parent, fmt.Sprintf("child-%05d", index)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := Scan(ctx, root, func(Node) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectedDirectories := int64(1 + 2 + fanout*2)
	if result.Directories != expectedDirectories {
		t.Fatalf("high fanout scan lost directory work: got %d want %d", result.Directories, expectedDirectories)
	}
}

func TestScanHandlesDirectoryLargerThanBulkOutputLimit(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "large-directory")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8200; index++ {
		if err := os.Mkdir(filepath.Join(parent, fmt.Sprintf("child-%05d", index)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := Scan(ctx, root, func(Node) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantDirectories := int64(1 + 1 + 8200)
	if result.Directories != wantDirectories {
		t.Fatalf("large directory lost entries: got %d want %d", result.Directories, wantDirectories)
	}
}

func TestConfiguredScanDoesNotDeadlockWhenNativeQueueFills(t *testing.T) {
	root := t.TempDir()
	fanout := directoryQueueCapacity + 128
	for _, parentName := range []string{"first", "second"} {
		parent := filepath.Join(root, parentName)
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < fanout; index++ {
			if err := os.Mkdir(filepath.Join(parent, fmt.Sprintf("native-child-%05d", index)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := (Scanner{MountResolver: func() ([]ports.Mount, error) {
		return []ports.Mount{{ID: "native-queue-volume", Path: root, DeviceProfile: scan.DeviceProfileSSD}}, nil
	}}).Scan(ctx, root, func(Node) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectedDirectories := int64(1 + 2 + fanout*2)
	if result.Directories != expectedDirectories {
		t.Fatalf("native high fanout scan lost directory work: got %d want %d", result.Directories, expectedDirectories)
	}
}

func TestConfiguredScanBatchesNodes(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 1024; index++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("batch-file-%04d", index)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	totalNodes := 0
	batchCount := 0
	multiNodeBatch := false
	result, err := (Scanner{MountResolver: func() ([]ports.Mount, error) {
		return []ports.Mount{{ID: "batch-volume", Path: root, DeviceProfile: scan.DeviceProfileSSD}}, nil
	}}).ScanBatched(context.Background(), root, func(nodes []Node) error {
		batchCount++
		totalNodes += len(nodes)
		multiNodeBatch = multiNodeBatch || len(nodes) > 1
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if totalNodes != int(result.Nodes) || result.Files != 1024 || result.Directories != 1 {
		t.Fatalf("batched scan summary mismatch: result=%#v emitted=%d", result, totalNodes)
	}
	if runtime.GOOS == "darwin" && (!multiNodeBatch || batchCount >= totalNodes) {
		t.Fatalf("darwin scan did not batch native output: batches=%d nodes=%d multi=%v", batchCount, totalNodes, multiNodeBatch)
	}
}

func TestConfiguredScanDeduplicatesHardlinks(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.bin")
	second := filepath.Join(root, "second-link.bin")
	if err := os.WriteFile(first, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Fatal(err)
	}

	var nodes []Node
	result, err := (Scanner{MountResolver: func() ([]ports.Mount, error) {
		return []ports.Mount{{ID: "native-hardlink-volume", Path: root, DeviceProfile: scan.DeviceProfileSSD}}, nil
	}}).Scan(context.Background(), root, func(node Node) error {
		nodes = append(nodes, node)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	owned := 0
	duplicate := 0
	for _, node := range nodes {
		if node.Kind != "file" {
			continue
		}
		if node.OwnedAllocated > 0 {
			owned++
		} else {
			duplicate++
		}
	}
	if owned != 1 || duplicate != 1 {
		t.Fatalf("configured scan did not preserve hardlink ownership: owned=%d duplicate=%d nodes=%#v", owned, duplicate, nodes)
	}
	if result.Files != 2 || result.Bytes <= 0 {
		t.Fatalf("configured scan summary lost hardlink files: result=%#v", result)
	}
	if got := result.DirectorySizes[1].OwnedAllocated; got != result.Bytes {
		t.Fatalf("configured root directory size does not match deduplicated bytes: root=%d bytes=%d", got, result.Bytes)
	}
}

func TestWorkersForProfileUsesFrozenBudgets(t *testing.T) {
	tests := []struct {
		profile scan.DeviceProfile
		want    int
	}{
		{profile: scan.DeviceProfileSSD, want: 8},
		{profile: scan.DeviceProfileRotational, want: 1},
		{profile: scan.DeviceProfileNetworkOrVirtual, want: 2},
		{profile: scan.DeviceProfileUnknown, want: 2},
	}
	for _, test := range tests {
		if got := workersForProfile(test.profile); got != test.want {
			t.Fatalf("workersForProfile(%q) = %d, want %d", test.profile, got, test.want)
		}
	}
}

func TestScanConfiguredRootSmoke(t *testing.T) {
	root := os.Getenv("MARMOT_SCAN_ROOT")
	if root == "" {
		t.Skip("set MARMOT_SCAN_ROOT to run a read-only scan smoke test")
	}
	started := time.Now()
	result, err := (Scanner{MountResolver: (platform.Adapter{}).ListMounts}).Scan(context.Background(), root, func(Node) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("root=%s elapsed=%s nodes=%d files=%d directories=%d bytes=%d issues=%d", root, time.Since(started), result.Nodes, result.Files, result.Directories, result.Bytes, len(result.Issues))
}

func TestScannerSkipsNestedMountAndCarriesVolumeID(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested-mount")
	similar := filepath.Join(root, "nested-mount-copy")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(similar, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("visible"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "hidden.txt"), []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(similar, "visible.txt"), []byte("visible"), 0o644); err != nil {
		t.Fatal(err)
	}

	var nodes []Node
	result, err := (Scanner{MountResolver: func() ([]ports.Mount, error) {
		return []ports.Mount{{ID: "root-volume", Path: root}, {ID: "nested-volume", Path: nested}}, nil
	}}).Scan(context.Background(), root, func(node Node) error {
		nodes = append(nodes, node)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 2 || len(nodes) != 4 {
		t.Fatalf("nested mount was scanned: files=%d nodes=%d", result.Files, len(nodes))
	}
	for _, node := range nodes {
		if node.VolumeID != "root-volume" {
			t.Fatalf("node has wrong volume identity: %#v", node)
		}
		if node.Path == filepath.Join(nested, "hidden.txt") {
			t.Fatal("nested mount file must not be present")
		}
		if node.Path == filepath.Join(similar, "visible.txt") && node.VolumeID != "root-volume" {
			t.Fatal("component-prefix sibling was assigned the wrong volume identity")
		}
	}
}

func TestScannerUsesPlatformMountCatalogForSmallDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("sample"), 0o644); err != nil {
		t.Fatal(err)
	}

	var nodes []Node
	result, err := (Scanner{MountResolver: (platform.Adapter{}).ListMounts}).Scan(context.Background(), root, func(node Node) error {
		nodes = append(nodes, node)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 || len(nodes) != 2 {
		t.Fatalf("unexpected live small-directory scan: result=%#v nodes=%#v", result, nodes)
	}
	if runtime.GOOS == "darwin" && nodes[0].VolumeID == "" {
		t.Fatal("macOS scan did not carry a mount identity")
	}
}
