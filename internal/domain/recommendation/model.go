// Package recommendation holds the advice domain: what a suggestion is, what it
// must carry to be reviewable, and what a suggestion may never do.
//
// The one rule the whole package exists to enforce: a Recommendation identifies
// its object by snapshot and node ID and NEVER by path. Acting on advice means
// looking the node up by ID, staging it in the Collector and going through the
// existing CleanupPlan lifecycle. An object that does not carry a path cannot
// authorise a file operation, which is the same structural argument ADR-0048
// makes for space-map projections (ADR-0061 §1, DDD invariant 10).
package recommendation

import "time"

// Recovery says what it costs to get the object back. It is the field that
// decides whether a suggestion is comfortable or frightening, so it is
// deliberately separate from Risk: a 40 GB build directory is regenerable but
// deleting it still costs an hour of rebuild.
type Recovery string

const (
	// The system or the application rebuilds it by itself, at no cost beyond time.
	RecoveryRegenerable Recovery = "regenerable"
	// Gone from disk but obtainable again from somewhere else.
	RecoveryRedownloadable Recovery = "redownloadable"
	// Not obtainable again. Nothing in this class may ever be suggested as safe.
	RecoveryIrreplaceable Recovery = "irreplaceable"
)

// Risk is what the user is asked to accept.
type Risk string

const (
	// Rebuilt automatically, contains no user-authored data.
	RiskSafe Risk = "safe"
	// Probably disposable, but the user has to look.
	RiskReview Risk = "review"
	// Deleting this breaks something identifiable.
	RiskRisky Risk = "risky"
)

// Source says who produced a suggestion. Kept on every item because the two
// have different failure modes and the UI must not present them as one thing:
// a rule is wrong the same way every time, a model is wrong differently every
// time.
type Source string

const (
	SourceRule    Source = "rule"
	SourceAdvisor Source = "advisor"
)

// Recommendation is one reviewable suggestion. No path field, on purpose --
// see the package comment.
type Recommendation struct {
	SnapshotID int64
	NodeID     int64
	Source     Source
	// RuleName is set when Source is SourceRule, or when an advisor suggestion
	// happens to land on a node a rule also matched.
	RuleName string
	Category string
	// ReclaimableBytes is always the snapshot's own figure. A model's arithmetic
	// is never trusted here (ADR-0061 §7.4).
	ReclaimableBytes int64
	Recovery         Recovery
	// Risk is a conclusion, never an input: Assess derives it from the fact
	// fields below and is the only writer (ADR-0067, DDD invariant 10a).
	Risk Risk
	// RiskReasons are the codes Assess gave for the tier, so the UI can say why
	// something is "需确认" instead of leaving the user to guess which of four
	// different facts produced the same label.
	RiskReasons []string
	// Confidence in [0,1]. A rule that names its object reports 1; a rule the
	// catalog declares Generic -- it knows the container, not the object, see
	// Rule.Generic -- reports less. The advisor reports its own.
	Confidence float64
	// The facts Assess worked from, kept so a caller that learns one more --
	// the application layer attaching an activity signal -- can reassess.
	DeclaredRisk Risk
	Activity     ActivityKind
	IdleDays     int64
	Guards       []string
	Generic      bool
	// Evidence is the facts the suggestion rests on, phrased so a user can check
	// them against what the space map shows.
	Evidence []string
	// WhatBreaks and HowToRestore are what a rule-based cleaner does not give
	// you, and the reason to trust a suggestion enough to act on it.
	WhatBreaks   string
	HowToRestore string
	// Manual marks a finding the tool must not act on itself: the path is
	// root-owned, so it is reported with the command and never staged (ADR-0065).
	Manual bool `json:"manual"`
	// Command is what to run in a terminal. Only set when Manual is.
	Command string `json:"command"`
}

// Rejection is a suggestion that did not survive validation. Kept rather than
// dropped silently: a tool that says "the model proposed 3 things I refused"
// is more trustworthy than one that quietly shows fewer rows (ADR-0061 §7.5).
type Rejection struct {
	NodeID int64
	// Name as the advisor claimed it, which may be an invention.
	ClaimedName string
	Reason      string
}

const (
	RejectUnknownNode  = "unknown_node"
	RejectNameMismatch = "name_mismatch"
	RejectProtected    = "protected"
	RejectOverlapping  = "overlapping"
	RejectBelowFloor   = "below_floor"
	RejectMalformed    = "malformed"
)

// ExtensionShare is one entry of a residue's extension profile: which kinds of
// file the opaque part of a node is made of. R-062 §3.3 is why this exists --
// ".jar x 139,969" and "one 3.28 GB .pack" are the same size and completely
// different recommendations.
type ExtensionShare struct {
	Extension string
	Bytes     int64
	Files     int64
}

