package cleanup

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type Item struct {
	Path     string
	Kind     string
	Device   uint64
	Inode    uint64
	Size     int64
	Mode     uint32
	Modified time.Time
}

const (
	PlanDraft     = "draft"
	PlanValidated = "validated"
	PlanConfirmed = "confirmed"
	PlanApplying  = "applying"
	PlanApplied   = "applied"
	PlanFailed    = "failed"
	PlanExpired   = "expired"
	PlanInvalid   = "invalid"
	PlanDiscarded = "discarded"
)

func NormalizePath(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || filepath.Clean(path) == string(filepath.Separator) {
		return "", errors.New("cleanup path must be an absolute non-root path")
	}
	return filepath.Clean(path), nil
}

func HasOverlappingPaths(paths []string) bool {
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			if IsPathWithin(paths[i], paths[j]) || IsPathWithin(paths[j], paths[i]) {
				return true
			}
		}
	}
	return false
}

func IsPathWithin(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
