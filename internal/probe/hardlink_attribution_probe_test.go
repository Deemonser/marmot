package probe

import (
	"context"
	"os"
	"testing"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/platform"
)

// Which objects does the scanner charge the bytes to, and is the answer stable?
//
// Two questions at once, because one fixture answers both. Point this at a tree
// holding a hardlinked pair and a cloned pair (`ln a b`, `cp -c a b`) in
// different directories.
//
//   - Hardlinks: exactly one of the pair should carry the bytes. ADR-0008 also
//     requires WHICH one not to depend on scan order, and the scanner decides it
//     first-come under a lock while batches arrive from concurrent workers.
//     Measured 2026-09-10 on a 9,604-file fixture with the pair at equal depth
//     in dir_03 and dir_57: stable, the same file carried them 12/12 runs. The
//     drift is structurally possible and was NOT observed.
//   - Clones: both members carry the full allocated size, 12/12 -- the scanner
//     has no clone handling at all, so a cloned pair is counted twice. Confirmed
//     against the disk: deleting one member freed nothing, deleting the second
//     freed the whole 8 MB.
//
// R-076 §10.2 records both, and TestCloneSizes reads the private size that a
// clone-aware accounting would have to be built on.
//
//	PROBE_HARDLINK_ROOT=/path/to/fixture go test ./internal/probe -run HardlinkAttribution -v
func TestHardlinkAttribution(t *testing.T) {
	root := os.Getenv("PROBE_HARDLINK_ROOT")
	if root == "" {
		t.Skip("set PROBE_HARDLINK_ROOT to a directory holding hardlinked files")
	}
	subject := scanner.Scanner{MountResolver: platform.Adapter{}.ListMounts}
	owners := map[string]int{}
	const runs = 12
	for range runs {
		var carriers []string
		_, err := subject.Scan(context.Background(), root, func(node scan.Node) error {
			if node.Kind == "file" && node.OwnedAllocated > 0 {
				carriers = append(carriers, node.Name)
			}
			return nil
		}, nil)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, path := range carriers {
			owners[path]++
		}
	}
	t.Logf("%d 次扫描，各文件成为记账方的次数：", runs)
	for name, count := range owners {
		t.Logf("  %-40s %d/%d", name, count, runs)
	}
}