// EvidenceQuery asks the snapshot for the skeleton.
type EvidenceQuery struct {
	SnapshotID int64
	// RootID scopes the skeleton to one subtree. Zero means the scan root. This
	// is what round two of the two-pass flow asks with: the advisor named a
	// region it could not classify, and this returns the inside of that region
	// alone rather than a finer skeleton of the whole disk (ADR-0061 §6).
	RootID int64
	// MinBytes is the absolute floor. Nodes whose subtree is smaller are not
	// worth a recommendation and are folded into their nearest kept ancestor's
	// residue. R-062 §3.2 rejects a depth cap as the alternative: it is cheaper
	// but cuts exactly the level 5-7 build outputs the feature exists to find.
	MinBytes int64
	// MinShare scales the floor with the volume, so a 4 TB disk does not produce
	// a skeleton ten times longer than a 400 GB one. The effective floor is
	// max(MinBytes, used * MinShare). Both numbers are the caller's policy; the
	// formula is applied by the store only because the store is what holds the
	// volume figures, and the floor it settled on comes back on the result so
	// nothing about it is implicit.
	MinShare float64
	// MaxNodes is a fail-loud ceiling, not a truncation point. Exceeding it
	// returns an error so the caller raises the floor, because a silently
	// shortened skeleton reads as "this is everything" when it is not.
	MaxNodes int
	// ExtensionsPerNode caps the residue profile carried per node.
	ExtensionsPerNode int
}

// EvidenceResult is the skeleton plus the volume it was taken from. The volume
// figures ride along rather than being fetched separately: they live on the
// same tree, and a second query would need the caller to know the root node's
// ID, which is exactly the kind of assumption that breaks quietly.
type EvidenceResult struct {
	Root             string
	VolumeTotalBytes uint64
	VolumeUsedBytes  uint64
	VolumeFreeBytes  uint64
	// FloorBytes is the floor actually applied after MinShare was considered.
	FloorBytes int64
	Nodes      []EvidenceNode
}

// EvidenceNode is one row of the skeleton. Unlike Recommendation this DOES carry
// a path: it is assembly material that never leaves the Go side as-is, and the
// rule catalog matches on it. What crosses to the frontend is Recommendation.
type EvidenceNode struct {
	ID       int64
	ParentID int64
	Path     string
	Name     string
	Kind     string
	// OwnedAllocated is the subtree total for a directory, the object's own size
	// for a file -- the same measurement the space map draws with.
	OwnedAllocated int64
	// Residue is the part of OwnedAllocated not covered by any kept descendant:
	// what the model cannot see inside of. Summed over every kept node this
	// equals the root's total (R-062 §2.1).
	Residue int64
	// Subtree counts, not residue counts: "139,969 files" describes the object,
	// and that is the number a person would check.
	SubtreeFiles int64
	SubtreeDirs  int64
	// BiggestFile in the subtree, which separates one huge object from a swarm.
	BiggestFile    int64
	NewestModified time.Time
	OldestModified time.Time
	// SourceNewestModified is the newest mtime in the subtree with build and
	// dependency directories excluded — when the person last touched the work
	// itself rather than when a tool last wrote output.
	//
	// The distinction is the whole point. A build directory last written 200 days
	// ago inside a project whose source changed yesterday is not cold: it is
	// about to be rebuilt, and deleting it costs exactly the download the user
	// was trying to avoid. Conditioning staleness on an artifact's own mtime gets
	// that case backwards.
	SourceNewestModified time.Time
	// IsProjectRoot marks a directory holding a project marker (.git,
	// package.json, Cargo.toml, build.gradle, go.mod, ...). Its
	// SourceNewestModified is the activity signal for everything beneath it.
	IsProjectRoot bool
	// FutureModified marks an mtime ahead of the wall clock. Not clamped away
	// silently: it is itself the signal that something is off with the object
	// (R-062 §3.6 measured one 534 days in the future on the reference machine).
	FutureModified bool
	// TopExtensions profiles the residue, descending by bytes.
	TopExtensions []ExtensionShare
	// Label is the text this node was actually rendered with. A collapsed row
	// reads `a/b/c` while the node's own path ends at `a`, so validating an
	// advisor's echo against the path alone rejects a faithful quotation of what
	// it was shown. Measured against a real advisor: 19 correct suggestions lost
	// that way. Empty means the label is just Name.
	Label string
}

// AgeDays is the clamped age of the newest thing in the subtree. Clamped rather
// than signed because "modified -534 days ago" is not a fact about staleness;
// FutureModified carries that fact instead.
func (n EvidenceNode) AgeDays(now time.Time) int64 {
	if n.NewestModified.IsZero() {
		return 0
	}
	days := int64(now.Sub(n.NewestModified).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

// OldestDays is the same clamp for the oldest thing in the subtree. A large gap
// between OldestDays and AgeDays says the object is still growing; a small one
// says the whole thing was written at once and abandoned.
func (n EvidenceNode) OldestDays(now time.Time) int64 {
	if n.OldestModified.IsZero() {
		return 0
	}
	days := int64(now.Sub(n.OldestModified).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}
