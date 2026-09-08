package scanner

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

// firmlinkTable is where macOS lists every firmlink on the system volume: one
// line per link, "<system path>\t<data-volume relative path>". Firmlinks are
// created only by the OS, so this file is the complete set.
const firmlinkTable = "/usr/share/firmlinks"

// loadFirmlinkSources returns the system-side paths of every firmlink, or an
// empty set where the table does not exist (any non-macOS root, or a test tree).
func loadFirmlinkSources() map[string]struct{} {
	data, err := os.ReadFile(firmlinkTable)
	if err != nil {
		return map[string]struct{}{}
	}
	return parseFirmlinkSources(string(data))
}

func parseFirmlinkSources(text string) map[string]struct{} {
	sources := map[string]struct{}{}
	lines := bufio.NewScanner(strings.NewReader(text))
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || !strings.HasPrefix(line, "/") {
			continue
		}
		source, _, _ := strings.Cut(line, "\t")
		source = strings.TrimSpace(source)
		if source != "" && source != "/" {
			sources[source] = struct{}{}
		}
	}
	return sources
}

// lookupIdentity is the device and inode a path lookup arrives at -- the same
// pair CaptureCleanupItem will compare against later. getattrlistbulk on "/"
// reports a firmlink as the catalog record on the sealed system volume (an inode
// in the 0x0FFFFFFF... range), but every path-based call resolves through the
// firmlink to the data-volume directory, so a node stored with the bulk identity
// can never be re-found by path. This is what the scanner records instead.
func lookupIdentity(path string) (device, inode uint64, ok bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, false
	}
	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat || stat == nil {
		return 0, 0, false
	}
	return uint64(stat.Dev), stat.Ino, true
}
