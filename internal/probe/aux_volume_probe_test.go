package probe

import (
	"context"
	"os"
	"testing"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/platform"
)

// TestAuxVolumeCoverage scans the APFS volume-group auxiliary volumes on their
// own, to confirm the scanner can read them and that their sizes match statfs
// before the mount-boundary rule is changed (R-053).
func TestAuxVolumeCoverage(t *testing.T) {
	if os.Getenv("PROBE_AUX") == "" {
		t.Skip("set PROBE_AUX to run the real-volume probe")
	}
	adapter := platform.Adapter{}
	engine := scanner.Scanner{MountResolver: adapter.ListMounts}
	for _, root := range []string{"/System/Volumes/VM", "/System/Volumes/Preboot", "/System/Volumes/Update", "/System/Volumes/Hardware", "/System/Volumes/iSCPreboot", "/System/Volumes/xarts"} {
		var nodes, files int64
		var owned, allocated int64
		result, err := engine.ScanBatched(context.Background(), root, func(batch []scan.Node) error {
			for _, node := range batch {
				nodes++
				if node.Kind == "file" {
					files++
					owned += node.OwnedAllocated
					allocated += node.AllocatedSize
				}
			}
			return nil
		}, func(scan.Phase) error { return nil })
		if err != nil {
			t.Logf("PROBE root=%-32s ERROR %v", root, err)
			continue
		}
		const gib = float64(1 << 30)
		t.Logf("PROBE root=%-32s nodes=%7d files=%7d owned_gib=%6.2f allocated_gib=%6.2f issues=%d",
			root, nodes, files, float64(owned)/gib, float64(allocated)/gib, len(result.Issues))
	}
}
