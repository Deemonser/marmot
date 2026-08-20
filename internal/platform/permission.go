package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"example.com/marmot/internal/domain/cleanup"
	"example.com/marmot/internal/ports"
)

type PermissionReport struct {
	Platform string
	State    string
	Message  string
}

type Adapter struct{}

func (Adapter) Probe() ports.PermissionReport {
	report := ProbePermissions()
	return ports.PermissionReport{Platform: report.Platform, State: report.State, Message: report.Message}
}

func (Adapter) NormalizeScanRoot(root string) (string, error) {
	if root == "" {
		root, _ = os.UserHomeDir()
	}
	if !filepath.IsAbs(root) || strings.ContainsRune(root, 0) {
		return "", errors.New("scan root must be an absolute path")
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("scan root must be a directory")
	}
	return root, nil
}

func (Adapter) CaptureCleanupItem(path string) (cleanup.Item, error) {
	path, err := cleanup.NormalizePath(path)
	if err != nil {
		return cleanup.Item{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return cleanup.Item{}, err
	}
	if info.Mode()&(os.ModeNamedPipe|os.ModeSocket|os.ModeDevice) != 0 {
		return cleanup.Item{}, errors.New("special files cannot be cleaned")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return cleanup.Item{}, errors.New("file identity is unavailable")
	}
	kind := "file"
	if info.Mode()&os.ModeSymlink != 0 {
		kind = "symlink"
	} else if info.IsDir() {
		kind = "directory"
	}
	return cleanup.Item{Path: path, Kind: kind, Device: uint64(stat.Dev), Inode: stat.Ino, Size: info.Size(), Mode: uint32(info.Mode() & os.ModeType), Modified: info.ModTime()}, nil
}

func (Adapter) Trash(path string) (string, error) {
	return Trash(path)
}

func ProbePermissions() PermissionReport {
	report := PermissionReport{Platform: runtime.GOOS, State: "partial", Message: "权限状态需要在签名应用身份下确认"}
	if runtime.GOOS != "darwin" {
		report.State = "unsupported"
		report.Message = "第一阶段只支持 macOS"
		return report
	}
	for _, path := range []string{"/", "/System/Library", "/Users"} {
		if _, err := os.ReadDir(path); err != nil {
			report.Message = "部分目录不可访问：" + path
			return report
		}
	}
	report.State = "available"
	report.Message = "基础目录可访问；Full Disk Access 仍需签名应用验证"
	return report
}
