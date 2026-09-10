package memtree

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"example.com/marmot/internal/domain/scan"
)

// The seed format version is a promise about the raw pages. If the record or
// the side-table entry changes shape, an old seed would be read as garbage
// unless the version changes with it -- so this test pins both to the version.
func TestSeedFormatPinsLayout(t *testing.T) {
	if seedFormatVersion != 1 {
		t.Fatalf("seed format version is %d; update the pinned sizes below with it", seedFormatVersion)
	}
	if size := unsafe.Sizeof(record{}); size != 64 {
		t.Fatalf("record is %d bytes; a seed written by this build would not match version %d", size, seedFormatVersion)
	}
	if size := unsafe.Sizeof(reclaimEntry{}); size != 40 {
		t.Fatalf("reclaim entry is %d bytes; a seed written by this build would not match version %d", size, seedFormatVersion)
	}
	if recordPageShift != 12 || arenaPageSize != 1<<20 {
		t.Fatalf("page sizes changed; bump the seed format version")
	}
}

func TestSeedRoundTrip(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	source, err := store.CreateSnapshot("task-a", "/root")
	if err != nil {
		t.Fatal(err)
	}
	original := reclaimFixture(t)
	store.mu.Lock()
	store.trees[source] = original
	store.mu.Unlock()
	if err := store.SetSnapshotVolume(source, 1000, 600, 400); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertIssues(source, []scan.Issue{{Path: "/root/denied", Message: "permission"}}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "current.seed")
	header := scan.SeedHeader{
		Root: "/root", Device: 42, JournalUUID: "E0637B1F", EventID: 12345, WrittenAt: time.Now(),
		Calibration:     scan.SeedCalibration{EventsPerID: 0.01, DirsPerEvent: 0.2, ReplayMsPerEvent: 0.3, SpliceMsPerDir: 1.6},
		FullScanSeconds: 18.5,
	}
	size, err := store.WriteSeed(source, path, header)
	if err != nil {
		t.Fatal(err)
	}
	if size == 0 {
		t.Fatal("seed file is empty")
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file left behind: %v", err)
	}

	read, err := store.ReadSeedHeader(path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Root != header.Root || read.Device != header.Device || read.JournalUUID != header.JournalUUID ||
		read.EventID != header.EventID || read.Calibration != header.Calibration || read.FullScanSeconds != header.FullScanSeconds {
		t.Fatalf("header changed on the way through: %+v", read)
	}

	// Load into a fresh, empty snapshot and compare what the space map and the
	// collector would see.
	target, err := store.CreateSnapshot("task-b", "/root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSeed(target, path); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	loaded := store.trees[target]
	store.mu.Unlock()
	if loaded.records.len() != original.records.len() || loaded.names.next != original.names.next || loaded.rootNodeID != original.rootNodeID {
		t.Fatalf("shape differs: records %d/%d names %d/%d root %d/%d",
			loaded.records.len(), original.records.len(), loaded.names.next, original.names.next, loaded.rootNodeID, original.rootNodeID)
	}
	for id := int64(1); id < original.records.len(); id++ {
		if *loaded.records.at(id) != *original.records.at(id) || loaded.path(id) != original.path(id) {
			t.Fatalf("node %d differs after load", id)
		}
	}
	if len(loaded.children(loaded.rootNodeID)) != len(original.children(original.rootNodeID)) {
		t.Fatal("children differ after load")
	}
	if got, want := loaded.reclaimable([]int64{1}), original.reclaimable([]int64{1}); got != want {
		t.Fatalf("reclaim side table did not survive: %+v vs %+v", got, want)
	}
	if loaded.volumeTotal != 1000 || loaded.volumeUsed != 600 || len(loaded.issues) != 1 || loaded.issueCount != 1 {
		t.Fatalf("volume figures or issues lost: %+v %d", loaded.issues, loaded.volumeTotal)
	}
	if loaded.nodeCount != original.nodeCount || loaded.bytes != original.bytes {
		t.Fatalf("counts lost: %d/%d %d/%d", loaded.nodeCount, original.nodeCount, loaded.bytes, original.bytes)
	}

	// A second load into the same, now non-empty snapshot is refused; reset
	// empties it again.
	if _, err := store.LoadSeed(target, path); err == nil {
		t.Fatal("loading over a populated snapshot must be refused")
	}
	if err := store.ResetSnapshot(target); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	reset := store.trees[target]
	store.mu.Unlock()
	if reset.records.len() != 1 || reset.taskID != "task-b" {
		t.Fatalf("reset should leave an empty tree with the same task: len=%d task=%s", reset.records.len(), reset.taskID)
	}
}

func TestSeedRejectsForeignAndTruncatedFiles(t *testing.T) {
	store := OpenStore()
	defer store.Close()
	dir := t.TempDir()

	foreign := filepath.Join(dir, "foreign.seed")
	if err := os.WriteFile(foreign, []byte("not a seed at all, just bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSeedHeader(foreign); !errors.Is(err, ErrSeedFormat) {
		t.Fatalf("foreign file: got %v, want ErrSeedFormat", err)
	}

	source, _ := store.CreateSnapshot("task", "/root")
	store.mu.Lock()
	store.trees[source] = reclaimFixture(t)
	store.mu.Unlock()
	path := filepath.Join(dir, "current.seed")
	if _, err := store.WriteSeed(source, path, scan.SeedHeader{Root: "/root", EventID: 1}); err != nil {
		t.Fatal(err)
	}
	whole, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "truncated.seed")
	if err := os.WriteFile(truncated, whole[:len(whole)-100], 0o600); err != nil {
		t.Fatal(err)
	}
	target, _ := store.CreateSnapshot("task-t", "/root")
	if _, err := store.LoadSeed(target, truncated); !errors.Is(err, ErrSeedCorrupt) {
		t.Fatalf("truncated file: got %v, want ErrSeedCorrupt", err)
	}
	// Nothing reached the tree.
	store.mu.Lock()
	length := store.trees[target].records.len()
	store.mu.Unlock()
	if length != 1 {
		t.Fatalf("a failed load must leave the snapshot empty, got %d records", length)
	}

	// A version bump in the file makes it unreadable, by design.
	bumped := append([]byte(nil), whole...)
	bumped[len(seedMagic)]++
	versioned := filepath.Join(dir, "versioned.seed")
	if err := os.WriteFile(versioned, bumped, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSeedHeader(versioned); !errors.Is(err, ErrSeedFormat) {
		t.Fatalf("other version: got %v, want ErrSeedFormat", err)
	}
}
