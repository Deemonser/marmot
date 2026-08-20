//go:build darwin

package platform

import (
	"os"
	"path/filepath"

	"example.com/marmot/internal/ports"
	"golang.org/x/sys/unix"
)

func (Adapter) ListVolumes() ([]ports.Volume, error) {
	paths := []string{"/"}
	entries, err := os.ReadDir("/Volumes")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				paths = append(paths, filepath.Join("/Volumes", entry.Name()))
			}
		}
	}
	volumes := make([]ports.Volume, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		volume := ports.Volume{ID: path, Name: filepath.Base(path), Path: path, Kind: "local", Permission: "unknown", Message: "容量信息不可用"}
		if path == "/" {
			volume.Name = "Macintosh HD"
		}
		var stat unix.Statfs_t
		if err := unix.Statfs(path, &stat); err != nil {
			volume.Permission = "unavailable"
			volume.Message = err.Error()
			volumes = append(volumes, volume)
			continue
		}
		blockSize := uint64(stat.Bsize)
		volume.TotalBytes = uint64(stat.Blocks) * blockSize
		volume.FreeBytes = uint64(stat.Bavail) * blockSize
		if volume.FreeBytes > volume.TotalBytes {
			volume.FreeBytes = volume.TotalBytes
		}
		volume.UsedBytes = volume.TotalBytes - volume.FreeBytes
		volume.Permission = "available"
		volume.Message = "可扫描；Full Disk Access 可能影响受限目录"
		volume.Scannable = true
		volumes = append(volumes, volume)
	}
	return volumes, nil
}
