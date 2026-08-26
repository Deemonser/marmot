package platform

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// The legacy cleanup is authorised for exactly three file names (ADR-0054 §1).
// It must not touch anything else in the directory, and a pattern-based
// implementation would.
func TestRemoveLegacySnapshotCacheDeletesOnlyTheNamedFiles(t *testing.T) {
	directory := t.TempDir()
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(directory, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("snapshots.db", 4096)
	write("snapshots.db-wal", 512)
	write("snapshots.db-shm", 128)
	// Everything below must survive: neighbours, look-alikes, and the live store.
	write("snapshots.db.backup", 10)
	write("snapshot-20.data", 10)
	write("snapshots.sqlite", 10)
	if err := os.Mkdir(filepath.Join(directory, "snapshots"), 0o700); err != nil {
		t.Fatal(err)
	}

	freed, removed, err := Adapter{}.RemoveLegacySnapshotCache(directory)
	if err != nil {
		t.Fatal(err)
	}
	if freed != 4096+512+128 {
		t.Fatalf("freed %d bytes, want %d", freed, 4096+512+128)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %v, want three files", removed)
	}
	survivors, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(survivors))
	for _, entry := range survivors {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	want := []string{"snapshot-20.data", "snapshots", "snapshots.db.backup", "snapshots.sqlite"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("cleanup touched the wrong files: left %v, want %v", names, want)
	}

	// Idempotent: the second run is the normal case on every later launch.
	freed, removed, err = Adapter{}.RemoveLegacySnapshotCache(directory)
	if err != nil || freed != 0 || len(removed) != 0 {
		t.Fatalf("second run was not a no-op: freed=%d removed=%v err=%v", freed, removed, err)
	}

	// A relative path is refused rather than resolved against the working
	// directory.
	if _, _, err := (Adapter{}).RemoveLegacySnapshotCache("marmot"); err == nil {
		t.Fatal("a relative directory must be refused")
	}
}

// A directory or symlink wearing one of those names is not ours to delete.
func TestRemoveLegacySnapshotCacheSkipsNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "snapshots.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "precious")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "snapshots.db-wal")); err != nil {
		t.Fatal(err)
	}
	freed, removed, err := Adapter{}.RemoveLegacySnapshotCache(directory)
	if err != nil || freed != 0 || len(removed) != 0 {
		t.Fatalf("non-regular entries must be skipped: freed=%d removed=%v err=%v", freed, removed, err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the symlink target was followed and deleted: %v", err)
	}
}
