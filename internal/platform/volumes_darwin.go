//go:build darwin

package platform

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"example.com/marmot/internal/domain/scan"
	"example.com/marmot/internal/ports"
	"golang.org/x/sys/unix"
)

type mountRecord struct {
	id   string
	path string
	stat unix.Statfs_t
}

type diskutilInfo struct {
	DeviceIdentifier   string
	VolumeName         string
	FilesystemType     string
	ContainerID        string
	VolumeGroupID      string
	TotalBytes         uint64
	UsedBytes          uint64
	FreeBytes          uint64
	ContainerTotal     uint64
	ContainerFree      uint64
	DeviceProfile      scan.DeviceProfile
	UsedKnown          bool
	FreeKnown          bool
	ContainerFreeKnown bool
	TotalKnown         bool
}

func (Adapter) ListMounts() ([]ports.Mount, error) {
	records, err := listMountRecords()
	if err != nil {
		return nil, err
	}
	mounts := make([]ports.Mount, 0, len(records))
	for _, record := range records {
		profile := scan.DeviceProfileUnknown
		if info, err := readDiskutilInfo(record.path); err == nil {
			profile = info.DeviceProfile
		}
		mounts = append(mounts, ports.Mount{ID: record.id, Path: record.path, DeviceProfile: profile})
	}
	return mounts, nil
}

func (Adapter) ListVolumes() ([]ports.Volume, error) {
	records, err := listMountRecords()
	if err != nil {
		return nil, err
	}
	kept := make([]mountRecord, 0, len(records))
	volumes := make([]ports.Volume, 0, len(records))
	for _, record := range records {
		kind, scannable, include := classifyMount(record)
		if !include {
			continue
		}
		kept = append(kept, record)
		volumes = append(volumes, ports.Volume{
			ID:            record.id,
			Name:          volumeName(record.path),
			Path:          record.path,
			Kind:          kind,
			Role:          volumeRole(kind),
			Permission:    "unknown",
			Scannable:     scannable,
			DeviceProfile: scan.DeviceProfileUnknown,
		})
	}
	// populateVolume spawns `diskutil info` per volume, which is ~95ms of process
	// startup each. Serially that was 754ms of the 761ms this call costs, and the
	// scan path waits on it before the walk can start. The calls are independent,
	// so they run together.
	//
	// Running them together was not enough: ten concurrent process spawns still
	// cost 390ms, which is the pause before the disk list appears at launch. And
	// eight of those ten are system auxiliaries -- Preboot, VM, xarts, the Update
	// mounts -- which are shown as a capacity summary and cannot be scanned. The
	// precise APFS split of volume-own versus container-shared space only matters
	// where a scan can be started, so only those pay for diskutil; the rest take
	// their numbers from the statfs record already in hand, at no cost.
	errs := make([]error, len(volumes))
	var wait sync.WaitGroup
	for index := range volumes {
		if !volumes[index].Scannable {
			errs[index] = populateFromStatfs(&volumes[index], kept[index].stat)
			continue
		}
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errs[index] = populateVolume(&volumes[index], kept[index])
		}(index)
	}
	wait.Wait()

	populated := volumes[:0]
	for index := range volumes {
		volume := volumes[index]
		kind := volume.Kind
		if err := errs[index]; err != nil {
			volume.Permission = "unavailable"
			volume.Message = "无法读取卷容量：" + err.Error()
			populated = append(populated, volume)
			continue
		}
		if kind == "system_auxiliary" {
			volume.Scannable = false
			volume.Message = "系统辅助卷，仅作为容量摘要展示"
		} else if volume.UsageBasis == "statfs_fallback_v1" {
			volume.Message = "容量来源已降级为 statfs；APFS 卷组数字可能共享"
		} else {
			volume.Message = "卷自身占用与 APFS 容器共享占用分开统计"
		}
		populated = append(populated, volume)
	}
	volumes = populated
	sort.SliceStable(volumes, func(i, j int) bool {
		leftRank, rightRank := volumeSortRank(volumes[i].Path), volumeSortRank(volumes[j].Path)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return volumes[i].Path < volumes[j].Path
	})
	return volumes, nil
}

func listMountRecords() ([]mountRecord, error) {
	count, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, fmt.Errorf("getfsstat: %w", err)
	}
	if count == 0 {
		return nil, nil
	}
	stats := make([]unix.Statfs_t, count)
	count, err = unix.Getfsstat(stats, unix.MNT_NOWAIT)
	if err != nil {
		return nil, fmt.Errorf("getfsstat entries: %w", err)
	}
	if count > len(stats) {
		stats = make([]unix.Statfs_t, count)
		count, err = unix.Getfsstat(stats, unix.MNT_NOWAIT)
		if err != nil {
			return nil, fmt.Errorf("getfsstat retry: %w", err)
		}
	}

	records := make([]mountRecord, 0, count)
	seen := make(map[string]struct{}, count)
	for _, stat := range stats[:count] {
		path := fixedCString(stat.Mntonname[:])
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		source := fixedCString(stat.Mntfromname[:])
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		records = append(records, mountRecord{
			id:   mountID(source, path),
			path: path,
			stat: stat,
		})
	}
	return records, nil
}

