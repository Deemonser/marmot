package probe

import (
	"fmt"
	"os"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// TestStartScanLatency measures how long StartScan blocks before returning.
// The UI only leaves the source page once this call resolves, so whatever it
// costs is dead time the user sees as an unresponsive button.
func TestStartScanLatency(t *testing.T) {
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		t.Skip("set PROBE_ROOT to run the real-volume probe")
	}
	adapter := platform.Adapter{}
	store := memtree.OpenStore()
	defer store.Close()
	service := marmotapp.NewService(marmotapp.Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter,
	})

	// The pieces StartScan does synchronously, timed on their own first.
	normalizeStart := time.Now()
	normalized, err := adapter.NormalizeScanRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	normalizeMS := time.Since(normalizeStart).Seconds() * 1000

	volumesStart := time.Now()
	volumes, err := adapter.ListVolumes()
	if err != nil {
		t.Fatal(err)
	}
	volumesMS := time.Since(volumesStart).Seconds() * 1000

	mountsStart := time.Now()
	mounts, err := adapter.ListMounts()
	if err != nil {
		t.Fatal(err)
	}
	mountsMS := time.Since(mountsStart).Seconds() * 1000

	started := time.Now()
	status, err := service.StartScan(marmotapp.ScanOptions{Root: normalized})
	if err != nil {
		t.Fatal(err)
	}
	startMS := time.Since(started).Seconds() * 1000

	// First status poll after the call returns: the UI does this too.
	pollStart := time.Now()
	if _, err := service.GetScanStatus(status.TaskID); err != nil {
		t.Fatal(err)
	}
	pollMS := time.Since(pollStart).Seconds() * 1000

	t.Logf("PROBE NormalizeScanRoot        %8.1f ms", normalizeMS)
	t.Logf("PROBE ListVolumes (%2d)         %8.1f ms", len(volumes), volumesMS)
	t.Logf("PROBE ListMounts  (%2d)         %8.1f ms", len(mounts), mountsMS)
	t.Logf("PROBE StartScan  (blocking)    %8.1f ms   <- the UI waits on this", startMS)
	t.Logf("PROBE GetScanStatus            %8.1f ms", pollMS)

	if _, err := service.CancelScan(status.TaskID); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stderr)
}
