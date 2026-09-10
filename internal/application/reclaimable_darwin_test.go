package application

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"example.com/marmot/internal/infrastructure/scanner"
	"example.com/marmot/internal/infrastructure/snapshot/memtree"
	"example.com/marmot/internal/platform"
)

// ADR-0074 gates 3 and 4, end to end on a real APFS directory: a hardlinked pair
// and a cloned pair, scanned by the real scanner into the real store, then asked
// what deleting each selection gives back. The numbers are the ones R-076 §10.2
// measured against df: one of a pair reclaims nothing, both reclaim the data
// once.
func TestReclaimableOnRealFixture(t *testing.T) {
	root, err := os.MkdirTemp("", "marmot-reclaim-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	// The scanner reports the real path; TMPDIR is a symlink on macOS.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	const size = 1 << 20
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "a"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "b"), 0o755))
	must(os.WriteFile(filepath.Join(root, "a", "plain.bin"), payload, 0o644))
	must(os.WriteFile(filepath.Join(root, "a", "linked.bin"), payload, 0o644))
	must(os.Link(filepath.Join(root, "a", "linked.bin"), filepath.Join(root, "b", "linked.bin")))
	must(os.WriteFile(filepath.Join(root, "a", "cloned.bin"), payload, 0o644))
	if out, err := exec.Command("cp", "-c", filepath.Join(root, "a", "cloned.bin"), filepath.Join(root, "b", "cloned.bin")).CombinedOutput(); err != nil {
		t.Skipf("cp -c (clonefile) unavailable here: %v %s", err, out)
	}

	store := memtree.OpenStore()
	defer store.Close()
	adapter := platform.Adapter{}
	service := NewService(Dependencies{
		Store: store, Scanner: scanner.Scanner{MountResolver: adapter.ListMounts},
		FileSystem: adapter, Permissions: adapter, Trash: adapter, Volumes: adapter, Preview: adapter, ScanTotals: adapter,
		Emit: func(string, any) {},
	})
	status, err := service.StartScan(ScanOptions{Root: root})
	must(err)
	var final ScanStatus
	deadline := time.Now().Add(30 * time.Second)
	for {
		final, err = service.GetScanStatus(status.TaskID)
		must(err)
		if final.State != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	id := func(path string) int64 {
		node, err := store.NodeByPath(final.SnapshotID, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return node.ID
	}
	plain := id(filepath.Join(root, "a", "plain.bin"))
	linkA, linkB := id(filepath.Join(root, "a", "linked.bin")), id(filepath.Join(root, "b", "linked.bin"))
	cloneA, cloneB := id(filepath.Join(root, "a", "cloned.bin")), id(filepath.Join(root, "b", "cloned.bin"))

	ask := func(ids ...int64) ReclaimableSummary {
		result, err := service.GetReclaimable(final.SnapshotID, ids)
		must(err)
		return result
	}
	plainOnly := ask(plain)
	if plainOnly.Unknown {
		t.Skip("this volume does not report the reclaim attributes; ADR-0074 gate 3/4 cannot run here")
	}
	allocated := plainOnly.Bytes
	if allocated < size {
		t.Fatalf("plain file should reclaim its allocated size, got %d", allocated)
	}
	// Gate 4: the hardlink pair.
	if got := ask(linkA); got.Bytes != 0 || got.SharedExcluded != allocated {
		t.Errorf("one hardlink: got %+v, want 0 reclaimable and %d shared", got, allocated)
	}
	if got := ask(linkB); got.Bytes != 0 {
		t.Errorf("the other hardlink alone: got %+v, want 0", got)
	}
	if got := ask(linkA, linkB); got.Bytes != allocated || got.SharedExcluded != 0 {
		t.Errorf("both hardlinks: got %+v, want %d", got, allocated)
	}
	// Gate 3: the clone pair. One of them alone reclaims nothing either way; a
	// kernel too old for ATTR_CMNEXT_CLONEID cannot tell the pair apart from
	// two files sharing with something outside, so it under-reports the pair --
	// the conservative direction, asserted as such rather than skipped.
	if got := ask(cloneA); got.Bytes != 0 || got.SharedExcluded != allocated {
		t.Errorf("one clone: got %+v, want 0 reclaimable and %d shared", got, allocated)
	}
	if scanner.CloneFactsAvailable() {
		if got := ask(cloneA, cloneB); got.Bytes != allocated || got.SharedExcluded != 0 {
			t.Errorf("both clones: got %+v, want %d", got, allocated)
		}
		if got := ask(id(root)); got.Bytes != 3*allocated || got.UpperBound != 3*allocated {
			t.Errorf("whole root: got %+v, want %d", got, 3*allocated)
		}
	} else {
		t.Logf("this kernel did not take the clone attributes; asserting the degraded, conservative answer instead")
		if got := ask(cloneA, cloneB); got.Bytes != 0 || got.SharedExcluded != 2*allocated {
			t.Errorf("both clones without clone identity: got %+v, want 0 reclaimable and %d shared", got, 2*allocated)
		}
		if got := ask(id(root)); got.Bytes != 2*allocated {
			t.Errorf("whole root without clone identity: got %+v, want %d", got, 2*allocated)
		}
	}
}
