package probe

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	marmotapp "example.com/marmot/internal/application"
	"example.com/marmot/internal/platform"
)

// How quickly does the source page's capacity meter follow the disk?
//
// The meter is a second reading of the same volume, taken live from the
// filesystem rather than from the snapshot the result page is drawn from, and
// the two disagreeing after a cleanup is what this measures. It decides whether
// re-reading the volume list when a deletion finishes can work at all, and how
// soon it has to be read.
//
// Reading "/" alone would prove nothing: on modern macOS that is the read-only
// system volume, fixed at about 12 GB here, and nothing a user deletes touches
// it. The row is the whole APFS volume group, which is what this reads.
//
// Measured 2026-09-11, APFS on an internal SSD, 600 MB in 1 MiB files:
//
//	wrote:   reading moved +597.7 MB after 0s / +576.8 after 6.9s / +600.3 after 5.1s
//	deleted: reading moved -584.2 MB after 100ms / -611.7 after 600ms / -600.2 after 800ms
//
// So a deletion lands within about a second and a write can take several -- the
// opposite of the usual worry, and the reason the frontend re-reads once when
// Execute returns and once again a moment later.
func TestStorageSourceUsageFollowsTheDisk(t *testing.T) {
	if os.Getenv("PROBE_SPACEUSED") == "" {
		t.Skip("set PROBE_SPACEUSED to run the real-volume probe (writes 600 MB to $HOME)")
	}
	// Through the same call the frontend makes, so this measures the number the
	// row is actually drawn from rather than a lookalike.
	adapter := platform.Adapter{}
	service := marmotapp.NewService(marmotapp.Dependencies{Volumes: adapter, Emit: func(string, any) {}})
	read := func() uint64 {
		sources, err := service.GetStorageSources()
		if err != nil {
			t.Fatal(err)
		}
		if len(sources) == 0 {
			t.Fatal("no storage sources")
		}
		return sources[0].UsedBytes
	}
	mb := func(a, b uint64) float64 { return float64(int64(a)-int64(b)) / (1 << 20) }
	// Wait for the reading to move by more than the noise between samples.
	settle := func(from uint64, up bool, limit time.Duration) (time.Duration, uint64) {
		start := time.Now()
		for time.Since(start) < limit {
			now := read()
			if (up && now > from+(64<<20)) || (!up && now+(64<<20) < from) {
				return time.Since(start), now
			}
			time.Sleep(100 * time.Millisecond)
		}
		return limit, read()
	}

	base := read()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(home, "marmot-spaceused-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	payload := make([]byte, 1<<20)
	for i := 0; i < 600; i++ {
		name := filepath.Join(dir, "blob-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		if err := os.WriteFile(name, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tookUp, grown := settle(base, true, 60*time.Second)
	t.Logf("wrote 600 MB: reading moved %+.1f MB after %s", mb(grown, base), tookUp.Round(100*time.Millisecond))

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	tookDown, shrunk := settle(grown, false, 60*time.Second)
	t.Logf("deleted it:   reading moved %+.1f MB after %s", mb(shrunk, grown), tookDown.Round(100*time.Millisecond))
	if tookDown >= 60*time.Second {
		t.Error("the deletion never showed up; re-reading the volume list after a cleanup cannot help")
	}
}