func mountID(source, path string) string {
	if source == "" {
		return "mount:" + filepath.Clean(path)
	}
	return "mount:" + source
}

func classifyMount(record mountRecord) (kind string, scannable, include bool) {
	switch {
	case record.path == "/":
		return "system_root", true, true
	case record.path == "/System/Volumes/Data":
		return "data", true, true
	case strings.HasPrefix(record.path, "/System/Volumes/Data/"):
		return "", false, false
	case strings.HasPrefix(record.path, "/System/Volumes/"):
		return "system_auxiliary", false, true
	case strings.HasPrefix(record.path, "/Volumes/"):
		return "external", true, true
	default:
		return "", false, false
	}
}

func volumeSortRank(path string) int {
	switch {
	case path == "/":
		return 0
	case path == "/System/Volumes/Data":
		return 1
	case strings.HasPrefix(path, "/Volumes/"):
		return 2
	default:
		return 3
	}
}

func volumeName(path string) string {
	switch path {
	case "/":
		return "Macintosh HD"
	case "/System/Volumes/Data":
		return "Macintosh HD - Data"
	default:
		name := filepath.Base(path)
		if name == "." || name == string(filepath.Separator) || name == "" {
			return path
		}
		return name
	}
}

func volumeRole(kind string) string {
	switch kind {
	case "system_root":
		return "system"
	case "data":
		return "data"
	default:
		return kind
	}
}

func populateVolume(volume *ports.Volume, record mountRecord) error {
	info, diskutilErr := readDiskutilInfo(record.path)
	if diskutilErr == nil {
		volume.ContainerID = info.ContainerID
		volume.VolumeGroupID = info.VolumeGroupID
		if info.VolumeName != "" {
			volume.Name = info.VolumeName
		}
		volume.TotalBytes = info.TotalBytes
		volume.UsedBytes = info.UsedBytes
		volume.FreeBytes = info.FreeBytes
		volume.ContainerTotalBytes = info.ContainerTotal
		volume.ContainerFreeBytes = info.ContainerFree
		volume.DeviceProfile = info.DeviceProfile
		if info.ContainerFreeKnown && info.ContainerTotal >= info.ContainerFree {
			volume.ContainerUsedBytes = info.ContainerTotal - info.ContainerFree
		}
		if info.FilesystemType == "apfs" && info.ContainerTotal > 0 {
			volume.UsageBasis = "diskutil_apfs_volume_v1"
		} else {
			volume.UsageBasis = "diskutil_volume_v1"
		}
		if info.TotalKnown && info.UsedKnown && info.FreeKnown {
			volume.Permission = "available"
			return nil
		}
	}

	if err := populateFromStatfs(volume, record.stat); err != nil {
		if diskutilErr != nil {
			return fmt.Errorf("diskutil: %v; statfs: %w", diskutilErr, err)
		}
		return err
	}
	volume.UsageBasis = "statfs_fallback_v1"
	volume.Permission = "available"
	return nil
}

func populateFromStatfs(volume *ports.Volume, stat unix.Statfs_t) error {
	if stat.Bsize == 0 {
		return errors.New("filesystem block size is zero")
	}
	blockSize := uint64(stat.Bsize)
	volume.TotalBytes = stat.Blocks * blockSize
	freeBlocks := stat.Bavail
	if freeBlocks > stat.Blocks {
		freeBlocks = stat.Blocks
	}
	volume.FreeBytes = freeBlocks * blockSize
	usedBlocks := uint64(0)
	if stat.Bfree <= stat.Blocks {
		usedBlocks = stat.Blocks - stat.Bfree
	}
	volume.UsedBytes = usedBlocks * blockSize
	volume.ContainerTotalBytes = volume.TotalBytes
	volume.ContainerUsedBytes = volume.UsedBytes
	volume.ContainerFreeBytes = volume.FreeBytes
	return nil
}

func readDiskutilInfo(path string) (diskutilInfo, error) {
	output, err := exec.Command("/usr/sbin/diskutil", "info", "-plist", path).Output()
	if err != nil {
		return diskutilInfo{}, fmt.Errorf("diskutil info %s: %w", path, err)
	}
	return parseDiskutilInfo(output)
}

