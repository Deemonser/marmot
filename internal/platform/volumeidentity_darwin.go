package platform

/*
#include <stdlib.h>
#include <sys/attr.h>
#include <unistd.h>
#include <string.h>
#include <stdint.h>

struct marmot_volinfo {
	unsigned char uuid[16];
	long long space_used;
	int have_uuid;
	int have_space_used;
};

static int marmot_read_volinfo(const char *path, struct marmot_volinfo *out) {
	struct attrlist request;
	memset(&request, 0, sizeof(request));
	request.bitmapcount = ATTR_BIT_MAP_COUNT;
	request.commonattr = ATTR_CMN_RETURNED_ATTRS;
	request.volattr = ATTR_VOL_INFO | ATTR_VOL_UUID | ATTR_VOL_SPACEUSED;

	char buffer[256];
	if (getattrlist(path, &request, buffer, sizeof(buffer), 0) != 0) return -1;

	char *cursor = buffer + sizeof(uint32_t);
	attribute_set_t returned;
	memcpy(&returned, cursor, sizeof(returned));
	cursor += sizeof(returned);

	// NOT ascending bit order: the reply packs ATTR_VOL_SPACEUSED (8 bytes)
	// BEFORE ATTR_VOL_UUID (16 bytes), verified byte-by-byte against known
	// volumes (R-068) — later-added attributes keep the man page's group
	// order, not their bitmask positions.
	if (returned.volattr & ATTR_VOL_SPACEUSED) {
		memcpy(&out->space_used, cursor, 8);
		cursor += 8;
		out->have_space_used = 1;
	}
	if (returned.volattr & ATTR_VOL_UUID) {
		memcpy(out->uuid, cursor, 16);
		out->have_uuid = 1;
	}
	return 0;
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"example.com/marmot/internal/domain/scan"
)

// nativeVolumeInfo is what one getattrlist call answers in ~10µs: which volume
// this is (its UUID, the identity cache's key) and how many bytes belong to it
// alone — ATTR_VOL_SPACEUSED is volume-own usage, the same quantity diskutil's
// APFSVolumeBytesUsed reports, verified equal on every volume of this machine
// (R-068). statfs cannot answer the second question on APFS: it reports the
// container's blocks, which is exactly why skipping diskutil for statfs was
// reverted once already.
type nativeVolumeInfo struct {
	uuid          string
	spaceUsed     uint64
	haveUUID      bool
	haveSpaceUsed bool
}

func readNativeVolumeInfo(path string) (nativeVolumeInfo, bool) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var out C.struct_marmot_volinfo
	if C.marmot_read_volinfo(cPath, &out) != 0 {
		return nativeVolumeInfo{}, false
	}
	info := nativeVolumeInfo{
		spaceUsed:     uint64(out.space_used),
		haveUUID:      out.have_uuid != 0,
		haveSpaceUsed: out.have_space_used != 0,
	}
	if info.haveUUID {
		raw := C.GoBytes(unsafe.Pointer(&out.uuid[0]), 16)
		info.uuid = fmt.Sprintf("%08X-%04X-%04X-%04X-%012X", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	}
	return info, true
}

// volumeIdentity is the part of a volume's description that does not change
// for the life of the volume: what it is called, which container and volume
// group it belongs to, whether the medium is solid-state. diskutil is the only
// source for these, at ~95ms of process spawn per call — so they are learned
// once per volume UUID and remembered across launches. A renamed volume keeps
// its stale name until the cache file is deleted; nothing else can go stale.
type volumeIdentity struct {
	Name           string `json:"name,omitempty"`
	ContainerID    string `json:"containerId,omitempty"`
	VolumeGroupID  string `json:"volumeGroupId,omitempty"`
	DeviceProfile  string `json:"deviceProfile,omitempty"`
	FilesystemType string `json:"filesystemType,omitempty"`
}

var identityCache struct {
	once sync.Once
	mu   sync.Mutex
	m    map[string]volumeIdentity
}

func volumeIdentityPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "marmot", "volume-identity.json"), nil
}

func loadIdentityCacheLocked() {
	identityCache.m = map[string]volumeIdentity{}
	path, err := volumeIdentityPath()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// A corrupt cache means diskutil runs once more, never a failure.
	_ = json.Unmarshal(raw, &identityCache.m)
}

func lookupVolumeIdentity(uuid string) (volumeIdentity, bool) {
	identityCache.mu.Lock()
	defer identityCache.mu.Unlock()
	identityCache.once.Do(loadIdentityCacheLocked)
	identity, ok := identityCache.m[uuid]
	return identity, ok
}

func storeVolumeIdentity(uuid string, identity volumeIdentity) {
	if uuid == "" {
		return
	}
	identityCache.mu.Lock()
	defer identityCache.mu.Unlock()
	identityCache.once.Do(loadIdentityCacheLocked)
	if existing, ok := identityCache.m[uuid]; ok && existing == identity {
		return
	}
	identityCache.m[uuid] = identity
	path, err := volumeIdentityPath()
	if err != nil {
		return
	}
	encoded, err := json.Marshal(identityCache.m)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".volume-identity-*")
	if err != nil {
		return
	}
	defer os.Remove(temp.Name())
	if temp.Chmod(0o600) != nil {
		temp.Close()
		return
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return
	}
	if temp.Close() != nil {
		return
	}
	_ = os.Rename(temp.Name(), path)
}

func identityFromDiskutil(info diskutilInfo) volumeIdentity {
	return volumeIdentity{
		Name:           info.VolumeName,
		ContainerID:    info.ContainerID,
		VolumeGroupID:  info.VolumeGroupID,
		DeviceProfile:  string(info.DeviceProfile),
		FilesystemType: info.FilesystemType,
	}
}

func deviceProfileFromIdentity(identity volumeIdentity) scan.DeviceProfile {
	switch scan.DeviceProfile(identity.DeviceProfile) {
	case scan.DeviceProfileSSD, scan.DeviceProfileRotational, scan.DeviceProfileNetworkOrVirtual:
		return scan.DeviceProfile(identity.DeviceProfile)
	default:
		return scan.DeviceProfileUnknown
	}
}
