package platform

import (
	"errors"
	"os"
	"path/filepath"
)

// legacySnapshotCacheFiles is the complete, hard-coded list of files the
// superseded SQLite snapshot store left behind (ADR-0028 replaced it with the
// append-only binary format; ADR-0054 authorises removing these).
//
// It is a fixed list on purpose. A cleaner that matched a pattern would turn one
// bad path join into an arbitrary delete, and this code runs unattended at
// startup. Adding an entry here requires a new ADR (ADR-0054 §3).
var legacySnapshotCacheFiles = []string{
	"snapshots.db",
	"snapshots.db-wal",
	"snapshots.db-shm",
}

// RemoveLegacySnapshotCache deletes the superseded SQLite cache inside the app's
// own cache directory and reports how many bytes that freed. A missing file is
// the normal case — a fresh install, or a second run — and is not an error
// (ADR-0054 §5).
//
// It removes cache the application itself produced and no longer reads, which is
// why it deletes outright instead of moving to the Trash: DDD invariant 9 governs
// the user's data, and putting gigabytes of dead cache in someone's Trash just
// moves the problem (ADR-0054 §4).
func (Adapter) RemoveLegacySnapshotCache(directory string) (int64, []string, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return 0, nil, errors.New("legacy cache directory must be an absolute path")
	}
	var freed int64
	var removed []string
	var firstErr error
	for _, name := range legacySnapshotCacheFiles {
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		// Only ever a regular file: a directory or a symlink under one of these
		// names is not ours, and following it would delete something else.
		if !info.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(path); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		freed += info.Size()
		removed = append(removed, name)
	}
	return freed, removed, firstErr
}
