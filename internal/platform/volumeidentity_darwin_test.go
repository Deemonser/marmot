package platform

import (
	"os"
	"sync"
	"testing"
	"time"
)

// The native numbers replace diskutil's only because they are the same
// quantity. This checks that on the machine it runs on, volume by volume: a
// mismatch beyond drift means ATTR_VOL_SPACEUSED does not mean what R-068
// measured, and the fast path must not ship.
func TestNativeSpaceUsedMatchesDiskutil(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns diskutil per volume")
	}
	records, err := listMountRecords()
	if err != nil {
		t.Fatal(err)
	}
	compared := 0
	for _, record := range records {
		native, ok := readNativeVolumeInfo(record.path)
		if !ok || !native.haveSpaceUsed {
			continue
		}
		info, err := readDiskutilInfo(record.path)
		if err != nil || !info.UsedKnown {
			continue
		}
		compared++
		diff := int64(native.spaceUsed) - int64(info.UsedBytes)
		if diff < 0 {
			diff = -diff
		}
		// The volume keeps living between the two reads; 2% or 64MB of drift
		// is measurement noise, not a different quantity.
		tolerance := int64(info.UsedBytes / 50)
		if tolerance < 64_000_000 {
			tolerance = 64_000_000
		}
		if diff > tolerance {
			t.Errorf("%s: native %d vs diskutil %d differ by %d", record.path, native.spaceUsed, info.UsedBytes, diff)
		}
	}
	if compared == 0 {
		t.Skip("no volume answered both queries")
	}
	t.Logf("compared %d volumes", compared)
}

func resetIdentityCache() {
	identityCache.mu.Lock()
	defer identityCache.mu.Unlock()
	identityCache.once = sync.Once{}
	identityCache.m = nil
}

func TestVolumeIdentityCacheRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	resetIdentityCache()
	t.Cleanup(resetIdentityCache)

	uuid := "11111111-2222-3333-4444-555555555555"
	if _, hit := lookupVolumeIdentity(uuid); hit {
		t.Fatal("empty cache reported a hit")
	}
	identity := volumeIdentity{Name: "Macintosh HD", ContainerID: "disk3", VolumeGroupID: "group-a", DeviceProfile: "ssd", FilesystemType: "apfs"}
	storeVolumeIdentity(uuid, identity)

	// A fresh process must see it: drop the in-memory map, keep the file.
	resetIdentityCache()
	got, hit := lookupVolumeIdentity(uuid)
	if !hit || got != identity {
		t.Fatalf("identity did not survive the file round trip: %+v hit=%v", got, hit)
	}

	// Corruption downgrades to a miss, never an error.
	path, err := volumeIdentityPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	resetIdentityCache()
	if _, hit := lookupVolumeIdentity(uuid); hit {
		t.Fatal("corrupt cache reported a hit")
	}
}

// The reason this module exists: a warm ListVolumes must not be a visible
// pause. 50ms is an order of magnitude above what R-068 measured (~1ms) and
// two below the 390ms it replaces, so a regression to subprocess-per-volume
// fails loudly without the test being flaky.
func TestWarmListVolumesIsNotAVisiblePause(t *testing.T) {
	if testing.Short() {
		t.Skip("touches every mounted volume")
	}
	adapter := Adapter{}
	if _, err := adapter.ListVolumes(); err != nil { // warm the identity cache
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := adapter.ListVolumes(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("warm ListVolumes: %s", elapsed)
	if elapsed > 50*time.Millisecond {
		t.Errorf("warm ListVolumes took %s; the identity cache is not being hit", elapsed)
	}
}
