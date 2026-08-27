package cleanup

import (
	"path/filepath"
	"strings"
)

// ProtectionSystemDependency is the one reason code the guard returns today: the
// path is part of what macOS itself depends on. It is a code and not a sentence
// because the wording belongs to the presentation layer, and because a sentence
// per entry would be repeated across every entry of a space map payload that is
// already capped (ADR-0048's density budget).
const ProtectionSystemDependency = "system_dependency"

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
	for _, tree := range protectedTrees {
		if clean == tree || IsPathWithin(tree, clean) {
			return ProtectionSystemDependency
		}
	}
	// A home folder itself, but not what is in it: /Users/alice is protected,
	// /Users/alice/Downloads is not.
	if parent := filepath.Dir(clean); parent == "/Users" && clean != "/Users" {
		return ProtectionSystemDependency
	}
	// A volume's own root, for the same reason the scan root is refused: the mount
	// point is not an ordinary directory.
	if strings.HasPrefix(clean, "/Volumes/") && filepath.Dir(clean) == "/Volumes" {
		return ProtectionSystemDependency
	}
	return ""
}
