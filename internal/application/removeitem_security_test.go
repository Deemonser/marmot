package application

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"example.com/marmot/internal/domain/cleanup"
)

// The staged item is pinned by device and inode when the plan is validated, and
// the chunk paths are rebuilt from the snapshot as absolute paths. If each one is
// resolved from "/" again at the moment of deletion, then swapping the item
// directory for a symlink in between redirects every removal through the link and
// out of the item -- turning "delete this cache" into "delete whatever that
// points at". The single recursive remove this replaced could not do that: it
// operated on the item path itself and does not follow a final symlink.
func TestRemoveItemRefusesAnItemSwappedForASymlink(t *testing.T) {
	service := testService(t)
	base := t.TempDir()
	item := filepath.Join(base, "cache")
	decoy := filepath.Join(base, "decoy")
	keep := filepath.Join(decoy, "pkg", "keep.txt")

	// The tree the plan was built from, and the chunk it produced.
	if err := os.MkdirAll(filepath.Join(item, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	captured, err := service.files.CaptureCleanupItem(item)
	if err != nil {
		t.Fatal(err)
	}
	// What must survive.
	if err := os.MkdirAll(filepath.Join(decoy, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The swap: between the plan's validation and the removal, the item becomes a
	// symlink to somewhere else. Made deterministic here; in the wild it is a race
	// on any directory the user can write.
	if err := os.RemoveAll(item); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, item); err != nil {
		t.Fatal(err)
	}

	var done atomic.Int64
	subtree := cleanup.Subtree{
		TotalNodes: 2,
		Chunks:     []cleanup.Chunk{{Paths: []string{filepath.Join(item, "pkg")}, Nodes: 1}},
	}
	_ = service.removeItem("plan-test", captured, subtree, &done, func() {})

	if _, err := os.Lstat(keep); err != nil {
		t.Fatalf("the removal followed the swapped item out of the plan and deleted %s: %v", keep, err)
	}
}

// The other half of the fix: resolving names inside a pinned descriptor must not
// make an ordinary cache undeletable. Caches are full of symlinks -- a
// node_modules/.bin is nothing else -- and some of them point outside the tree.
// Those must be unlinked like any other entry, and what they point at left alone.
func TestRemoveItemDeletesATreeFullOfSymlinks(t *testing.T) {
	service := testService(t)
	base := t.TempDir()
	item := filepath.Join(base, "node_modules")
	bin := filepath.Join(item, ".bin")
	outside := filepath.Join(base, "outside.txt")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(item, "real.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"absolute-outside": outside,
		"relative-escape":  "../../outside.txt",
		"relative-inside":  "../real.js",
		"dangling":         "nowhere-at-all",
	} {
		if err := os.Symlink(target, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}

	captured, err := service.files.CaptureCleanupItem(item)
	if err != nil {
		t.Fatal(err)
	}
	var done atomic.Int64
	subtree := cleanup.Subtree{
		TotalNodes: 7,
		Chunks:     []cleanup.Chunk{{Paths: []string{bin}, Nodes: 5}},
	}
	if err := service.removeItem("plan-test", captured, subtree, &done, func() {}); err != nil {
		t.Fatalf("a cache holding symlinks could not be deleted: %v", err)
	}
	if _, err := os.Lstat(item); !os.IsNotExist(err) {
		t.Fatalf("the item survived: %v", err)
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Fatalf("a symlink target outside the item was followed and deleted: %v", err)
	}
}
