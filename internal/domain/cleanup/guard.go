package cleanup

import (
	"path/filepath"
	"strings"
)

// Why a path is protected. Codes and not sentences: the wording belongs to the
// presentation layer, and a sentence per entry would be repeated across every
// entry of a space map payload that is already capped (ADR-0048's density
// budget).
//
// Three codes and not one because they are three different statements, and one
// of them was visibly wrong when they shared: a home folder is not "something
// macOS depends on", it is the account's own data, and a mount point is not a
// folder at all. The refusal exists to tell the user something true.
const (
	// Part of what macOS itself depends on.
	ProtectionSystemDependency = "system_dependency"
	// A user account's home folder. Everything inside it stays deletable.
	ProtectionHomeFolder = "home_folder"
	// A mounted volume's own mount point, which is not an ordinary directory.
	ProtectionVolumeRoot = "volume_root"
)

// protectedExactly may not be deleted themselves, while what is inside them may.
// Losing any of these breaks the system or the account even though every file
// under them is ordinary: /Users is the reference's own example, and it lets you
// delete anything inside a home folder.
//
// Deliberately conservative. The cost of a missing entry is a user trashing
// something the OS needed; the cost of an extra one is a refusal they can work
// around by collecting the contents instead.
var protectedExactly = map[string]struct{}{
	"/Applications": {},
	"/Library":      {},
	"/System":       {},
	"/Users":        {},
	"/Volumes":      {},
	"/bin":          {},
	"/cores":        {},
	"/dev":          {},
	"/etc":          {},
	"/home":         {},
	"/opt":          {},
	"/private":      {},
	"/sbin":         {},
	"/tmp":          {},
	"/usr":          {},
	"/var":          {},
}

// protectedTrees may not be deleted at any depth. /System is read-only and
// SIP-backed, so a delete there fails anyway -- refusing up front says why
// instead of letting it fail at the Trash. The others hold the databases the
// system keeps its own state in.
var protectedTrees = []string{
	"/System",
	"/private/var/db",
	"/private/var/folders",
}

// unprotectedTrees are carved back out of the trees above. /usr/local is where
// third-party software installs, and the reference treats it as ordinary.
var unprotectedTrees = []string{
	"/usr/local",
}

// DeleteBlock reports why a path may not be deleted, or "" when it may. It is the
// only place that question is answered: the space map consults it when granting
// the collect capability, and plan creation consults it again before anything is
// staged, so a frontend that ignored the first answer still cannot get past the
// second.
func DeleteBlock(path string) string {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return ""
	}
	for _, allowed := range unprotectedTrees {
		if clean == allowed || IsPathWithin(allowed, clean) {
			return ""
		}
	}
	if clean == string(filepath.Separator) {
		return ProtectionSystemDependency
	}
	if _, blocked := protectedExactly[clean]; blocked {
		return ProtectionSystemDependency
	}
	// Before the trees below, because the more specific statement has to win: the
	// firmlinked spelling /System/Volumes/Data/Users/alice reaches the same home
	// folder, and answering "macOS depends on it" there would be the same untruth
	// as answering it for /Users/alice. Matched on the parent rather than a
	// literal list so it holds for every account, and for both spellings so the
	// answer does not depend on which of the two the scan happened to walk.
	if parent := filepath.Dir(clean); parent == "/Users" || parent == "/System/Volumes/Data/Users" {
		return ProtectionHomeFolder
	}
	for _, tree := range protectedTrees {
		if clean == tree || IsPathWithin(tree, clean) {
			return ProtectionSystemDependency
		}
	}
	// A volume's own root, for the same reason the scan root is refused: the mount
	// point is not an ordinary directory.
	if strings.HasPrefix(clean, "/Volumes/") && filepath.Dir(clean) == "/Volumes" {
		return ProtectionVolumeRoot
	}
	return ""
}