func parseDiskutilInfo(data []byte) (diskutilInfo, error) {
	value, err := parsePlist(data)
	if err != nil {
		return diskutilInfo{}, err
	}
	dict, ok := value.(map[string]any)
	if !ok {
		return diskutilInfo{}, errors.New("diskutil plist root is not a dictionary")
	}
	if errorValue, ok := plistString(dict, "ErrorMessage"); ok {
		return diskutilInfo{}, errors.New(errorValue)
	}
	device, _ := plistString(dict, "DeviceIdentifier")
	if device == "" {
		return diskutilInfo{}, errors.New("diskutil plist has no device identifier")
	}
	info := diskutilInfo{
		DeviceIdentifier: device,
		VolumeName:       plistStringValue(dict, "VolumeName"),
		FilesystemType:   strings.ToLower(plistStringValue(dict, "FilesystemType")),
		ContainerID:      plistStringValue(dict, "APFSContainerReference"),
		VolumeGroupID:    plistStringValue(dict, "APFSVolumeGroupID"),
	}
	info.DeviceProfile = deviceProfileFromDiskutil(dict)
	info.TotalBytes, info.TotalKnown = plistUint(dict, "TotalSize")
	if !info.TotalKnown {
		info.TotalBytes, info.TotalKnown = plistUint(dict, "Size")
	}
	info.ContainerTotal, _ = plistUint(dict, "APFSContainerSize")
	if info.ContainerTotal > 0 && !info.TotalKnown {
		info.TotalBytes = info.ContainerTotal
		info.TotalKnown = true
	}
	info.UsedBytes, info.UsedKnown = plistUint(dict, "CapacityInUse")
	if info.FilesystemType == "apfs" {
		info.ContainerFree, info.ContainerFreeKnown = plistUint(dict, "APFSContainerFree")
	}
	if !info.FreeKnown {
		info.FreeBytes, info.FreeKnown = plistUint(dict, "FreeSpace")
	}
	if !info.FreeKnown && info.ContainerFreeKnown {
		info.FreeBytes = info.ContainerFree
		info.FreeKnown = true
	}
	return info, nil
}

func parsePlist(data []byte) (any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "plist" {
			continue
		}
		return parsePlistContainer(decoder)
	}
}

func parsePlistContainer(decoder *xml.Decoder) (any, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			return parsePlistElement(decoder, token)
		case xml.EndElement:
			return nil, errors.New("empty plist")
		}
	}
}

func parsePlistElement(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		return parsePlistDict(decoder)
	case "array":
		return parsePlistArray(decoder)
	case "string", "integer", "real", "date", "data":
		var text string
		if err := decoder.DecodeElement(&text, &start); err != nil {
			return nil, err
		}
		return strings.TrimSpace(text), nil
	case "true", "false":
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return start.Name.Local == "true", nil
	default:
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unsupported plist element: %s", start.Name.Local)
	}
}

func parsePlistDict(decoder *xml.Decoder) (map[string]any, error) {
	result := make(map[string]any)
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.EndElement:
			return result, nil
		case xml.StartElement:
			if token.Name.Local != "key" {
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			var key string
			if err := decoder.DecodeElement(&key, &token); err != nil {
				return nil, err
			}
			valueStart, err := nextPlistStart(decoder)
			if err != nil {
				return nil, err
			}
			value, err := parsePlistElement(decoder, valueStart)
			if err != nil {
				return nil, err
			}
			result[key] = value
		}
	}
}

func parsePlistArray(decoder *xml.Decoder) ([]any, error) {
	result := make([]any, 0)
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.EndElement:
			return result, nil
		case xml.StartElement:
			value, err := parsePlistElement(decoder, token)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
	}
}

func nextPlistStart(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		if start, ok := token.(xml.StartElement); ok {
			return start, nil
		}
	}
}

func plistString(dict map[string]any, key string) (string, bool) {
	value, ok := dict[key].(string)
	return value, ok && value != ""
}

func plistStringValue(dict map[string]any, key string) string {
	value, _ := plistString(dict, key)
	return value
}

func plistUint(dict map[string]any, key string) (uint64, bool) {
	value, ok := dict[key]
	if !ok {
		return 0, false
	}
	switch value := value.(type) {
	case uint64:
		return value, true
	case int64:
		if value >= 0 {
			return uint64(value), true
		}
	case string:
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func plistBool(dict map[string]any, key string) (bool, bool) {
	value, ok := dict[key]
	if !ok {
		return false, false
	}
	result, ok := value.(bool)
	return result, ok
}

func deviceProfileFromDiskutil(dict map[string]any) scan.DeviceProfile {
	virtual, _ := plistString(dict, "VirtualOrPhysical")
	protocol, _ := plistString(dict, "Protocol")
	mediaType, _ := plistString(dict, "MediaType")
	classification := strings.ToLower(strings.Join([]string{virtual, protocol, mediaType}, " "))
	if strings.Contains(classification, "virtual") || strings.Contains(classification, "network") || strings.Contains(classification, "disk image") {
		return scan.DeviceProfileNetworkOrVirtual
	}
	if solidState, known := plistBool(dict, "SolidState"); known {
		if solidState {
			return scan.DeviceProfileSSD
		}
		return scan.DeviceProfileRotational
	}
	if strings.Contains(classification, "solid state") || strings.Contains(classification, "ssd") {
		return scan.DeviceProfileSSD
	}
	if strings.Contains(classification, "rotational") || strings.Contains(classification, "hard disk") {
		return scan.DeviceProfileRotational
	}
	return scan.DeviceProfileUnknown
}

func fixedCString(value []byte) string {
	if index := bytes.IndexByte(value, 0); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(string(value))
}

var _ ports.MountCatalog = Adapter{}
