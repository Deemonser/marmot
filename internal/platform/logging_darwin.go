package platform

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Diagnostics on disk, because there were none.
//
// The app wrote one log line in its entire lifetime -- a scan summary -- to
// stderr, and a bundle launched from Finder has no stderr. Twice in one session a
// question about what the app had just done ("why is the analysis slow", "the
// delete failed") could only be answered by re-running things under a test
// harness and reasoning backwards. A tool that moves gigabytes to the trash and
// keeps no record of having done so cannot be debugged by the person it happened
// to.
//
// Deliberately plain: one file, one rotation, no levels, no dependency. What was
// missing was the record, not a logging framework.
const (
	logMaxBytes = 4 << 20
	logDirName  = "marmot"
	logFileName = "marmot.log"
)

// OpenLog points the standard logger at ~/Library/Logs/marmot/marmot.log as well
// as stderr, and returns the file so the caller can close it.
//
// Every failure here is returned rather than fatal: not being able to write a log
// is not a reason to refuse to run.
func OpenLog() (io.Closer, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	dir := filepath.Join(home, "Library", "Logs", logDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, logFileName)
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > logMaxBytes {
		// One generation back. A disk-space tool that grows an unbounded log file
		// would be a poor joke.
		_ = os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, "", err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, file))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("--- marmot 启动，pid %d ---", os.Getpid())
	return file, path, nil
}

// FreeSpaceNote reports free space on the volume holding a path, for logging
// beside an operation that may have failed because of it. Empty when
// unavailable. Moving to the trash is a rename within a volume and needs no
// space, but a full disk breaks enough other things that the number belongs in
// the record.
func FreeSpaceNote(path string) string {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return ""
	}
	return fmt.Sprintf("该卷可用 %.1f GB", float64(stat.Bavail)*float64(stat.Bsize)/1e9)
}
