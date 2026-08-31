package probe

import (
	"os"
	"strings"
	"testing"

	"example.com/marmot/internal/platform"
)

// Does the filesystem report a private size, and does it actually separate a
// clone from a unique file? Measured before changing the scanner, because the fix
// rests entirely on this one answer.
//
//	PROBE_CLONE="/path/a:/path/b" go test ./internal/probe -run CloneSizes -v
func TestCloneSizes(t *testing.T) {
	paths := os.Getenv("PROBE_CLONE")
	if paths == "" {
		t.Skip("set PROBE_CLONE to a colon-separated list of files")
	}
	for _, path := range strings.Split(paths, ":") {
		if path == "" {
			continue
		}
		sizes, err := platform.ReadFileSizes(path)
		if err != nil {
			t.Logf("  %-64s %v", path, err)
			continue
		}
		if !sizes.HasPrivate {
			t.Logf("  %-64s alloc=%.1fMB  私有大小不可用", path, float64(sizes.Allocated)/1e6)
			continue
		}
		t.Logf("  %-64s alloc=%.1fMB  私有=%.1fMB  共享=%.1fMB",
			path, float64(sizes.Allocated)/1e6, float64(sizes.Private)/1e6,
			float64(sizes.Allocated-sizes.Private)/1e6)
	}
}
