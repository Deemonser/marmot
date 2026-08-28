package memtree

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"example.com/marmot/internal/domain/recommendation"
)

// Evidence assembly: one post-order walk of the whole tree that emits the
// skeleton the advisor is asked about.
//
// The walk visits every node even though only nodes at or above the floor are
// emitted, and that is deliberate. Children are stored biggest-first, so the
// walk COULD stop at the first small sibling and touch only the ~1k kept nodes.
// It does not, because the fields that make a suggestion judgeable -- subtree
// file count, biggest single file, extension profile -- are sums over the part
// that gets pruned away. R-062 §3.3 is the case in point: `.gradle/caches/
// 8.13/transforms` and a single 3.28 GB git pack are both "one opaque lump of
// N GB" by size alone, and completely different recommendations once you know
// one is 139,969 files and the other is 1.
//
// The cost is one pass over the record table on an explicit user action, with
// memory proportional to depth rather than to the tree.
//
// The kept set is closed under ancestors -- a node at or above the floor forces
// its parent above it too -- so what comes back is always a connected subtree
// rooted at the scan root, never a scattering of nodes.

const (
	// A cycle in the parent links would otherwise walk forever. Real trees are
	// nowhere near this deep; hitting it means the records are corrupt, and
	// failing loudly beats hanging.
	maxEvidenceDepth = 512
	// Distinct extensions tracked per frame. Beyond this the tail is folded into
	// one bucket rather than growing without bound.
	maxTrackedExtensions = 96
)

type evidenceFrame struct {
	id         int64
	childIndex int
	// kept and own are fixed when the frame is pushed: the roll-up has already
	// been applied by the time a query runs, so a node's total is known before
	// its children are walked.
	kept bool
	own  int64

	files       int64
	dirs        int64
	biggestFile int64
	newestUnix  int64
	oldestUnix  int64
	// residue extension profile: bytes and counts of files NOT already
	// attributed to a kept descendant.
	extensions map[string]*recommendation.ExtensionShare
	// residue bytes, decremented as kept children claim their share.
	residue int64
}

func (q *treeQuery) evidenceNodes(query recommendation.EvidenceQuery) (recommendation.EvidenceResult, error) {
	tree := q.tree
	if query.MinBytes <= 0 {
		return recommendation.EvidenceResult{}, fmt.Errorf("%w: evidence floor must be positive", ErrInvalidRequest)
	}
	total, used, free := q.mapVolume()
	floor := query.MinBytes
	if query.MinShare > 0 {
		if scaled := int64(float64(used) * query.MinShare); scaled > floor {
			floor = scaled
		}
	}
	result := recommendation.EvidenceResult{
		Root: tree.root, VolumeTotalBytes: total, VolumeUsedBytes: used, VolumeFreeBytes: free,
		FloorBytes: floor,
	}
	maxNodes := query.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 4096
	}
	perNode := query.ExtensionsPerNode
	if perNode <= 0 {
		perNode = 3
	}
	tree.ensureGrouped()

	rootID := tree.rootNodeID
	if rootID <= 0 || !tree.valid(rootID) {
		return recommendation.EvidenceResult{}, ErrResultUnavailable
	}

	nowUnix := time.Now().UnixNano()
	collected := make([]recommendation.EvidenceNode, 0, 256)
	stack := make([]evidenceFrame, 0, 64)
	// The scan root is always kept: it is the frame everything else hangs off,
	// and its residue is what nothing below the floor could explain.
	stack = append(stack, newEvidenceFrame(tree, rootID, floor))

	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		children := tree.children(top.id)
		if top.childIndex < len(children) {
			childID := int64(children[top.childIndex])
			top.childIndex++
			if len(stack) >= maxEvidenceDepth {
				return recommendation.EvidenceResult{}, fmt.Errorf("%w: node %d is deeper than %d, records are inconsistent", ErrInvalidRequest, childID, maxEvidenceDepth)
			}
			stack = append(stack, newEvidenceFrame(tree, childID, floor))
			continue
		}

		// Post-order: every descendant of top has been folded in already.
		finished := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if finished.kept {
			collected = append(collected, evidenceNodeFrom(tree, finished, nowUnix, perNode))
			if len(collected) > maxNodes {
				return recommendation.EvidenceResult{}, fmt.Errorf("%w: floor %d bytes keeps more than %d nodes; raise the floor", ErrInvalidRequest, floor, maxNodes)
			}
		}
		if len(stack) == 0 {
			break
		}
		mergeIntoParent(&stack[len(stack)-1], finished)
	}

	// Emitted in post-order; the caller wants the skeleton biggest-first.
	sort.Slice(collected, func(left, right int) bool {
		if collected[left].OwnedAllocated != collected[right].OwnedAllocated {
			return collected[left].OwnedAllocated > collected[right].OwnedAllocated
		}
		return collected[left].ID < collected[right].ID
	})
	result.Nodes = collected
	return result, nil
}

