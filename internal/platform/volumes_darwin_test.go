//go:build darwin

package platform

import (
	"os"
	"strings"
	"testing"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
	"golang.org/x/sys/unix"
)

func TestParseDiskutilInfoSeparatesVolumeAndContainerCapacity(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>DeviceIdentifier</key><string>disk3s5</string>
<key>VolumeName</key><string>Macintosh HD - Data</string>
<key>FilesystemType</key><string>apfs</string>
<key>APFSContainerReference</key><string>disk3</string>
<key>APFSVolumeGroupID</key><string>3FFF83A2-AA8A-4C34-B2B2-910B9E300E59</string>
<key>TotalSize</key><integer>245107195904</integer>
<key>CapacityInUse</key><integer>206645858304</integer>
<key>FreeSpace</key><integer>123456789</integer>
<key>APFSContainerSize</key><integer>245107195904</integer>
<key>APFSContainerFree</key><integer>7973371904</integer>
<key>SolidState</key><true/>
<key>Internal</key><true/>
<key>APFSSnapshot</key><false/>
</dict></plist>`)

	info, err := parseDiskutilInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.DeviceIdentifier != "disk3s5" || info.VolumeName != "Macintosh HD - Data" || info.ContainerID != "disk3" || info.VolumeGroupID == "" {
		t.Fatalf("unexpected identity: %#v", info)
	}
	if !info.TotalKnown || !info.UsedKnown || !info.FreeKnown {
		t.Fatalf("capacity presence was lost: %#v", info)
	}
	if info.UsedBytes != 206645858304 || info.FreeBytes != 123456789 || info.ContainerFree != 7973371904 || !info.ContainerFreeKnown || info.ContainerTotal != 245107195904 {
		t.Fatalf("capacity fields were mixed: %#v", info)
	}
	if info.DeviceProfile != scan.DeviceProfileSSD {
		t.Fatalf("device profile was not parsed: %#v", info)
	}
}

func TestParseDiskutilErrorPlist(t *testing.T) {
	_, err := parseDiskutilInfo([]byte(`<plist version="1.0"><dict><key>ErrorMessage</key><string>Could not find disk</string></dict></plist>`))
	if err == nil || !strings.Contains(err.Error(), "Could not find disk") {
		t.Fatalf("expected diskutil error, got %v", err)
	}
}

func TestClassifyMountHidesFirmlinkSupportMounts(t *testing.T) {
	tests := []struct {
		path      string
		kind      string
		scannable bool
		include   bool
	}{
		{path: "/", kind: "system_root", scannable: true, include: true},
		{path: "/System/Volumes/Data", kind: "data", scannable: true, include: true},
		{path: "/System/Volumes/Data/home", include: false},
		{path: "/System/Volumes/Preboot", kind: "system_auxiliary", include: true},
		{path: "/Volumes/Backup", kind: "external", scannable: true, include: true},
	}
	for _, test := range tests {
		kind, scannable, include := classifyMount(mountRecord{path: test.path})
		if kind != test.kind || scannable != test.scannable || include != test.include {
			t.Fatalf("classifyMount(%q) = (%q, %t, %t), want (%q, %t, %t)", test.path, kind, scannable, include, test.kind, test.scannable, test.include)
		}
	}
}

func TestPopulateFromStatfsUsesBfreeForUsedBytes(t *testing.T) {
	var volume ports.Volume
	err := populateFromStatfs(&volume, unix.Statfs_t{Bsize: 4096, Blocks: 100, Bfree: 40, Bavail: 30})
	if err != nil {
		t.Fatal(err)
	}
	if volume.TotalBytes != 409600 || volume.UsedBytes != 245760 || volume.FreeBytes != 122880 {
		t.Fatalf("statfs capacity semantics are wrong: %#v", volume)
	}
}

func TestListVolumesReportsCurrentMacCapacitySources(t *testing.T) {
	volumes, err := (Adapter{}).ListVolumes()
	if err != nil {
		t.Fatal(err)
	}
	var root, data *ports.Volume
	for index := range volumes {
		volume := &volumes[index]
		t.Logf("volume path=%s kind=%s used=%d containerUsed=%d basis=%s scannable=%t", volume.Path, volume.Kind, volume.UsedBytes, volume.ContainerUsedBytes, volume.UsageBasis, volume.Scannable)
		switch volume.Path {
		case "/":
			root = volume
		case "/System/Volumes/Data":
			data = volume
		}
		if strings.HasPrefix(volume.Path, "/System/Volumes/Data/") {
			t.Fatalf("firmlink support mount leaked into volume catalog: %s", volume.Path)
		}
	}
	if root == nil || root.UsedBytes == 0 || root.UsageBasis == "" {
		t.Fatalf("root volume capacity is missing: %#v", volumes)
	}
	if _, err := os.Stat("/System/Volumes/Data"); err == nil && data == nil {
		t.Fatal("Data mount exists but was not discovered")
	}
	if root.VolumeGroupID == "" || data == nil || data.VolumeGroupID != root.VolumeGroupID {
		t.Fatalf("System/Data volume group identity is missing: root=%#v data=%#v", root, data)
	}
}

// Every volume's usage must come from the source that can tell volume-own space
// from container-shared space.
//
// Skipping diskutil for the eight non-scannable auxiliaries was tried as a
// startup optimisation and reverted. statfs is not a slower diskutil: on APFS it
// reports the CONTAINER's blocks and free space, so every volume in a shared
// container computes its used bytes as the container's. Measured after that
// change, Preboot, Update and VM each claimed 191.8 GB used and the volumes summed
// to 931.3 GB on a 245 GB disk -- which made the source row's scan denominator
// astronomical and pinned the progress bar at zero for an entire walk.
//
// The invariant is the basis, not the number: a number can look plausible on one
// machine while coming from a source that cannot answer the question.
func TestEveryVolumeUsageComesFromASourceThatSeparatesSharedSpace(t *testing.T) {
	volumes, err := (Adapter{}).ListVolumes()
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) == 0 {
		t.Skip("no volumes reported")
	}
	container := uint64(0)
	for _, volume := range volumes {
		if volume.TotalBytes > container {
			container = volume.TotalBytes
		}
	}
	for _, volume := range volumes {
		if volume.Permission == "unavailable" {
			continue
		}
		if volume.UsageBasis == "" {
			t.Errorf("%s reports no usage basis at all", volume.Path)
			continue
		}
		if volume.UsageBasis == "statfs_fallback_v1" {
			// Allowed only as a genuine fallback, never as a shortcut: on a shared
			// container it cannot separate own from shared usage.
			t.Logf("%s fell back to statfs; its usage may be container-wide", volume.Path)
			continue
		}
		// The specific symptom: an auxiliary volume claiming the container's usage.
		if !volume.Scannable && container > 0 && volume.UsedBytes > container/2 {
			t.Errorf("%s claims %d bytes used of a %d-byte container, which is the container's own usage",
				volume.Path, volume.UsedBytes, container)
		}
	}
}
