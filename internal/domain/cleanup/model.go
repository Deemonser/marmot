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

// Chunk is one unit of deletion work: a group of paths a single worker can hand
// to a recursive remove on its own. Grouping rather than one path per unit is
// what makes a wide, shallow tree chunkable at all -- a thousand package
// directories of two hundred nodes each never produce a single subtree big
// enough to be worth its own unit, but three of them together do.
type Chunk struct {
	Paths []string
	Nodes int64
}

// Subtree is a plan for deleting one staged item: how many nodes the snapshot
// says are under it, and the chunks that cover most of them.
//
// The chunks never cover all of it. What is left -- the item's own directories,
// branches too small to be worth a unit, and anything created since the scan --
// is deleted by a final sweep of the item path itself, which is also what makes
// the item's disappearance the real completion test rather than the arithmetic.
type Subtree struct {
	TotalNodes int64
	Chunks     []Chunk
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