func newEvidenceFrame(tree *tree, id int64, floor int64) evidenceFrame {
	entry := *tree.records.at(id)
	frame := evidenceFrame{
		id:         id,
		kept:       id == tree.rootNodeID || entry.ownedAllocated >= floor,
		own:        entry.ownedAllocated,
		newestUnix: entry.modifiedUnix,
		oldestUnix: entry.modifiedUnix,
		residue:    entry.ownedAllocated,
	}
	if tree.kinds.value(entry.kind) == "directory" {
		frame.dirs = 1
	} else {
		frame.files = 1
		frame.biggestFile = entry.ownedAllocated
		frame.extensions = map[string]*recommendation.ExtensionShare{
			extensionOf(tree.names.get(entry.nameOffset, entry.nameLength)): {
				Extension: extensionOf(tree.names.get(entry.nameOffset, entry.nameLength)),
				Bytes:     entry.ownedAllocated,
				Files:     1,
			},
		}
	}
	return frame
}

func mergeIntoParent(parent *evidenceFrame, child evidenceFrame) {
	parent.files += child.files
	parent.dirs += child.dirs
	if child.biggestFile > parent.biggestFile {
		parent.biggestFile = child.biggestFile
	}
	if child.newestUnix > parent.newestUnix {
		parent.newestUnix = child.newestUnix
	}
	if child.oldestUnix != 0 && (parent.oldestUnix == 0 || child.oldestUnix < parent.oldestUnix) {
		parent.oldestUnix = child.oldestUnix
	}
	if child.kept {
		// A kept child accounts for itself. Its bytes leave the parent's residue
		// and its extension profile stays with it -- this is what makes the
		// residues a partition rather than a set of overlapping totals.
		parent.residue -= child.own
		return
	}
	for extension, share := range child.extensions {
		addExtension(parent, extension, share.Bytes, share.Files)
	}
}

func addExtension(frame *evidenceFrame, extension string, bytes, files int64) {
	if frame.extensions == nil {
		frame.extensions = make(map[string]*recommendation.ExtensionShare, 8)
	}
	if existing, ok := frame.extensions[extension]; ok {
		existing.Bytes += bytes
		existing.Files += files
		return
	}
	if len(frame.extensions) >= maxTrackedExtensions {
		// Fold the tail into one bucket rather than growing without bound. The
		// profile is a hint about what a lump is made of, and a hundredth
		// distinct extension does not change that answer.
		extension = ""
		if existing, ok := frame.extensions[extension]; ok {
			existing.Bytes += bytes
			existing.Files += files
			return
		}
	}
	frame.extensions[extension] = &recommendation.ExtensionShare{Extension: extension, Bytes: bytes, Files: files}
}

func extensionOf(name string) string {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot == len(name)-1 {
		return ""
	}
	extension := strings.ToLower(name[dot:])
	// A dotted version segment is not a file type.
	if len(extension) > 12 {
		return ""
	}
	return extension
}

func evidenceNodeFrom(tree *tree, frame evidenceFrame, nowUnix int64, perNode int) recommendation.EvidenceNode {
	entry := *tree.records.at(frame.id)
	name := tree.names.get(entry.nameOffset, entry.nameLength)
	if frame.id == tree.rootNodeID {
		name = path.Base(tree.root)
	}
	residue := frame.residue
	if residue < 0 {
		residue = 0
	}
	node := recommendation.EvidenceNode{
		ID:             frame.id,
		ParentID:       entry.parentID,
		Path:           tree.path(frame.id),
		Name:           name,
		Kind:           tree.kinds.value(entry.kind),
		OwnedAllocated: entry.ownedAllocated,
		Residue:        residue,
		SubtreeFiles:   frame.files,
		SubtreeDirs:    frame.dirs,
		BiggestFile:    frame.biggestFile,
		NewestModified: modifiedTime(frame.newestUnix),
		OldestModified: modifiedTime(frame.oldestUnix),
		FutureModified: frame.newestUnix > nowUnix,
		TopExtensions:  topExtensions(frame.extensions, perNode),
	}
	return node
}

func topExtensions(profile map[string]*recommendation.ExtensionShare, limit int) []recommendation.ExtensionShare {
	if len(profile) == 0 {
		return nil
	}
	shares := make([]recommendation.ExtensionShare, 0, len(profile))
	for _, share := range profile {
		shares = append(shares, *share)
	}
	sort.Slice(shares, func(left, right int) bool {
		if shares[left].Bytes != shares[right].Bytes {
			return shares[left].Bytes > shares[right].Bytes
		}
		return shares[left].Extension < shares[right].Extension
	})
	if len(shares) > limit {
		shares = shares[:limit]
	}
	return shares
}
